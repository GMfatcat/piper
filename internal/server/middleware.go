// Package server — middleware.go
//
// Provides three middleware layers:
//   - requireLocalhost: restricts handler to loopback callers only (§11.2).
//   - rateLimit: per-IP in-memory token bucket, 10 req/sec (§11.3).
//   - logging: slog-based per-request structured log line.
package server

import (
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// requireLocalhost
// ---------------------------------------------------------------------------

// requireLocalhost rejects requests from any address other than 127.0.0.1 or
// ::1. On rejection it writes a 403 FORBIDDEN JSON envelope and stops the
// middleware chain.
func requireLocalhost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || (host != "127.0.0.1" && host != "::1") {
			writeError(w, http.StatusForbidden, ErrCodeForbidden, "write operations require localhost")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// rateLimit
// ---------------------------------------------------------------------------

// rateLimitEntry tracks the token-bucket state for one IP.
type rateLimitEntry struct {
	tokens     float64
	lastRefill time.Time
}

// rateLimiter holds all per-IP state.
//
// Phase 1: no background cleanup goroutine — stale entries accumulate until
// restart. This is acceptable for a locally-deployed tool.
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateLimitEntry
	// nowFn allows tests to inject a fake clock so they can advance time
	// without calling time.Sleep.
	nowFn func() time.Time
}

// newRateLimiter creates a rateLimiter with the real clock.
func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		entries: make(map[string]*rateLimitEntry),
		nowFn:   time.Now,
	}
}

const (
	rateLimitRate  = 10.0 // tokens per second
	rateLimitBurst = 10.0 // maximum token bucket capacity
)

// allow reports whether the request from ip should be allowed. It atomically
// refills tokens (based on elapsed time) and deducts one token.
func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.nowFn()

	entry, exists := rl.entries[ip]
	if !exists {
		// First request from this IP: start with a full bucket minus one.
		rl.entries[ip] = &rateLimitEntry{
			tokens:     rateLimitBurst - 1,
			lastRefill: now,
		}
		return true
	}

	// Refill based on elapsed time since last request.
	elapsed := now.Sub(entry.lastRefill).Seconds()
	entry.tokens += elapsed * rateLimitRate
	if entry.tokens > rateLimitBurst {
		entry.tokens = rateLimitBurst
	}
	entry.lastRefill = now

	if entry.tokens < 1 {
		return false
	}
	entry.tokens--
	return true
}

// rateLimit wraps next with per-IP token-bucket rate limiting.
// The rateLimiter is shared across all uses of this middleware via closure.
func rateLimit(rl *rateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			// Malformed RemoteAddr — use raw value as key.
			host = r.RemoteAddr
		}
		if !rl.allow(host) {
			writeError(w, http.StatusTooManyRequests, ErrCodeTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// logging
// ---------------------------------------------------------------------------

// responseRecorder captures the status code written by a handler so the
// logging middleware can include it in the log record.
type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (rr *responseRecorder) WriteHeader(status int) {
	rr.status = status
	rr.ResponseWriter.WriteHeader(status)
}

// logging wraps next with structured slog logging. Each request emits one
// slog.Info line with method, path, status code, and elapsed duration.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
		)
	})
}
