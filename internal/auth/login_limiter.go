package auth

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultAuthMaxFailedAttempts = 5
	defaultAuthFailureWindow     = 10 * time.Minute
	defaultAuthLockoutDuration   = 15 * time.Minute
	defaultAuthIPMaxFailures     = 20
)

// LoginAttemptLimiterConfig controls in-memory login throttling.
type LoginAttemptLimiterConfig struct {
	Enabled           bool
	MaxFailedAttempts int
	FailureWindow     time.Duration
	LockoutDuration   time.Duration
	IPMaxFailures     int
	Now               func() time.Time
}

// LoginAttemptDecision describes whether a login attempt may proceed.
type LoginAttemptDecision struct {
	Allowed    bool
	RetryAfter time.Duration
	Reason     string
}

// LoginAttemptRecord describes limiter state after a failed login attempt.
type LoginAttemptRecord struct {
	Locked     bool
	RetryAfter time.Duration
	Reason     string
}

// LoginAttemptLimiter slows repeated failed logins without requiring an
// external proxy or host-level fail2ban integration.
type LoginAttemptLimiter struct {
	mu sync.Mutex

	enabled           bool
	maxFailedAttempts int
	failureWindow     time.Duration
	lockoutDuration   time.Duration
	ipMaxFailures     int
	now               func() time.Time

	userIPBuckets map[string]*loginAttemptBucket
	ipBuckets     map[string]*loginAttemptBucket
}

type loginAttemptBucket struct {
	failures       int
	firstFailureAt time.Time
	nextAllowedAt  time.Time
	lockedUntil    time.Time
}

// NewLoginAttemptLimiter creates a concurrency-safe in-memory limiter.
func NewLoginAttemptLimiter(config LoginAttemptLimiterConfig) *LoginAttemptLimiter {
	if config.MaxFailedAttempts <= 0 {
		config.MaxFailedAttempts = defaultAuthMaxFailedAttempts
	}
	if config.FailureWindow <= 0 {
		config.FailureWindow = defaultAuthFailureWindow
	}
	if config.LockoutDuration <= 0 {
		config.LockoutDuration = defaultAuthLockoutDuration
	}
	if config.IPMaxFailures <= 0 {
		config.IPMaxFailures = defaultAuthIPMaxFailures
	}
	if config.Now == nil {
		config.Now = time.Now
	}

	return &LoginAttemptLimiter{
		enabled:           config.Enabled,
		maxFailedAttempts: config.MaxFailedAttempts,
		failureWindow:     config.FailureWindow,
		lockoutDuration:   config.LockoutDuration,
		ipMaxFailures:     config.IPMaxFailures,
		now:               config.Now,
		userIPBuckets:     make(map[string]*loginAttemptBucket),
		ipBuckets:         make(map[string]*loginAttemptBucket),
	}
}

// Check returns whether a login attempt may proceed before password work starts.
func (l *LoginAttemptLimiter) Check(username, ip string) LoginAttemptDecision {
	if l == nil || !l.enabled {
		return LoginAttemptDecision{Allowed: true}
	}

	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	decision := LoginAttemptDecision{Allowed: true}
	userIPKey := loginUserIPKey(username, ip)
	ipKey := normalizeLoginIP(ip)

	if bucket := l.userIPBuckets[userIPKey]; bucket != nil {
		bucket.resetExpiredWindow(now, l.failureWindow)
		decision = stricterDecision(decision, bucket.decision(now, "user_ip"))
		if bucket.isIdle(now, l.failureWindow) {
			delete(l.userIPBuckets, userIPKey)
		}
	}

	if bucket := l.ipBuckets[ipKey]; bucket != nil {
		bucket.resetExpiredWindow(now, l.failureWindow)
		decision = stricterDecision(decision, bucket.decision(now, "ip"))
		if bucket.isIdle(now, l.failureWindow) {
			delete(l.ipBuckets, ipKey)
		}
	}

	return decision
}

// RecordFailure records a failed password attempt and returns lockout state.
func (l *LoginAttemptLimiter) RecordFailure(username, ip string) LoginAttemptRecord {
	if l == nil || !l.enabled {
		return LoginAttemptRecord{}
	}

	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	userRecord := l.recordFailure(l.userIPBuckets, loginUserIPKey(username, ip), now, l.maxFailedAttempts, "user_ip_locked")
	ipRecord := l.recordFailure(l.ipBuckets, normalizeLoginIP(ip), now, l.ipMaxFailures, "ip_locked")

	if ipRecord.Locked && ipRecord.RetryAfter > userRecord.RetryAfter {
		return ipRecord
	}
	return userRecord
}

// RecordSuccess clears caller buckets after a successful login.
func (l *LoginAttemptLimiter) RecordSuccess(username, ip string) {
	if l == nil || !l.enabled {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.userIPBuckets, loginUserIPKey(username, ip))
	delete(l.ipBuckets, normalizeLoginIP(ip))
}

func (l *LoginAttemptLimiter) recordFailure(buckets map[string]*loginAttemptBucket, key string, now time.Time, maxFailures int, lockReason string) LoginAttemptRecord {
	bucket := buckets[key]
	if bucket == nil {
		bucket = &loginAttemptBucket{}
		buckets[key] = bucket
	}

	bucket.resetExpiredWindow(now, l.failureWindow)
	if bucket.failures == 0 {
		bucket.firstFailureAt = now
	}
	bucket.failures++

	if bucket.failures >= maxFailures {
		bucket.lockedUntil = now.Add(l.lockoutDuration)
		bucket.nextAllowedAt = bucket.lockedUntil
		return LoginAttemptRecord{
			Locked:     true,
			RetryAfter: l.lockoutDuration,
			Reason:     lockReason,
		}
	}

	bucket.nextAllowedAt = now.Add(loginBackoff(bucket.failures))
	return LoginAttemptRecord{}
}

func (b *loginAttemptBucket) decision(now time.Time, prefix string) LoginAttemptDecision {
	if !b.lockedUntil.IsZero() && now.Before(b.lockedUntil) {
		return LoginAttemptDecision{
			Allowed:    false,
			RetryAfter: b.lockedUntil.Sub(now),
			Reason:     prefix + "_locked",
		}
	}
	if !b.nextAllowedAt.IsZero() && now.Before(b.nextAllowedAt) {
		return LoginAttemptDecision{
			Allowed:    false,
			RetryAfter: b.nextAllowedAt.Sub(now),
			Reason:     prefix + "_backoff",
		}
	}
	return LoginAttemptDecision{Allowed: true}
}

func (b *loginAttemptBucket) resetExpiredWindow(now time.Time, window time.Duration) {
	if b.firstFailureAt.IsZero() {
		return
	}
	if now.Sub(b.firstFailureAt) <= window {
		return
	}
	if !b.lockedUntil.IsZero() && now.Before(b.lockedUntil) {
		return
	}
	*b = loginAttemptBucket{}
}

func (b *loginAttemptBucket) isIdle(now time.Time, window time.Duration) bool {
	if b.failures == 0 {
		return true
	}
	if !b.lockedUntil.IsZero() && now.Before(b.lockedUntil) {
		return false
	}
	if !b.nextAllowedAt.IsZero() && now.Before(b.nextAllowedAt) {
		return false
	}
	return now.Sub(b.firstFailureAt) > window
}

func stricterDecision(current, candidate LoginAttemptDecision) LoginAttemptDecision {
	if candidate.Allowed {
		return current
	}
	if current.Allowed || candidate.RetryAfter > current.RetryAfter {
		return candidate
	}
	return current
}

func loginBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if failures > 4 {
		failures = 4
	}
	return time.Duration(1<<failures) * time.Second
}

func loginUserIPKey(username, ip string) string {
	return fmt.Sprintf("%s|%s", normalizeLoginUsername(username), normalizeLoginIP(ip))
}

func normalizeLoginUsername(username string) string {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return "<empty>"
	}
	return username
}

func normalizeLoginIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "<unknown>"
	}
	return ip
}
