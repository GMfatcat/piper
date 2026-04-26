// Package server — handlers_api.go
//
// All /api/* HTTP handlers. Each handler:
//  1. Parses path parameters / query string / request body.
//  2. Validates input (port ranges, required fields, etc.).
//  3. Calls into Deps.
//  4. Maps domain errors to JSON error envelopes.
//  5. Writes the success JSON envelope.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ---------------------------------------------------------------------------
// handleHealth
// ---------------------------------------------------------------------------

// handleHealth serves GET /api/health.
// No dependencies required; always returns {"ok":true,"data":{"status":"ok"}}.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// handleScanLatest
// ---------------------------------------------------------------------------

// handleScanLatest serves GET /api/scan/latest.
// It retrieves the latest snapshot from Deps.Snap and runs CheckPorts over
// every port in the snapshot to build the full PortStatus list.
func (s *Server) handleScanLatest(w http.ResponseWriter, r *http.Request) {
	snap, err := s.deps.Snap.LatestSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "failed to retrieve snapshot: "+err.Error())
		return
	}

	// Collect the unique set of ports from all snapshot sources.
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

	// Sort ports for stable output (ascending).
	sortInts(ports)

	// Run Checker over all ports.
	checker := service.Checker{
		Snap:     s.deps.Snap,
		Reserves: s.deps.Reserves,
	}
	result, err := checker.CheckPorts(r.Context(), ports)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "check ports failed: "+err.Error())
		return
	}

	// Ensure results is never JSON null (use empty slice).
	if result.Results == nil {
		result.Results = []service.PortStatus{}
	}

	writeOK(w, result)
}

// ---------------------------------------------------------------------------
// handlePort
// ---------------------------------------------------------------------------

// handlePort serves GET /api/port/{port}.
// Returns a single PortStatus (not wrapped in an array).
func (s *Server) handlePort(w http.ResponseWriter, r *http.Request) {
	portStr := r.PathValue("port")
	port, err := strconv.Atoi(portStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, fmt.Sprintf("port %q is not a number", portStr))
		return
	}
	if port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, ErrCodePortOutOfRange, fmt.Sprintf("port %d is out of valid range [1, 65535]", port))
		return
	}

	checker := service.Checker{
		Snap:     s.deps.Snap,
		Reserves: s.deps.Reserves,
	}
	result, err := checker.CheckPorts(r.Context(), []int{port})
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "check port failed: "+err.Error())
		return
	}

	if len(result.Results) == 0 {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "unexpected empty result")
		return
	}

	// Return the single PortStatus (not an array).
	writeOK(w, result.Results[0])
}

// ---------------------------------------------------------------------------
// handleCheck
// ---------------------------------------------------------------------------

// handleCheck serves POST /api/check.
// Accepts {"ports": [8080, "9000-9005"]} — mixed numbers and range strings.
func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "failed to read request body")
		return
	}

	var req CheckRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if len(req.Ports) == 0 {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "ports field is required and must be non-empty")
		return
	}

	ports, err := parsePortsField(req.Ports)
	if err != nil {
		// Distinguish out-of-range from bad-format.
		if strings.Contains(err.Error(), "out of range") {
			writeError(w, http.StatusBadRequest, ErrCodePortOutOfRange, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		}
		return
	}

	checker := service.Checker{
		Snap:     s.deps.Snap,
		Reserves: s.deps.Reserves,
	}
	result, err := checker.CheckPorts(r.Context(), ports)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "check ports failed: "+err.Error())
		return
	}

	if result.Results == nil {
		result.Results = []service.PortStatus{}
	}

	writeOK(w, result)
}

// ---------------------------------------------------------------------------
// handleSuggest
// ---------------------------------------------------------------------------

// handleSuggest serves GET /api/suggest.
//
// Query params: n (default 1), from (default 8000), to (default 9999).
//
// Partial result handling: when ErrNoFreePortsFound is returned (fewer than n
// free ports exist), the handler still returns HTTP 200 with the partial
// suggested list plus a "notes" field containing
// "fewer than n free ports found". This keeps the envelope consistent
// (ok:true) and lets the caller decide how to display the partial result.
func (s *Server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	n, err := parseQueryInt(q.Get("n"), 1)
	if err != nil || n < 1 {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "n must be a positive integer")
		return
	}

	from, err := parseQueryInt(q.Get("from"), 8000)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "from must be an integer")
		return
	}

	to, err := parseQueryInt(q.Get("to"), 9999)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "to must be an integer")
		return
	}

	// Fetch snapshot once to get scanned_at.
	snap, err := s.deps.Snap.LatestSnapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "snapshot unavailable: "+err.Error())
		return
	}

	suggester := service.Suggester{
		Snap:     s.deps.Snap,
		Reserves: s.deps.Reserves,
	}

	suggested, sugErr := suggester.SuggestFreePorts(r.Context(), n, from, to)
	// suggester may return a non-nil error even on partial success.

	// Handle validation errors (from, to, n bounds).
	if sugErr != nil && !errors.Is(sugErr, service.ErrNoFreePortsFound) {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, sugErr.Error())
		return
	}

	resp := SuggestResponse{
		Suggested: suggested,
		SearchRange: SuggestRangeField{
			From: from,
			To:   to,
		},
		ScannedAt: snap.ScannedAt,
	}

	// Ensure suggested is never JSON null.
	if resp.Suggested == nil {
		resp.Suggested = []int{}
	}

	// Attach a note when fewer than n ports were found.
	if errors.Is(sugErr, service.ErrNoFreePortsFound) {
		resp.Notes = []string{fmt.Sprintf("fewer than %d free ports found in range [%d, %d]", n, from, to)}
	}

	writeOK(w, resp)
}

// ---------------------------------------------------------------------------
// handleListReservations
// ---------------------------------------------------------------------------

// handleListReservations serves GET /api/reservations.
func (s *Server) handleListReservations(w http.ResponseWriter, r *http.Request) {
	reservations, err := s.deps.Reserves.ListReservations(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "list reservations failed: "+err.Error())
		return
	}

	resp := make([]ReservationResponse, len(reservations))
	for i, res := range reservations {
		resp[i] = reservationToResponse(res)
	}

	writeOK(w, resp)
}

// ---------------------------------------------------------------------------
// handleCreateReservation
// ---------------------------------------------------------------------------

// handleCreateReservation serves POST /api/reservations (localhost-only via middleware).
//
// created_by is populated from os/user.Current().Username. If the OS call
// fails (e.g. sandboxed environment), created_by is left blank.
func (s *Server) handleCreateReservation(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "failed to read request body")
		return
	}

	var req CreateReservationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Port < 1 || req.Port > 65535 {
		writeError(w, http.StatusBadRequest, ErrCodePortOutOfRange, fmt.Sprintf("port %d is out of valid range [1, 65535]", req.Port))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "name is required")
		return
	}

	// Resolve created_by from OS user; fall back to empty string on error.
	createdBy := ""
	if u, err := user.Current(); err == nil {
		createdBy = u.Username
	}

	reservation := store.Reservation{
		Port:      req.Port,
		Name:      req.Name,
		Note:      req.Note,
		CreatedAt: s.deps.now().UTC(),
		CreatedBy: createdBy,
	}

	if err := s.deps.Reserves.InsertReservation(r.Context(), reservation); err != nil {
		if errors.Is(err, store.ErrReservationExists) {
			writeError(w, http.StatusConflict, ErrCodeReservationExists,
				fmt.Sprintf("port %d is already reserved", req.Port))
			return
		}
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "insert reservation failed: "+err.Error())
		return
	}

	writeOK(w, reservationToResponse(reservation))
}

// ---------------------------------------------------------------------------
// handleDeleteReservation
// ---------------------------------------------------------------------------

// handleDeleteReservation serves DELETE /api/reservations/{port} (localhost-only via middleware).
func (s *Server) handleDeleteReservation(w http.ResponseWriter, r *http.Request) {
	portStr := r.PathValue("port")
	port, err := strconv.Atoi(portStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, fmt.Sprintf("port %q is not a number", portStr))
		return
	}
	if port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, ErrCodePortOutOfRange, fmt.Sprintf("port %d is out of valid range [1, 65535]", port))
		return
	}

	if err := s.deps.Reserves.DeleteReservation(r.Context(), port); err != nil {
		if errors.Is(err, store.ErrReservationNotFound) {
			writeError(w, http.StatusNotFound, ErrCodeNotFound,
				fmt.Sprintf("no reservation found for port %d", port))
			return
		}
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "delete reservation failed: "+err.Error())
		return
	}

	writeOK(w, map[string]any{"port": port, "deleted": true})
}

// ---------------------------------------------------------------------------
// handleHistory
// ---------------------------------------------------------------------------

// handleHistory serves GET /api/history.
//
// Query params:
//   - port  (optional int)
//   - days  (optional int, default 7)
//   - event (optional string: occupied|released|reserved|unreserved)
//   - limit (optional int, default 50)
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	days, err := parseQueryInt(q.Get("days"), 7)
	if err != nil || days < 1 {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "days must be a positive integer")
		return
	}

	limit, err := parseQueryInt(q.Get("limit"), 50)
	if err != nil || limit < 1 {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "limit must be a positive integer")
		return
	}

	hq := store.HistoryQuery{
		Since: s.deps.now().UTC().AddDate(0, 0, -days),
		Limit: limit,
	}

	// Optional port filter.
	if portStr := q.Get("port"); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "port must be an integer")
			return
		}
		hq.Port = &port
	}

	// Optional event filter.
	if eventStr := q.Get("event"); eventStr != "" {
		hq.Event = store.EventType(eventStr)
	}

	events, err := s.deps.History.QueryEvents(r.Context(), hq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "query history failed: "+err.Error())
		return
	}

	resp := HistoryResponse{Events: make([]EventResponse, len(events))}
	for i, ev := range events {
		resp.Events[i] = EventResponse{
			ID:        ev.ID,
			Port:      ev.Port,
			Event:     string(ev.Event),
			Occupant:  ev.Occupant,
			Timestamp: ev.Timestamp.UTC().Format(time.RFC3339),
		}
	}

	writeOK(w, resp)
}

// ---------------------------------------------------------------------------
// handleScanTrigger
// ---------------------------------------------------------------------------

// handleScanTrigger serves POST /api/scan/trigger (localhost-only via middleware).
//
// If Deps.OnRefresh is nil, responds with 503 NOT_SUPPORTED per §6.3.
func (s *Server) handleScanTrigger(w http.ResponseWriter, r *http.Request) {
	if s.deps.OnRefresh == nil {
		writeError(w, http.StatusServiceUnavailable, ErrCodeNotSupported,
			"scan trigger requires --serve to be running with refresh handler")
		return
	}

	if err := s.deps.OnRefresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternal, "scan trigger failed: "+err.Error())
		return
	}

	writeOK(w, map[string]any{"triggered": true})
}

// ---------------------------------------------------------------------------
// Port-parsing helper
// ---------------------------------------------------------------------------

// parsePortsField parses the JSON "ports" field, which can contain a mix of:
//   - float64 values (JSON numbers), interpreted as integer port numbers
//   - string values, either "8080" (bare port) or "8080-8085" (range)
//
// Mirrors cli.ParsePortArgs semantics. Returns 400 BAD_REQUEST on invalid input.
func parsePortsField(raw []any) ([]int, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("ports list is empty")
	}

	var out []int

	for _, item := range raw {
		switch v := item.(type) {
		case float64:
			// JSON numbers decode as float64.
			port := int(v)
			if err := validatePort(port); err != nil {
				return nil, err
			}
			out = append(out, port)

		case string:
			v = strings.TrimSpace(v)
			if strings.Contains(v, "-") {
				// Range: "8080-8085"
				parts := strings.SplitN(v, "-", 2)
				if len(parts) != 2 {
					return nil, fmt.Errorf("invalid port range %q", v)
				}
				start, err := parsePortStr(parts[0])
				if err != nil {
					return nil, fmt.Errorf("invalid range %q: %w", v, err)
				}
				end, err := parsePortStr(parts[1])
				if err != nil {
					return nil, fmt.Errorf("invalid range %q: %w", v, err)
				}
				if start > end {
					return nil, fmt.Errorf("invalid range %q: start > end", v)
				}
				for p := start; p <= end; p++ {
					out = append(out, p)
				}
			} else {
				// Bare port string: "8080"
				port, err := parsePortStr(v)
				if err != nil {
					return nil, err
				}
				out = append(out, port)
			}

		default:
			return nil, fmt.Errorf("unsupported port value type %T", item)
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no valid ports parsed")
	}
	return out, nil
}

// parsePortStr converts a string to an int port number with range validation.
func parsePortStr(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("port %q is not a number", s)
	}
	return n, validatePort(n)
}

// validatePort returns an error if port is outside [1, 65535].
func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d out of range [1, 65535]", port)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Misc helpers
// ---------------------------------------------------------------------------

// parseQueryInt parses a query parameter as int, returning defaultVal when
// the parameter is empty. Returns an error if the value is present but not
// a valid integer.
func parseQueryInt(s string, defaultVal int) (int, error) {
	if s == "" {
		return defaultVal, nil
	}
	return strconv.Atoi(s)
}

// reservationToResponse converts a store.Reservation to the API wire shape.
func reservationToResponse(r store.Reservation) ReservationResponse {
	return ReservationResponse{
		Port:      r.Port,
		Name:      r.Name,
		Note:      r.Note,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		CreatedBy: r.CreatedBy,
	}
}

// sortInts sorts a []int slice in ascending order without importing sort
// (avoids a potentially heavy import for a small use-case; simple insertion
// sort is fine for the ~few-hundred-port counts we'll see).
func sortInts(s []int) {
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j] > key {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}
