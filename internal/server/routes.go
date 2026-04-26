// Package server — routes.go
//
// registerRoutes wires all /api/* routes into the ServeMux using Go 1.22+
// method+path routing syntax (e.g. "GET /api/health").
//
// Middleware composition (innermost → outermost):
//   - Read routes:  logging → rateLimit → handler
//   - Write routes: logging → rateLimit → requireLocalhost → handler
package server

import (
	"net/http"
)

// registerRoutes attaches all handlers to s.mux. It is called once from New.
func (s *Server) registerRoutes() {
	rl := newRateLimiter()

	// Helper: wrap a read-only handler.
	read := func(h http.HandlerFunc) http.Handler {
		return logging(rateLimit(rl, h))
	}

	// Helper: wrap a write handler (localhost-only).
	write := func(h http.HandlerFunc) http.Handler {
		return logging(rateLimit(rl, requireLocalhost(h)))
	}

	// ── Read routes ──────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/health", read(s.handleHealth))
	s.mux.Handle("GET /api/scan/latest", read(s.handleScanLatest))
	s.mux.Handle("GET /api/port/{port}", read(s.handlePort))
	s.mux.Handle("POST /api/check", read(s.handleCheck))
	s.mux.Handle("GET /api/suggest", read(s.handleSuggest))
	s.mux.Handle("GET /api/reservations", read(s.handleListReservations))
	s.mux.Handle("GET /api/history", read(s.handleHistory))

	// ── Write routes (localhost only) ────────────────────────────────────────
	s.mux.Handle("POST /api/reservations", write(s.handleCreateReservation))
	s.mux.Handle("DELETE /api/reservations/{port}", write(s.handleDeleteReservation))
	s.mux.Handle("POST /api/scan/trigger", write(s.handleScanTrigger))

	s.registerWebRoutes(s.mux)
}
