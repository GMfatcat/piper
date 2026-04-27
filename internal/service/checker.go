package service

import (
	"context"
	"errors"
	"time"

	"github.com/GMfatcat/piper/internal/scanner"
	"github.com/GMfatcat/piper/internal/store"
)

// ---------------------------------------------------------------------------
// Consumer-side interfaces
// ---------------------------------------------------------------------------

// ScanSnapshot is the data Checker needs from a scanner result. Once
// scanner.ScanResult lands, a thin adapter function can populate this struct
// from that type without any import cycle.
type ScanSnapshot struct {
	ScannedAt time.Time
	SS        []scanner.SSEntry
	Docker    []scanner.DockerEntry
	// Inspected maps container ID → DockerEntry with full port detail (from
	// docker inspect). Used to expand sibling ports.
	Inspected map[string]scanner.DockerEntry
	UFW       []scanner.UFWRule
	UFWActive bool
	// UFWReadable is true when the underlying `ufw status` command succeeded,
	// independent of whether the firewall itself is active. False indicates a
	// permission/missing-binary problem the operator must fix (sudoers).
	UFWReadable bool
}

// SnapshotProvider abstracts the source of the latest scan data. In
// production this will call scanner.Scanner.Scan and adapt the result; in
// tests it is satisfied by stubSnap.
type SnapshotProvider interface {
	LatestSnapshot(ctx context.Context) (ScanSnapshot, error)
}

// ReservationGetter abstracts looking up a single port's explicit reservation.
// In production this is satisfied directly by *store.Store (whose
// GetReservation signature already matches). In tests it is satisfied by
// stubReserves.
type ReservationGetter interface {
	GetReservation(ctx context.Context, port int) (store.Reservation, error)
}

// ---------------------------------------------------------------------------
// Checker
// ---------------------------------------------------------------------------

// Checker is the single entry-point for CheckPort business logic. Inject
// SnapshotProvider and ReservationGetter; the zero value is not useful.
type Checker struct {
	Snap     SnapshotProvider
	Reserves ReservationGetter
}

// CheckPorts returns a CheckResult with one PortStatus per requested port.
// Requested order is preserved.
//
// Decision tree (design §6.1):
//  1. Look up explicit reservation → if it exists AND there is no listener →
//     reserved_explicit (reservation wins over nothing).
//  2. Find an SS listener for the port:
//     a. If the listener process is docker-proxy → look up the docker entry;
//        if the container is running → used_docker, expand container_other_ports.
//        if the container is exited/stopped → reserved_implicit (with container info).
//     b. Otherwise → used_process, Occupant.Type="process".
//  3. Find a docker entry whose host-port list includes this port (no SS entry):
//     a. Container running → used_docker, expand container_other_ports.
//     b. Container stopped → reserved_implicit.
//  4. No SS entry, no docker entry, no explicit reservation → free.
//  5. Attach UFW info from the UFW slice (keyed by port, proto ignored in Phase 1).
//  6. Dedup: if the port already appeared in an earlier result's
//     container_other_ports, emit a minimal {Port, State, DedupOf=ownerPort}
//     record instead of the full detail.
//
// Tiebreak — explicit reservation vs active listener (step 1 vs steps 2/3):
// When BOTH an explicit reservation AND an active listener exist for the same
// port, the listener wins (state = used_process or used_docker). The
// Reservation field is still populated so the caller can surface the planning
// conflict. This matches the real-world semantics: if someone already deployed
// against a "reserved" port, reality takes priority.
func (c *Checker) CheckPorts(ctx context.Context, ports []int) (CheckResult, error) {
	snap, err := c.Snap.LatestSnapshot(ctx)
	if err != nil {
		return CheckResult{}, err
	}

	// Build fast-lookup maps from the snapshot.
	ssByPort := buildSSByPort(snap.SS)
	dockerByHostPort := buildDockerByHostPort(snap.Docker)
	ufwByPort := buildUFWByPort(snap.UFW)

	// Track which ports have already been "fully presented" by appearing in an
	// earlier result's ContainerOtherPorts. Maps port → ownerPort.
	presentedPorts := make(map[int]int)

	results := make([]PortStatus, 0, len(ports))

	for _, port := range ports {
		// Check if this port has already been presented via another result's
		// container_other_ports.
		if ownerPort, alreadySeen := presentedPorts[port]; alreadySeen {
			results = append(results, PortStatus{
				Port:    port,
				State:   dedupState(port, ssByPort, dockerByHostPort),
				DedupOf: ownerPort,
			})
			continue
		}

		ps, err := c.classifyPort(ctx, port, snap, ssByPort, dockerByHostPort, ufwByPort)
		if err != nil {
			return CheckResult{}, err
		}

		// If this is a docker result, mark all sibling ports as presented.
		if ps.Occupant != nil && ps.Occupant.Type == "docker" {
			for _, cop := range ps.ContainerOtherPorts {
				presentedPorts[cop.Port] = port
			}
		}

		results = append(results, ps)
	}

	return CheckResult{
		ScannedAt: snap.ScannedAt,
		Results:   results,
	}, nil
}

// classifyPort applies the decision tree to a single port and returns its
// PortStatus. UFW attachment is also performed here.
func (c *Checker) classifyPort(
	ctx context.Context,
	port int,
	snap ScanSnapshot,
	ssByPort map[int]scanner.SSEntry,
	dockerByHostPort map[int]scanner.DockerEntry,
	ufwByPort map[int]scanner.UFWRule,
) (PortStatus, error) {
	ps := PortStatus{Port: port}

	// Step 1: Check for explicit reservation.
	var explicitRes *ReservationInfo
	reservation, err := c.Reserves.GetReservation(ctx, port)
	if err != nil && !errors.Is(err, store.ErrReservationNotFound) {
		return PortStatus{}, err
	}
	if err == nil {
		// Explicit reservation found.
		explicitRes = &ReservationInfo{
			Source: "explicit",
			Name:   reservation.Name,
			Note:   reservation.Note,
		}
	}

	// Steps 2 & 3: Check SS and Docker.
	ssEntry, hasSSEntry := ssByPort[port]
	dockerEntry, hasDockerEntry := dockerByHostPort[port]

	if hasSSEntry {
		// Step 2: There is a listener.
		if ssEntry.ProcessName == "docker-proxy" && hasDockerEntry {
			// Step 2a: docker-proxy → classify by docker container state.
			ps = c.classifyDockerPort(port, dockerEntry, snap.Inspected, ufwByPort)
		} else {
			// Step 2b: regular process listener.
			ps.State = PortUsedProcess
			ps.Occupant = &Occupant{
				Type:        "process",
				PID:         ssEntry.PID,
				ProcessName: ssEntry.ProcessName,
			}
		}
		// Attach reservation if it exists (planning conflict — listener wins
		// over reservation, but we still report the reservation so the caller
		// can surface the conflict).
		ps.Reservation = explicitRes
	} else if hasDockerEntry {
		// Step 3: No SS listener but docker has this port mapped.
		ps = c.classifyDockerPort(port, dockerEntry, snap.Inspected, ufwByPort)
		// (No listener, so explicit reservation is only reported when docker
		// entry is not already an implicit reservation.)
		if explicitRes != nil && ps.Reservation == nil {
			ps.Reservation = explicitRes
		}
	} else if explicitRes != nil {
		// Step 1 result: explicit reservation, no listener.
		ps.State = PortReservedExplicit
		ps.Reservation = explicitRes
	} else {
		// Step 4: Nothing found → free.
		ps.State = PortFree
	}

	// Step 5: Attach UFW.
	if rule, ok := ufwByPort[port]; ok {
		ps.UFW = &UFWInfo{
			Action:  rule.Action,
			RuleNum: rule.RuleNum,
		}
	}

	return ps, nil
}

// classifyDockerPort builds a PortStatus for a port owned by a docker
// container. It expands sibling ports via the Inspected map when available.
// UFW is attached for the primary port and for each sibling.
func (c *Checker) classifyDockerPort(
	primaryPort int,
	docker scanner.DockerEntry,
	inspected map[string]scanner.DockerEntry,
	ufwByPort map[int]scanner.UFWRule,
) PortStatus {
	ps := PortStatus{Port: primaryPort}

	isRunning := docker.State == "running"
	if isRunning {
		ps.State = PortUsedDocker
	} else {
		ps.State = PortReservedImplicit
		ps.Reservation = &ReservationInfo{
			Source:          "implicit",
			ContainerName:   docker.Name,
			ContainerStatus: docker.State,
		}
	}

	// Build occupant only for running containers (reserved_implicit has no
	// active occupant in the traditional sense, but the design shows occupant
	// as null for reserved_implicit).
	if isRunning {
		ps.Occupant = &Occupant{
			Type:            "docker",
			ContainerID:     docker.ID,
			ContainerName:   docker.Name,
			ContainerImage:  docker.Image,
			ContainerStatus: docker.State,
			ContainerUptime: docker.Status, // human text from docker ps
		}
	}

	// Expand sibling ports: use Inspected entry if available (more accurate),
	// fall back to the docker-ps DockerEntry.
	var allPorts []scanner.HostPort
	if inspectedEntry, ok := inspected[docker.ID]; ok {
		allPorts = inspectedEntry.Ports
	} else {
		allPorts = docker.Ports
	}

	// Collect unique host ports (excluding the primary one) for container_other_ports.
	seen := make(map[int]struct{})
	var otherPorts []ContainerOtherPort
	for _, hp := range allPorts {
		if hp.HostPort == primaryPort {
			continue
		}
		if _, alreadySeen := seen[hp.HostPort]; alreadySeen {
			continue
		}
		seen[hp.HostPort] = struct{}{}

		cop := ContainerOtherPort{Port: hp.HostPort}
		if rule, ok := ufwByPort[hp.HostPort]; ok {
			cop.UFW = &UFWInfo{
				Action:  rule.Action,
				RuleNum: rule.RuleNum,
			}
		}
		otherPorts = append(otherPorts, cop)
	}

	// Per design §4.5: container_other_ports is null (not []) for non-docker
	// states, but for docker states we use the slice (may be nil if no others).
	// The JSON tag has no omitempty, so nil → JSON null and []…→ JSON array.
	ps.ContainerOtherPorts = otherPorts

	return ps
}

// dedupState returns the PortState of a port that is being deduplicated
// (appeared earlier via container_other_ports). We compute the minimal state
// so the caller knows whether it is docker/implicit without full detail.
func dedupState(
	port int,
	ssByPort map[int]scanner.SSEntry,
	dockerByHostPort map[int]scanner.DockerEntry,
) PortState {
	if docker, ok := dockerByHostPort[port]; ok {
		if docker.State == "running" {
			return PortUsedDocker
		}
		return PortReservedImplicit
	}
	if _, ok := ssByPort[port]; ok {
		return PortUsedProcess
	}
	return PortFree
}

// ---------------------------------------------------------------------------
// Snapshot index helpers
// ---------------------------------------------------------------------------

// buildSSByPort indexes SS entries by port number. If the same port appears
// multiple times (SO_REUSEPORT), the first entry wins.
func buildSSByPort(entries []scanner.SSEntry) map[int]scanner.SSEntry {
	m := make(map[int]scanner.SSEntry, len(entries))
	for _, e := range entries {
		if _, exists := m[e.Port]; !exists {
			m[e.Port] = e
		}
	}
	return m
}

// buildDockerByHostPort indexes docker entries by each of their host ports.
// If two containers claim the same host port, the first one wins (conflict
// resolution is out of scope for Phase 1).
func buildDockerByHostPort(entries []scanner.DockerEntry) map[int]scanner.DockerEntry {
	m := make(map[int]scanner.DockerEntry)
	for _, e := range entries {
		for _, hp := range e.Ports {
			if _, exists := m[hp.HostPort]; !exists {
				m[hp.HostPort] = e
			}
		}
	}
	return m
}

// buildUFWByPort indexes UFW rules by port number, ignoring protocol (Phase 1).
// If multiple rules cover the same port (e.g. tcp + udp), the first wins.
func buildUFWByPort(rules []scanner.UFWRule) map[int]scanner.UFWRule {
	m := make(map[int]scanner.UFWRule, len(rules))
	for _, r := range rules {
		if r.Port == 0 {
			// Complex rules without a parseable port — skip.
			continue
		}
		if _, exists := m[r.Port]; !exists {
			m[r.Port] = r
		}
	}
	return m
}
