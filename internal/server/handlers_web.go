// Package server — handlers_web.go
//
// Web page handlers for the Piper UI:
//   - handleOverview          GET /
//   - handleReservationsPage  GET /reservations
//   - handleHistoryPage       GET /history
//   - handleStaticAsset       GET /static/{filename}
//   - handleNotFound          catch-all 404
//
// registerWebRoutes wires these handlers into the ServeMux and is called from
// registerRoutes (routes.go) after the API routes are registered.
//
// All web routes use logging → rateLimit middleware (same as read-only API
// routes). Write-action buttons are gated in the template via
// TemplateData.IsLocalhost, set by isLocalhostRequest.
package server

import (
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// safeFilename matches filenames safe to serve from /static/.
// Only alphanumeric, dot, dash, underscore; no path separators.
var safeFilename = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// staticContentTypes maps file extensions to MIME types.
var staticContentTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "application/javascript; charset=utf-8",
	".png": "image/png",
	".ico": "image/x-icon",
}

// ---------------------------------------------------------------------------
// registerWebRoutes
// ---------------------------------------------------------------------------

// registerWebRoutes attaches the web page handlers to mux. It is called by
// registerRoutes (routes.go) immediately after the API routes.
func (s *Server) registerWebRoutes(mux *http.ServeMux) {
	rl := newRateLimiter()

	read := func(h http.HandlerFunc) http.Handler {
		return logging(rateLimit(rl, h))
	}

	mux.Handle("GET /", read(s.handleOverview))
	mux.Handle("GET /reservations", read(s.handleReservationsPage))
	mux.Handle("GET /history", read(s.handleHistoryPage))
	mux.Handle("GET /static/{filename}", read(s.handleStaticAsset))
}

// ---------------------------------------------------------------------------
// handleOverview — GET /
// ---------------------------------------------------------------------------

// handleOverview renders the Overview page (§5.3).
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	// The root "/" pattern catches everything not matched by more specific
	// patterns; return 404 for any path other than "/".
	if r.URL.Path != "/" {
		s.handleNotFound(w, r)
		return
	}

	data := TemplateData{
		Title:       "Overview — piper",
		IsLocalhost: isLocalhostRequest(r),
		Now:         s.deps.now(),
		UFWActive:   true, // default; overridden below if snapshot available
		UFWReadable: true, // default; overridden below if snapshot available
	}

	snap, err := s.deps.Snap.LatestSnapshot(r.Context())
	if err == nil {
		data.LastScanAt = snap.ScannedAt
		data.UFWActive = snap.UFWActive
		data.UFWReadable = snap.UFWReadable

		// Collect all ports from the snapshot for the port table.
		portSet := make(map[int]struct{})
		for _, e := range snap.SS {
			portSet[e.Port] = struct{}{}
		}
		for _, d := range snap.Docker {
			for _, hp := range d.Ports {
				if hp.HostPort != 0 {
					portSet[hp.HostPort] = struct{}{}
				}
			}
		}

		ports := make([]int, 0, len(portSet))
		for p := range portSet {
			ports = append(ports, p)
		}
		sortInts(ports)

		if len(ports) > 0 {
			checker := service.Checker{
				Snap:     s.deps.Snap,
				Reserves: s.deps.Reserves,
			}
			result, err := checker.CheckPorts(r.Context(), ports)
			if err == nil {
				// Filter out free ports — overview only shows stateful ones.
				for _, ps := range result.Results {
					if ps.State != service.PortFree {
						data.Ports = append(data.Ports, ps)
					}
				}
			}
		}
	}

	render(w, "web/templates/overview.html", data)
}

// ---------------------------------------------------------------------------
// handleReservationsPage — GET /reservations
// ---------------------------------------------------------------------------

// handleReservationsPage renders the Reservations page (§5.4).
// Write buttons (+ New / del) are rendered only when IsLocalhost is true.
func (s *Server) handleReservationsPage(w http.ResponseWriter, r *http.Request) {
	data := TemplateData{
		Title:       "Reservations — piper",
		IsLocalhost: isLocalhostRequest(r),
		Now:         s.deps.now(),
		UFWActive:   true,
		UFWReadable: true,
	}

	snap, err := s.deps.Snap.LatestSnapshot(r.Context())
	if err == nil {
		data.UFWActive = snap.UFWActive
		data.UFWReadable = snap.UFWReadable
		data.LastScanAt = snap.ScannedAt
	}

	if s.deps.Reserves != nil {
		reservations, err := s.deps.Reserves.ListReservations(r.Context())
		if err == nil {
			data.Reservations = reservations
		}
	}

	render(w, "web/templates/reservations.html", data)
}

// ---------------------------------------------------------------------------
// handleHistoryPage — GET /history
// ---------------------------------------------------------------------------

// handleHistoryPage renders the History page (§5.5).
// Events are shown newest-first (QueryEvents orders by timestamp DESC).
func (s *Server) handleHistoryPage(w http.ResponseWriter, r *http.Request) {
	data := TemplateData{
		Title:       "History — piper",
		IsLocalhost: isLocalhostRequest(r),
		Now:         s.deps.now(),
		UFWActive:   true,
		UFWReadable: true,
	}

	snap, err := s.deps.Snap.LatestSnapshot(r.Context())
	if err == nil {
		data.UFWActive = snap.UFWActive
		data.UFWReadable = snap.UFWReadable
		data.LastScanAt = snap.ScannedAt
	}

	if s.deps.History != nil {
		hq := parseHistoryQueryParams(r, s.deps.now())
		events, err := s.deps.History.QueryEvents(r.Context(), hq)
		if err == nil {
			data.Events = events
		}
	}

	render(w, "web/templates/history.html", data)
}

// parseHistoryQueryParams builds a store.HistoryQuery from the URL query
// parameters on the history filter form.
func parseHistoryQueryParams(r *http.Request, now time.Time) store.HistoryQuery {
	q := r.URL.Query()

	days := 7
	if d, err := strconv.Atoi(q.Get("days")); err == nil && d > 0 {
		days = d
	}

	hq := store.HistoryQuery{
		Since: now.UTC().AddDate(0, 0, -days),
		Limit: 200,
	}

	if portStr := q.Get("port"); portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			hq.Port = &p
		}
	}

	if eventStr := q.Get("event"); eventStr != "" {
		hq.Event = store.EventType(eventStr)
	}

	return hq
}

// ---------------------------------------------------------------------------
// handleStaticAsset — GET /static/{filename}
// ---------------------------------------------------------------------------

// handleStaticAsset serves static files from the embedded web/static/ directory.
//
// Security:
//   - Only filenames matching [a-zA-Z0-9._-]+ are accepted (400 otherwise).
//   - Filenames containing "/" or "\" are rejected with 400.
func (s *Server) handleStaticAsset(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")

	// Reject path traversal and unsafe characters.
	if !safeFilename.MatchString(filename) || strings.ContainsAny(filename, "/\\") {
		http.Error(w, "bad request: invalid filename", http.StatusBadRequest)
		return
	}

	// Build the embed-FS path.
	fsPath := "web/static/" + filename

	data, err := webFS.ReadFile(fsPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Determine Content-Type by extension.
	ext := strings.ToLower(filepath.Ext(filename))
	ct, ok := staticContentTypes[ext]
	if !ok {
		ct = "application/octet-stream"
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// ---------------------------------------------------------------------------
// handleNotFound — catch-all 404
// ---------------------------------------------------------------------------

// handleNotFound serves a minimal 404 response for unrecognised routes.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "404 page not found", http.StatusNotFound)
}
