package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/GMfatcat/piper/internal/store"
)

// ReservationLister is the consumer-side interface the suggester needs. The
// real implementation is *store.Store (its ListReservations matches this
// signature).
type ReservationLister interface {
	ListReservations(ctx context.Context) ([]store.Reservation, error)
}

// ErrNoFreePortsFound is returned when fewer than n free ports exist in
// [from, to]. The partial result (however many were found) is returned
// alongside this error so the caller can surface "found 2 of 3 requested".
var ErrNoFreePortsFound = errors.New("not enough free ports in the requested range")

// Suggester implements SuggestFreePorts business logic. Inject SnapshotProvider
// and ReservationLister; the zero value is not useful.
type Suggester struct {
	Snap     SnapshotProvider
	Reserves ReservationLister
}

// SuggestFreePorts implements design §4.7:
//
//	Sequential scan from `from` (inclusive) to `to` (inclusive), returning the
//	first `n` ports satisfying ALL of:
//	  - not present in snapshot SS (no listener)
//	  - not present in snapshot Docker as a host port (no docker -p mapping,
//	    running OR stopped — stopped == implicit reservation)
//	  - not in explicit reservations
//
// Validation:
//   - n <= 0    → return empty slice + nil error
//   - from < 1  → return error "from must be >= 1"
//   - to > 65535 → return error "to must be <= 65535"
//   - from > to  → return error "from must be <= to"
//
// On range exhaustion (fewer than n found), returns ErrNoFreePortsFound along
// with the partial result found so far.
//
// UFW rules are NOT a disqualifier — having a UFW allow rule for a port with
// no listener is fine; suggest can still hand out that port.
func (s *Suggester) SuggestFreePorts(ctx context.Context, n, from, to int) ([]int, error) {
	// --- Validation ---
	if n <= 0 {
		return []int{}, nil
	}
	if from < 1 {
		return nil, fmt.Errorf("from must be >= 1")
	}
	if to > 65535 {
		return nil, fmt.Errorf("to must be <= 65535")
	}
	if from > to {
		return nil, fmt.Errorf("from must be <= to")
	}

	// --- Build lookup sets (once, before the loop) ---

	// 1. Get snapshot.
	snap, err := s.Snap.LatestSnapshot(ctx)
	if err != nil {
		return nil, err
	}

	// ssPorts: ports with active listeners.
	ssPorts := make(map[int]struct{}, len(snap.SS))
	for _, entry := range snap.SS {
		ssPorts[entry.Port] = struct{}{}
	}

	// dockerPorts: all host ports bound by any docker container (running OR
	// stopped). HostPort == 0 means pure EXPOSE (no host binding) — skip it.
	dockerPorts := make(map[int]struct{})
	for _, entry := range snap.Docker {
		for _, hp := range entry.Ports {
			if hp.HostPort == 0 {
				continue
			}
			dockerPorts[hp.HostPort] = struct{}{}
		}
	}

	// 2. Get explicit reservations.
	reservations, err := s.Reserves.ListReservations(ctx)
	if err != nil {
		return nil, err
	}

	explicitReserved := make(map[int]struct{}, len(reservations))
	for _, r := range reservations {
		explicitReserved[r.Port] = struct{}{}
	}

	// --- Sequential scan ---
	result := make([]int, 0, n)

	for port := from; port <= to; port++ {
		if _, blocked := ssPorts[port]; blocked {
			continue
		}
		if _, blocked := dockerPorts[port]; blocked {
			continue
		}
		if _, blocked := explicitReserved[port]; blocked {
			continue
		}
		result = append(result, port)
		if len(result) == n {
			break
		}
	}

	if len(result) < n {
		return result, ErrNoFreePortsFound
	}
	return result, nil
}
