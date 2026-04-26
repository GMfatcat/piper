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

// ─── Stubs ────────────────────────────────────────────────────────────────────

// Note: memReservationStore and memEventAppender are defined in reserve_test.go.

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestRunRelease_HappyPath verifies that releasing an existing reservation
// removes it from the store and appends an EventUnreserved history event.
func TestRunRelease_HappyPath(t *testing.T) {
	rs := newMemReservationStore()
	ea := &memEventAppender{}
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}

	// Pre-seed a reservation.
	err := rs.InsertReservation(context.Background(), store.Reservation{
		Port:      9100,
		Name:      "vllm-llama",
		Note:      "next week deployment",
		CreatedAt: time.Now().UTC(),
		CreatedBy: "testuser",
	})
	if err != nil {
		t.Fatalf("pre-seed InsertReservation error: %v", err)
	}

	deps := ReleaseDeps{
		Reservations: rs,
		Events:       ea,
		Out:          w,
	}

	err = RunRelease(context.Background(), deps, 9100)
	if err != nil {
		t.Fatalf("RunRelease error: %v", err)
	}

	// Verify reservation gone.
	_, err = rs.GetReservation(context.Background(), 9100)
	if !errors.Is(err, store.ErrReservationNotFound) {
		t.Errorf("expected ErrReservationNotFound after release, got: %v", err)
	}

	// Verify history event appended.
	if len(ea.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ea.events))
	}
	ev := ea.events[0]
	if ev.Event != store.EventUnreserved {
		t.Errorf("expected event=unreserved, got %q", ev.Event)
	}
	if ev.Port != 9100 {
		t.Errorf("expected port=9100, got %d", ev.Port)
	}
}

// TestRunRelease_NotFound verifies that releasing a port with no reservation
// returns a friendly error.
func TestRunRelease_NotFound(t *testing.T) {
	rs := newMemReservationStore()
	ea := &memEventAppender{}
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}

	deps := ReleaseDeps{
		Reservations: rs,
		Events:       ea,
		Out:          w,
	}

	err := RunRelease(context.Background(), deps, 9999)
	if err == nil {
		t.Fatal("expected error for non-existent reservation, got nil")
	}

	msg := err.Error()
	if !strings.Contains(msg, "9999") {
		t.Errorf("error should mention port 9999, got: %q", msg)
	}
}

// TestRunRelease_HistoryUsesPreviousName verifies that the EventUnreserved row's
// Occupant matches the deleted reservation's name (not empty).
func TestRunRelease_HistoryUsesPreviousName(t *testing.T) {
	rs := newMemReservationStore()
	ea := &memEventAppender{}
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}

	// Pre-seed a reservation with a specific name.
	err := rs.InsertReservation(context.Background(), store.Reservation{
		Port:      8080,
		Name:      "my-unique-service",
		CreatedAt: time.Now().UTC(),
		CreatedBy: "testuser",
	})
	if err != nil {
		t.Fatalf("pre-seed InsertReservation error: %v", err)
	}

	deps := ReleaseDeps{
		Reservations: rs,
		Events:       ea,
		Out:          w,
	}

	err = RunRelease(context.Background(), deps, 8080)
	if err != nil {
		t.Fatalf("RunRelease error: %v", err)
	}

	if len(ea.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ea.events))
	}
	ev := ea.events[0]
	if ev.Occupant != "my-unique-service" {
		t.Errorf("expected Occupant=my-unique-service, got %q", ev.Occupant)
	}
	if ev.Event != store.EventUnreserved {
		t.Errorf("expected event=unreserved, got %q", ev.Event)
	}
}
