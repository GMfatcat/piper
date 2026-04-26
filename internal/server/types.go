// Package server implements the HTTP API layer for Piper.
//
// This file defines request/response shapes, the JSON envelope, error codes,
// and JSON write helpers used by all handlers.
package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------
// Envelope
// ---------------------------------------------------------------------------

// Envelope is the §6.2 top-level response wrapper.
// Every API response is either {ok:true,data:...} or {ok:false,error:{...}}.
type Envelope struct {
	OK    bool      `json:"ok"`
	Data  any       `json:"data,omitempty"`
	Error *APIError `json:"error,omitempty"`
}

// APIError carries a machine-readable code and a human-readable message.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Error codes
// ---------------------------------------------------------------------------

const (
	ErrCodeBadRequest        = "BAD_REQUEST"
	ErrCodeNotFound          = "NOT_FOUND"
	ErrCodeForbidden         = "FORBIDDEN"
	ErrCodeReservationExists = "RESERVATION_EXISTS"
	ErrCodeInternal          = "INTERNAL"
	ErrCodeTooManyRequests   = "TOO_MANY_REQUESTS"
	ErrCodePortOutOfRange    = "PORT_OUT_OF_RANGE"
	ErrCodeNotSupported      = "NOT_SUPPORTED"
)

// ---------------------------------------------------------------------------
// JSON write helpers
// ---------------------------------------------------------------------------

// writeJSON marshals env to JSON and writes it to w with the given HTTP status.
// Content-Type is always set to application/json.
func writeJSON(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(env) // ignore write errors — connection may have closed
}

// writeError sends a {ok:false,error:{code,message}} envelope.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, Envelope{
		OK:    false,
		Error: &APIError{Code: code, Message: msg},
	})
}

// writeOK sends a {ok:true,data:data} envelope with HTTP 200.
func writeOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, Envelope{OK: true, Data: data})
}

// ---------------------------------------------------------------------------
// API request shapes
// ---------------------------------------------------------------------------

// CheckRequest is the POST /api/check request body.
// The ports field is []any to support mixed integer and string range values,
// e.g. [8080, "9000-9005"].
type CheckRequest struct {
	Ports []any `json:"ports"`
}

// CreateReservationRequest is the POST /api/reservations request body.
type CreateReservationRequest struct {
	Port int    `json:"port"`
	Name string `json:"name"`
	Note string `json:"note"`
}

// ---------------------------------------------------------------------------
// API response shapes
// ---------------------------------------------------------------------------

// ScanLatestResponse is the data field for GET /api/scan/latest.
type ScanLatestResponse struct {
	ScannedAt time.Time   `json:"scanned_at"`
	Results   interface{} `json:"results"`
}

// SuggestResponse is the data field for GET /api/suggest.
// When fewer than n free ports are found, Notes carries an explanation.
type SuggestResponse struct {
	Suggested   []int             `json:"suggested"`
	SearchRange SuggestRangeField `json:"search_range"`
	ScannedAt   time.Time         `json:"scanned_at"`
	// Notes carries human-readable notices (e.g. "fewer than n free ports found").
	// Omitted when empty so successful responses stay clean.
	Notes []string `json:"notes,omitempty"`
}

// SuggestRangeField describes the port range that was searched.
type SuggestRangeField struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// ReservationResponse is the JSON shape for a single reservation returned by
// the API. It uses explicit string timestamps so the format is unambiguous.
type ReservationResponse struct {
	Port      int    `json:"port"`
	Name      string `json:"name"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
	CreatedBy string `json:"created_by"`
}

// HistoryResponse is the data field for GET /api/history.
type HistoryResponse struct {
	Events []EventResponse `json:"events"`
}

// EventResponse is the JSON shape for a single history event.
type EventResponse struct {
	ID        int64  `json:"id"`
	Port      int    `json:"port"`
	Event     string `json:"event"`
	Occupant  string `json:"occupant"`
	Timestamp string `json:"timestamp"`
}
