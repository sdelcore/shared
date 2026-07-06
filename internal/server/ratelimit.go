package server

import (
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// limiter is a per-key token bucket. It guards the AI endpoints so a runaway
// site loop cannot burn unbounded upstream API spend.
type limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens added per second
	burst   float64 // bucket capacity
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// newAILimiter builds the AI limiter from SHARED_AI_RATE (requests per minute,
// default 30; 0 disables). Burst is fixed at 10. A nil limiter allows all.
func newAILimiter() *limiter {
	perMin := 30.0
	if v := os.Getenv("SHARED_AI_RATE"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			perMin = n
		}
	}
	if perMin <= 0 {
		return nil
	}
	return &limiter{
		rate:    perMin / 60,
		burst:   10,
		buckets: map[string]*bucket{},
	}
}

func (l *limiter) allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// aiRateLimit wraps an AI handler, rejecting over-limit callers (keyed per
// site) with 429 before any upstream request is made.
func (s *Server) aiRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ai.allow(s.siteFromRequest(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next(w, r)
	}
}
