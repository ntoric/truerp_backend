package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// fixedWindowLimiter counts requests per key in fixed time windows. It is
// in-memory and per process — enough to blunt scraping and spam against
// unauthenticated endpoints, not a distributed quota.
type fixedWindowLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	hits      map[string]*windowCounter
	lastSweep time.Time
}

type windowCounter struct {
	start time.Time
	count int
}

func (l *fixedWindowLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) > l.window {
		for k, wc := range l.hits {
			if now.Sub(wc.start) >= l.window {
				delete(l.hits, k)
			}
		}
		l.lastSweep = now
	}

	wc, ok := l.hits[key]
	if !ok || now.Sub(wc.start) >= l.window {
		l.hits[key] = &windowCounter{start: now, count: 1}
		return true
	}
	if wc.count >= l.limit {
		return false
	}
	wc.count++
	return true
}

// RateLimitByIP allows at most limit requests per client IP within window
// for the routes it is attached to. Each call creates an independent bucket,
// so attach separate instances to endpoints that need separate budgets.
func RateLimitByIP(limit int, window time.Duration) gin.HandlerFunc {
	l := &fixedWindowLimiter{
		limit:  limit,
		window: window,
		hits:   map[string]*windowCounter{},
	}
	return func(c *gin.Context) {
		if !l.allow(c.ClientIP(), time.Now()) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests. Please wait a moment and try again."})
			return
		}
		c.Next()
	}
}
