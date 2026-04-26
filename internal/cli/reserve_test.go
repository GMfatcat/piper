package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/store"
)

// ─── In-memory stubs for reserve/release ─────────────────────────────────────

// memReservationStore is an in-memory implementation of the ReservationStore interface.
type memReservationStore struct {
	reservations map[int]store.Reservation
}

func newMemReservationStore() *memReservationStore {
	return &memReservationStore{
		reservations: make(map[int]store.Reservation),
	}
}

func (m *memReservationStore) InsertReservation(_ context.Context, r store.Reservation) error {
	if _, exists := m.reservations[r.Port]; exists {
		return store.ErrReservationExists
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	m.reservations[r.Port] = r
	return nil
}

func (m *memReservationStore) GetReservation(_ context.Context, port int) (store.Reservation, error) {
	r, ok := m.reservations[port]
	if !ok {
		return store.Reservation{}, store.ErrReservationNotFound
	}
	return r, nil
}

func (m *memReservationStore) DeleteReservation(_ context.Context, port int) error {
	if _, ok := m.reservations[port]; !ok {
		return store.ErrReservationNotFound
	}
	delete(m.reservations, port)
	return nil
}

// memEventAppender is an in-memory implementation of the EventAppender interface.
type memEventAppender struct {
	events []store.Event
}

func (m *memEventAppender) AppendEvent(_ context.Context, e store.Event) (store.Event, error) {
	e.ID = int64(len(m.events) + 1)
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	m.events = append(m.events, e)
	return e, nil
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestRunReserve_HappyPath verifies that a successful reserve persists the
// reservation and appends an EventReserved history event.
func TestRunReserve_HappyPath(t *testing.T) {
	rs := newMemReservationStore()
	ea := &memEventAppender{}
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}

	deps := ReserveDeps{
		Reservations: rs,
		Events:       ea,
		Out:          w,
		UserFn:       func() string { return "testuser" },
	}

	err := RunReserve(context.Background(), deps, 9100, "vllm-llama", "")
	if err != nil {
		t.Fatalf("RunReserve error: %v", err)
	}

	// Verify reservation persisted.
	r, err := rs.GetReservation(context.Background(), 9100)
	if err != nil {
		t.Fatalf("GetReservation error: %v", err)
	}
	if r.Name != "vllm-llama" {
		t.Errorf("expected name=vllm-llama, got %q", r.Name)
	}
	if r.CreatedBy != "testuser" {
		t.Errorf("expected created_by=testuser, got %q", r.CreatedBy)
	}

	// Verify history event.
	if len(ea.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ea.events))
	}
	ev := ea.events[0]
	if ev.Event != store.EventReserved {
		t.Errorf("expected event=reserved, got %q", ev.Event)
	}
	if ev.Port != 9100 {
		t.Errorf("expected port=9100, got %d", ev.Port)
	}
	if ev.Occupant != "vllm-llama" {
		t.Errorf("expected occupant=vllm-llama, got %q", ev.Occupant)
	}
}

// TestRunReserve_DuplicateError verifies that reserving the same port twice
// returns a friendly error mentioning the port.
func TestRunReserve_DuplicateError(t *testing.T) {
	rs := newMemReservationStore()
	ea := &memEventAppender{}
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}

	deps := ReserveDeps{
		Reservations: rs,
		Events:       ea,
		Out:          w,
		UserFn:       func() string { return "testuser" },
	}

	// First reservation succeeds.
	err := RunReserve(context.Background(), deps, 8080, "service-a", "")
	if err != nil {
		t.Fatalf("first RunReserve error: %v", err)
	}

	// Second reservation on same port should fail with a friendly error.
	err = RunReserve(context.Background(), deps, 8080, "service-b", "")
	if err == nil {
		t.Fatal("expected error for duplicate reservation, got nil")
	}

	msg := err.Error()
	if !strings.Contains(msg, "8080") {
		t.Errorf("error message should mention port 8080, got: %q", msg)
	}
}

// TestRunReserve_NoteIsOptional verifies that a reservation without a note
// has an empty Note field.
func TestRunReserve_NoteIsOptional(t *testing.T) {
	rs := newMemReservationStore()
	ea := &memEventAppender{}
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}

	deps := ReserveDeps{
		Reservations: rs,
		Events:       ea,
		Out:          w,
		UserFn:       func() string { return "testuser" },
	}

	err := RunReserve(context.Background(), deps, 9200, "my-service", "")
	if err != nil {
		t.Fatalf("RunReserve error: %v", err)
	}

	r, err := rs.GetReservation(context.Background(), 9200)
	if err != nil {
		t.Fatalf("GetReservation error: %v", err)
	}
	if r.Note != "" {
		t.Errorf("expected empty note, got %q", r.Note)
	}
}

// TestRunReserve_CreatedByFallback verifies that when userFn returns "unknown"
// (simulating os/user.Current error), the reservation still persists with
// created_by="unknown".
func TestRunReserve_CreatedByFallback(t *testing.T) {
	rs := newMemReservationStore()
	ea := &memEventAppender{}
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}

	deps := ReserveDeps{
		Reservations: rs,
		Events:       ea,
		Out:          w,
		UserFn:       func() string { return "unknown" }, // simulated fallback
	}

	err := RunReserve(context.Background(), deps, 9300, "test-svc", "")
	if err != nil {
		t.Fatalf("RunReserve error: %v", err)
	}

	r, err := rs.GetReservation(context.Background(), 9300)
	if err != nil {
		t.Fatalf("GetReservation error: %v", err)
	}
	if r.CreatedBy != "unknown" {
		t.Errorf("expected created_by=unknown, got %q", r.CreatedBy)
	}
}

// ─── ReservationStore interface errors propagation ───────────────────────────

// failingReservationStore always fails on insert.
type failingInsertStore struct {
	*memReservationStore
	insertErr error
}

func (f *failingInsertStore) InsertReservation(ctx context.Context, r store.Reservation) error {
	return f.insertErr
}

// TestRunReserve_InsertErrorPropagation verifies generic insert errors are propagated.
func TestRunReserve_InsertErrorPropagation(t *testing.T) {
	rs := &failingInsertStore{
		memReservationStore: newMemReservationStore(),
		insertErr:           errors.New("database error"),
	}
	ea := &memEventAppender{}
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}

	deps := ReserveDeps{
		Reservations: rs,
		Events:       ea,
		Out:          w,
		UserFn:       func() string { return "testuser" },
	}

	err := RunReserve(context.Background(), deps, 9400, "test-svc", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
