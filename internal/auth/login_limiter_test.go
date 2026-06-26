package auth

import (
	"testing"
	"time"
)

func TestLoginAttemptLimiterBackoffAndLockout(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginAttemptLimiter(LoginAttemptLimiterConfig{
		Enabled:           true,
		MaxFailedAttempts: 5,
		FailureWindow:     10 * time.Minute,
		LockoutDuration:   15 * time.Minute,
		IPMaxFailures:     20,
		Now: func() time.Time {
			return now
		},
	})

	expectedBackoffs := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
	}

	for i, expected := range expectedBackoffs {
		if decision := limiter.Check("Admin", "192.0.2.10"); !decision.Allowed {
			t.Fatalf("attempt %d should be allowed before recording failure: %+v", i+1, decision)
		}

		record := limiter.RecordFailure("Admin", "192.0.2.10")
		if record.Locked {
			t.Fatalf("attempt %d should not lock yet: %+v", i+1, record)
		}

		decision := limiter.Check("admin", "192.0.2.10")
		if decision.Allowed {
			t.Fatalf("attempt %d should require backoff", i+1)
		}
		if decision.RetryAfter != expected {
			t.Fatalf("attempt %d retry after = %s, want %s", i+1, decision.RetryAfter, expected)
		}

		now = now.Add(expected)
	}

	if decision := limiter.Check("admin", "192.0.2.10"); !decision.Allowed {
		t.Fatalf("fifth attempt should be allowed after prior backoff: %+v", decision)
	}

	record := limiter.RecordFailure("admin", "192.0.2.10")
	if !record.Locked {
		t.Fatalf("fifth failure should lock")
	}
	if record.RetryAfter != 15*time.Minute {
		t.Fatalf("lockout retry after = %s, want 15m", record.RetryAfter)
	}

	decision := limiter.Check("admin", "192.0.2.10")
	if decision.Allowed {
		t.Fatal("locked attempt should not be allowed")
	}
	if decision.RetryAfter != 15*time.Minute {
		t.Fatalf("locked retry after = %s, want 15m", decision.RetryAfter)
	}
}

func TestLoginAttemptLimiterSuccessResetsCaller(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginAttemptLimiter(LoginAttemptLimiterConfig{
		Enabled: true,
		Now: func() time.Time {
			return now
		},
	})

	limiter.RecordFailure("admin", "192.0.2.10")
	if decision := limiter.Check("admin", "192.0.2.10"); decision.Allowed {
		t.Fatal("failed login should create backoff")
	}

	limiter.RecordSuccess("admin", "192.0.2.10")
	if decision := limiter.Check("admin", "192.0.2.10"); !decision.Allowed {
		t.Fatalf("successful login should reset caller throttle: %+v", decision)
	}
}

func TestLoginAttemptLimiterIPBucketLocksSpraying(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginAttemptLimiter(LoginAttemptLimiterConfig{
		Enabled:           true,
		MaxFailedAttempts: 10,
		FailureWindow:     10 * time.Minute,
		LockoutDuration:   15 * time.Minute,
		IPMaxFailures:     3,
		Now: func() time.Time {
			return now
		},
	})

	for _, username := range []string{"one", "two"} {
		if decision := limiter.Check(username, "192.0.2.10"); !decision.Allowed {
			t.Fatalf("spray attempt for %s should be allowed before threshold: %+v", username, decision)
		}
		limiter.RecordFailure(username, "192.0.2.10")
		now = now.Add(16 * time.Second)
	}

	record := limiter.RecordFailure("three", "192.0.2.10")
	if !record.Locked {
		t.Fatal("third IP-level failure should lock when IPMaxFailures is 3")
	}
	if record.Reason != "ip_locked" {
		t.Fatalf("lock reason = %q, want ip_locked", record.Reason)
	}

	decision := limiter.Check("four", "192.0.2.10")
	if decision.Allowed {
		t.Fatal("IP lock should block another username")
	}
	if decision.Reason != "ip_locked" {
		t.Fatalf("decision reason = %q, want ip_locked", decision.Reason)
	}
}
