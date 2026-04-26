package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/scanner"
	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ─── Additional stubs for list/scan ──────────────────────────────────────────

// stubReservationLister returns a fixed list or error.
type stubReservationLister struct {
	reservations []store.Reservation
	err          error
}

func (s *stubReservationLister) ListReservations(_ context.Context) ([]store.Reservation, error) {
	return s.reservations, s.err
}

// ─── Shared test fixture builder ─────────────────────────────────────────────

// makeListTestDeps builds a set of deps useful for most list tests.
// Snapshot has:
//   - port 8080: ss listener (used_process, non-docker)
//   - port 8090: docker running container "running-server"
//   - port 8091: docker stopped container "stopped-server" (reserved_implicit)
//
// Explicit reservation: port 9100 (for "planned-service")
func makeListTestDeps(buf *bytes.Buffer) (ListDeps, *stubReservationLister) {
	snap := service.ScanSnapshot{
		ScannedAt: time.Now(),
		SS: []scanner.SSEntry{
			{Port: 8080, ProcessName: "python3", PID: 1234},
		},
		Docker: []scanner.DockerEntry{
			{
				ID:    "run001",
				Name:  "running-server",
				Image: "run:latest",
				State: "running",
				Ports: []scanner.HostPort{{HostPort: 8090, ContainerPort: 8090}},
			},
			{
				ID:    "stop001",
				Name:  "stopped-server",
				Image: "stop:latest",
				State: "exited",
				Ports: []scanner.HostPort{{HostPort: 8091, ContainerPort: 8091}},
			},
		},
		Inspected: map[string]scanner.DockerEntry{},
		UFW:       []scanner.UFWRule{},
	}

	reservations := []store.Reservation{
		{Port: 9100, Name: "planned-service", CreatedAt: time.Now()},
	}
	lister := &stubReservationLister{reservations: reservations}

	w := &Writer{Format: FormatText, NoColor: true, Stdout: buf}

	deps := ListDeps{
		Snap:        InMemorySnapProvider{Snap: snap},
		Reserves:    &emptyReserves{},
		AllReserves: lister,
		Out:         w,
	}
	return deps, lister
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestRunList_DefaultExcludesFree verifies that the zero ListFilter (all
// stateful) returns: ss port + docker running + docker stopped + explicit
// reservation = 4 ports.
func TestRunList_DefaultExcludesFree(t *testing.T) {
	var buf bytes.Buffer
	deps, _ := makeListTestDeps(&buf)

	result, err := RunList(context.Background(), deps, ListFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 4 {
		t.Errorf("expected 4 ports, got %d: %v", len(result.Results), portList(result))
	}
}

// TestRunList_OnlyUsed verifies that filter Used=true returns only
// used_process and used_docker ports (8080 + 8090 = 2 ports).
func TestRunList_OnlyUsed(t *testing.T) {
	var buf bytes.Buffer
	deps, _ := makeListTestDeps(&buf)

	result, err := RunList(context.Background(), deps, ListFilter{Used: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 2 {
		t.Errorf("expected 2 used ports, got %d: %v", len(result.Results), portList(result))
	}
	for _, ps := range result.Results {
		if ps.State != service.PortUsedProcess && ps.State != service.PortUsedDocker {
			t.Errorf("unexpected state %q for port %d", ps.State, ps.Port)
		}
	}
}

// TestRunList_OnlyReserved verifies that filter ReservedExplicit=ReservedImplicit=true
// returns 2 ports: stopped-server (8091, implicit) + planned-service (9100, explicit).
func TestRunList_OnlyReserved(t *testing.T) {
	var buf bytes.Buffer
	deps, _ := makeListTestDeps(&buf)

	result, err := RunList(context.Background(), deps, ListFilter{
		ReservedExplicit: true,
		ReservedImplicit: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 2 {
		t.Errorf("expected 2 reserved ports, got %d: %v", len(result.Results), portList(result))
	}
	for _, ps := range result.Results {
		if ps.State != service.PortReservedExplicit && ps.State != service.PortReservedImplicit {
			t.Errorf("unexpected state %q for port %d", ps.State, ps.Port)
		}
	}
}

// TestRunList_FreeRequiresRange verifies that filter Free=true without
// From/To bounds returns an error.
func TestRunList_FreeRequiresRange(t *testing.T) {
	var buf bytes.Buffer
	deps, _ := makeListTestDeps(&buf)

	_, err := RunList(context.Background(), deps, ListFilter{Free: true})
	if err == nil {
		t.Fatal("expected error for --free without range, got nil")
	}
	if !strings.Contains(err.Error(), "--from") && !strings.Contains(err.Error(), "--to") {
		t.Errorf("expected range-hint error, got: %v", err)
	}
}

// TestRunList_FreeWithRange verifies that --free with From=9000, To=9005 and
// nothing in that range returns 6 free ports.
func TestRunList_FreeWithRange(t *testing.T) {
	var buf bytes.Buffer
	deps, _ := makeListTestDeps(&buf)

	result, err := RunList(context.Background(), deps, ListFilter{
		Free: true,
		From: 9000,
		To:   9005,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 6 {
		t.Errorf("expected 6 free ports (9000-9005), got %d: %v", len(result.Results), portList(result))
	}
	for _, ps := range result.Results {
		if ps.State != service.PortFree {
			t.Errorf("expected free state for port %d, got %q", ps.Port, ps.State)
		}
		if ps.Port < 9000 || ps.Port > 9005 {
			t.Errorf("port %d out of range 9000-9005", ps.Port)
		}
	}
}

// TestRunList_ContainerFilter verifies that --container NAME keeps only ports
// whose occupant container matches.
//
// Snapshot:
//   - port 8080: docker running "translate-server" (image: translate:v2)
//   - port 8081: docker running "subtitle-server"  (image: subtitle:v1)
func TestRunList_ContainerFilter(t *testing.T) {
	snap := service.ScanSnapshot{
		ScannedAt: time.Now(),
		Docker: []scanner.DockerEntry{
			{
				ID:    "ts001",
				Name:  "translate-server",
				Image: "translate:v2",
				State: "running",
				Ports: []scanner.HostPort{{HostPort: 8080, ContainerPort: 80}},
			},
			{
				ID:    "ss001",
				Name:  "subtitle-server",
				Image: "subtitle:v1",
				State: "running",
				Ports: []scanner.HostPort{{HostPort: 8081, ContainerPort: 80}},
			},
		},
		Inspected: map[string]scanner.DockerEntry{},
	}

	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := ListDeps{
		Snap:        InMemorySnapProvider{Snap: snap},
		Reserves:    &emptyReserves{},
		AllReserves: &stubReservationLister{},
		Out:         w,
	}

	result, err := RunList(context.Background(), deps, ListFilter{Container: "translate-server"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 1 {
		t.Errorf("expected 1 port for translate-server, got %d: %v", len(result.Results), portList(result))
	}
	if len(result.Results) == 1 && result.Results[0].Port != 8080 {
		t.Errorf("expected port 8080, got %d", result.Results[0].Port)
	}
}

// TestRunList_OutputViaWriter verifies that Writer.Write is called with a
// CheckResult containing the filtered ports.
func TestRunList_OutputViaWriter(t *testing.T) {
	var buf bytes.Buffer
	deps, _ := makeListTestDeps(&buf)

	_, err := RunList(context.Background(), deps, ListFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if out == "" {
		t.Error("expected non-empty output from Writer")
	}
	// At minimum, port 8080 should appear in the text output.
	if !strings.Contains(out, "8080") {
		t.Errorf("output missing port 8080:\n%s", out)
	}
}

// ─── Helper ───────────────────────────────────────────────────────────────────

// portList extracts a human-readable list of ports from a CheckResult for
// debugging test failures.
func portList(r service.CheckResult) []int {
	var ports []int
	for _, ps := range r.Results {
		ports = append(ports, ps.Port)
	}
	return ports
}

// TestRunList_PropagatesSnapError verifies snap errors bubble up.
func TestRunList_PropagatesSnapError(t *testing.T) {
	wantErr := errors.New("snap failed")
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := ListDeps{
		Snap:        &stubSnap{err: wantErr},
		Reserves:    &emptyReserves{},
		AllReserves: &stubReservationLister{},
		Out:         w,
	}

	_, err := RunList(context.Background(), deps, ListFilter{})
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wantErr, got %v", err)
	}
}
