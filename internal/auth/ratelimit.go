package auth

import (
	"sync"
	"time"
)

// Limiter is a per-key token bucket: burst attempts are allowed immediately,
// then one more attempt becomes available every interval. Keys are client IP
// addresses, so one noisy source cannot lock everyone else out.
type Limiter struct {
	burst    int
	interval time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter returns a limiter allowing burst attempts, refilling one token
// per interval.
func NewLimiter(burst int, interval time.Duration) *Limiter {
	return &Limiter{burst: burst, interval: interval, buckets: map[string]*bucket{}, now: time.Now}
}

// Allow consumes one token for key, reporting whether the attempt may proceed.
func (l *Limiter) Allow(key string) bool {
	if l == nil || l.burst <= 0 {
		return true
	}
	if key == "" {
		key = "unknown"
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	// Drop buckets that have been idle long enough to be full again, so the
	// map cannot grow without bound.
	if len(l.buckets) > 4096 {
		for k, b := range l.buckets {
			if now.Sub(b.last) > l.interval*time.Duration(l.burst) {
				delete(l.buckets, k)
			}
		}
	}

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(l.burst), last: now}
		l.buckets[key] = b
	}
	refill := now.Sub(b.last).Seconds() / l.interval.Seconds()
	b.tokens = minFloat(float64(l.burst), b.tokens+refill)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Reset clears the bucket for a key, used after a successful authentication.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
