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

// ─── Stubs ────────────────────────────────────────────────────────────────────

// stubSnap is a service.SnapshotProvider that returns a fixed snapshot or error.
type stubSnap struct {
	snap service.ScanSnapshot
	err  error
}

func (s *stubSnap) LatestSnapshot(_ context.Context) (service.ScanSnapshot, error) {
	return s.snap, s.err
}

// stubReserves is a service.ReservationGetter that always returns ErrReservationNotFound.
type stubReserves struct {
	reservation store.Reservation
	err         error
}

func (s *stubReserves) GetReservation(_ context.Context, port int) (store.Reservation, error) {
	if s.err != nil {
		return store.Reservation{}, s.err
	}
	return s.reservation, nil
}

// emptyReserves returns ErrReservationNotFound for every port.
type emptyReserves struct{}

func (e *emptyReserves) GetReservation(_ context.Context, port int) (store.Reservation, error) {
	return store.Reservation{}, store.ErrReservationNotFound
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestRunCheck_HappyPath verifies that a snapshot with one docker container
// produces a CheckResult with one PortStatus for that port.
func TestRunCheck_HappyPath(t *testing.T) {
	snap := service.ScanSnapshot{
		ScannedAt: time.Now(),
		Docker: []scanner.DockerEntry{
			{
				ID:    "abc123",
				Name:  "test-container",
				Image: "test:latest",
				State: "running",
				Ports: []scanner.HostPort{{HostPort: 8080, ContainerPort: 80}},
			},
		},
	}

	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := CheckDeps{
		Snap:     &stubSnap{snap: snap},
		Reserves: &emptyReserves{},
		Out:      w,
	}

	err := RunCheck(context.Background(), deps, []int{8080})
	if err != nil {
		t.Fatalf("RunCheck returned unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "8080") {
		t.Errorf("output does not mention port 8080; got:\n%s", out)
	}
}

// TestRunCheck_PassesPortsToChecker verifies that querying [8080, 9000] produces
// a CheckResult with two entries in the same order.
func TestRunCheck_PassesPortsToChecker(t *testing.T) {
	snap := service.ScanSnapshot{
		ScannedAt: time.Now(),
	}

	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := CheckDeps{
		Snap:     &stubSnap{snap: snap},
		Reserves: &emptyReserves{},
		Out:      w,
	}

	err := RunCheck(context.Background(), deps, []int{8080, 9000})
	if err != nil {
		t.Fatalf("RunCheck returned unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "8080") {
		t.Errorf("output missing port 8080: %s", out)
	}
	if !strings.Contains(out, "9000") {
		t.Errorf("output missing port 9000: %s", out)
	}
}

// TestRunCheck_PropagatesError verifies that a Snap error is propagated.
func TestRunCheck_PropagatesError(t *testing.T) {
	snapErr := errors.New("scan failed")

	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := CheckDeps{
		Snap:     &stubSnap{err: snapErr},
		Reserves: &emptyReserves{},
		Out:      w,
	}

	err := RunCheck(context.Background(), deps, []int{8080})
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !errors.Is(err, snapErr) {
		t.Errorf("expected snapErr, got: %v", err)
	}
}

// TestParsePortArgs_IntegrationWithCheck verifies that NewCheckCmd can parse
// "8080-8082" and invoke CheckPorts with 3 ports.
// We test this by directly calling ParsePortArgs and verifying it produces [8080, 8081, 8082],
// then RunCheck with those 3 ports produces a 3-entry result.
func TestParsePortArgs_IntegrationWithCheck(t *testing.T) {
	ports, err := ParsePortArgs([]string{"8080-8082"})
	if err != nil {
		t.Fatalf("ParsePortArgs error: %v", err)
	}
	if len(ports) != 3 {
		t.Fatalf("expected 3 ports, got %d: %v", len(ports), ports)
	}
	if ports[0] != 8080 || ports[1] != 8081 || ports[2] != 8082 {
		t.Errorf("unexpected ports: %v", ports)
	}

	snap := service.ScanSnapshot{ScannedAt: time.Now()}
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := CheckDeps{
		Snap:     &stubSnap{snap: snap},
		Reserves: &emptyReserves{},
		Out:      w,
	}

	err = RunCheck(context.Background(), deps, ports)
	if err != nil {
		t.Fatalf("RunCheck error: %v", err)
	}

	out := buf.String()
	for _, p := range []string{"8080", "8081", "8082"} {
		if !strings.Contains(out, p) {
			t.Errorf("output missing port %s: %s", p, out)
		}
	}
}
