package cli

import (
	"context"
	"fmt"

	"github.com/GMfatcat/piper/internal/service"
	"github.com/spf13/cobra"
)

// CheckDeps bundles dependencies so RunCheck is testable with stubs.
type CheckDeps struct {
	Snap     service.SnapshotProvider  // provides the latest scan snapshot
	Reserves service.ReservationGetter // looks up explicit reservations
	Out      *Writer
}

// RunCheck is the testable inner function. cobra glue lives in NewCheckCmd.
func RunCheck(ctx context.Context, d CheckDeps, ports []int) error {
	checker := &service.Checker{Snap: d.Snap, Reserves: d.Reserves}
	res, err := checker.CheckPorts(ctx, ports)
	if err != nil {
		return err
	}
	return d.Out.Write(res)
}

// NewCheckCmd builds the cobra command. Reads --data-dir, --format, --output,
// --no-color from inherited persistent flags.
func NewCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check <port> [more ports | range]",
		Short: "Check one or more ports for status",
		Long: `Check the status of one or more ports.

Arguments accept bare ports (8080), ranges (8080-8085), or a mix.

Examples:
  piper check 8080
  piper check 8080 8081 9000
  piper check 8080-8085
  piper check 8080 9000-9005 9100`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			// Parse port arguments.
			ports, err := ParsePortArgs(args)
			if err != nil {
				return fmt.Errorf("check: %w", err)
			}

			// Resolve data dir and open store.
			dataDir, _ := cmd.Flags().GetString("data-dir")
			dir, err := ResolveDataDir(dataDir)
			if err != nil {
				return fmt.Errorf("check: %w", err)
			}
			st, err := OpenStore(dir)
			if err != nil {
				return fmt.Errorf("check: open store: %w", err)
			}
			defer st.Close()

			// Run a fresh scan.
			snap, _, err := ScanOnce(ctx)
			if err != nil {
				return fmt.Errorf("check: scan: %w", err)
			}
			snapProvider := InMemorySnapProvider{Snap: snap}

			// Build Writer from flags.
			format, _ := cmd.Flags().GetString("format")
			output, _ := cmd.Flags().GetString("output")
			noColor, _ := cmd.Flags().GetBool("no-color")
			w := NewWriter(Format(format), output, noColor)

			deps := CheckDeps{
				Snap:     snapProvider,
				Reserves: st,
				Out:      w,
			}

			return RunCheck(ctx, deps, ports)
		},
	}

	return cmd
}
