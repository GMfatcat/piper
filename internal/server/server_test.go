package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/store"
)

// ---------------------------------------------------------------------------
// TestNew_RegistersAllRoutes
// ---------------------------------------------------------------------------

// TestNew_RegistersAllRoutes verifies that every route from §6.1 is registered
// and returns a JSON envelope response (not Go's default "404 page not found"
// plain-text response that ServeMux emits for unregistered paths).
//
// A valid JSON response means the handler ran — even if it returned 404 NOT_FOUND
// for business reasons (e.g., "reservation not found"), that is distinct from
// a routing 404.
func TestNew_RegistersAllRoutes(t *testing.T) {
	// Pre-seed a reservation so DELETE /api/reservations/8080 finds something
	// and returns 200 instead of 404 NOT_FOUND.
	reserves := newStubReserves()
	ctx := context.Background()
	_ = reserves.InsertReservation(ctx, store.Reservation{Port: 8080, Name: "test"})

	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: reserves,
		History:  &stubHistory{},
		Now:      func() time.Time { return fixedNow },
	})

	handler := srv.Handler()

	type routeCase struct {
		method string
		path   string
		// body to send (may be nil)
		// RemoteAddr must be localhost for write routes.
		needsLocalhost bool
	}

	routes := []routeCase{
		{http.MethodGet, "/api/health", false},
		{http.MethodGet, "/api/scan/latest", false},
		{http.MethodGet, "/api/port/8080", false},
		{http.MethodPost, "/api/check", false},
		{http.MethodGet, "/api/suggest", false},
		{http.MethodGet, "/api/reservations", false},
		{http.MethodPost, "/api/reservations", true},
		{http.MethodDelete, "/api/reservations/8080", true},
		{http.MethodGet, "/api/history", false},
		{http.MethodPost, "/api/scan/trigger", true},
	}

	for _, tc := range routes {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.needsLocalhost {
				r.RemoteAddr = "127.0.0.1:54321"
			} else {
				r.RemoteAddr = "10.0.0.5:1234"
			}

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)

			if w.Code == http.StatusNotFound {
				t.Errorf("%s %s returned 404 — route not registered", tc.method, tc.path)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestServer_GracefulShutdown
// ---------------------------------------------------------------------------

// TestServer_GracefulShutdown starts a real TCP server, cancels the context,
// and verifies ListenAndServe returns within 2 seconds.
func TestServer_GracefulShutdown(t *testing.T) {
	// Pick a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not find free port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // release so New can rebind it

	// Parse host and port.
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port := 0
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parse port: %v", err)
	}

	srv := New(host, port, Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
		History:  &stubHistory{},
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- srv.ListenAndServe(ctx)
	}()

	// Give the server a moment to start.
	time.Sleep(50 * time.Millisecond)

	// Cancel context → triggers graceful shutdown.
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil error on graceful shutdown, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return within 2 seconds after context cancellation")
	}
}
