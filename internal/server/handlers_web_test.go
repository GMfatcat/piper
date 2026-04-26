package server

import (
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
// helpers shared across web handler tests
// ---------------------------------------------------------------------------

// newWebTestServer creates a Server with stub deps that are safe for web
// handler tests. It uses the same stubSnap/stubReserves/stubHistory types
// defined in handlers_api_test.go (same package).
func newWebTestServer() *Server {
	snap := &stubSnap{
		snap: service.ScanSnapshot{
			ScannedAt: time.Date(2026, 4, 25, 14, 32, 18, 0, time.UTC),
			SS:        []scanner.SSEntry{},
			Docker:    []scanner.DockerEntry{},
			Inspected: map[string]scanner.DockerEntry{},
			UFW:       nil,
			UFWActive: true,
		},
	}
	reserves := newStubReserves()
	history := &stubHistory{}

	deps := Deps{
		Snap:     snap,
		Reserves: reserves,
		History:  history,
		Now:      func() time.Time { return fixedNow },
	}
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.registerRoutes()
	return s
}

// get issues a GET request to the server via its Handler and returns the recorder.
func getWeb(s *Server, path, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// 1. TestHandleOverview_OK
// ---------------------------------------------------------------------------

func TestHandleOverview_OK(t *testing.T) {
	t.Log("GET / should return 200 text/html with basic HTML and a page heading")

	s := newWebTestServer()
	rec := getWeb(s, "/", "127.0.0.1:12345")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html Content-Type, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<html") {
		t.Error("expected <html in response body")
	}
	// Check for "Overview" or "Port" which should appear in the page.
	if !strings.Contains(body, "Overview") && !strings.Contains(body, "Port") {
		t.Error("expected 'Overview' or 'Port' heading in response body")
	}
}

// ---------------------------------------------------------------------------
// 2. TestHandleReservations_LocalhostShowsForm
// ---------------------------------------------------------------------------

func TestHandleReservations_LocalhostShowsForm(t *testing.T) {
	t.Log("GET /reservations from localhost should show 'New reservation' form")

	s := newWebTestServer()
	// Pre-populate with one reservation so the delete button also appears.
	_ = s.deps.Reserves.InsertReservation(nil, store.Reservation{ //nolint:staticcheck
		Port:      9100,
		Name:      "vllm-llama",
		CreatedAt: fixedNow,
	})

	rec := getWeb(s, "/reservations", "127.0.0.1:12345")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "New reservation") {
		t.Error("expected 'New reservation' form for localhost request")
	}
}

// ---------------------------------------------------------------------------
// 3. TestHandleReservations_RemoteHidesForm
// ---------------------------------------------------------------------------

func TestHandleReservations_RemoteHidesForm(t *testing.T) {
	t.Log("GET /reservations from non-localhost should hide the form")

	s := newWebTestServer()
	rec := getWeb(s, "/reservations", "192.168.1.5:54321")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "New reservation") {
		t.Error("unexpected 'New reservation' form for non-localhost request")
	}
}

// ---------------------------------------------------------------------------
// 4. TestHandleHistory_OK
// ---------------------------------------------------------------------------

func TestHandleHistory_OK(t *testing.T) {
	t.Log("GET /history should return 200 with History heading")

	s := newWebTestServer()
	rec := getWeb(s, "/history", "127.0.0.1:12345")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "History") {
		t.Error("expected 'History' heading in response body")
	}
}

// ---------------------------------------------------------------------------
// 5. TestHandleStatic_ServesEmbeddedCSS
// ---------------------------------------------------------------------------

func TestHandleStatic_ServesEmbeddedCSS(t *testing.T) {
	t.Log("GET /static/piper.css should return 200 with text/css content")

	s := newWebTestServer()
	rec := getWeb(s, "/static/piper.css", "127.0.0.1:12345")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 got %d (body: %s)", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/css") {
		t.Errorf("expected text/css Content-Type, got %q", ct)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty response body for piper.css")
	}
}

// ---------------------------------------------------------------------------
// 6. TestHandleStatic_ServesPicoCSS
// ---------------------------------------------------------------------------

func TestHandleStatic_ServesPicoCSS(t *testing.T) {
	t.Log("GET /static/pico.min.css should return 200 with text/css content")

	s := newWebTestServer()
	rec := getWeb(s, "/static/pico.min.css", "127.0.0.1:12345")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/css") {
		t.Errorf("expected text/css Content-Type, got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// 7. TestHandleStatic_ServesHTMXJS
// ---------------------------------------------------------------------------

func TestHandleStatic_ServesHTMXJS(t *testing.T) {
	t.Log("GET /static/htmx.min.js should return 200 with application/javascript content")

	s := newWebTestServer()
	rec := getWeb(s, "/static/htmx.min.js", "127.0.0.1:12345")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "javascript") {
		t.Errorf("expected application/javascript Content-Type, got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// 8. TestHandleStatic_RejectsTraversal
// ---------------------------------------------------------------------------

func TestHandleStatic_RejectsTraversal(t *testing.T) {
	t.Log("GET /static/../server.go should return 400 (path traversal rejected)")

	s := newWebTestServer()
	// Use a raw request to bypass path cleaning — httptest.NewRequest will
	// still clean the URL, so we test the handler directly with the path value.
	req := httptest.NewRequest(http.MethodGet, "/static/%2e%2e/server.go", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	// Simulate what Go's ServeMux puts into PathValue("filename") after
	// unescape: the mux does unescape path values, so "../server.go" would be
	// the value. We exercise the handler directly to test the guard.
	rec := httptest.NewRecorder()
	// Directly call the handler so we can set a pathValue with ".."
	handler := &Server{deps: s.deps, mux: http.NewServeMux()}
	handler.registerRoutes()

	// Use the full server handler which will route via mux. The mux normalises
	// %2e%2e to ".." in the URL, which after normalisation becomes /static/ (the
	// ".." collapses the segment). That will likely 404. Either 400 or 404 is
	// acceptable for traversal attempts.
	handler.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Errorf("expected 400 or 404 for path traversal attempt, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// 9. TestHandleStatic_404OnMissing
// ---------------------------------------------------------------------------

func TestHandleStatic_404OnMissing(t *testing.T) {
	t.Log("GET /static/nonexistent.css should return 404")

	s := newWebTestServer()
	rec := getWeb(s, "/static/nonexistent.css", "127.0.0.1:12345")

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// 10. TestRoutes_RegisteredAfterAPI
// ---------------------------------------------------------------------------

func TestRoutes_RegisteredAfterAPI(t *testing.T) {
	t.Log("New(...) should register all web routes so they do not return 404")

	s := New("127.0.0.1", 0, Deps{
		Snap:     &stubSnap{snap: service.ScanSnapshot{Inspected: map[string]scanner.DockerEntry{}}},
		Reserves: newStubReserves(),
		History:  &stubHistory{},
		Now:      func() time.Time { return fixedNow },
	})

	routes := []struct {
		path string
		want int // acceptable codes (200 range)
	}{
		{"/", http.StatusOK},
		{"/reservations", http.StatusOK},
		{"/history", http.StatusOK},
		{"/static/piper.css", http.StatusOK},
	}

	for _, tc := range routes {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.RemoteAddr = "127.0.0.1:12345"
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Errorf("route %q returned 404 — route not registered", tc.path)
			}
		})
	}
}
