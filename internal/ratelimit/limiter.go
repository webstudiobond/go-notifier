// Package ratelimit provides in-memory token bucket rate limiting for outgoing notifications.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter manages request rate limiting using a token bucket algorithm.
type Limiter struct {
	lastRefill time.Time
	tokens     float64
	capacity   float64
	fillRate   float64
	mu         sync.Mutex
	enabled    bool
}

// NewLimiter creates an initialized token bucket Limiter.
func NewLimiter(ratePerMinute, burst int) *Limiter {
	if ratePerMinute <= 0 {
		return &Limiter{enabled: false}
	}

	capVal := float64(burst)
	if capVal <= 0 {
		capVal = float64(ratePerMinute)
	}

	return &Limiter{
		lastRefill: time.Now(),
		tokens:     capVal,
		capacity:   capVal,
		fillRate:   float64(ratePerMinute) / 60.0,
		enabled:    true,
	}
}

// Allow reports whether an incoming event is within rate limits.
func (l *Limiter) Allow() bool {
	if !l.enabled {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(l.lastRefill).Seconds()
	l.lastRefill = now

	l.tokens += elapsed * l.fillRate
	if l.tokens > l.capacity {
		l.tokens = l.capacity
	}

	if l.tokens >= 1.0 {
		l.tokens -= 1.0
		return true
	}

	return false
}
