package service

import (
	"sort"

	"github.com/GMfatcat/piper/internal/store"
)

// DiffSnapshots compares the previous snapshot to the current one and emits
// history events:
//   - port present in prev but ABSENT in curr → store.EventReleased
//   - port ABSENT in prev but present in curr → store.EventOccupied
//   - port present in BOTH → no event (state changes within "still used" do not emit)
//
// "Present" means the port appears as a row in the snapshot regardless of
// state (used_process / used_docker / reserved_implicit). The reservation
// table is NOT consulted here — explicit reservations live in their own
// table and don't appear in scan_snapshots.
//
// Occupant string for emitted events:
//   - row.ContainerName if non-empty
//   - else row.ProcessName if non-empty
//   - else "" (NULL in DB)
//
// Event timestamps are LEFT ZERO (caller should set or let DB default fire).
// Returned events have stable order: released events first (port ASC), then
// occupied events (port ASC). Caller may re-order if needed.
//
// Pure function — no I/O, no DB, no scanner.
func DiffSnapshots(prev, curr []store.SnapshotRow) []store.Event {
	// Index prev by port.
	prevByPort := make(map[int]store.SnapshotRow, len(prev))
	for _, r := range prev {
		prevByPort[r.Port] = r
	}

	// Index curr by port.
	currByPort := make(map[int]store.SnapshotRow, len(curr))
	for _, r := range curr {
		currByPort[r.Port] = r
	}

	// Collect released ports: in prev but not in curr.
	var released []store.Event
	for port, row := range prevByPort {
		if _, inCurr := currByPort[port]; !inCurr {
			released = append(released, store.Event{
				Port:     port,
				Event:    store.EventReleased,
				Occupant: occupantFor(row),
			})
		}
	}

	// Collect occupied ports: in curr but not in prev.
	var occupied []store.Event
	for port, row := range currByPort {
		if _, inPrev := prevByPort[port]; !inPrev {
			occupied = append(occupied, store.Event{
				Port:     port,
				Event:    store.EventOccupied,
				Occupant: occupantFor(row),
			})
		}
	}

	// Sort each sub-list by port ASC for stable output.
	sort.Slice(released, func(i, j int) bool {
		return released[i].Port < released[j].Port
	})
	sort.Slice(occupied, func(i, j int) bool {
		return occupied[i].Port < occupied[j].Port
	})

	// Concatenate: released first, then occupied.
	events := make([]store.Event, 0, len(released)+len(occupied))
	events = append(events, released...)
	events = append(events, occupied...)
	return events
}

// occupantFor derives the occupant string for a history event from a snapshot
// row. ContainerName takes priority; ProcessName is the fallback; "" when both
// are empty (the store will write NULL to the DB).
func occupantFor(r store.SnapshotRow) string {
	if r.ContainerName != "" {
		return r.ContainerName
	}
	return r.ProcessName
}
