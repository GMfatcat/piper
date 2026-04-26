// Package server implements the HTTP server for Piper.
//
// Architecture:
//   - Server wraps net/http.Server with a pre-configured ServeMux.
//   - Deps bundles all handler dependencies; consumer-side interfaces are
//     defined in this package so tests can mock without importing concrete types.
//   - ListenAndServe blocks until ctx is canceled, then performs a graceful
//     shutdown (waits for in-flight requests to drain).
package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ---------------------------------------------------------------------------
// Consumer-side interfaces
// ---------------------------------------------------------------------------

// ReservationStore is the subset of *store.Store the server needs for
// reservation operations.
type ReservationStore interface {
	InsertReservation(ctx context.Context, r store.Reservation) error
	DeleteReservation(ctx context.Context, port int) error
	GetReservation(ctx context.Context, port int) (store.Reservation, error)
	ListReservations(ctx context.Context) ([]store.Reservation, error)
}

// HistoryStore is the subset of *store.Store the server needs for history
// operations.
type HistoryStore interface {
	AppendEvent(ctx context.Context, e store.Event) (store.Event, error)
	QueryEvents(ctx context.Context, q store.HistoryQuery) ([]store.Event, error)
}

// ---------------------------------------------------------------------------
// Deps
// ---------------------------------------------------------------------------

// Deps bundles everything the handlers need. All fields are optional except
// Snap — a nil Snap will cause panics in most handlers.
type Deps struct {
	// Snap provides the latest scan snapshot. Required.
	Snap service.SnapshotProvider

	// Reserves is the reservation store. Required for reservation handlers.
	Reserves ReservationStore

	// History is the history store. Required for history handlers.
	History HistoryStore

	// OnRefresh is called by POST /api/scan/trigger. If nil, the endpoint
	// returns 503 NOT_SUPPORTED (design §6.3 notes this requires --serve mode).
	OnRefresh func(ctx context.Context) error

	// Now returns the current time. Defaults to time.Now when nil.
	// Inject a fake clock in tests.
	Now func() time.Time
}

// now returns the current time, using the injectable clock when set.
func (d *Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Server wraps net/http.Server with the registered handler.
type Server struct {
	deps Deps
	mux  *http.ServeMux
	srv  *http.Server
}

// New constructs a Server bound to host:port. Caller starts it via
// ListenAndServe.
func New(host string, port int, deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.registerRoutes()
	s.srv = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", host, port),
		Handler: s.mux,
	}
	return s
}

// Handler exposes the constructed http.Handler for httptest use.
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe blocks until ctx cancellation triggers graceful shutdown.
// It returns the underlying http.Server error (nil on graceful shutdown, which
// is signaled by http.ErrServerClosed).
func (s *Server) ListenAndServe(ctx context.Context) error {
	// Start the server in a goroutine.
	errCh := make(chan error, 1)
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	// Wait for context cancellation or server error.
	select {
	case <-ctx.Done():
		// Graceful shutdown with a 5-second deadline.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server shutdown: %w", err)
		}
		// Drain the goroutine's return value (should be nil after Shutdown).
		<-errCh
		return nil
	case err := <-errCh:
		return err
	}
}
