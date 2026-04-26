package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/store"
)

// openTestStore is a helper that opens a fresh Store in t.TempDir().
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestAppendEvent_New verifies that AppendEvent returns a non-zero ID and
// non-zero Timestamp, and that a subsequent QueryEvents returns the same event.
func TestAppendEvent_New(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	in := store.Event{
		Port:      8080,
		Event:     store.EventOccupied,
		Occupant:  "nginx",
		Timestamp: now,
	}

	got, err := s.AppendEvent(ctx, in)
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if got.ID == 0 {
		t.Error("returned event has zero ID")
	}
	if got.Timestamp.IsZero() {
		t.Error("returned event has zero Timestamp")
	}

	events, err := s.QueryEvents(ctx, store.HistoryQuery{})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	e := events[0]
	if e.ID != got.ID {
		t.Errorf("ID: got %d, want %d", e.ID, got.ID)
	}
	if e.Port != 8080 {
		t.Errorf("Port: got %d, want 8080", e.Port)
	}
	if e.Event != store.EventOccupied {
		t.Errorf("Event: got %q, want %q", e.Event, store.EventOccupied)
	}
	if e.Occupant != "nginx" {
		t.Errorf("Occupant: got %q, want %q", e.Occupant, "nginx")
	}
}

// TestAppendEvent_DefaultTimestamp verifies that when Timestamp is zero,
// the stored timestamp is set by SQLite DEFAULT CURRENT_TIMESTAMP and is
// within 5 seconds of time.Now().UTC().
func TestAppendEvent_DefaultTimestamp(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	before := time.Now().UTC()

	in := store.Event{
		Port:  9090,
		Event: store.EventReleased,
		// Timestamp left as zero value
	}

	got, err := s.AppendEvent(ctx, in)
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	after := time.Now().UTC()

	if got.Timestamp.IsZero() {
		t.Fatal("returned Timestamp is zero")
	}
	// Allow a generous 5-second window for the DB round-trip.
	if got.Timestamp.Before(before.Add(-5*time.Second)) || got.Timestamp.After(after.Add(5*time.Second)) {
		t.Errorf("Timestamp %v is not within 5s of now (before=%v after=%v)", got.Timestamp, before, after)
	}
}

// TestAppendEvent_InvalidEventType verifies that an unknown event type is
// rejected with an error and no row is inserted.
func TestAppendEvent_InvalidEventType(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	in := store.Event{
		Port:  8080,
		Event: store.EventType("garbage"),
	}

	_, err := s.AppendEvent(ctx, in)
	if err == nil {
		t.Fatal("expected error for invalid event type, got nil")
	}

	// Verify no row was inserted.
	var count int
	if scanErr := s.DB().QueryRow(`SELECT COUNT(*) FROM history`).Scan(&count); scanErr != nil {
		t.Fatalf("count query: %v", scanErr)
	}
	if count != 0 {
		t.Errorf("expected 0 rows after rejection, got %d", count)
	}
}

// TestAppendEvent_NullOccupant verifies that an empty Occupant is stored as
// SQL NULL and read back as empty string (not "NULL").
func TestAppendEvent_NullOccupant(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	in := store.Event{
		Port:      8080,
		Event:     store.EventReserved,
		Occupant:  "", // should be stored as NULL
		Timestamp: now,
	}

	got, err := s.AppendEvent(ctx, in)
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if got.Occupant != "" {
		t.Errorf("returned Occupant = %q, want empty string", got.Occupant)
	}

	events, err := s.QueryEvents(ctx, store.HistoryQuery{})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Occupant != "" {
		t.Errorf("queried Occupant = %q, want empty string", events[0].Occupant)
	}
}

// TestQueryEvents_OrderingNewestFirst inserts three events with explicit
// timestamps t1 < t2 < t3 in non-chronological order and verifies that
// QueryEvents returns them newest-first (t3, t2, t1).
func TestQueryEvents_OrderingNewestFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := base
	t2 := base.Add(1 * time.Hour)
	t3 := base.Add(2 * time.Hour)

	// Insert in non-chronological order to verify ordering is by timestamp, not insert order.
	for _, e := range []store.Event{
		{Port: 8080, Event: store.EventOccupied, Timestamp: t2},
		{Port: 8080, Event: store.EventOccupied, Timestamp: t1},
		{Port: 8080, Event: store.EventOccupied, Timestamp: t3},
	} {
		if _, err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	events, err := s.QueryEvents(ctx, store.HistoryQuery{})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Newest first: t3, t2, t1.
	want := []time.Time{t3, t2, t1}
	for i, e := range events {
		if !e.Timestamp.Equal(want[i]) {
			t.Errorf("events[%d].Timestamp = %v, want %v", i, e.Timestamp, want[i])
		}
	}
}

// TestQueryEvents_FilterByPort inserts events for ports 8080, 8081, 8080 and
// verifies that filtering by Port=8080 returns exactly 2 events.
func TestQueryEvents_FilterByPort(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	inputs := []store.Event{
		{Port: 8080, Event: store.EventOccupied, Timestamp: base},
		{Port: 8081, Event: store.EventOccupied, Timestamp: base.Add(1 * time.Minute)},
		{Port: 8080, Event: store.EventReleased, Timestamp: base.Add(2 * time.Minute)},
	}
	for _, e := range inputs {
		if _, err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	port := 8080
	events, err := s.QueryEvents(ctx, store.HistoryQuery{Port: &port})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events for port 8080, got %d", len(events))
	}
	for _, e := range events {
		if e.Port != 8080 {
			t.Errorf("expected port 8080, got %d", e.Port)
		}
	}
}

// TestQueryEvents_FilterByEvent inserts occupied, released, and reserved events
// and verifies that filtering by Event=EventReleased returns only those.
func TestQueryEvents_FilterByEvent(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	inputs := []store.Event{
		{Port: 8080, Event: store.EventOccupied, Timestamp: base},
		{Port: 8080, Event: store.EventReleased, Timestamp: base.Add(1 * time.Minute)},
		{Port: 8081, Event: store.EventReserved, Timestamp: base.Add(2 * time.Minute)},
		{Port: 8081, Event: store.EventReleased, Timestamp: base.Add(3 * time.Minute)},
	}
	for _, e := range inputs {
		if _, err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	events, err := s.QueryEvents(ctx, store.HistoryQuery{Event: store.EventReleased})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 released events, got %d", len(events))
	}
	for _, e := range events {
		if e.Event != store.EventReleased {
			t.Errorf("expected EventReleased, got %q", e.Event)
		}
	}
}

// TestQueryEvents_FilterBySince inserts events at t-10d, t-5d, and t-1d and
// verifies that Since=now-7d returns only the 2 newer events.
func TestQueryEvents_FilterBySince(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	now := time.Now().UTC()
	tMinus10 := now.AddDate(0, 0, -10).Truncate(time.Second)
	tMinus5 := now.AddDate(0, 0, -5).Truncate(time.Second)
	tMinus1 := now.AddDate(0, 0, -1).Truncate(time.Second)
	since := now.AddDate(0, 0, -7)

	inputs := []store.Event{
		{Port: 8080, Event: store.EventOccupied, Timestamp: tMinus10},
		{Port: 8080, Event: store.EventReleased, Timestamp: tMinus5},
		{Port: 8080, Event: store.EventOccupied, Timestamp: tMinus1},
	}
	for _, e := range inputs {
		if _, err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	events, err := s.QueryEvents(ctx, store.HistoryQuery{Since: since})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events since %v, got %d", since, len(events))
	}
	for _, e := range events {
		if e.Timestamp.Before(since) {
			t.Errorf("event timestamp %v is before Since %v", e.Timestamp, since)
		}
	}
}

// TestQueryEvents_RespectLimit inserts 5 events and verifies that Limit=3
// returns exactly 3 (the 3 newest).
func TestQueryEvents_RespectLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		e := store.Event{
			Port:      8080,
			Event:     store.EventOccupied,
			Timestamp: base.Add(time.Duration(i) * time.Hour),
		}
		if _, err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	events, err := s.QueryEvents(ctx, store.HistoryQuery{Limit: 3})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 events with Limit=3, got %d", len(events))
	}

	// Verify these are the 3 newest (hours 4, 3, 2 → offsets 4h, 3h, 2h).
	want4 := base.Add(4 * time.Hour)
	want3 := base.Add(3 * time.Hour)
	want2 := base.Add(2 * time.Hour)
	if !events[0].Timestamp.Equal(want4) {
		t.Errorf("events[0] timestamp = %v, want %v", events[0].Timestamp, want4)
	}
	if !events[1].Timestamp.Equal(want3) {
		t.Errorf("events[1] timestamp = %v, want %v", events[1].Timestamp, want3)
	}
	if !events[2].Timestamp.Equal(want2) {
		t.Errorf("events[2] timestamp = %v, want %v", events[2].Timestamp, want2)
	}
}

// TestQueryEvents_EmptyResult verifies that QueryEvents on a fresh store
// returns an empty non-nil slice.
func TestQueryEvents_EmptyResult(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	events, err := s.QueryEvents(ctx, store.HistoryQuery{})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if events == nil {
		t.Error("QueryEvents returned nil, want empty non-nil slice")
	}
	if len(events) != 0 {
		t.Errorf("expected empty slice, got %d events", len(events))
	}
}

// TestCleanupHistory_DeletesOldOnly inserts events at t-40d, t-30d, t-29d, t-1d
// and verifies that CleanupHistory with cutoff=t-30d deletes only t-40d (strict
// less-than), returning count=1.
func TestCleanupHistory_DeletesOldOnly(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	now := time.Now().UTC()
	tMinus40 := now.AddDate(0, 0, -40).Truncate(time.Second)
	tMinus30 := now.AddDate(0, 0, -30).Truncate(time.Second)
	tMinus29 := now.AddDate(0, 0, -29).Truncate(time.Second)
	tMinus1 := now.AddDate(0, 0, -1).Truncate(time.Second)
	cutoff := tMinus30

	inputs := []store.Event{
		{Port: 8080, Event: store.EventOccupied, Timestamp: tMinus40},
		{Port: 8080, Event: store.EventReleased, Timestamp: tMinus30},
		{Port: 8080, Event: store.EventOccupied, Timestamp: tMinus29},
		{Port: 8080, Event: store.EventReleased, Timestamp: tMinus1},
	}
	for _, e := range inputs {
		if _, err := s.AppendEvent(ctx, e); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	deleted, err := s.CleanupHistory(ctx, cutoff)
	if err != nil {
		t.Fatalf("CleanupHistory: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 row deleted, got %d", deleted)
	}

	// Verify 3 rows remain.
	events, err := s.QueryEvents(ctx, store.HistoryQuery{})
	if err != nil {
		t.Fatalf("QueryEvents after cleanup: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 remaining events, got %d", len(events))
	}

	// The t-40d event must be gone.
	for _, e := range events {
		if e.Timestamp.Equal(tMinus40) {
			t.Errorf("event at t-40d was not deleted")
		}
	}
}

// TestCleanupHistory_NoMatches verifies that CleanupHistory on a fresh store
// returns 0, nil.
func TestCleanupHistory_NoMatches(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	cutoff := time.Now().UTC()
	deleted, err := s.CleanupHistory(ctx, cutoff)
	if err != nil {
		t.Fatalf("CleanupHistory: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted on fresh store, got %d", deleted)
	}
}
