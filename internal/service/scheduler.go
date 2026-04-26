package service

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Clock abstraction
// ---------------------------------------------------------------------------

// Clock abstracts time so tests can run with deterministic time advancement.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
	NewTimer(d time.Duration) Timer
}

// Ticker wraps time.Ticker for testability.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Timer wraps time.Timer for testability.
type Timer interface {
	C() <-chan time.Time
	// Stop prevents the timer from firing. Returns true if the call stopped
	// the timer before it fired.
	Stop() bool
	// Reset changes the timer's expiry to d from now. Returns true if the
	// timer had not yet expired.
	Reset(d time.Duration) bool
}

// ---------------------------------------------------------------------------
// RealClock — production implementation backed by the time package
// ---------------------------------------------------------------------------

// RealClock backs Clock with the time package. Use this in production.
type RealClock struct{}

func (RealClock) Now() time.Time                   { return time.Now() }
func (RealClock) NewTicker(d time.Duration) Ticker { return realTicker{time.NewTicker(d)} }
func (RealClock) NewTimer(d time.Duration) Timer   { return realTimer{time.NewTimer(d)} }

// realTicker wraps *time.Ticker to satisfy Ticker.
type realTicker struct{ t *time.Ticker }

func (rt realTicker) C() <-chan time.Time { return rt.t.C }
func (rt realTicker) Stop()              { rt.t.Stop() }

// realTimer wraps *time.Timer to satisfy Timer.
type realTimer struct{ t *time.Timer }

func (rt realTimer) C() <-chan time.Time   { return rt.t.C }
func (rt realTimer) Stop() bool            { return rt.t.Stop() }
func (rt realTimer) Reset(d time.Duration) bool { return rt.t.Reset(d) }

// ---------------------------------------------------------------------------
// ScanFunc / CleanupFunc
// ---------------------------------------------------------------------------

// ScanFunc runs one scan-and-persist pass. Provided by the caller (likely in
// cmd/piper/serve.go in a later round) so the scheduler stays decoupled from
// scanner + store concrete types.
type ScanFunc func(ctx context.Context) error

// CleanupFunc runs one cleanup pass (delete history older than retention).
type CleanupFunc func(ctx context.Context) error

// ---------------------------------------------------------------------------
// Scheduler
// ---------------------------------------------------------------------------

// Scheduler runs ScanFunc on an interval and CleanupFunc daily.
type Scheduler struct {
	Clock    Clock
	Scan     ScanFunc
	Cleanup  CleanupFunc
	Interval time.Duration  // scan period (e.g. 5*time.Minute per design §4.3)
	Location *time.Location // for "midnight" calculation; defaults to Asia/Taipei when nil

	// OnScanError, OnCleanupError are called with each error so the caller
	// can log it. If nil, errors are silently swallowed.
	OnScanError    func(error)
	OnCleanupError func(error)

	// resolvedLoc is set at the start of Run so tests can inspect it.
	// Protected by resolvedMu.
	resolvedLoc *time.Location
	resolvedMu  sync.Mutex
}

// ResolvedLocation returns the timezone that was resolved during Run.
// It returns nil if Run has not been called yet or has not reached the
// location-resolution step. This is intentionally exported for test 10.
func (s *Scheduler) ResolvedLocation() *time.Location {
	s.resolvedMu.Lock()
	defer s.resolvedMu.Unlock()
	return s.resolvedLoc
}

// setResolvedLocation stores the resolved location thread-safely.
func (s *Scheduler) setResolvedLocation(loc *time.Location) {
	s.resolvedMu.Lock()
	defer s.resolvedMu.Unlock()
	s.resolvedLoc = loc
}

// Run blocks until ctx is canceled, doing:
//  1. Resolve location (nil → attempt Asia/Taipei, fall back to UTC).
//  2. Immediate Scan() once.
//  3. Immediate Cleanup() once (per design §4.3 step 2).
//  4. Loop: select on scan ticker, cleanup timer (re-armed daily at next midnight
//     in Location), and ctx.Done().
//
// Errors from Scan/Cleanup do NOT terminate Run — they are reported via the
// OnXxxError callbacks (if set) and the loop continues.
//
// Returns ctx.Err() when ctx is canceled (typically context.Canceled).
// Returns a non-nil error immediately if Interval <= 0 (fail fast).
func (s *Scheduler) Run(ctx context.Context) error {
	if s.Interval <= 0 {
		return fmt.Errorf("scheduler: interval must be > 0, got %v", s.Interval)
	}

	// Resolve location: nil → Asia/Taipei if available, else UTC.
	// This fallback is intentional: servers deployed outside Taiwan must not
	// crash just because the timezone DB is minimal.
	loc := s.Location
	if loc == nil {
		var err error
		loc, err = time.LoadLocation("Asia/Taipei")
		if err != nil {
			// Timezone database unavailable or Asia/Taipei not found.
			loc = time.UTC
		}
	}
	s.setResolvedLocation(loc)

	clock := s.Clock
	if clock == nil {
		clock = RealClock{}
	}

	// --- Immediate passes (run synchronously before starting the loop) ---
	//
	// Design §4.3 says startup should run one scan and one cleanup before
	// entering the periodic loop. We run them inline here so the immediate
	// passes complete before the loop select begins. Any error is reported
	// via callbacks; failures do NOT abort Run.
	if err := s.Scan(ctx); err != nil {
		if s.OnScanError != nil {
			s.OnScanError(err)
		}
	}
	if err := s.Cleanup(ctx); err != nil {
		if s.OnCleanupError != nil {
			s.OnCleanupError(err)
		}
	}

	// Check if context was canceled during the immediate passes.
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// --- Periodic loop ---
	scanTicker := clock.NewTicker(s.Interval)
	defer scanTicker.Stop()

	cleanupTimer := clock.NewTimer(NextMidnightIn(clock.Now(), loc))
	defer cleanupTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-scanTicker.C():
			if err := s.Scan(ctx); err != nil {
				if s.OnScanError != nil {
					s.OnScanError(err)
				}
			}

		case <-cleanupTimer.C():
			if err := s.Cleanup(ctx); err != nil {
				if s.OnCleanupError != nil {
					s.OnCleanupError(err)
				}
			}
			// Re-arm the cleanup timer for the NEXT midnight.
			cleanupTimer.Reset(NextMidnightIn(clock.Now(), loc))
		}
	}
}

// ---------------------------------------------------------------------------
// NextMidnightIn
// ---------------------------------------------------------------------------

// NextMidnightIn computes the duration from `now` until the next 00:00:00 in
// loc. It never returns zero or a negative duration: if `now` is exactly
// midnight, the next midnight is 24 hours away.
//
// Exported so tests and callers can verify the daily-cleanup math.
func NextMidnightIn(now time.Time, loc *time.Location) time.Duration {
	// Convert to the target location and find the start of tomorrow.
	local := now.In(loc)
	year, month, day := local.Date()
	// Midnight tomorrow in the same location.
	nextMidnight := time.Date(year, month, day+1, 0, 0, 0, 0, loc)
	d := nextMidnight.Sub(now)
	if d <= 0 {
		// Should not happen given the day+1 logic, but guard against it.
		d = 24 * time.Hour
	}
	return d
}
