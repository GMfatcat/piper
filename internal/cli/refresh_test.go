package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── Test 1: HTTP 200 OK → success ───────────────────────────────────────────

func TestRunRefresh_HTTPSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	// Parse host and port from test server URL.
	host, port := parseTestServerAddr(t, ts.URL)

	var outBuf bytes.Buffer
	w := NewWriter(FormatText, "", false)
	w.Stdout = &outBuf

	deps := RefreshDeps{
		HTTPClient: ts.Client(),
		Host:       host,
		Port:       port,
		Out:        w,
	}

	err := RunRefresh(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if !strings.Contains(outBuf.String(), "ok") {
		t.Errorf("expected 'ok' in output, got: %q", outBuf.String())
	}
}

// ─── Test 2: HTTP 500 → error with status ────────────────────────────────────

func TestRunRefresh_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	host, port := parseTestServerAddr(t, ts.URL)

	var outBuf bytes.Buffer
	w := NewWriter(FormatText, "", false)
	w.Stdout = &outBuf

	deps := RefreshDeps{
		HTTPClient: ts.Client(),
		Host:       host,
		Port:       port,
		Out:        w,
	}

	err := RunRefresh(context.Background(), deps)
	if err == nil {
		t.Fatal("expected non-nil error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code 500, got: %v", err)
	}
}

// ─── Test 3: connection refused → fallback called, RunRefresh returns nil ────

func TestRunRefresh_ConnectionRefusedFallback(t *testing.T) {
	fallbackCalled := false

	var outBuf bytes.Buffer
	w := NewWriter(FormatText, "", false)
	w.Stdout = &outBuf

	deps := RefreshDeps{
		HTTPClient: http.DefaultClient,
		// Use a port that is definitely not listening.
		Host: "127.0.0.1",
		Port: 19999, // unlikely to be in use during tests
		Out:  w,
		Fallback: func(ctx context.Context) error {
			fallbackCalled = true
			return nil
		},
	}

	err := RunRefresh(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error from fallback path, got: %v", err)
	}
	if !fallbackCalled {
		t.Error("expected Fallback to be called on connection refused, but it was not")
	}
}

// ─── Test 4: connection refused + fallback returns error ─────────────────────

func TestRunRefresh_FallbackError(t *testing.T) {
	sentinelErr := errors.New("fallback scan failed")

	var outBuf bytes.Buffer
	w := NewWriter(FormatText, "", false)
	w.Stdout = &outBuf

	deps := RefreshDeps{
		HTTPClient: http.DefaultClient,
		Host:       "127.0.0.1",
		Port:       19998, // unlikely to be in use
		Out:        w,
		Fallback: func(ctx context.Context) error {
			return sentinelErr
		},
	}

	err := RunRefresh(context.Background(), deps)
	if err == nil {
		t.Fatal("expected non-nil error from fallback path")
	}
	if !errors.Is(err, sentinelErr) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// parseTestServerAddr extracts host and port from a test server URL like
// "http://127.0.0.1:12345".
func parseTestServerAddr(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	// Strip scheme.
	addr := strings.TrimPrefix(rawURL, "http://")
	addr = strings.TrimPrefix(addr, "https://")

	lastColon := strings.LastIndex(addr, ":")
	if lastColon < 0 {
		t.Fatalf("cannot parse test server addr: %q", rawURL)
	}
	host := addr[:lastColon]
	portStr := addr[lastColon+1:]

	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("cannot parse port from %q: %v", portStr, err)
	}
	return host, port
}
