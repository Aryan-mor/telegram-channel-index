package service

import (
	"sync"
	"time"
)

// limiter is a token bucket per key. It holds limit tokens, refilled evenly over a window.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter() *limiter { return &limiter{buckets: map[string]*bucket{}} }

// allow reports whether one action fits in the budget. A limit of zero or less means no limit.
func (l *limiter) allow(key string, limit int, window time.Duration, now time.Time) bool {
	if limit <= 0 || window <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: float64(limit), at: now}
		l.buckets[key] = b
	}
	if el := now.Sub(b.at); el > 0 {
		b.tokens += el.Seconds() * float64(limit) / window.Seconds()
		if b.tokens > float64(limit) {
			b.tokens = float64(limit)
		}
		b.at = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
