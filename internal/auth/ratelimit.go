package auth

import (
	"sync"
	"time"
)

// RateLimiter is a sliding-window limiter (per key, e.g. "login:user" or
// "pin:user") used to blunt credential brute force.
type RateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	max     int
	hits    map[string][]time.Time
}

func NewRateLimiter(window time.Duration, max int) *RateLimiter {
	return &RateLimiter{window: window, max: max, hits: map[string][]time.Time{}}
}

// Allow records a hit and reports whether the key is within budget.
func (r *RateLimiter) Allow(key string) bool {
	now := time.Now()
	cutoff := now.Add(-r.window)
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.hits[key]
	kept := list[:0]
	for _, t := range list {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.max {
		r.hits[key] = kept
		return false
	}
	r.hits[key] = append(kept, now)
	return true
}

// Forget clears a key (on success, so honest users never hit the ceiling).
func (r *RateLimiter) Forget(key string) {
	r.mu.Lock()
	delete(r.hits, key)
	r.mu.Unlock()
}
