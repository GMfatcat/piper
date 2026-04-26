package cli

import (
	"context"
	"fmt"

	"github.com/GMfatcat/piper/internal/service"
	"github.com/spf13/cobra"
)

// RunScan is `piper scan` — a convenience alias for
// `piper list --used --reserved` (used + reserved_explicit + reserved_implicit).
// It delegates to RunList with the appropriate filter and returns the result.
func RunScan(ctx context.Context, d ListDeps) (service.CheckResult, error) {
	return RunList(ctx, d, ListFilter{
		Used:             true,
		ReservedExplicit: true,
		ReservedImplicit: true,
	})
}

// NewScanCmd returns the cobra.Command for `piper scan`.
//
// depsFactory is called inside RunE so callers (and tests) can inject deps.
// scan has no flags of its own beyond the global persistent flags.
func NewScanCmd(depsFactory func() ListDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan and display all stateful ports",
		Long:  "Run a fresh scan and display all used + reserved ports (alias for: piper list --used --reserved).",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			deps := depsFactory()
			_, err := RunScan(ctx, deps)
			if err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			return nil
		},
	}

	return cmd
}
