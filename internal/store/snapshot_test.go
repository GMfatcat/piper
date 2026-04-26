package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/store"
)

// openSnapshotTestStore opens a fresh Store backed by a temp-dir database and
// registers cleanup via t.Cleanup. Each test call gets its own isolated DB.
func openSnapshotTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestSaveSnapshot_FreshStore saves 3 rows with a fixed scannedAt and verifies
// that GetSnapshot returns those 3 rows ordered by port plus the same timestamp
// (within 1ms).
func TestSaveSnapshot_FreshStore(t *testing.T) {
	s := openSnapshotTestStore(t)
	ctx := t.Context()

	scannedAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	rows := []store.SnapshotRow{
		{Port: 9000, State: store.StateUsedProcess, PID: 1234, ProcessName: "nginx"},
		{Port: 8000, State: store.StateUsedDocker, ContainerName: "web", ContainerID: "abc123"},
		{Port: 8500, State: store.StateReservedImplicit, ContainerName: "stopped-svc"},
	}

	if err := s.SaveSnapshot(ctx, rows, scannedAt); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	got, gotAt, err := s.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("GetSnapshot returned %d rows, want 3", len(got))
	}

	// Rows must be ordered by port ASC: 8000, 8500, 9000.
	wantPorts := []int{8000, 8500, 9000}
	for i, r := range got {
		if r.Port != wantPorts[i] {
			t.Errorf("got[%d].Port = %d, want %d", i, r.Port, wantPorts[i])
		}
	}

	// Timestamp must match within 1ms.
	if diff := gotAt.Sub(scannedAt).Abs(); diff > time.Millisecond {
		t.Errorf("scannedAt = %v, want %v (diff %v > 1ms)", gotAt, scannedAt, diff)
	}
}

// TestSaveSnapshot_OverwritesPrevious saves 3 rows then saves 2 different rows;
// GetSnapshot should return only the second set.
func TestSaveSnapshot_OverwritesPrevious(t *testing.T) {
	s := openSnapshotTestStore(t)
	ctx := t.Context()

	first := []store.SnapshotRow{
		{Port: 8080, State: store.StateUsedProcess, PID: 100, ProcessName: "sshd"},
		{Port: 8081, State: store.StateUsedProcess, PID: 101, ProcessName: "httpd"},
		{Port: 8082, State: store.StateReservedImplicit, ContainerName: "old-svc"},
	}
	if err := s.SaveSnapshot(ctx, first, time.Now().UTC()); err != nil {
		t.Fatalf("first SaveSnapshot: %v", err)
	}

	scannedAt2 := time.Date(2026, 4, 25, 13, 0, 0, 0, time.UTC)
	second := []store.SnapshotRow{
		{Port: 9000, State: store.StateUsedDocker, ContainerName: "new-svc", ContainerID: "def456"},
		{Port: 9001, State: store.StateUsedProcess, PID: 200, ProcessName: "ollama"},
	}
	if err := s.SaveSnapshot(ctx, second, scannedAt2); err != nil {
		t.Fatalf("second SaveSnapshot: %v", err)
	}

	got, gotAt, err := s.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("GetSnapshot returned %d rows, want 2", len(got))
	}
	if got[0].Port != 9000 {
		t.Errorf("got[0].Port = %d, want 9000", got[0].Port)
	}
	if got[1].Port != 9001 {
		t.Errorf("got[1].Port = %d, want 9001", got[1].Port)
	}
	if diff := gotAt.Sub(scannedAt2).Abs(); diff > time.Millisecond {
		t.Errorf("scannedAt = %v, want %v (diff %v > 1ms)", gotAt, scannedAt2, diff)
	}
}

// TestSaveSnapshot_EmptyRowsStillUpdatesMeta verifies that saving an empty
// slice still updates scan_meta so GetSnapshot returns empty slice + the given timestamp.
func TestSaveSnapshot_EmptyRowsStillUpdatesMeta(t *testing.T) {
	s := openSnapshotTestStore(t)
	ctx := t.Context()

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.SaveSnapshot(ctx, []store.SnapshotRow{}, t0); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	got, gotAt, err := s.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("expected 0 rows, got %d", len(got))
	}
	if diff := gotAt.Sub(t0).Abs(); diff > time.Millisecond {
		t.Errorf("scannedAt = %v, want %v (diff %v > 1ms)", gotAt, t0, diff)
	}
}

// TestSaveSnapshot_ZeroScannedAtUsesNow verifies that passing scannedAt=time.Time{}
// causes GetSnapshot to return a scannedAt within 5s of time.Now().UTC().
func TestSaveSnapshot_ZeroScannedAtUsesNow(t *testing.T) {
	s := openSnapshotTestStore(t)
	ctx := t.Context()

	before := time.Now().UTC()
	if err := s.SaveSnapshot(ctx, []store.SnapshotRow{}, time.Time{}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	after := time.Now().UTC()

	_, gotAt, err := s.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	if gotAt.IsZero() {
		t.Fatal("gotAt is zero, expected a real timestamp")
	}
	window := 5 * time.Second
	if gotAt.Before(before.Add(-window)) || gotAt.After(after.Add(window)) {
		t.Errorf("scannedAt = %v, want between %v and %v", gotAt, before.Add(-window), after.Add(window))
	}
}

// TestSaveSnapshot_InvalidStateRejected verifies that a row with an unknown
// State causes SaveSnapshot to return an error without modifying the DB.
func TestSaveSnapshot_InvalidStateRejected(t *testing.T) {
	s := openSnapshotTestStore(t)
	ctx := t.Context()

	// Pre-seed with a valid snapshot.
	seed := []store.SnapshotRow{
		{Port: 7777, State: store.StateUsedProcess, PID: 42, ProcessName: "seed"},
	}
	seedAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if err := s.SaveSnapshot(ctx, seed, seedAt); err != nil {
		t.Fatalf("seed SaveSnapshot: %v", err)
	}

	// Attempt to save a row with a bogus state.
	bad := []store.SnapshotRow{
		{Port: 8080, State: store.SnapshotState("bogus")},
	}
	err := s.SaveSnapshot(ctx, bad, time.Now().UTC())
	if err == nil {
		t.Fatal("expected error for invalid State, got nil")
	}

	// DB should be unchanged: still contains the seed row.
	got, gotAt, err2 := s.GetSnapshot(ctx)
	if err2 != nil {
		t.Fatalf("GetSnapshot after rejection: %v", err2)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 row (seed), got %d", len(got))
	}
	if len(got) == 1 && got[0].Port != 7777 {
		t.Errorf("expected port 7777, got %d", got[0].Port)
	}
	if diff := gotAt.Sub(seedAt).Abs(); diff > time.Millisecond {
		t.Errorf("scannedAt changed after rejection: got %v, want %v", gotAt, seedAt)
	}
}

// TestSaveSnapshot_NullableFieldsRoundTrip saves a row where all nullable
// fields are zero/empty and verifies that read-back returns zero values
// (not the literal string "NULL").
func TestSaveSnapshot_NullableFieldsRoundTrip(t *testing.T) {
	s := openSnapshotTestStore(t)
	ctx := t.Context()

	row := store.SnapshotRow{
		Port:  8080,
		State: store.StateUsedProcess,
		// All nullable fields left at zero values.
		PID:             0,
		ProcessName:     "",
		Cmdline:         "",
		ContainerID:     "",
		ContainerName:   "",
		ContainerImage:  "",
		ContainerStatus: "",
		UFWAction:       "",
		UFWRuleNum:      0,
	}

	if err := s.SaveSnapshot(ctx, []store.SnapshotRow{row}, time.Now().UTC()); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	got, _, err := s.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}

	r := got[0]
	if r.PID != 0 {
		t.Errorf("PID = %d, want 0", r.PID)
	}
	if r.ProcessName != "" {
		t.Errorf("ProcessName = %q, want empty", r.ProcessName)
	}
	if r.Cmdline != "" {
		t.Errorf("Cmdline = %q, want empty", r.Cmdline)
	}
	if r.ContainerID != "" {
		t.Errorf("ContainerID = %q, want empty", r.ContainerID)
	}
	if r.ContainerName != "" {
		t.Errorf("ContainerName = %q, want empty", r.ContainerName)
	}
	if r.ContainerImage != "" {
		t.Errorf("ContainerImage = %q, want empty", r.ContainerImage)
	}
	if r.ContainerStatus != "" {
		t.Errorf("ContainerStatus = %q, want empty", r.ContainerStatus)
	}
	if r.UFWAction != "" {
		t.Errorf("UFWAction = %q, want empty", r.UFWAction)
	}
	if r.UFWRuleNum != 0 {
		t.Errorf("UFWRuleNum = %d, want 0", r.UFWRuleNum)
	}
}

// TestSaveSnapshot_PopulatedFieldsRoundTrip saves a fully-populated docker
// container row and verifies all fields match exactly on read-back.
func TestSaveSnapshot_PopulatedFieldsRoundTrip(t *testing.T) {
	s := openSnapshotTestStore(t)
	ctx := t.Context()

	row := store.SnapshotRow{
		Port:            8443,
		State:           store.StateUsedDocker,
		PID:             55001,
		ProcessName:     "docker-proxy",
		Cmdline:         "docker-proxy -proto tcp -host-ip 0.0.0.0 -host-port 8443",
		ContainerID:     "a3f5b2c7d8e9",
		ContainerName:   "ai-translate-server",
		ContainerImage:  "translate:v2",
		ContainerStatus: "running",
		UFWAction:       "allow",
		UFWRuleNum:      5,
	}

	scannedAt := time.Date(2026, 4, 25, 14, 32, 18, 0, time.UTC)
	if err := s.SaveSnapshot(ctx, []store.SnapshotRow{row}, scannedAt); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	got, gotAt, err := s.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}

	r := got[0]
	if r.Port != row.Port {
		t.Errorf("Port = %d, want %d", r.Port, row.Port)
	}
	if r.State != row.State {
		t.Errorf("State = %q, want %q", r.State, row.State)
	}
	if r.PID != row.PID {
		t.Errorf("PID = %d, want %d", r.PID, row.PID)
	}
	if r.ProcessName != row.ProcessName {
		t.Errorf("ProcessName = %q, want %q", r.ProcessName, row.ProcessName)
	}
	if r.Cmdline != row.Cmdline {
		t.Errorf("Cmdline = %q, want %q", r.Cmdline, row.Cmdline)
	}
	if r.ContainerID != row.ContainerID {
		t.Errorf("ContainerID = %q, want %q", r.ContainerID, row.ContainerID)
	}
	if r.ContainerName != row.ContainerName {
		t.Errorf("ContainerName = %q, want %q", r.ContainerName, row.ContainerName)
	}
	if r.ContainerImage != row.ContainerImage {
		t.Errorf("ContainerImage = %q, want %q", r.ContainerImage, row.ContainerImage)
	}
	if r.ContainerStatus != row.ContainerStatus {
		t.Errorf("ContainerStatus = %q, want %q", r.ContainerStatus, row.ContainerStatus)
	}
	if r.UFWAction != row.UFWAction {
		t.Errorf("UFWAction = %q, want %q", r.UFWAction, row.UFWAction)
	}
	if r.UFWRuleNum != row.UFWRuleNum {
		t.Errorf("UFWRuleNum = %d, want %d", r.UFWRuleNum, row.UFWRuleNum)
	}
	if diff := gotAt.Sub(scannedAt).Abs(); diff > time.Millisecond {
		t.Errorf("scannedAt = %v, want %v (diff %v > 1ms)", gotAt, scannedAt, diff)
	}
}

// TestGetSnapshot_FreshStoreNoMeta verifies that calling GetSnapshot on a
// brand-new DB (no SaveSnapshot ever run) returns empty slice + zero time + nil error.
func TestGetSnapshot_FreshStoreNoMeta(t *testing.T) {
	s := openSnapshotTestStore(t)
	ctx := t.Context()

	got, gotAt, err := s.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetSnapshot on fresh store: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 rows, got %d", len(got))
	}
	if !gotAt.IsZero() {
		t.Errorf("expected zero time, got %v", gotAt)
	}
}

// TestSaveSnapshot_TransactionalAtomicity pre-seeds 1 row, then tries to save
// a slice containing an invalid state="bogus" entry. The error must be returned
// and the previously-seeded row must survive (transaction rolled back).
func TestSaveSnapshot_TransactionalAtomicity(t *testing.T) {
	s := openSnapshotTestStore(t)
	ctx := t.Context()

	// Pre-seed one valid row.
	seedAt := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	seed := []store.SnapshotRow{
		{Port: 6000, State: store.StateUsedProcess, PID: 999, ProcessName: "seeded"},
	}
	if err := s.SaveSnapshot(ctx, seed, seedAt); err != nil {
		t.Fatalf("seed SaveSnapshot: %v", err)
	}

	// Now try to save a mixed slice: one valid row + one invalid state.
	// The invalid state check is done before the transaction opens, so either:
	// - validation rejects immediately (before DELETE), OR
	// - if validation happens inside tx, rollback must restore the seed.
	// Either way the seed must survive.
	mixed := []store.SnapshotRow{
		{Port: 7000, State: store.StateUsedDocker, ContainerName: "valid-svc"},
		{Port: 7001, State: store.SnapshotState("bogus")},
	}
	err := s.SaveSnapshot(ctx, mixed, time.Now().UTC())
	if err == nil {
		t.Fatal("expected error for slice containing invalid state, got nil")
	}

	// Seed must survive.
	got, gotAt, err2 := s.GetSnapshot(ctx)
	if err2 != nil {
		t.Fatalf("GetSnapshot after failed save: %v", err2)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 row (seed) to survive, got %d", len(got))
	}
	if len(got) == 1 && got[0].Port != 6000 {
		t.Errorf("expected port 6000, got %d", got[0].Port)
	}
	if diff := gotAt.Sub(seedAt).Abs(); diff > time.Millisecond {
		t.Errorf("scan_meta changed after failed save: got %v, want %v", gotAt, seedAt)
	}
}

// TestSaveSnapshot_OrderingByPort inserts ports in order 9000, 8000, 8500
// (non-ascending) and verifies that GetSnapshot returns them as 8000, 8500, 9000.
func TestSaveSnapshot_OrderingByPort(t *testing.T) {
	s := openSnapshotTestStore(t)
	ctx := t.Context()

	rows := []store.SnapshotRow{
		{Port: 9000, State: store.StateUsedProcess, PID: 1},
		{Port: 8000, State: store.StateUsedDocker, ContainerName: "a"},
		{Port: 8500, State: store.StateReservedImplicit, ContainerName: "b"},
	}

	if err := s.SaveSnapshot(ctx, rows, time.Now().UTC()); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	got, _, err := s.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(got))
	}

	wantPorts := []int{8000, 8500, 9000}
	for i, r := range got {
		if r.Port != wantPorts[i] {
			t.Errorf("got[%d].Port = %d, want %d", i, r.Port, wantPorts[i])
		}
	}
}
