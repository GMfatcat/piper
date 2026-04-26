package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// okHandler is a trivial handler that responds HTTP 200.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// ---------------------------------------------------------------------------
// requireLocalhost
// ---------------------------------------------------------------------------

func TestRequireLocalhost_AllowsIPv4Loopback(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	requireLocalhost(next).ServeHTTP(w, r)

	if !called {
		t.Fatal("expected next handler to be called for IPv4 loopback")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestRequireLocalhost_AllowsIPv6Loopback(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "[::1]:1234"
	w := httptest.NewRecorder()

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	requireLocalhost(next).ServeHTTP(w, r)

	if !called {
		t.Fatal("expected next handler to be called for IPv6 loopback")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestRequireLocalhost_RejectsLAN(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "192.168.1.5:1234"
	w := httptest.NewRecorder()

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	requireLocalhost(next).ServeHTTP(w, r)

	if called {
		t.Fatal("expected next handler NOT to be called for LAN address")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}

	var env Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if env.OK {
		t.Fatal("expected ok=false")
	}
	if env.Error == nil || env.Error.Code != ErrCodeForbidden {
		t.Fatalf("expected FORBIDDEN error code, got %+v", env.Error)
	}
}

func TestRequireLocalhost_MalformedAddrRejected(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "garbage" // no colon, SplitHostPort will fail
	w := httptest.NewRecorder()

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	requireLocalhost(next).ServeHTTP(w, r)

	if called {
		t.Fatal("expected next handler NOT to be called for malformed address")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// rateLimit
// ---------------------------------------------------------------------------

// newTestRateLimiter creates a rateLimiter with an injectable clock.
func newTestRateLimiter(now func() time.Time) *rateLimiter {
	rl := newRateLimiter()
	rl.nowFn = now
	return rl
}

func TestRateLimit_AllowsBurst(t *testing.T) {
	fixedTime := time.Now()
	rl := newTestRateLimiter(func() time.Time { return fixedTime })
	h := rateLimit(rl, okHandler)

	// 10 requests in quick succession (all at the same time) should be allowed.
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, w.Code)
		}
	}
}

func TestRateLimit_BlocksAfterBurst(t *testing.T) {
	fixedTime := time.Now()
	rl := newTestRateLimiter(func() time.Time { return fixedTime })
	h := rateLimit(rl, okHandler)

	// Exhaust the burst.
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.0.0.2:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}

	// 11th request should be blocked.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.2:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}

	var env Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if env.OK {
		t.Fatal("expected ok=false")
	}
	if env.Error == nil || env.Error.Code != ErrCodeTooManyRequests {
		t.Fatalf("expected TOO_MANY_REQUESTS, got %+v", env.Error)
	}
}

func TestRateLimit_RefillsOverTime(t *testing.T) {
	// Use a mutable clock variable that we can advance.
	now := time.Now()
	rl := newTestRateLimiter(func() time.Time { return now })
	h := rateLimit(rl, okHandler)

	ip := "10.0.0.3:1234"

	// Exhaust the burst (10 requests at t=0).
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = ip
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}

	// Verify 11th is blocked.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = ip
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatal("expected 429 after burst exhausted")
	}

	// Advance clock by 200ms → 2 tokens should refill (rate = 10/sec → 0.2s = 2 tokens).
	now = now.Add(200 * time.Millisecond)

	// One more request should now be allowed.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = ip
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 after refill, got %d", w2.Code)
	}
}

func TestRateLimit_PerIPSeparate(t *testing.T) {
	fixedTime := time.Now()
	rl := newTestRateLimiter(func() time.Time { return fixedTime })
	h := rateLimit(rl, okHandler)

	ipA := "10.0.0.4:1234"
	ipB := "10.0.0.5:1234"

	// Exhaust IP A.
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = ipA
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}
	// Verify A is blocked.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = ipA
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatal("expected IP A to be rate limited")
	}

	// IP B's first request should be allowed.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = ipB
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected IP B to be allowed, got %d", w2.Code)
	}
}

// ---------------------------------------------------------------------------
// logging
// ---------------------------------------------------------------------------

// logCapture captures slog records into a buffer for test assertions.
type logCapture struct {
	buf bytes.Buffer
}

// handler returns an slog.Handler that writes JSON to buf.
func (lc *logCapture) handler() slog.Handler {
	return slog.NewJSONHandler(&lc.buf, &slog.HandlerOptions{Level: slog.LevelDebug})
}

func TestLogging_Records(t *testing.T) {
	// Install a test slog handler that captures output.
	lc := &logCapture{}
	old := slog.Default()
	slog.SetDefault(slog.New(lc.handler()))
	defer slog.SetDefault(old)

	// Create a simple handler that returns 200.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := logging(inner)
	r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	r = r.WithContext(context.Background())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	logOutput := lc.buf.String()
	if logOutput == "" {
		t.Fatal("expected log output, got nothing")
	}

	// Parse the JSON log line.
	var record map[string]any
	if err := json.Unmarshal([]byte(logOutput), &record); err != nil {
		// Multiple lines possible; try first line.
		lines := bytes.Split(lc.buf.Bytes(), []byte("\n"))
		for _, line := range lines {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			if err2 := json.Unmarshal(line, &record); err2 == nil {
				break
			}
		}
		if record == nil {
			t.Fatalf("could not parse log output as JSON: %s", logOutput)
		}
	}

	if record["method"] != "GET" {
		t.Errorf("expected method=GET in log, got %v", record["method"])
	}
	if record["path"] != "/api/health" {
		t.Errorf("expected path=/api/health in log, got %v", record["path"])
	}
	// Status is logged as a number; JSON numbers decode as float64.
	statusVal, ok := record["status"]
	if !ok {
		t.Error("expected status field in log")
	} else {
		statusFloat, ok := statusVal.(float64)
		if !ok || int(statusFloat) != 200 {
			t.Errorf("expected status=200 in log, got %v", statusVal)
		}
	}
}
