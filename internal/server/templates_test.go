package server

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// renderToString renders the named page template into a buffer and returns
// the resulting HTML string.
func renderToString(t *testing.T, page string, data TemplateData) string {
	t.Helper()
	rt, err := buildRenderTemplate(page)
	if err != nil {
		t.Fatalf("buildRenderTemplate(%q): %v", page, err)
	}
	var buf bytes.Buffer
	if err := rt.ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("ExecuteTemplate: %v", err)
	}
	return buf.String()
}

// baseData returns a TemplateData with safe defaults (non-zero Now etc.).
func baseData() TemplateData {
	return TemplateData{
		Now:       time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC),
		UFWActive: true,
	}
}

// ---------------------------------------------------------------------------
// 1. TestParseTemplates_NoErrors
// ---------------------------------------------------------------------------

func TestParseTemplates_NoErrors(t *testing.T) {
	t.Log("parsing all templates should succeed without error")
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates() returned error: %v", err)
	}
	if tmpl == nil {
		t.Fatal("parseTemplates() returned nil template")
	}

	// Verify all expected templates are in the set.
	for _, name := range []string{
		"layout",
		"web/templates/overview.html",
		"web/templates/reservations.html",
		"web/templates/history.html",
	} {
		if tmpl.Lookup(name) == nil {
			t.Errorf("template %q not found in parsed set", name)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. TestRender_Overview_HasStatefulPorts
// ---------------------------------------------------------------------------

func TestRender_Overview_HasStatefulPorts(t *testing.T) {
	t.Log("overview with 2 stateful ports should contain port numbers and table headers")

	data := baseData()
	data.Ports = []service.PortStatus{
		{Port: 8080, State: service.PortUsedDocker},
		{Port: 9100, State: service.PortReservedExplicit},
	}

	html := renderToString(t, "web/templates/overview.html", data)

	for _, want := range []string{"8080", "9100", "Port", "State"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected output to contain %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. TestRender_Overview_UFWBannerWhenInactive
// ---------------------------------------------------------------------------

func TestRender_Overview_UFWBannerWhenInactive(t *testing.T) {
	t.Log("UFWActive=false should render the UFW warning banner")

	data := baseData()
	data.UFWActive = false

	html := renderToString(t, "web/templates/overview.html", data)

	if !strings.Contains(html, "UFW is inactive") {
		t.Error("expected 'UFW is inactive' banner when UFWActive=false")
	}
}

// ---------------------------------------------------------------------------
// 4. TestRender_Overview_NoUFWBannerWhenActive
// ---------------------------------------------------------------------------

func TestRender_Overview_NoUFWBannerWhenActive(t *testing.T) {
	t.Log("UFWActive=true should NOT render the UFW warning banner")

	data := baseData()
	data.UFWActive = true

	html := renderToString(t, "web/templates/overview.html", data)

	if strings.Contains(html, "UFW is inactive") {
		t.Error("unexpected 'UFW is inactive' banner when UFWActive=true")
	}
}

// ---------------------------------------------------------------------------
// 5. TestRender_Reservations_LocalhostShowsButtons
// ---------------------------------------------------------------------------

func TestRender_Reservations_LocalhostShowsButtons(t *testing.T) {
	t.Log("IsLocalhost=true should render '+ New reservation' and delete buttons")

	data := baseData()
	data.IsLocalhost = true
	data.Reservations = []store.Reservation{
		{Port: 9100, Name: "vllm-llama", Note: "test", CreatedAt: time.Now(), CreatedBy: "ymu"},
	}

	html := renderToString(t, "web/templates/reservations.html", data)

	if !strings.Contains(html, "New reservation") {
		t.Error("expected '+ New reservation' form when IsLocalhost=true")
	}
	if !strings.Contains(html, "del") {
		t.Error("expected delete button when IsLocalhost=true")
	}
}

// ---------------------------------------------------------------------------
// 6. TestRender_Reservations_RemoteHidesButtons
// ---------------------------------------------------------------------------

func TestRender_Reservations_RemoteHidesButtons(t *testing.T) {
	t.Log("IsLocalhost=false should hide '+ New reservation' and delete buttons")

	data := baseData()
	data.IsLocalhost = false
	data.Reservations = []store.Reservation{
		{Port: 9100, Name: "vllm-llama", Note: "test", CreatedAt: time.Now(), CreatedBy: "ymu"},
	}

	html := renderToString(t, "web/templates/reservations.html", data)

	if strings.Contains(html, "New reservation") {
		t.Error("unexpected '+ New reservation' form when IsLocalhost=false")
	}
	// The delete button is rendered only inside {{if $.IsLocalhost}}.
	// Check that the hx-delete attribute is absent to confirm the button is hidden.
	if strings.Contains(html, "hx-delete") {
		t.Error("unexpected delete button (hx-delete) when IsLocalhost=false")
	}
}

// ---------------------------------------------------------------------------
// 7. TestRender_History_TimelineOrder
// ---------------------------------------------------------------------------

func TestRender_History_TimelineOrder(t *testing.T) {
	t.Log("history page should contain all 3 events (pass-through; order set by caller)")

	base := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	data := baseData()
	data.Events = []store.Event{
		{ID: 3, Port: 9100, Event: store.EventOccupied, Timestamp: base.Add(3 * time.Hour)},
		{ID: 2, Port: 9100, Event: store.EventReleased, Timestamp: base.Add(2 * time.Hour)},
		{ID: 1, Port: 8080, Event: store.EventReserved, Timestamp: base.Add(1 * time.Hour)},
	}

	html := renderToString(t, "web/templates/history.html", data)

	for _, want := range []string{"9100", "8080", "occupied", "released", "reserved"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected history output to contain %q", want)
		}
	}

	// Verify all 3 events appear: check that the three timestamp strings appear.
	ts1 := base.Add(3 * time.Hour).Format("2006-01-02 15:04:05")
	ts2 := base.Add(2 * time.Hour).Format("2006-01-02 15:04:05")
	ts3 := base.Add(1 * time.Hour).Format("2006-01-02 15:04:05")
	for _, ts := range []string{ts1, ts2, ts3} {
		if !strings.Contains(html, ts) {
			t.Errorf("expected history output to contain timestamp %q", ts)
		}
	}
}
