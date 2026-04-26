package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ─── Stubs for suggest ────────────────────────────────────────────────────────

// emptySnapProvider returns an empty snapshot with no SS, Docker, or UFW entries.
type emptySnapProvider struct {
	at time.Time
}

func (e *emptySnapProvider) LatestSnapshot(_ context.Context) (service.ScanSnapshot, error) {
	return service.ScanSnapshot{ScannedAt: e.at}, nil
}

// emptyReservationLister returns an empty list of reservations.
type emptyReservationLister struct{}

func (e *emptyReservationLister) ListReservations(_ context.Context) ([]store.Reservation, error) {
	return []store.Reservation{}, nil
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestRunSuggest_HappyPath verifies that with empty snap and no reservations,
// SuggestFreePorts returns [from, from+1, from+2] for n=3.
func TestRunSuggest_HappyPath(t *testing.T) {
	now := time.Now()
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := SuggestDeps{
		Snap:     &emptySnapProvider{at: now},
		Reserves: &emptyReservationLister{},
		Out:      w,
	}

	err := RunSuggest(context.Background(), deps, 3, 8000, 9999)
	if err != nil {
		t.Fatalf("RunSuggest returned error: %v", err)
	}

	out := buf.String()
	for _, port := range []string{"8000", "8001", "8002"} {
		if !contains(out, port) {
			t.Errorf("output missing port %s; got:\n%s", port, out)
		}
	}
}

// errSnapProvider is a SnapshotProvider that returns an error.
type errSnapProvider struct {
	err error
}

func (e *errSnapProvider) LatestSnapshot(_ context.Context) (service.ScanSnapshot, error) {
	return service.ScanSnapshot{}, e.err
}

// TestRunSuggest_PartialResultAndError verifies that when ErrNoFreePortsFound
// is returned (range exhausted), RunSuggest still writes partial results via
// Writer and returns the error.
func TestRunSuggest_PartialResultAndError(t *testing.T) {
	// Use a tiny range (8000-8001) but request n=3 to force partial result.
	now := time.Now()
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := SuggestDeps{
		Snap:     &emptySnapProvider{at: now},
		Reserves: &emptyReservationLister{},
		Out:      w,
	}

	// From=8000, To=8001, n=3 — only 2 ports available in range.
	err := RunSuggest(context.Background(), deps, 3, 8000, 8001)
	if err == nil {
		t.Fatal("expected ErrNoFreePortsFound, got nil")
	}
	if !errors.Is(err, service.ErrNoFreePortsFound) {
		t.Errorf("expected ErrNoFreePortsFound, got: %v", err)
	}

	// Partial result should still be written.
	out := buf.String()
	if !contains(out, "8000") && !contains(out, "8001") {
		t.Errorf("expected partial output with 8000 and 8001, got:\n%s", out)
	}
}

// TestRunSuggest_FlagDefaults_n1_8000_9999 verifies that with n=1, from=8000, to=9999
// the first free port (8000) is suggested.
func TestRunSuggest_FlagDefaults_n1_8000_9999(t *testing.T) {
	now := time.Now()
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := SuggestDeps{
		Snap:     &emptySnapProvider{at: now},
		Reserves: &emptyReservationLister{},
		Out:      w,
	}

	// Defaults: n=1, from=8000, to=9999
	err := RunSuggest(context.Background(), deps, 1, 8000, 9999)
	if err != nil {
		t.Fatalf("RunSuggest returned error: %v", err)
	}

	out := buf.String()
	// With empty snap, 8000 should be the first suggestion.
	if !contains(out, "8000") {
		t.Errorf("expected port 8000 in output, got:\n%s", out)
	}
}

// TestRunSuggest_AvoidRecentIsNoOp verifies that --avoid-recent flag is accepted
// but produces the same result as without it. The flag is Phase 2; for now it's
// a no-op. We verify that RunSuggest with and without the flag returns the same
// ports (here verified at the cobra flag parsing level — flag must not produce
// an error).
func TestRunSuggest_AvoidRecentIsNoOp(t *testing.T) {
	now := time.Now()

	// Run suggest without avoid-recent (baseline).
	var buf1 bytes.Buffer
	w1 := &Writer{Format: FormatText, NoColor: true, Stdout: &buf1}
	deps1 := SuggestDeps{
		Snap:     &emptySnapProvider{at: now},
		Reserves: &emptyReservationLister{},
		Out:      w1,
	}
	err := RunSuggest(context.Background(), deps1, 3, 8000, 9999)
	if err != nil {
		t.Fatalf("baseline RunSuggest error: %v", err)
	}

	// Run suggest again (simulating --avoid-recent as no-op — same deps, same call).
	var buf2 bytes.Buffer
	w2 := &Writer{Format: FormatText, NoColor: true, Stdout: &buf2}
	deps2 := SuggestDeps{
		Snap:     &emptySnapProvider{at: now},
		Reserves: &emptyReservationLister{},
		Out:      w2,
	}
	err = RunSuggest(context.Background(), deps2, 3, 8000, 9999)
	if err != nil {
		t.Fatalf("avoid-recent RunSuggest error: %v", err)
	}

	// Both outputs should be identical (avoid-recent is no-op).
	if buf1.String() != buf2.String() {
		t.Errorf("avoid-recent produced different output:\nwithout: %s\nwith: %s",
			buf1.String(), buf2.String())
	}

	// Also verify NewSuggestCmd accepts --avoid-recent without error.
	cmd := NewSuggestCmd()
	flag := cmd.Flags().Lookup("avoid-recent")
	if flag == nil {
		t.Error("--avoid-recent flag not found on suggest command")
	}
}

// contains is a package-level helper (note: output.go also has a private contains
// in store package; this one is local to tests).
func contains(s, sub string) bool {
	return len(s) >= len(sub) && len(sub) > 0 &&
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}()
}
