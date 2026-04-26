package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
	"github.com/spf13/cobra"
)

// ─── Deps ─────────────────────────────────────────────────────────────────────

// ListDeps holds the injected dependencies for the list (and scan) subcommand.
type ListDeps struct {
	// Snap provides the current port scan snapshot.
	Snap service.SnapshotProvider

	// Reserves is used by the Checker when classifying individual port states
	// (single-port explicit-reservation lookup).
	Reserves service.ReservationGetter

	// AllReserves lists ALL explicit reservations so we can include explicitly-
	// reserved ports in the stateful candidate set.
	AllReserves service.ReservationLister

	// Out is the output writer.
	Out *Writer
}

// ─── ListFilter ───────────────────────────────────────────────────────────────

// ListFilter describes which port states to include in a RunList result.
//
// Zero value (all fields false/zero/"") means "default = all stateful ports
// (everything except free)".
type ListFilter struct {
	Used             bool
	Free             bool
	ReservedExplicit bool
	ReservedImplicit bool
	Container        string
	From             int
	To               int
}

// hasStateFilter returns true when at least one state-based flag is set
// (Used, Free, ReservedExplicit, ReservedImplicit). Container-only is NOT
// a state filter — when only Container is set, all stateful ports matching
// that container are returned.
func (f ListFilter) hasStateFilter() bool {
	return f.Used || f.Free || f.ReservedExplicit || f.ReservedImplicit
}

// ─── RunList ──────────────────────────────────────────────────────────────────

// RunList runs a port listing filtered by f and writes results via deps.Out.
// It returns the service.CheckResult for callers (like RunScan) that want to
// inspect or re-use the data.
func RunList(ctx context.Context, d ListDeps, f ListFilter) (service.CheckResult, error) {
	// Validate --free requires a range.
	if f.Free && f.From == 0 && f.To == 0 {
		return service.CheckResult{}, fmt.Errorf("use --from and --to with --free to bound the range")
	}

	// Step 1: Obtain snapshot.
	snap, err := d.Snap.LatestSnapshot(ctx)
	if err != nil {
		return service.CheckResult{}, fmt.Errorf("list: snapshot: %w", err)
	}

	// Step 2: Obtain all explicit reservations.
	reservations, err := d.AllReserves.ListReservations(ctx)
	if err != nil {
		return service.CheckResult{}, fmt.Errorf("list: reservations: %w", err)
	}

	// Step 3: Build the stateful port set (union of all non-free ports).
	statefulPorts := buildStatefulPortSet(snap, reservations)

	// Step 4: Build the candidate port list based on the filter.
	candidatePorts := buildCandidatePorts(f, statefulPorts)

	// Step 5: For each candidate, classify it using the Checker.
	// Build an in-memory ReservationGetter backed by AllReserves so that
	// explicitly-reserved ports are correctly identified even when the
	// caller's Reserves only handles single-port lookups.
	reserveGetter := newMapReservationGetter(reservations)
	checker := &service.Checker{
		Snap:     d.Snap,
		Reserves: reserveGetter,
	}
	checkResult, err := checker.CheckPorts(ctx, candidatePorts)
	if err != nil {
		return service.CheckResult{}, fmt.Errorf("list: check ports: %w", err)
	}

	// Step 6: Apply state filter to the classified results.
	filtered := applyStateFilter(checkResult.Results, f)

	result := service.CheckResult{
		ScannedAt: checkResult.ScannedAt,
		Results:   filtered,
	}

	// Step 7: Write output.
	if err := d.Out.Write(result); err != nil {
		return result, fmt.Errorf("list: write: %w", err)
	}

	return result, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// buildStatefulPortSet returns the union of all ports that are NOT free:
//   - SS listeners
//   - Docker running containers (used_docker)
//   - Docker stopped containers (reserved_implicit)
//   - Explicit reservations
func buildStatefulPortSet(snap service.ScanSnapshot, reservations []store.Reservation) map[int]struct{} {
	stateful := make(map[int]struct{})

	for _, e := range snap.SS {
		stateful[e.Port] = struct{}{}
	}
	for _, d := range snap.Docker {
		for _, hp := range d.Ports {
			if hp.HostPort != 0 {
				stateful[hp.HostPort] = struct{}{}
			}
		}
	}
	for _, r := range reservations {
		stateful[r.Port] = struct{}{}
	}

	return stateful
}

// buildCandidatePorts returns a sorted list of port numbers to classify.
// For non-free filters: returns all stateful ports.
// For free filter: returns ports in [From, To] not in statefulPorts.
func buildCandidatePorts(f ListFilter, statefulPorts map[int]struct{}) []int {
	var ports []int

	if f.Free {
		// Iterate from..to, pick ports NOT in stateful set.
		for p := f.From; p <= f.To; p++ {
			if _, isStateful := statefulPorts[p]; !isStateful {
				ports = append(ports, p)
			}
		}
		return ports
	}

	// Default / explicit non-free filter: use all stateful ports.
	for p := range statefulPorts {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports
}

// applyStateFilter keeps only PortStatus entries matching the filter criteria.
// Multiple flags are OR'd together. Zero filter (isDefault) keeps all non-free.
// Container filter is ANDed on top of state filter.
func applyStateFilter(statuses []service.PortStatus, f ListFilter) []service.PortStatus {
	var result []service.PortStatus

	for _, ps := range statuses {
		if !matchesFilter(ps, f) {
			continue
		}
		result = append(result, ps)
	}
	return result
}

// matchesFilter returns true if ps should be included given filter f.
func matchesFilter(ps service.PortStatus, f ListFilter) bool {
	// Determine state match first.
	stateMatch := false

	if f.Free {
		// Free filter: only keep free ports.
		stateMatch = ps.State == service.PortFree
	} else if !f.hasStateFilter() {
		// No state filter set (default / container-only): keep everything except free.
		stateMatch = ps.State != service.PortFree
	} else {
		// Explicit state flags (OR logic).
		if f.Used && (ps.State == service.PortUsedProcess || ps.State == service.PortUsedDocker) {
			stateMatch = true
		}
		if f.ReservedExplicit && ps.State == service.PortReservedExplicit {
			stateMatch = true
		}
		if f.ReservedImplicit && ps.State == service.PortReservedImplicit {
			stateMatch = true
		}
	}

	if !stateMatch {
		return false
	}

	// Container filter: if set, only keep ports whose occupant matches.
	if f.Container != "" {
		occupantName := occupantContainerName(ps)
		return occupantName == f.Container
	}

	return true
}

// occupantContainerName extracts the container name from a PortStatus for the
// --container filter. It checks Occupant (used_docker) and Reservation
// (reserved_implicit / reserved_explicit with container info).
func occupantContainerName(ps service.PortStatus) string {
	if ps.Occupant != nil && ps.Occupant.Type == "docker" {
		return ps.Occupant.ContainerName
	}
	if ps.Reservation != nil {
		return ps.Reservation.ContainerName
	}
	return ""
}

// ─── mapReservationGetter ─────────────────────────────────────────────────────

// mapReservationGetter is an in-memory service.ReservationGetter backed by a
// map built from the AllReserves listing. Used internally by RunList so the
// Checker correctly identifies explicit reservations without a live DB round-
// trip per port.
type mapReservationGetter struct {
	m map[int]store.Reservation
}

func newMapReservationGetter(reservations []store.Reservation) *mapReservationGetter {
	m := make(map[int]store.Reservation, len(reservations))
	for _, r := range reservations {
		m[r.Port] = r
	}
	return &mapReservationGetter{m: m}
}

func (g *mapReservationGetter) GetReservation(_ context.Context, port int) (store.Reservation, error) {
	if r, ok := g.m[port]; ok {
		return r, nil
	}
	return store.Reservation{}, fmt.Errorf("%w: port %d", store.ErrReservationNotFound, port)
}

// ensure mapReservationGetter satisfies the interface at compile time.
var _ service.ReservationGetter = (*mapReservationGetter)(nil)


// ─── NewListCmd ───────────────────────────────────────────────────────────────

// NewListCmd returns the cobra.Command for `piper list`.
//
// depsFactory is called inside RunE so callers (and tests) can inject deps.
func NewListCmd(depsFactory func() ListDeps) *cobra.Command {
	var (
		flagUsed             bool
		flagFree             bool
		flagReserved         bool
		flagReservedExplicit bool
		flagReservedImplicit bool
		flagAll              bool
		flagContainer        string
		flagFrom             int
		flagTo               int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List ports by state",
		Long:  "List ports filtered by state (used, free, reserved, etc.).",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			deps := depsFactory()

			f := ListFilter{
				Used:      flagUsed,
				Free:      flagFree,
				Container: flagContainer,
				From:      flagFrom,
				To:        flagTo,
			}

			// --reserved expands to both explicit and implicit.
			if flagReserved {
				f.ReservedExplicit = true
				f.ReservedImplicit = true
			} else {
				f.ReservedExplicit = flagReservedExplicit
				f.ReservedImplicit = flagReservedImplicit
			}

			// --all is the default; when set explicitly, clear the specific flags.
			if flagAll {
				f = ListFilter{Container: flagContainer, From: flagFrom, To: flagTo}
			}

			_, err := RunList(ctx, deps, f)
			return err
		},
	}

	cmd.Flags().BoolVar(&flagUsed, "used", false, "Only used ports (process + docker)")
	cmd.Flags().BoolVar(&flagFree, "free", false, "Only free ports (requires --from/--to)")
	cmd.Flags().BoolVar(&flagReserved, "reserved", false, "Only reserved ports (explicit + implicit)")
	cmd.Flags().BoolVar(&flagReservedExplicit, "reserved-explicit", false, "Only explicitly reserved ports")
	cmd.Flags().BoolVar(&flagReservedImplicit, "reserved-implicit", false, "Only implicitly reserved ports (stopped containers)")
	cmd.Flags().BoolVar(&flagAll, "all", false, "All stateful ports (default behavior)")
	cmd.Flags().StringVar(&flagContainer, "container", "", "Only ports belonging to the named container")
	cmd.Flags().IntVar(&flagFrom, "from", 0, "Start of port range (used with --free)")
	cmd.Flags().IntVar(&flagTo, "to", 0, "End of port range (used with --free)")

	return cmd
}
