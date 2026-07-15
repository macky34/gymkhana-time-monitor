package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a per-key token bucket. Used to throttle POST
// /api/register, GET /a/{token}, and /api/mypage/* to 10 requests per 10
// seconds per client IP.
type rateLimiter struct {
	mu           sync.Mutex
	buckets      map[string]*tokenBucket
	capacity     float64
	refillPerSec float64
}

type tokenBucket struct {
	tokens   float64
	lastTime time.Time
}

func newRateLimiter(capacity float64, window time.Duration) *rateLimiter {
	return &rateLimiter{
		buckets:      make(map[string]*tokenBucket),
		capacity:     capacity,
		refillPerSec: capacity / window.Seconds(),
	}
}

func (rl *rateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		rl.buckets[key] = &tokenBucket{tokens: rl.capacity - 1, lastTime: now}
		return true
	}

	elapsed := now.Sub(b.lastTime).Seconds()
	b.tokens += elapsed * rl.refillPerSec
	if b.tokens > rl.capacity {
		b.tokens = rl.capacity
	}
	b.lastTime = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// clientIP prefers CF-Connecting-IP (Cloudflare) and otherwise falls back
// to the host part of RemoteAddr.
func clientIP(r *http.Request) string {
	if v := r.Header.Get("CF-Connecting-IP"); v != "" {
		return v
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
