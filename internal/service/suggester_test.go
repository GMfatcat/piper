package service

import (
	"context"
	"errors"
	"testing"

	"github.com/GMfatcat/piper/internal/scanner"
	"github.com/GMfatcat/piper/internal/store"
)

// ---------------------------------------------------------------------------
// Stubs — named to avoid collision with checker_test.go's stubSnap/stubReserves
// ---------------------------------------------------------------------------

// suggesterStubSnap implements SnapshotProvider for suggester tests.
type suggesterStubSnap struct {
	snap ScanSnapshot
	err  error
}

func (s suggesterStubSnap) LatestSnapshot(_ context.Context) (ScanSnapshot, error) {
	return s.snap, s.err
}

// stubReservationLister implements ReservationLister for suggester tests.
type stubReservationLister struct {
	rows []store.Reservation
	err  error
}

func (s stubReservationLister) ListReservations(_ context.Context) ([]store.Reservation, error) {
	return s.rows, s.err
}

// ---------------------------------------------------------------------------
// Helper: build a Suggester from stubs.
// ---------------------------------------------------------------------------

func newSuggester(snap ScanSnapshot, snapErr error, rows []store.Reservation, listErr error) *Suggester {
	return &Suggester{
		Snap:     suggesterStubSnap{snap: snap, err: snapErr},
		Reserves: stubReservationLister{rows: rows, err: listErr},
	}
}

// emptySnapForSuggester returns a minimal ScanSnapshot with no entries.
func emptySnapForSuggester() ScanSnapshot {
	return ScanSnapshot{
		Inspected: map[string]scanner.DockerEntry{},
	}
}

// ---------------------------------------------------------------------------
// Test 1: TestSuggest_AllFree_PicksFirstN
// Empty snapshot, no reservations, n=3, from=8000, to=9999 → [8000, 8001, 8002]
// ---------------------------------------------------------------------------

func TestSuggest_AllFree_PicksFirstN(t *testing.T) {
	s := newSuggester(emptySnapForSuggester(), nil, nil, nil)

	result, err := s.SuggestFreePorts(context.Background(), 3, 8000, 9999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("want 3 results, got %d: %v", len(result), result)
	}
	want := []int{8000, 8001, 8002}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("result[%d]: want %d, got %d", i, w, result[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Test 2: TestSuggest_SkipsSSListeners
// SS has 8000, 8001 → suggest n=2 from 8000 returns [8002, 8003]
// ---------------------------------------------------------------------------

func TestSuggest_SkipsSSListeners(t *testing.T) {
	snap := emptySnapForSuggester()
	snap.SS = []scanner.SSEntry{
		{Port: 8000, PID: 100, ProcessName: "nginx"},
		{Port: 8001, PID: 101, ProcessName: "fastapi"},
	}

	s := newSuggester(snap, nil, nil, nil)

	result, err := s.SuggestFreePorts(context.Background(), 2, 8000, 9999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("want 2 results, got %d: %v", len(result), result)
	}
	want := []int{8002, 8003}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("result[%d]: want %d, got %d", i, w, result[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Test 3: TestSuggest_SkipsDockerHostPorts
// docker entry running with HostPort=8000; another entry stopped with HostPort=8001
// → both skipped → suggest n=2 from 8000 returns [8002, 8003]
// Confirms BOTH running & stopped containers' host ports are blockers.
// ---------------------------------------------------------------------------

func TestSuggest_SkipsDockerHostPorts(t *testing.T) {
	snap := emptySnapForSuggester()
	snap.Docker = []scanner.DockerEntry{
		{
			ID:    "running-container-id",
			Name:  "running-svc",
			Image: "myimage:latest",
			State: "running",
			Ports: []scanner.HostPort{
				{HostPort: 8000, ContainerPort: 80, Proto: "tcp"},
			},
		},
		{
			ID:    "stopped-container-id",
			Name:  "stopped-svc",
			Image: "myimage:latest",
			State: "exited",
			Ports: []scanner.HostPort{
				{HostPort: 8001, ContainerPort: 80, Proto: "tcp"},
			},
		},
	}

	s := newSuggester(snap, nil, nil, nil)

	result, err := s.SuggestFreePorts(context.Background(), 2, 8000, 9999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("want 2 results, got %d: %v", len(result), result)
	}
	want := []int{8002, 8003}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("result[%d]: want %d, got %d", i, w, result[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Test 4: TestSuggest_SkipsExplicitReservations
// reservation on 8000, 8002 → suggest n=2 from 8000 returns [8001, 8003]
// ---------------------------------------------------------------------------

func TestSuggest_SkipsExplicitReservations(t *testing.T) {
	rows := []store.Reservation{
		{Port: 8000, Name: "reserved-a"},
		{Port: 8002, Name: "reserved-b"},
	}

	s := newSuggester(emptySnapForSuggester(), nil, rows, nil)

	result, err := s.SuggestFreePorts(context.Background(), 2, 8000, 9999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("want 2 results, got %d: %v", len(result), result)
	}
	want := []int{8001, 8003}
	for i, w := range want {
		if result[i] != w {
			t.Errorf("result[%d]: want %d, got %d", i, w, result[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Test 5: TestSuggest_AllConflictsTogether
// 8000 in ss, 8001 in docker, 8002 in reservations, 8003 free → suggest n=1 from 8000 returns [8003]
// ---------------------------------------------------------------------------

func TestSuggest_AllConflictsTogether(t *testing.T) {
	snap := emptySnapForSuggester()
	snap.SS = []scanner.SSEntry{
		{Port: 8000, PID: 100, ProcessName: "nginx"},
	}
	snap.Docker = []scanner.DockerEntry{
		{
			ID:    "container-abc",
			Name:  "some-svc",
			State: "running",
			Ports: []scanner.HostPort{
				{HostPort: 8001, ContainerPort: 80, Proto: "tcp"},
			},
		},
	}
	rows := []store.Reservation{
		{Port: 8002, Name: "planned-service"},
	}

	s := newSuggester(snap, nil, rows, nil)

	result, err := s.SuggestFreePorts(context.Background(), 1, 8000, 9999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("want 1 result, got %d: %v", len(result), result)
	}
	if result[0] != 8003 {
		t.Errorf("result[0]: want 8003, got %d", result[0])
	}
}

// ---------------------------------------------------------------------------
// Test 6: TestSuggest_RangeExhausted_ReturnsErrAndPartial
// only 8000-8002 in range, all reserved, n=3 → returns []int{} (partial), wraps ErrNoFreePortsFound
// ---------------------------------------------------------------------------

func TestSuggest_RangeExhausted_ReturnsErrAndPartial(t *testing.T) {
	rows := []store.Reservation{
		{Port: 8000, Name: "r-a"},
		{Port: 8001, Name: "r-b"},
		{Port: 8002, Name: "r-c"},
	}

	s := newSuggester(emptySnapForSuggester(), nil, rows, nil)

	result, err := s.SuggestFreePorts(context.Background(), 3, 8000, 8002)
	if !errors.Is(err, ErrNoFreePortsFound) {
		t.Fatalf("want ErrNoFreePortsFound, got: %v", err)
	}
	// Partial result: zero free ports found in the range.
	if result == nil {
		t.Error("result should be non-nil (empty slice), got nil")
	}
	if len(result) != 0 {
		t.Errorf("want empty partial result, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// Test 7: TestSuggest_PartialResult
// only 8001 free in 8000-8002 (8000+8002 occupied), n=3 → result=[8001], err=ErrNoFreePortsFound
// ---------------------------------------------------------------------------

func TestSuggest_PartialResult(t *testing.T) {
	rows := []store.Reservation{
		{Port: 8000, Name: "r-a"},
		{Port: 8002, Name: "r-c"},
	}

	s := newSuggester(emptySnapForSuggester(), nil, rows, nil)

	result, err := s.SuggestFreePorts(context.Background(), 3, 8000, 8002)
	if !errors.Is(err, ErrNoFreePortsFound) {
		t.Fatalf("want ErrNoFreePortsFound, got: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("want partial result [8001], got %v", result)
	}
	if result[0] != 8001 {
		t.Errorf("result[0]: want 8001, got %d", result[0])
	}
}

// ---------------------------------------------------------------------------
// Test 8: TestSuggest_NLeqZero
// n=0 → empty slice, no error
// ---------------------------------------------------------------------------

func TestSuggest_NLeqZero(t *testing.T) {
	s := newSuggester(emptySnapForSuggester(), nil, nil, nil)

	result, err := s.SuggestFreePorts(context.Background(), 0, 8000, 9999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("want empty slice, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// Test 9: TestSuggest_InvalidFrom
// from=0 → error containing "from must be >= 1"
// ---------------------------------------------------------------------------

func TestSuggest_InvalidFrom(t *testing.T) {
	s := newSuggester(emptySnapForSuggester(), nil, nil, nil)

	_, err := s.SuggestFreePorts(context.Background(), 1, 0, 9999)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if err.Error() == "" {
		t.Fatal("want non-empty error message")
	}
	// Check error message contains expected substring.
	const want = "from must be >= 1"
	if !containsStr(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// Test 10: TestSuggest_InvalidTo
// to=70000 → error containing "to must be <= 65535"
// ---------------------------------------------------------------------------

func TestSuggest_InvalidTo(t *testing.T) {
	s := newSuggester(emptySnapForSuggester(), nil, nil, nil)

	_, err := s.SuggestFreePorts(context.Background(), 1, 8000, 70000)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	const want = "to must be <= 65535"
	if !containsStr(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// Test 11: TestSuggest_FromGreaterThanTo
// from=9000 to=8000 → error
// ---------------------------------------------------------------------------

func TestSuggest_FromGreaterThanTo(t *testing.T) {
	s := newSuggester(emptySnapForSuggester(), nil, nil, nil)

	_, err := s.SuggestFreePorts(context.Background(), 1, 9000, 8000)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	const want = "from must be <= to"
	if !containsStr(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// Test 12: TestSuggest_SnapshotProviderError
// stubSnap.err non-nil → returns that error
// ---------------------------------------------------------------------------

func TestSuggest_SnapshotProviderError(t *testing.T) {
	snapErr := errors.New("snapshot provider: connection refused")

	s := newSuggester(emptySnapForSuggester(), snapErr, nil, nil)

	_, err := s.SuggestFreePorts(context.Background(), 1, 8000, 9999)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, snapErr) {
		t.Errorf("want snapErr, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 13: TestSuggest_ReservationListerError
// stubReservations.err non-nil → returns that error
// ---------------------------------------------------------------------------

func TestSuggest_ReservationListerError(t *testing.T) {
	listErr := errors.New("db: unable to open database file")

	s := newSuggester(emptySnapForSuggester(), nil, nil, listErr)

	_, err := s.SuggestFreePorts(context.Background(), 1, 8000, 9999)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, listErr) {
		t.Errorf("want listErr, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 14: TestSuggest_DockerHostPortZeroIgnored
// docker entry with HostPort=0 → not added to blocker set, port 8000 is free
// ---------------------------------------------------------------------------

func TestSuggest_DockerHostPortZeroIgnored(t *testing.T) {
	snap := emptySnapForSuggester()
	snap.Docker = []scanner.DockerEntry{
		{
			ID:    "expose-only-container",
			Name:  "expose-svc",
			State: "running",
			Ports: []scanner.HostPort{
				{HostPort: 0, ContainerPort: 80, Proto: "tcp"}, // pure EXPOSE, no host binding
			},
		},
	}

	s := newSuggester(snap, nil, nil, nil)

	result, err := s.SuggestFreePorts(context.Background(), 1, 8000, 8000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("want 1 result (8000 is free), got %d: %v", len(result), result)
	}
	if result[0] != 8000 {
		t.Errorf("result[0]: want 8000, got %d", result[0])
	}
}

// ---------------------------------------------------------------------------
// Local helper — substring check (avoids importing strings just for tests)
// ---------------------------------------------------------------------------

func containsStr(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
