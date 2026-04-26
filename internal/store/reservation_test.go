package store_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/store"
)

// newTestStore opens a fresh Store backed by a temp-dir database and registers
// cleanup via t.Cleanup. It follows the same pattern used in migrate_test.go.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestInsertReservation_New inserts a reservation and verifies GetReservation
// returns identical fields.
func TestInsertReservation_New(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	r := store.Reservation{
		Port:      8080,
		Name:      "grafana",
		Note:      "monitoring dashboard",
		CreatedAt: ts,
		CreatedBy: "alice",
	}

	if err := s.InsertReservation(ctx, r); err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}

	got, err := s.GetReservation(ctx, 8080)
	if err != nil {
		t.Fatalf("GetReservation: %v", err)
	}

	if got.Port != r.Port {
		t.Errorf("Port = %d, want %d", got.Port, r.Port)
	}
	if got.Name != r.Name {
		t.Errorf("Name = %q, want %q", got.Name, r.Name)
	}
	if got.Note != r.Note {
		t.Errorf("Note = %q, want %q", got.Note, r.Note)
	}
	if got.CreatedBy != r.CreatedBy {
		t.Errorf("CreatedBy = %q, want %q", got.CreatedBy, r.CreatedBy)
	}
	if diff := got.CreatedAt.Sub(ts).Abs(); diff > time.Microsecond {
		t.Errorf("CreatedAt = %v, want %v (diff %v > 1µs)", got.CreatedAt, ts, diff)
	}
}

// TestInsertReservation_DuplicatePort verifies inserting the same port twice
// returns ErrReservationExists.
func TestInsertReservation_DuplicatePort(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	r := store.Reservation{Port: 9090, Name: "prometheus"}

	if err := s.InsertReservation(ctx, r); err != nil {
		t.Fatalf("first InsertReservation: %v", err)
	}

	err := s.InsertReservation(ctx, r)
	if !errors.Is(err, store.ErrReservationExists) {
		t.Errorf("second InsertReservation error = %v, want ErrReservationExists", err)
	}
}

// TestInsertReservation_NullableFields inserts with empty Note and CreatedBy,
// then reads back and verifies both are empty strings (not the literal "NULL").
func TestInsertReservation_NullableFields(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	r := store.Reservation{
		Port:      7777,
		Name:      "test-service",
		Note:      "",      // empty → should store as SQL NULL, return as ""
		CreatedBy: "",      // empty → should store as SQL NULL, return as ""
	}

	if err := s.InsertReservation(ctx, r); err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}

	got, err := s.GetReservation(ctx, 7777)
	if err != nil {
		t.Fatalf("GetReservation: %v", err)
	}

	if got.Note != "" {
		t.Errorf("Note = %q, want empty string", got.Note)
	}
	if got.CreatedBy != "" {
		t.Errorf("CreatedBy = %q, want empty string", got.CreatedBy)
	}
}

// TestInsertReservation_PreservesProvidedTimestamp verifies that a non-zero
// CreatedAt passed to InsertReservation is preserved on read-back (within 1µs).
func TestInsertReservation_PreservesProvidedTimestamp(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	// A specific past UTC timestamp.
	ts := time.Date(2024, 1, 20, 8, 30, 0, 500000, time.UTC)
	r := store.Reservation{
		Port:      6060,
		Name:      "old-service",
		CreatedAt: ts,
	}

	if err := s.InsertReservation(ctx, r); err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}

	got, err := s.GetReservation(ctx, 6060)
	if err != nil {
		t.Fatalf("GetReservation: %v", err)
	}

	if diff := got.CreatedAt.Sub(ts).Abs(); diff > time.Microsecond {
		t.Errorf("CreatedAt = %v, want %v (diff %v > 1µs)", got.CreatedAt, ts, diff)
	}
}

// TestInsertReservation_DefaultsTimestamp verifies that a zero CreatedAt lets
// SQLite fill in a recent UTC timestamp (within last 5 seconds of now).
func TestInsertReservation_DefaultsTimestamp(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	before := time.Now().UTC()

	r := store.Reservation{
		Port: 5000,
		Name: "new-service",
		// CreatedAt intentionally zero
	}

	if err := s.InsertReservation(ctx, r); err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}

	after := time.Now().UTC()

	got, err := s.GetReservation(ctx, 5000)
	if err != nil {
		t.Fatalf("GetReservation: %v", err)
	}

	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt is zero, expected SQLite to default it")
	}
	// Allow a generous 5-second window to account for any clock drift.
	window := 5 * time.Second
	low := before.Add(-window)
	high := after.Add(window)
	if got.CreatedAt.Before(low) || got.CreatedAt.After(high) {
		t.Errorf("CreatedAt = %v, want between %v and %v", got.CreatedAt, low, high)
	}
}

// TestGetReservation_NotFound verifies that fetching a port with no reservation
// returns ErrReservationNotFound.
func TestGetReservation_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	_, err := s.GetReservation(ctx, 9999)
	if !errors.Is(err, store.ErrReservationNotFound) {
		t.Errorf("GetReservation error = %v, want ErrReservationNotFound", err)
	}
}

// TestDeleteReservation_Existing inserts a reservation, deletes it, then
// confirms GetReservation returns ErrReservationNotFound.
func TestDeleteReservation_Existing(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	r := store.Reservation{Port: 4040, Name: "to-be-deleted"}
	if err := s.InsertReservation(ctx, r); err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}

	if err := s.DeleteReservation(ctx, 4040); err != nil {
		t.Fatalf("DeleteReservation: %v", err)
	}

	_, err := s.GetReservation(ctx, 4040)
	if !errors.Is(err, store.ErrReservationNotFound) {
		t.Errorf("post-delete GetReservation error = %v, want ErrReservationNotFound", err)
	}
}

// TestDeleteReservation_Missing verifies that deleting a port that was never
// reserved returns ErrReservationNotFound.
func TestDeleteReservation_Missing(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	err := s.DeleteReservation(ctx, 3333)
	if !errors.Is(err, store.ErrReservationNotFound) {
		t.Errorf("DeleteReservation error = %v, want ErrReservationNotFound", err)
	}
}

// TestListReservations_Empty verifies that a fresh store returns an empty
// non-nil slice with no error.
func TestListReservations_Empty(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	list, err := s.ListReservations(ctx)
	if err != nil {
		t.Fatalf("ListReservations: %v", err)
	}
	if list == nil {
		t.Error("ListReservations returned nil, want empty non-nil slice")
	}
	if len(list) != 0 {
		t.Errorf("ListReservations returned %d items, want 0", len(list))
	}
}

// TestListReservations_OrderedByPort inserts ports 9100, 8080, 8443 (intentionally
// out of order) and verifies ListReservations returns them as 8080, 8443, 9100.
func TestListReservations_OrderedByPort(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	ports := []int{9100, 8080, 8443}
	for _, p := range ports {
		r := store.Reservation{Port: p, Name: "service"}
		if err := s.InsertReservation(ctx, r); err != nil {
			t.Fatalf("InsertReservation(%d): %v", p, err)
		}
	}

	list, err := s.ListReservations(ctx)
	if err != nil {
		t.Fatalf("ListReservations: %v", err)
	}

	if len(list) != 3 {
		t.Fatalf("ListReservations returned %d items, want 3", len(list))
	}

	want := []int{8080, 8443, 9100}
	for i, r := range list {
		if r.Port != want[i] {
			t.Errorf("list[%d].Port = %d, want %d", i, r.Port, want[i])
		}
	}
}
