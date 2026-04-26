package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fake clock infrastructure
// ---------------------------------------------------------------------------

// fakeClock implements Clock with a controllable "now" and fake tickers/timers.
// All methods are safe for concurrent use; callers hold mu before mutating.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
	timers  []*fakeTimer
}

func newFakeClock(t time.Time) *fakeClock {
	return &fakeClock{now: t}
}

func (fc *fakeClock) Now() time.Time {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.now
}

// WaitForTimers blocks until at least n timers have been registered, or the
// timeout elapses. Used by tests to eliminate a race where Advance can run
// before Scheduler.Run reaches its NewTimer() call (immediate scan/cleanup
// finish before timer registration). Returns true on success.
func (fc *fakeClock) WaitForTimers(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		got := len(fc.timers)
		fc.mu.Unlock()
		if got >= n {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// WaitForTickers is the ticker-equivalent of WaitForTimers.
func (fc *fakeClock) WaitForTickers(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		got := len(fc.tickers)
		fc.mu.Unlock()
		if got >= n {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func (fc *fakeClock) NewTicker(d time.Duration) Ticker {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ft := &fakeTicker{
		clock:    fc,
		interval: d,
		next:     fc.now.Add(d),
		ch:       make(chan time.Time, 1),
	}
	fc.tickers = append(fc.tickers, ft)
	return ft
}

func (fc *fakeClock) NewTimer(d time.Duration) Timer {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ft := &fakeTimer{
		clock: fc,
		next:  fc.now.Add(d),
		ch:    make(chan time.Time, 1),
	}
	fc.timers = append(fc.timers, ft)
	return ft
}

// Advance bumps the clock by d and fires any tickers/timers whose next-fire
// time is <= the new now. Advancing is done under the clock's lock; channel
// sends are done outside the lock to avoid deadlock with receivers.
func (fc *fakeClock) Advance(d time.Duration) {
	fc.mu.Lock()
	fc.now = fc.now.Add(d)
	now := fc.now

	// Collect pending fires; we will fire them outside the lock.
	type tickerFire struct {
		ft   *fakeTicker
		when time.Time
	}
	type timerFire struct {
		ft   *fakeTimer
		when time.Time
	}

	var tickerFires []tickerFire
	var timerFires []timerFire

	for _, t := range fc.tickers {
		for !t.stopped && !t.next.After(now) {
			tickerFires = append(tickerFires, tickerFire{t, t.next})
			t.next = t.next.Add(t.interval)
		}
	}
	for _, t := range fc.timers {
		if !t.fired && !t.stopped && !t.next.After(now) {
			timerFires = append(timerFires, timerFire{t, t.next})
			t.fired = true
		}
	}
	fc.mu.Unlock()

	// Send on channels outside lock.
	for _, f := range tickerFires {
		select {
		case f.ft.ch <- f.when:
		default:
			// channel full — drop (ticker semantics)
		}
	}
	for _, f := range timerFires {
		select {
		case f.ft.ch <- f.when:
		default:
		}
	}
}

// ---------------------------------------------------------------------------
// fakeTicker
// ---------------------------------------------------------------------------

type fakeTicker struct {
	clock    *fakeClock
	interval time.Duration
	next     time.Time
	ch       chan time.Time
	stopped  bool
}

func (ft *fakeTicker) C() <-chan time.Time { return ft.ch }
func (ft *fakeTicker) Stop() {
	ft.clock.mu.Lock()
	defer ft.clock.mu.Unlock()
	ft.stopped = true
}

// ---------------------------------------------------------------------------
// fakeTimer
// ---------------------------------------------------------------------------

type fakeTimer struct {
	clock   *fakeClock
	next    time.Time
	ch      chan time.Time
	stopped bool
	fired   bool
}

func (ft *fakeTimer) C() <-chan time.Time { return ft.ch }

func (ft *fakeTimer) Stop() bool {
	ft.clock.mu.Lock()
	defer ft.clock.mu.Unlock()
	if ft.fired || ft.stopped {
		ft.stopped = true
		return false
	}
	ft.stopped = true
	return true
}

func (ft *fakeTimer) Reset(d time.Duration) bool {
	ft.clock.mu.Lock()
	// drain any pending send first
	alreadyFired := ft.fired
	ft.fired = false
	ft.stopped = false
	ft.next = ft.clock.now.Add(d)
	ft.clock.mu.Unlock()

	// drain channel if needed
	select {
	case <-ft.ch:
	default:
	}
	return !alreadyFired
}

// ---------------------------------------------------------------------------
// Helper: build a simple Scheduler with a fake clock.
// ---------------------------------------------------------------------------

func newTestScheduler(clock *fakeClock, interval time.Duration, scan ScanFunc, cleanup CleanupFunc) *Scheduler {
	return &Scheduler{
		Clock:    clock,
		Scan:     scan,
		Cleanup:  cleanup,
		Interval: interval,
	}
}

// ---------------------------------------------------------------------------
// Test 1: Interval <= 0 → error
// ---------------------------------------------------------------------------

func TestScheduler_RequiresPositiveInterval(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Now())
	s := newTestScheduler(clock, 0, func(ctx context.Context) error { return nil }, func(ctx context.Context) error { return nil })

	err := s.Run(context.Background())
	if err == nil {
		t.Fatal("expected error for zero Interval, got nil")
	}
	if !containsString(err.Error(), "interval") {
		t.Fatalf("error should mention 'interval', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Immediate scan + cleanup before any tick
// ---------------------------------------------------------------------------

func TestScheduler_FiresImmediateScanAndCleanup(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Now())

	scanCh := make(chan struct{}, 1)
	cleanupCh := make(chan struct{}, 1)

	scan := func(ctx context.Context) error {
		scanCh <- struct{}{}
		return nil
	}
	cleanup := func(ctx context.Context) error {
		cleanupCh <- struct{}{}
		return nil
	}

	s := newTestScheduler(clock, 5*time.Minute, scan, cleanup)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Both scan and cleanup should fire immediately (before any tick).
	waitForSignal(t, scanCh, 200*time.Millisecond, "immediate scan")
	waitForSignal(t, cleanupCh, 200*time.Millisecond, "immediate cleanup")

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// Test 3: Scan fires on interval
// ---------------------------------------------------------------------------

func TestScheduler_ScanFiresOnInterval(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Now())

	var mu sync.Mutex
	scanCount := 0
	scan := func(ctx context.Context) error {
		mu.Lock()
		scanCount++
		mu.Unlock()
		return nil
	}
	noopCleanup := func(ctx context.Context) error { return nil }

	s := newTestScheduler(clock, 5*time.Minute, scan, noopCleanup)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Wait for the immediate scan to register (goroutine needs to start and
	// call Scan once before we advance the clock).
	waitForCount(t, &mu, &scanCount, 1, 2*time.Second, "initial scan")
	if !clock.WaitForTickers(1, 2*time.Second) {
		t.Fatal("scan ticker not registered in time")
	}

	// Advance 5 min → ticker fires → total == 2
	clock.Advance(5 * time.Minute)
	waitForCount(t, &mu, &scanCount, 2, 2*time.Second, "scan after 5min")

	// Advance another 5 min → total == 3
	clock.Advance(5 * time.Minute)
	waitForCount(t, &mu, &scanCount, 3, 2*time.Second, "scan after 10min")

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// Test 4: Cleanup fires at midnight
// ---------------------------------------------------------------------------

func TestScheduler_CleanupFiresAtMidnight(t *testing.T) {
	t.Parallel()

	// Set clock to 23:59:00 Asia/Taipei
	taipei, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Skip("Asia/Taipei not available, skipping test")
	}
	start := time.Date(2026, 4, 25, 23, 59, 0, 0, taipei)
	clock := newFakeClock(start)

	var mu sync.Mutex
	cleanupCount := 0
	cleanup := func(ctx context.Context) error {
		mu.Lock()
		cleanupCount++
		mu.Unlock()
		return nil
	}
	noopScan := func(ctx context.Context) error { return nil }

	s := &Scheduler{
		Clock:    clock,
		Scan:     noopScan,
		Cleanup:  cleanup,
		Interval: 5 * time.Minute,
		Location: taipei,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Wait for immediate cleanup AND timer registration before any Advance.
	waitForCount(t, &mu, &cleanupCount, 1, 2*time.Second, "immediate cleanup")
	if !clock.WaitForTimers(1, 2*time.Second) {
		t.Fatal("cleanup timer not registered in time")
	}

	// Advance 1 min → crosses midnight in Taipei → cleanup fires again
	clock.Advance(1 * time.Minute)
	waitForCount(t, &mu, &cleanupCount, 2, 2*time.Second, "midnight cleanup")

	// Advance another 24h → cleanup fires a third time
	clock.Advance(24 * time.Hour)
	waitForCount(t, &mu, &cleanupCount, 3, 2*time.Second, "next midnight cleanup")

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// Test 5: ScanError — continues running
// ---------------------------------------------------------------------------

func TestScheduler_ContinuesOnScanError(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Now())

	errScan := errors.New("scan failed")
	var errMu sync.Mutex
	var errs []error

	scan := func(ctx context.Context) error { return errScan }
	noopCleanup := func(ctx context.Context) error { return nil }

	s := newTestScheduler(clock, 5*time.Minute, scan, noopCleanup)
	s.OnScanError = func(e error) {
		errMu.Lock()
		errs = append(errs, e)
		errMu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Wait for initial scan error AND ticker registration before any Advance.
	waitForErrCount(t, &errMu, &errs, 1, 2*time.Second, "initial scan error")
	if !clock.WaitForTickers(1, 2*time.Second) {
		t.Fatal("scan ticker not registered in time")
	}

	// Advance 5 min → second scan error
	clock.Advance(5 * time.Minute)
	waitForErrCount(t, &errMu, &errs, 2, 2*time.Second, "second scan error")

	// Advance another 5 min → third scan error
	clock.Advance(5 * time.Minute)
	waitForErrCount(t, &errMu, &errs, 3, 2*time.Second, "third scan error")

	// Run must not have exited
	select {
	case <-done:
		t.Fatal("Run exited unexpectedly on scan errors")
	default:
	}

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// Test 6: CleanupError — continues running
// ---------------------------------------------------------------------------

func TestScheduler_ContinuesOnCleanupError(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Now())

	errCleanup := errors.New("cleanup failed")
	var errMu sync.Mutex
	var errs []error

	taipei, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Skip("Asia/Taipei timezone not available")
	}

	// Start at 23:59 so next midnight fires after 1 minute
	start := time.Date(2026, 4, 25, 23, 59, 0, 0, taipei)
	clock2 := newFakeClock(start)

	noopScan := func(ctx context.Context) error { return nil }
	cleanup := func(ctx context.Context) error { return errCleanup }

	s := &Scheduler{
		Clock:    clock2,
		Scan:     noopScan,
		Cleanup:  cleanup,
		Interval: 5 * time.Minute,
		Location: taipei,
		OnCleanupError: func(e error) {
			errMu.Lock()
			errs = append(errs, e)
			errMu.Unlock()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Immediate cleanup fires and errors
	waitForErrCount(t, &errMu, &errs, 1, 2*time.Second, "immediate cleanup error")

	// Wait for the cleanup timer to actually be registered before Advance,
	// otherwise Advance happens before NewTimer and the timer is created
	// with next = clock.now + 24h (won't fire on this 1-min advance).
	if !clock2.WaitForTimers(1, 2*time.Second) {
		t.Fatal("cleanup timer not registered in time")
	}

	// Advance 1 min → midnight cleanup → second error
	clock2.Advance(1 * time.Minute)
	waitForErrCount(t, &errMu, &errs, 2, 2*time.Second, "midnight cleanup error")

	// Run must not have exited
	select {
	case <-done:
		t.Fatal("Run exited unexpectedly on cleanup errors")
	default:
	}

	_ = clock // suppress unused var warning

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// Test 7: nil callbacks are safe (no panic)
// ---------------------------------------------------------------------------

func TestScheduler_NilErrorCallbacksAreSafe(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Now())

	scan := func(ctx context.Context) error { return errors.New("boom") }
	cleanup := func(ctx context.Context) error { return errors.New("boom cleanup") }

	s := &Scheduler{
		Clock:          clock,
		Scan:           scan,
		Cleanup:        cleanup,
		Interval:       5 * time.Minute,
		OnScanError:    nil,
		OnCleanupError: nil,
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Give it a moment to run the immediate scan/cleanup (which will error)
	// without panicking.
	time.Sleep(50 * time.Millisecond)

	// No panic = success.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run did not return after cancel")
	}
}

// ---------------------------------------------------------------------------
// Test 8: ctx cancellation returns promptly
// ---------------------------------------------------------------------------

func TestScheduler_RespectsContextCancellation(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Now())

	scan := func(ctx context.Context) error { return nil }
	cleanup := func(ctx context.Context) error { return nil }

	s := newTestScheduler(clock, 5*time.Minute, scan, cleanup)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Let the immediate scan+cleanup complete.
	time.Sleep(50 * time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return promptly after context cancel")
	}
}

// ---------------------------------------------------------------------------
// Test 9: NextMidnightIn — table-driven
// ---------------------------------------------------------------------------

func TestNextMidnightIn(t *testing.T) {
	taipei, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Skip("Asia/Taipei not available")
	}

	cases := []struct {
		name    string
		now     time.Time
		loc     *time.Location
		wantDur time.Duration
	}{
		{
			name:    "12:00 UTC → 12h",
			now:     time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
			loc:     time.UTC,
			wantDur: 12 * time.Hour,
		},
		{
			name:    "23:59 Taipei → 1min",
			now:     time.Date(2026, 4, 25, 23, 59, 0, 0, taipei),
			loc:     taipei,
			wantDur: 1 * time.Minute,
		},
		{
			name:    "exactly midnight → 24h (never zero)",
			now:     time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
			loc:     time.UTC,
			wantDur: 24 * time.Hour,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NextMidnightIn(tc.now, tc.loc)
			if got != tc.wantDur {
				t.Errorf("NextMidnightIn(%v, %v) = %v; want %v",
					tc.now, tc.loc, got, tc.wantDur)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 10: nil Location defaults to Asia/Taipei or UTC without error
// ---------------------------------------------------------------------------

func TestScheduler_DefaultsToTaipeiOrUTC(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Now())

	scan := func(ctx context.Context) error { return nil }
	cleanup := func(ctx context.Context) error { return nil }

	// Location intentionally left nil
	s := &Scheduler{
		Clock:    clock,
		Scan:     scan,
		Cleanup:  cleanup,
		Interval: 5 * time.Minute,
		Location: nil,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Give goroutine time to start and resolve location.
	time.Sleep(50 * time.Millisecond)

	// ResolvedLocation should be set now.
	resolved := s.ResolvedLocation()
	if resolved == nil {
		t.Fatal("ResolvedLocation returned nil after Run started")
	}

	// It must be either Asia/Taipei or UTC.
	name := resolved.String()
	if name != "Asia/Taipei" && name != "UTC" {
		t.Fatalf("resolved location is %q; want Asia/Taipei or UTC", name)
	}

	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// Synchronisation helpers
// ---------------------------------------------------------------------------

func waitForSignal(t *testing.T, ch <-chan struct{}, timeout time.Duration, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for %s", label)
	}
}

func waitForCount(t *testing.T, mu *sync.Mutex, counter *int, want int, timeout time.Duration, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := *counter
		mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	got := *counter
	mu.Unlock()
	if got < want {
		t.Fatalf("%s: want count >= %d, got %d", label, want, got)
	}
}

func waitForErrCount(t *testing.T, mu *sync.Mutex, errs *[]error, want int, timeout time.Duration, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(*errs)
		mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	got := len(*errs)
	mu.Unlock()
	if got < want {
		t.Fatalf("%s: want error count >= %d, got %d", label, want, got)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
