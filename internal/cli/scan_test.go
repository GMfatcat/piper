package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/scanner"
	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// TestRunScan_ReturnsStatefulPorts verifies that RunScan (alias for
// piper list --used --reserved) returns all stateful ports and NOT free ports.
//
// Input matches makeListTestDeps:
//   - port 8080: used_process (ss)
//   - port 8090: used_docker  (docker running)
//   - port 8091: reserved_implicit (docker stopped)
//   - port 9100: reserved_explicit (store reservation)
//
// Expected: 4 ports (same as RunList default filter).
func TestRunScan_ReturnsStatefulPorts(t *testing.T) {
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

	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := ListDeps{
		Snap:        InMemorySnapProvider{Snap: snap},
		Reserves:    &emptyReserves{},
		AllReserves: &stubReservationLister{reservations: reservations},
		Out:         w,
	}

	result, err := RunScan(context.Background(), deps)
	if err != nil {
		t.Fatalf("RunScan error: %v", err)
	}

	if len(result.Results) != 4 {
		t.Errorf("expected 4 stateful ports, got %d: %v", len(result.Results), portList(result))
	}

	// Verify no free ports slipped through.
	for _, ps := range result.Results {
		if ps.State == service.PortFree {
			t.Errorf("RunScan should not return free port %d", ps.Port)
		}
	}
}
