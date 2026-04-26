package service

import (
	"testing"

	"github.com/GMfatcat/piper/internal/store"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// makeRow is a convenience constructor for SnapshotRow with the fields most
// relevant to diffing. Fields not needed by DiffSnapshots (PID, Cmdline, etc.)
// are left at their zero values.
func makeRow(port int, state store.SnapshotState, containerName, processName string) store.SnapshotRow {
	return store.SnapshotRow{
		Port:          port,
		State:         state,
		ContainerName: containerName,
		ProcessName:   processName,
	}
}

// eventMap indexes a slice of Events by port for O(1) lookup in assertions.
func eventMap(events []store.Event) map[int]store.Event {
	m := make(map[int]store.Event, len(events))
	for _, e := range events {
		m[e.Port] = e
	}
	return m
}

// ---------------------------------------------------------------------------
// Test 1: both empty → empty slice (not nil)
// ---------------------------------------------------------------------------

func TestDiffSnapshots_BothEmpty(t *testing.T) {
	// nil inputs
	got := DiffSnapshots(nil, nil)
	if got == nil {
		t.Error("(nil, nil): want non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("(nil, nil): want 0 events, got %d", len(got))
	}

	// empty-slice inputs
	got = DiffSnapshots([]store.SnapshotRow{}, []store.SnapshotRow{})
	if got == nil {
		t.Error("([], []): want non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("([], []): want 0 events, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Test 2: prev empty, curr has 3 rows → 3 EventOccupied
// ---------------------------------------------------------------------------

func TestDiffSnapshots_AllOccupied(t *testing.T) {
	curr := []store.SnapshotRow{
		makeRow(8080, store.StateUsedProcess, "", "nginx"),
		makeRow(8443, store.StateUsedDocker, "myapp", "docker-proxy"),
		makeRow(9000, store.StateReservedImplicit, "old-container", ""),
	}

	got := DiffSnapshots(nil, curr)

	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d: %+v", len(got), got)
	}

	em := eventMap(got)

	// All must be EventOccupied
	for _, port := range []int{8080, 8443, 9000} {
		ev, ok := em[port]
		if !ok {
			t.Errorf("port %d: missing event", port)
			continue
		}
		if ev.Event != store.EventOccupied {
			t.Errorf("port %d: want EventOccupied, got %q", port, ev.Event)
		}
	}

	// Occupant checks
	if em[8080].Occupant != "nginx" {
		t.Errorf("port 8080 occupant: want %q, got %q", "nginx", em[8080].Occupant)
	}
	if em[8443].Occupant != "myapp" {
		t.Errorf("port 8443 occupant: want %q (ContainerName wins), got %q", "myapp", em[8443].Occupant)
	}
	if em[9000].Occupant != "old-container" {
		t.Errorf("port 9000 occupant: want %q, got %q", "old-container", em[9000].Occupant)
	}
}

// ---------------------------------------------------------------------------
// Test 3: prev has 3 rows, curr empty → 3 EventReleased
// ---------------------------------------------------------------------------

func TestDiffSnapshots_AllReleased(t *testing.T) {
	prev := []store.SnapshotRow{
		makeRow(8080, store.StateUsedProcess, "", "nginx"),
		makeRow(8443, store.StateUsedDocker, "myapp", "docker-proxy"),
		makeRow(9000, store.StateReservedImplicit, "old-container", ""),
	}

	got := DiffSnapshots(prev, nil)

	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d: %+v", len(got), got)
	}

	em := eventMap(got)

	for _, port := range []int{8080, 8443, 9000} {
		ev, ok := em[port]
		if !ok {
			t.Errorf("port %d: missing event", port)
			continue
		}
		if ev.Event != store.EventReleased {
			t.Errorf("port %d: want EventReleased, got %q", port, ev.Event)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 4: prev == curr (same ports, same states) → no events
// ---------------------------------------------------------------------------

func TestDiffSnapshots_NoChange(t *testing.T) {
	rows := []store.SnapshotRow{
		makeRow(8080, store.StateUsedProcess, "", "nginx"),
		makeRow(9000, store.StateUsedDocker, "mycontainer", "docker-proxy"),
	}

	got := DiffSnapshots(rows, rows)

	if len(got) != 0 {
		t.Errorf("want 0 events for identical snapshots, got %d: %+v", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// Test 5: mixed add/remove — prev [8080, 9000, 9100], curr [8080, 8081, 9100]
//
// Expected: released 9000, occupied 8081.
// Ordering: released first (port ASC), then occupied (port ASC).
// ---------------------------------------------------------------------------

func TestDiffSnapshots_MixedAddRemove(t *testing.T) {
	prev := []store.SnapshotRow{
		makeRow(8080, store.StateUsedProcess, "", "nginx"),
		makeRow(9000, store.StateUsedDocker, "removed-app", "docker-proxy"),
		makeRow(9100, store.StateUsedProcess, "", "redis"),
	}
	curr := []store.SnapshotRow{
		makeRow(8080, store.StateUsedProcess, "", "nginx"),
		makeRow(8081, store.StateUsedDocker, "new-app", "docker-proxy"),
		makeRow(9100, store.StateUsedProcess, "", "redis"),
	}

	got := DiffSnapshots(prev, curr)

	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}

	// First event must be released 9000 (released before occupied)
	if got[0].Event != store.EventReleased {
		t.Errorf("got[0].Event: want EventReleased, got %q", got[0].Event)
	}
	if got[0].Port != 9000 {
		t.Errorf("got[0].Port: want 9000, got %d", got[0].Port)
	}

	// Second event must be occupied 8081
	if got[1].Event != store.EventOccupied {
		t.Errorf("got[1].Event: want EventOccupied, got %q", got[1].Event)
	}
	if got[1].Port != 8081 {
		t.Errorf("got[1].Port: want 8081, got %d", got[1].Port)
	}
}

// ---------------------------------------------------------------------------
// Test 6: occupant — ContainerName wins over ProcessName
// ---------------------------------------------------------------------------

func TestDiffSnapshots_OccupantContainerWins(t *testing.T) {
	curr := []store.SnapshotRow{
		{
			Port:          8080,
			State:         store.StateUsedDocker,
			ContainerName: "mycontainer",
			ProcessName:   "docker-proxy",
		},
	}

	got := DiffSnapshots(nil, curr)

	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].Occupant != "mycontainer" {
		t.Errorf("occupant: want %q, got %q", "mycontainer", got[0].Occupant)
	}
}

// ---------------------------------------------------------------------------
// Test 7: occupant — ProcessName used when ContainerName is empty
// ---------------------------------------------------------------------------

func TestDiffSnapshots_OccupantProcessWhenNoContainer(t *testing.T) {
	curr := []store.SnapshotRow{
		{
			Port:          6379,
			State:         store.StateUsedProcess,
			ContainerName: "",
			ProcessName:   "redis-server",
		},
	}

	got := DiffSnapshots(nil, curr)

	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].Occupant != "redis-server" {
		t.Errorf("occupant: want %q, got %q", "redis-server", got[0].Occupant)
	}
}

// ---------------------------------------------------------------------------
// Test 8: occupant — both empty → ""
// ---------------------------------------------------------------------------

func TestDiffSnapshots_OccupantEmpty(t *testing.T) {
	curr := []store.SnapshotRow{
		{
			Port:          5432,
			State:         store.StateUsedProcess,
			ContainerName: "",
			ProcessName:   "",
		},
	}

	got := DiffSnapshots(nil, curr)

	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].Occupant != "" {
		t.Errorf("occupant: want %q (empty → DB NULL), got %q", "", got[0].Occupant)
	}
}

// ---------------------------------------------------------------------------
// Test 9: state change within "still used" → no event emitted
//
// Port 8080 was used_process in prev, now used_docker in curr.
// Both are "present", so no occupied/released event should fire.
// ---------------------------------------------------------------------------

func TestDiffSnapshots_StateChangeNoEvent(t *testing.T) {
	prev := []store.SnapshotRow{
		makeRow(8080, store.StateUsedProcess, "", "old-process"),
	}
	curr := []store.SnapshotRow{
		makeRow(8080, store.StateUsedDocker, "new-container", "docker-proxy"),
	}

	got := DiffSnapshots(prev, curr)

	if len(got) != 0 {
		t.Errorf("want 0 events for state-change-within-present, got %d: %+v", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// Test 10: emitted events have zero Timestamp (caller controls timestamping)
// ---------------------------------------------------------------------------

func TestDiffSnapshots_TimestampZero(t *testing.T) {
	curr := []store.SnapshotRow{
		makeRow(8080, store.StateUsedProcess, "", "nginx"),
	}
	prev := []store.SnapshotRow{
		makeRow(9000, store.StateUsedProcess, "", "redis"),
	}

	got := DiffSnapshots(prev, curr)

	// Expect: released 9000, occupied 8080
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d", len(got))
	}

	for _, ev := range got {
		if !ev.Timestamp.IsZero() {
			t.Errorf("port %d: Timestamp: want zero, got %v", ev.Port, ev.Timestamp)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 11: ordering — released ports ASC first, then occupied ports ASC
//
// Released ports: [9100, 8000, 8500] → sorted: [8000, 8500, 9100]
// Occupied ports: [9000, 7878]       → sorted: [7878, 9000]
// Final order: [released 8000, released 8500, released 9100, occupied 7878, occupied 9000]
// ---------------------------------------------------------------------------

func TestDiffSnapshots_OrderingSorted(t *testing.T) {
	prev := []store.SnapshotRow{
		makeRow(9100, store.StateUsedProcess, "", "p1"),
		makeRow(8000, store.StateUsedProcess, "", "p2"),
		makeRow(8500, store.StateUsedProcess, "", "p3"),
	}
	curr := []store.SnapshotRow{
		makeRow(9000, store.StateUsedProcess, "", "p4"),
		makeRow(7878, store.StateUsedProcess, "", "p5"),
	}

	got := DiffSnapshots(prev, curr)

	if len(got) != 5 {
		t.Fatalf("want 5 events, got %d: %+v", len(got), got)
	}

	wantOrder := []struct {
		port  int
		event store.EventType
	}{
		{8000, store.EventReleased},
		{8500, store.EventReleased},
		{9100, store.EventReleased},
		{7878, store.EventOccupied},
		{9000, store.EventOccupied},
	}

	for i, want := range wantOrder {
		if got[i].Port != want.port {
			t.Errorf("got[%d].Port: want %d, got %d", i, want.port, got[i].Port)
		}
		if got[i].Event != want.event {
			t.Errorf("got[%d].Event: want %q, got %q", i, want.event, got[i].Event)
		}
	}
}
