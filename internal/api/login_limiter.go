package api

import (
	"time"

	"scriberr/internal/auth"
	"scriberr/internal/config"
)

func newLoginAttemptLimiter(cfg *config.Config) *auth.LoginAttemptLimiter {
	if cfg == nil {
		return auth.NewLoginAttemptLimiter(auth.LoginAttemptLimiterConfig{Enabled: true})
	}

	enabled := cfg.AuthRateLimitEnabled
	if !enabled && cfg.AuthMaxFailedAttempts == 0 && cfg.AuthFailureWindowSeconds == 0 && cfg.AuthLockoutSeconds == 0 && cfg.AuthIPMaxFailedAttempts == 0 {
		enabled = true
	}

	return auth.NewLoginAttemptLimiter(auth.LoginAttemptLimiterConfig{
		Enabled:           enabled,
		MaxFailedAttempts: cfg.AuthMaxFailedAttempts,
		FailureWindow:     time.Duration(cfg.AuthFailureWindowSeconds) * time.Second,
		LockoutDuration:   time.Duration(cfg.AuthLockoutSeconds) * time.Second,
		IPMaxFailures:     cfg.AuthIPMaxFailedAttempts,
	})
}

func retryAfterSeconds(duration time.Duration) int {
	if duration <= 0 {
		return 0
	}
	return int((duration + time.Second - time.Nanosecond) / time.Second)
}
