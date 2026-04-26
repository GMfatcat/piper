package cli

import (
	"context"
	"fmt"

	"github.com/GMfatcat/piper/internal/service"
	"github.com/spf13/cobra"
)

// SuggestDeps bundles dependencies so RunSuggest is testable with stubs.
type SuggestDeps struct {
	Snap     service.SnapshotProvider  // provides the latest scan snapshot
	Reserves service.ReservationLister // lists explicit reservations
	Out      *Writer
}

// RunSuggest is the testable inner function. cobra glue lives in NewSuggestCmd.
//
// If SuggestFreePorts returns ErrNoFreePortsFound, the partial result is still
// written via Writer, and the error is returned so cobra can print it.
func RunSuggest(ctx context.Context, d SuggestDeps, n, from, to int) error {
	// Fetch the snapshot once to get ScannedAt for the payload.
	snap, snapErr := d.Snap.LatestSnapshot(ctx)
	if snapErr != nil {
		return snapErr
	}

	suggester := &service.Suggester{Snap: d.Snap, Reserves: d.Reserves}
	ports, suggestErr := suggester.SuggestFreePorts(ctx, n, from, to)

	// Build the payload regardless of error (may be partial).
	payload := SuggestionPayload{
		Suggested: ports,
	}
	payload.SearchRange.From = from
	payload.SearchRange.To = to
	payload.ScannedAt = snap.ScannedAt

	// Always write (even if partial), then return the suggest error.
	writeErr := d.Out.Write(payload)
	if writeErr != nil {
		return writeErr
	}
	return suggestErr
}

// NewSuggestCmd builds the cobra command for `piper suggest`.
func NewSuggestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "suggest",
		Short: "Suggest free ports in a range",
		Long: `Suggest one or more free ports from the specified range.

The algorithm scans sequentially from --from to --to, returning the first N ports
that have no active listener, no docker mapping, and no explicit reservation.

Examples:
  piper suggest
  piper suggest -n 3
  piper suggest -n 2 --from 9000 --to 9999`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			n, _ := cmd.Flags().GetInt("n")
			from, _ := cmd.Flags().GetInt("from")
			to, _ := cmd.Flags().GetInt("to")

			// Resolve data dir and open store.
			dataDir, _ := cmd.Flags().GetString("data-dir")
			dir, err := ResolveDataDir(dataDir)
			if err != nil {
				return fmt.Errorf("suggest: %w", err)
			}
			st, err := OpenStore(dir)
			if err != nil {
				return fmt.Errorf("suggest: open store: %w", err)
			}
			defer st.Close()

			// Run a fresh scan.
			snap, _, err := ScanOnce(ctx)
			if err != nil {
				return fmt.Errorf("suggest: scan: %w", err)
			}
			snapProvider := InMemorySnapProvider{Snap: snap}

			// Build Writer from flags.
			format, _ := cmd.Flags().GetString("format")
			output, _ := cmd.Flags().GetString("output")
			noColor, _ := cmd.Flags().GetBool("no-color")
			w := NewWriter(Format(format), output, noColor)

			deps := SuggestDeps{
				Snap:     snapProvider,
				Reserves: st,
				Out:      w,
			}

			return RunSuggest(ctx, deps, n, from, to)
		},
	}

	// Local flags.
	cmd.Flags().IntP("n", "n", 1, "number of ports to suggest")
	cmd.Flags().Int("from", 8000, "start of port search range")
	cmd.Flags().Int("to", 9999, "end of port search range")
	// Phase 2 flag — accepted but no-op.
	cmd.Flags().Bool("avoid-recent", false, "(Phase 2; not yet implemented) avoid ports released in the last 7 days")

	return cmd
}
