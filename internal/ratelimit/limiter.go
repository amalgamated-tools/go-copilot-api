// Package ratelimit implements a simple adaptive rate limiter.
package ratelimit

import (
	"sync"
	"time"
)

// Mode describes the current rate-limiter state.
type Mode string

const (
	ModeNormal      Mode = "normal"
	ModeRateLimited Mode = "rate_limited"
	ModeRecovering  Mode = "recovering"
)

// Config holds rate limiter parameters.
type Config struct {
	BaseRetryIntervalSeconds        float64
	ConsecutiveSuccessesForRecovery int
}

// Status is a snapshot of the rate limiter state.
type Status struct {
	Mode                 Mode    `json:"mode"`
	ConsecutiveSuccesses int     `json:"consecutiveSuccesses"`
	LastRateLimitedAt    *int64  `json:"lastRateLimitedAt"`
	RetryAfterSeconds    float64 `json:"retryAfterSeconds"`
}

// Limiter is the adaptive rate limiter.
type Limiter struct {
	mu                   sync.Mutex
	mode                 Mode
	consecutiveSuccesses int
	lastRateLimitedAt    *time.Time
	retryAfter           time.Time
	config               Config
}

var global *Limiter

// Init creates and stores the global rate limiter.
func Init(cfg Config) {
	global = &Limiter{
		mode:   ModeNormal,
		config: cfg,
	}
}

// Get returns the global limiter (nil if not initialized).
func Get() *Limiter {
	return global
}

// RecordSuccess marks a successful request.
func (l *Limiter) RecordSuccess() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.mode == ModeRecovering {
		l.consecutiveSuccesses++
		if l.consecutiveSuccesses >= l.config.ConsecutiveSuccessesForRecovery {
			l.mode = ModeNormal
			l.consecutiveSuccesses = 0
		}
	}
}

// RecordRateLimit marks a rate-limit error and updates retry timing.
func (l *Limiter) RecordRateLimit(retryAfterSec float64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.lastRateLimitedAt = &now
	l.mode = ModeRateLimited
	l.consecutiveSuccesses = 0

	interval := retryAfterSec
	if interval <= 0 {
		interval = l.config.BaseRetryIntervalSeconds
	}
	l.retryAfter = now.Add(time.Duration(interval * float64(time.Second)))
}

// WaitIfNeeded blocks until the rate limiter allows proceeding.
func (l *Limiter) WaitIfNeeded() {
	l.mu.Lock()

	if l.mode == ModeNormal {
		l.mu.Unlock()
		return
	}

	waitUntil := l.retryAfter
	l.mu.Unlock()

	now := time.Now()
	if waitUntil.After(now) {
		time.Sleep(waitUntil.Sub(now))
	}

	l.mu.Lock()
	if l.mode == ModeRateLimited {
		l.mode = ModeRecovering
	}
	l.mu.Unlock()
}

// GetStatus returns a snapshot of the limiter state.
func (l *Limiter) GetStatus() Status {
	l.mu.Lock()
	defer l.mu.Unlock()

	retryAfterSeconds := 0.0
	if !l.retryAfter.IsZero() {
		remaining := time.Until(l.retryAfter).Seconds()
		if remaining > 0 {
			retryAfterSeconds = remaining
		}
	}

	s := Status{
		Mode:                 l.mode,
		ConsecutiveSuccesses: l.consecutiveSuccesses,
		RetryAfterSeconds:    retryAfterSeconds,
	}
	if l.lastRateLimitedAt != nil {
		ts := l.lastRateLimitedAt.UnixMilli()
		s.LastRateLimitedAt = &ts
	}
	return s
}

// GetConfig returns the limiter configuration.
func (l *Limiter) GetConfig() Config {
	return l.config
}
