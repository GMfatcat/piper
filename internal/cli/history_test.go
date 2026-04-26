package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/store"
)

// ─── Stub for EventQuerier ─────────────────────────────────────────────────

// stubEventQuerier records the query it received and returns a fixed result.
type stubEventQuerier struct {
	receivedQuery store.HistoryQuery
	events        []store.Event
	err           error
}

func (s *stubEventQuerier) QueryEvents(_ context.Context, q store.HistoryQuery) ([]store.Event, error) {
	s.receivedQuery = q
	return s.events, s.err
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestRunHistory_PassesQueryThrough verifies that RunHistory forwards the
// caller-constructed HistoryQuery to the EventQuerier unchanged.
func TestRunHistory_PassesQueryThrough(t *testing.T) {
	stub := &stubEventQuerier{}
	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := HistoryDeps{Events: stub, Out: w}

	port := 8080
	q := store.HistoryQuery{Port: &port, Limit: 25}

	if err := RunHistory(context.Background(), deps, q); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stub.receivedQuery.Port == nil || *stub.receivedQuery.Port != 8080 {
		t.Errorf("expected Port=8080, got %v", stub.receivedQuery.Port)
	}
	if stub.receivedQuery.Limit != 25 {
		t.Errorf("expected Limit=25, got %d", stub.receivedQuery.Limit)
	}
}

// TestRunHistory_DefaultsApplied verifies that NewHistoryCmd applies --days 7
// by default (constructs a Since approximately 7 days ago).
func TestRunHistory_DefaultsApplied(t *testing.T) {
	stub := &stubEventQuerier{}
	var buf bytes.Buffer

	cmd := NewHistoryCmd(func() HistoryDeps {
		return HistoryDeps{
			Events: stub,
			Out:    &Writer{Format: FormatText, NoColor: true, Stdout: &buf},
		}
	})
	// Cobra needs an output sink to avoid os.Stderr spam in tests.
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	before := time.Now().UTC()
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute error: %v", err)
	}
	after := time.Now().UTC()

	// Since should be approximately 7 days before cmd execution.
	wantSinceLow := before.Add(-7 * 24 * time.Hour).Add(-2 * time.Second)
	wantSinceHigh := after.Add(-7 * 24 * time.Hour).Add(2 * time.Second)

	since := stub.receivedQuery.Since
	if since.IsZero() {
		t.Fatal("expected Since to be set (7 days), got zero")
	}
	if since.Before(wantSinceLow) || since.After(wantSinceHigh) {
		t.Errorf("Since=%v out of expected range [%v, %v]", since, wantSinceLow, wantSinceHigh)
	}
}

// TestRunHistory_RespectsLimitDefault verifies that NewHistoryCmd applies
// --limit 50 by default.
func TestRunHistory_RespectsLimitDefault(t *testing.T) {
	stub := &stubEventQuerier{}
	var buf bytes.Buffer

	cmd := NewHistoryCmd(func() HistoryDeps {
		return HistoryDeps{
			Events: stub,
			Out:    &Writer{Format: FormatText, NoColor: true, Stdout: &buf},
		}
	})
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute error: %v", err)
	}

	if stub.receivedQuery.Limit != 50 {
		t.Errorf("expected default Limit=50, got %d", stub.receivedQuery.Limit)
	}
}

// TestRunHistory_OutputsViaWriter verifies that the 3 events returned by the
// stub are forwarded to the Writer.
func TestRunHistory_OutputsViaWriter(t *testing.T) {
	now := time.Now().UTC()
	events := []store.Event{
		{ID: 1, Port: 8080, Event: store.EventOccupied, Occupant: "test-container", Timestamp: now},
		{ID: 2, Port: 8081, Event: store.EventReleased, Occupant: "", Timestamp: now},
		{ID: 3, Port: 9000, Event: store.EventReserved, Occupant: "planned", Timestamp: now},
	}
	stub := &stubEventQuerier{events: events}

	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := HistoryDeps{Events: stub, Out: w}

	if err := RunHistory(context.Background(), deps, store.HistoryQuery{Limit: 50}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	for _, p := range []string{"8080", "8081", "9000"} {
		if !containsStr(out, p) {
			t.Errorf("output missing port %s:\n%s", p, out)
		}
	}
}

// TestRunHistory_PropagatesError verifies that a querier error is surfaced.
func TestRunHistory_PropagatesError(t *testing.T) {
	wantErr := errors.New("db exploded")
	stub := &stubEventQuerier{err: wantErr}

	var buf bytes.Buffer
	w := &Writer{Format: FormatText, NoColor: true, Stdout: &buf}
	deps := HistoryDeps{Events: stub, Out: w}

	err := RunHistory(context.Background(), deps, store.HistoryQuery{})
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wantErr, got: %v", err)
	}
}

// containsStr is a simple substring helper used across cli tests.
func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
