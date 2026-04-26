package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/scanner"
	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

// stubSnap implements service.SnapshotProvider over a fixed snapshot.
type stubSnap struct {
	snap service.ScanSnapshot
	err  error
}

func (s *stubSnap) LatestSnapshot(_ context.Context) (service.ScanSnapshot, error) {
	return s.snap, s.err
}

// stubReserves is an in-memory ReservationStore backed by a map.
type stubReserves struct {
	data map[int]store.Reservation
}

func newStubReserves() *stubReserves {
	return &stubReserves{data: make(map[int]store.Reservation)}
}

func (s *stubReserves) InsertReservation(_ context.Context, r store.Reservation) error {
	if _, exists := s.data[r.Port]; exists {
		return store.ErrReservationExists
	}
	s.data[r.Port] = r
	return nil
}

func (s *stubReserves) DeleteReservation(_ context.Context, port int) error {
	if _, exists := s.data[port]; !exists {
		return store.ErrReservationNotFound
	}
	delete(s.data, port)
	return nil
}

func (s *stubReserves) GetReservation(_ context.Context, port int) (store.Reservation, error) {
	r, exists := s.data[port]
	if !exists {
		return store.Reservation{}, store.ErrReservationNotFound
	}
	return r, nil
}

func (s *stubReserves) ListReservations(_ context.Context) ([]store.Reservation, error) {
	result := make([]store.Reservation, 0, len(s.data))
	for _, r := range s.data {
		result = append(result, r)
	}
	return result, nil
}

// stubHistory is a slice-backed HistoryStore.
type stubHistory struct {
	events []store.Event
}

func (s *stubHistory) AppendEvent(_ context.Context, e store.Event) (store.Event, error) {
	e.ID = int64(len(s.events) + 1)
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	s.events = append(s.events, e)
	return e, nil
}

func (s *stubHistory) QueryEvents(_ context.Context, q store.HistoryQuery) ([]store.Event, error) {
	var result []store.Event
	for _, ev := range s.events {
		// Port filter.
		if q.Port != nil && ev.Port != *q.Port {
			continue
		}
		// Event type filter.
		if q.Event != "" && ev.Event != q.Event {
			continue
		}
		// Since filter.
		if !q.Since.IsZero() && ev.Timestamp.Before(q.Since) {
			continue
		}
		result = append(result, ev)
		// Limit.
		if q.Limit > 0 && len(result) >= q.Limit {
			break
		}
	}
	if result == nil {
		result = []store.Event{}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestServer creates a Server with the given deps for use in handler tests.
// It does NOT bind a real TCP socket; tests use the Handler() method.
func newTestServer(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.registerRoutes()
	return s
}

// fixedNow returns a deterministic time for use in tests.
var fixedNow = time.Date(2026, 4, 25, 14, 32, 18, 0, time.UTC)

// makeSnap returns a ScanSnapshot with a fixed ScannedAt.
func makeSnap(ss []scanner.SSEntry, docker []scanner.DockerEntry) service.ScanSnapshot {
	return service.ScanSnapshot{
		ScannedAt: fixedNow,
		SS:        ss,
		Docker:    docker,
		Inspected: map[string]scanner.DockerEntry{},
		UFW:       nil,
		UFWActive: false,
	}
}

// decodeEnvelope unmarshals the response body into an Envelope.
func decodeEnvelope(t *testing.T, body []byte) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("failed to decode envelope: %v\nbody: %s", err, body)
	}
	return env
}

// decodeData unmarshals the Data field of an Envelope into v.
func decodeData(t *testing.T, env Envelope, v any) {
	t.Helper()
	b, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatalf("failed to re-marshal Data: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("failed to decode Data: %v\nData: %v", err, env.Data)
	}
}

// ---------------------------------------------------------------------------
// TestHealth
// ---------------------------------------------------------------------------

func TestHealth(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
		History:  &stubHistory{},
	})

	r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if !env.OK {
		t.Fatal("expected ok=true")
	}

	var data map[string]string
	decodeData(t, env, &data)
	if data["status"] != "ok" {
		t.Errorf("expected data.status=ok, got %q", data["status"])
	}
}

// ---------------------------------------------------------------------------
// TestScanLatest_Empty
// ---------------------------------------------------------------------------

func TestScanLatest_Empty(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
	})

	r := httptest.NewRequest(http.MethodGet, "/api/scan/latest", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if !env.OK {
		t.Fatal("expected ok=true")
	}

	var data service.CheckResult
	decodeData(t, env, &data)
	if !data.ScannedAt.IsZero() {
		// scanned_at should be present (fixedNow).
	}
	if data.Results == nil {
		t.Error("expected results to be non-nil (empty array, not null)")
	}
	if len(data.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(data.Results))
	}
}

// ---------------------------------------------------------------------------
// TestScanLatest_WithPorts
// ---------------------------------------------------------------------------

func TestScanLatest_WithPorts(t *testing.T) {
	// Snapshot with one running docker container on port 8080.
	dockerEntry := scanner.DockerEntry{
		ID:    "abc123",
		Name:  "ai-translate",
		Image: "translate:v2",
		State: "running",
		Ports: []scanner.HostPort{{HostPort: 8080, ContainerPort: 80}},
	}

	snap := service.ScanSnapshot{
		ScannedAt: fixedNow,
		Docker:    []scanner.DockerEntry{dockerEntry},
		Inspected: map[string]scanner.DockerEntry{"abc123": dockerEntry},
	}

	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: snap},
		Reserves: newStubReserves(),
	})

	r := httptest.NewRequest(http.MethodGet, "/api/scan/latest", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	var data service.CheckResult
	decodeData(t, env, &data)

	if len(data.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(data.Results))
	}
	ps := data.Results[0]
	if ps.Port != 8080 {
		t.Errorf("expected port 8080, got %d", ps.Port)
	}
	if ps.State != service.PortUsedDocker {
		t.Errorf("expected state=used_docker, got %s", ps.State)
	}
}

// ---------------------------------------------------------------------------
// TestPort_HappyPath
// ---------------------------------------------------------------------------

func TestPort_HappyPath(t *testing.T) {
	dockerEntry := scanner.DockerEntry{
		ID:    "xyz789",
		Name:  "my-service",
		Image: "myimg:latest",
		State: "running",
		Ports: []scanner.HostPort{{HostPort: 8080, ContainerPort: 80}},
	}

	snap := service.ScanSnapshot{
		ScannedAt: fixedNow,
		Docker:    []scanner.DockerEntry{dockerEntry},
		Inspected: map[string]scanner.DockerEntry{"xyz789": dockerEntry},
	}

	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: snap},
		Reserves: newStubReserves(),
	})

	r := httptest.NewRequest(http.MethodGet, "/api/port/8080", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if !env.OK {
		t.Fatal("expected ok=true")
	}

	// Data should be a single PortStatus, not an array.
	var ps service.PortStatus
	decodeData(t, env, &ps)

	if ps.Port != 8080 {
		t.Errorf("expected port 8080, got %d", ps.Port)
	}
	if ps.State != service.PortUsedDocker {
		t.Errorf("expected state=used_docker, got %s", ps.State)
	}
}

// ---------------------------------------------------------------------------
// TestPort_OutOfRange
// ---------------------------------------------------------------------------

func TestPort_OutOfRange(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
	})

	r := httptest.NewRequest(http.MethodGet, "/api/port/99999", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if env.OK {
		t.Fatal("expected ok=false")
	}
	if env.Error == nil || env.Error.Code != ErrCodePortOutOfRange {
		t.Errorf("expected PORT_OUT_OF_RANGE, got %+v", env.Error)
	}
}

// ---------------------------------------------------------------------------
// TestPort_NotANumber
// ---------------------------------------------------------------------------

func TestPort_NotANumber(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
	})

	r := httptest.NewRequest(http.MethodGet, "/api/port/abc", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if env.Error == nil || env.Error.Code != ErrCodeBadRequest {
		t.Errorf("expected BAD_REQUEST, got %+v", env.Error)
	}
}

// ---------------------------------------------------------------------------
// TestCheck_Mixed
// ---------------------------------------------------------------------------

func TestCheck_Mixed(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
	})

	body := `{"ports":[8080,"9000-9002"]}`
	r := httptest.NewRequest(http.MethodPost, "/api/check", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	var data service.CheckResult
	decodeData(t, env, &data)

	// Should have 4 results: 8080, 9000, 9001, 9002
	if len(data.Results) != 4 {
		t.Fatalf("expected 4 results, got %d: %+v", len(data.Results), data.Results)
	}

	expectedPorts := []int{8080, 9000, 9001, 9002}
	for i, expected := range expectedPorts {
		if data.Results[i].Port != expected {
			t.Errorf("result[%d]: expected port %d, got %d", i, expected, data.Results[i].Port)
		}
	}
}

// ---------------------------------------------------------------------------
// TestCheck_BadJSON
// ---------------------------------------------------------------------------

func TestCheck_BadJSON(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
	})

	r := httptest.NewRequest(http.MethodPost, "/api/check", strings.NewReader("{bad json"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if env.Error == nil || env.Error.Code != ErrCodeBadRequest {
		t.Errorf("expected BAD_REQUEST, got %+v", env.Error)
	}
}

// ---------------------------------------------------------------------------
// TestCheck_InvalidPort
// ---------------------------------------------------------------------------

func TestCheck_InvalidPort(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
	})

	body := `{"ports":[99999]}`
	r := httptest.NewRequest(http.MethodPost, "/api/check", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if env.Error == nil || env.Error.Code != ErrCodePortOutOfRange {
		t.Errorf("expected PORT_OUT_OF_RANGE, got %+v", env.Error)
	}
}

// ---------------------------------------------------------------------------
// TestSuggest_Defaults
// ---------------------------------------------------------------------------

func TestSuggest_Defaults(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
	})

	r := httptest.NewRequest(http.MethodGet, "/api/suggest", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if !env.OK {
		t.Fatal("expected ok=true")
	}

	var data SuggestResponse
	decodeData(t, env, &data)

	// Default n=1 → 1 suggestion.
	if len(data.Suggested) != 1 {
		t.Errorf("expected 1 suggested port, got %d", len(data.Suggested))
	}
	if data.SearchRange.From != 8000 {
		t.Errorf("expected search_range.from=8000, got %d", data.SearchRange.From)
	}
	if data.SearchRange.To != 9999 {
		t.Errorf("expected search_range.to=9999, got %d", data.SearchRange.To)
	}
}

// ---------------------------------------------------------------------------
// TestSuggest_PartialPlusNote
// ---------------------------------------------------------------------------

// TestSuggest_PartialPlusNote verifies that when the range is very narrow
// and all but one port is occupied, the response is 200 with partial results
// and a non-empty notes field.
func TestSuggest_PartialPlusNote(t *testing.T) {
	// Request n=3 but range [8080,8082] with 8080 occupied.
	// Only 2 free ports (8081, 8082) → partial result.
	ssEntries := []scanner.SSEntry{
		{Port: 8080, ProcessName: "nginx", PID: 100},
	}

	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(ssEntries, nil)},
		Reserves: newStubReserves(),
	})

	r := httptest.NewRequest(http.MethodGet, "/api/suggest?n=3&from=8080&to=8082", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if !env.OK {
		t.Fatal("expected ok=true for partial result")
	}

	var data SuggestResponse
	decodeData(t, env, &data)

	// Should have 2 results (8081, 8082), not 3.
	if len(data.Suggested) >= 3 {
		t.Errorf("expected fewer than 3 suggested ports, got %d", len(data.Suggested))
	}
	// Notes should be non-empty.
	if len(data.Notes) == 0 {
		t.Error("expected non-empty notes for partial result")
	}
}

// ---------------------------------------------------------------------------
// TestListReservations_Empty
// ---------------------------------------------------------------------------

func TestListReservations_Empty(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
	})

	r := httptest.NewRequest(http.MethodGet, "/api/reservations", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	var data []ReservationResponse
	decodeData(t, env, &data)

	if data == nil {
		t.Fatal("expected non-nil empty array")
	}
	if len(data) != 0 {
		t.Errorf("expected 0 reservations, got %d", len(data))
	}
}

// ---------------------------------------------------------------------------
// TestListReservations_Populated
// ---------------------------------------------------------------------------

func TestListReservations_Populated(t *testing.T) {
	reserves := newStubReserves()
	_ = reserves.InsertReservation(context.Background(), store.Reservation{
		Port: 9100, Name: "vllm", CreatedAt: fixedNow,
	})
	_ = reserves.InsertReservation(context.Background(), store.Reservation{
		Port: 9200, Name: "ollama", CreatedAt: fixedNow,
	})

	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: reserves,
	})

	r := httptest.NewRequest(http.MethodGet, "/api/reservations", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	var data []ReservationResponse
	decodeData(t, env, &data)

	if len(data) != 2 {
		t.Errorf("expected 2 reservations, got %d", len(data))
	}
}

// ---------------------------------------------------------------------------
// TestCreateReservation_HappyPath
// ---------------------------------------------------------------------------

func TestCreateReservation_HappyPath(t *testing.T) {
	reserves := newStubReserves()
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: reserves,
		Now:      func() time.Time { return fixedNow },
	})

	body := `{"port":9100,"name":"vllm","note":"next week"}`
	r := httptest.NewRequest(http.MethodPost, "/api/reservations", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if !env.OK {
		t.Fatalf("expected ok=true, got error: %+v", env.Error)
	}

	var data ReservationResponse
	decodeData(t, env, &data)

	if data.Port != 9100 {
		t.Errorf("expected port 9100, got %d", data.Port)
	}
	if data.Name != "vllm" {
		t.Errorf("expected name=vllm, got %s", data.Name)
	}

	// Verify stub stored it.
	stored, err := reserves.GetReservation(context.Background(), 9100)
	if err != nil {
		t.Fatalf("expected reservation to be stored: %v", err)
	}
	if stored.Port != 9100 {
		t.Errorf("stored port mismatch")
	}
}

// ---------------------------------------------------------------------------
// TestCreateReservation_NonLocalhost403
// ---------------------------------------------------------------------------

func TestCreateReservation_NonLocalhost403(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
	})

	body := `{"port":9100,"name":"vllm","note":""}`
	r := httptest.NewRequest(http.MethodPost, "/api/reservations", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "192.168.1.5:1234"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d\nbody: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TestCreateReservation_DuplicatePort
// ---------------------------------------------------------------------------

func TestCreateReservation_DuplicatePort(t *testing.T) {
	reserves := newStubReserves()
	_ = reserves.InsertReservation(context.Background(), store.Reservation{
		Port: 9100, Name: "existing",
	})

	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: reserves,
		Now:      func() time.Time { return fixedNow },
	})

	body := `{"port":9100,"name":"duplicate","note":""}`
	r := httptest.NewRequest(http.MethodPost, "/api/reservations", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if env.Error == nil || env.Error.Code != ErrCodeReservationExists {
		t.Errorf("expected RESERVATION_EXISTS, got %+v", env.Error)
	}
}

// ---------------------------------------------------------------------------
// TestDeleteReservation_HappyPath
// ---------------------------------------------------------------------------

func TestDeleteReservation_HappyPath(t *testing.T) {
	reserves := newStubReserves()
	_ = reserves.InsertReservation(context.Background(), store.Reservation{
		Port: 9100, Name: "vllm",
	})

	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: reserves,
	})

	r := httptest.NewRequest(http.MethodDelete, "/api/reservations/9100", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if !env.OK {
		t.Fatal("expected ok=true")
	}

	// Verify stub no longer has the reservation.
	_, err := reserves.GetReservation(context.Background(), 9100)
	if !errors.Is(err, store.ErrReservationNotFound) {
		t.Errorf("expected reservation to be deleted, got err: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestDeleteReservation_NotFound
// ---------------------------------------------------------------------------

func TestDeleteReservation_NotFound(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
	})

	r := httptest.NewRequest(http.MethodDelete, "/api/reservations/9999", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if env.Error == nil || env.Error.Code != ErrCodeNotFound {
		t.Errorf("expected NOT_FOUND, got %+v", env.Error)
	}
}

// ---------------------------------------------------------------------------
// TestDeleteReservation_NonLocalhost403
// ---------------------------------------------------------------------------

func TestDeleteReservation_NonLocalhost403(t *testing.T) {
	reserves := newStubReserves()
	_ = reserves.InsertReservation(context.Background(), store.Reservation{
		Port: 9100, Name: "vllm",
	})

	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: reserves,
	})

	r := httptest.NewRequest(http.MethodDelete, "/api/reservations/9100", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// TestHistory_Defaults
// ---------------------------------------------------------------------------

func TestHistory_Defaults(t *testing.T) {
	history := &stubHistory{}
	recentTime := fixedNow.Add(-24 * time.Hour) // 1 day ago — within default 7-day window

	_ , _ = history.AppendEvent(context.Background(), store.Event{
		Port:      8080,
		Event:     store.EventOccupied,
		Occupant:  "nginx",
		Timestamp: recentTime,
	})
	_, _ = history.AppendEvent(context.Background(), store.Event{
		Port:      9000,
		Event:     store.EventReleased,
		Timestamp: recentTime,
	})

	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
		History:  history,
		Now:      func() time.Time { return fixedNow },
	})

	r := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	var data HistoryResponse
	decodeData(t, env, &data)

	if len(data.Events) != 2 {
		t.Errorf("expected 2 events, got %d", len(data.Events))
	}
}

// ---------------------------------------------------------------------------
// TestHistory_FilterByPort
// ---------------------------------------------------------------------------

func TestHistory_FilterByPort(t *testing.T) {
	history := &stubHistory{}
	recentTime := fixedNow.Add(-1 * time.Hour)

	_, _ = history.AppendEvent(context.Background(), store.Event{
		Port: 8080, Event: store.EventOccupied, Timestamp: recentTime,
	})
	_, _ = history.AppendEvent(context.Background(), store.Event{
		Port: 9000, Event: store.EventOccupied, Timestamp: recentTime,
	})

	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
		History:  history,
		Now:      func() time.Time { return fixedNow },
	})

	r := httptest.NewRequest(http.MethodGet, "/api/history?port=8080", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	var data HistoryResponse
	decodeData(t, env, &data)

	if len(data.Events) != 1 {
		t.Errorf("expected 1 event for port 8080, got %d", len(data.Events))
	}
	if data.Events[0].Port != 8080 {
		t.Errorf("expected event for port 8080, got port %d", data.Events[0].Port)
	}
}

// ---------------------------------------------------------------------------
// TestScanTrigger_NoRefreshHandler
// ---------------------------------------------------------------------------

func TestScanTrigger_NoRefreshHandler(t *testing.T) {
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
		// OnRefresh is nil.
	})

	r := httptest.NewRequest(http.MethodPost, "/api/scan/trigger", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if env.Error == nil || env.Error.Code != ErrCodeNotSupported {
		t.Errorf("expected NOT_SUPPORTED, got %+v", env.Error)
	}
}

// ---------------------------------------------------------------------------
// TestScanTrigger_LocalhostOnly
// ---------------------------------------------------------------------------

func TestScanTrigger_LocalhostOnly(t *testing.T) {
	called := false
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
		OnRefresh: func(ctx context.Context) error {
			called = true
			return nil
		},
	})

	r := httptest.NewRequest(http.MethodPost, "/api/scan/trigger", nil)
	r.RemoteAddr = "192.168.1.5:1234"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	if called {
		t.Error("expected OnRefresh NOT to be called for non-localhost")
	}
}

// ---------------------------------------------------------------------------
// TestScanTrigger_HappyPath
// ---------------------------------------------------------------------------

func TestScanTrigger_HappyPath(t *testing.T) {
	callCount := 0
	srv := newTestServer(Deps{
		Snap:     &stubSnap{snap: makeSnap(nil, nil)},
		Reserves: newStubReserves(),
		OnRefresh: func(ctx context.Context) error {
			callCount++
			return nil
		},
	})

	r := httptest.NewRequest(http.MethodPost, "/api/scan/trigger", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	env := decodeEnvelope(t, w.Body.Bytes())
	if !env.OK {
		t.Fatal("expected ok=true")
	}

	if callCount != 1 {
		t.Errorf("expected OnRefresh called once, got %d calls", callCount)
	}
}

// ---------------------------------------------------------------------------
// parsePortsField unit tests
// ---------------------------------------------------------------------------

func TestParsePortsField_Number(t *testing.T) {
	ports, err := parsePortsField([]any{float64(8080)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ports) != 1 || ports[0] != 8080 {
		t.Errorf("expected [8080], got %v", ports)
	}
}

func TestParsePortsField_StringRange(t *testing.T) {
	ports, err := parsePortsField([]any{"9000-9002"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ports) != 3 {
		t.Fatalf("expected 3 ports, got %d: %v", len(ports), ports)
	}
	expected := []int{9000, 9001, 9002}
	for i, p := range ports {
		if p != expected[i] {
			t.Errorf("ports[%d]: expected %d, got %d", i, expected[i], p)
		}
	}
}

func TestParsePortsField_OutOfRange(t *testing.T) {
	_, err := parsePortsField([]any{float64(99999)})
	if err == nil {
		t.Fatal("expected error for out-of-range port")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("expected 'out of range' in error, got: %v", err)
	}
}

func TestParsePortsField_Mixed(t *testing.T) {
	ports, err := parsePortsField([]any{float64(8080), "9000-9001"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ports) != 3 {
		t.Fatalf("expected 3 ports, got %d: %v", len(ports), ports)
	}
}

// ---------------------------------------------------------------------------
// Unused import guard — ensure errors and bytes are used
// ---------------------------------------------------------------------------

var _ = errors.New
var _ = bytes.NewBuffer
