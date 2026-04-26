package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/GMfatcat/piper/internal/store"
	"github.com/spf13/cobra"
)

// ─── Consumer-side interface ──────────────────────────────────────────────────

// EventQuerier is the consumer-side interface for querying history events.
// *store.Store satisfies this interface.
type EventQuerier interface {
	QueryEvents(ctx context.Context, q store.HistoryQuery) ([]store.Event, error)
}

// ─── Deps ─────────────────────────────────────────────────────────────────────

// HistoryDeps holds the injected dependencies for the history subcommand.
type HistoryDeps struct {
	Events EventQuerier
	Out    *Writer
}

// ─── RunHistory ───────────────────────────────────────────────────────────────

// RunHistory queries history events using the provided HistoryQuery and writes
// the results via the Writer in deps.
func RunHistory(ctx context.Context, d HistoryDeps, q store.HistoryQuery) error {
	events, err := d.Events.QueryEvents(ctx, q)
	if err != nil {
		return fmt.Errorf("history: query: %w", err)
	}
	return d.Out.Write(events)
}

// ─── NewHistoryCmd ────────────────────────────────────────────────────────────

// NewHistoryCmd returns the cobra.Command for `piper history`.
//
// The depsFactory parameter allows callers (and tests) to inject custom deps.
// In production (main.go) it is called with a real store; in tests a stub is used.
func NewHistoryCmd(depsFactory func() HistoryDeps) *cobra.Command {
	var (
		flagPort  int
		flagDays  int
		flagEvent string
		flagLimit int
	)

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Query port history events",
		Long:  "Query the history of port occupancy events (occupied, released, reserved, unreserved).",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			deps := depsFactory()

			// Build HistoryQuery from flags.
			q := store.HistoryQuery{}

			if cmd.Flags().Changed("port") && flagPort != 0 {
				p := flagPort
				q.Port = &p
			}

			// Convert --days to a Since timestamp.
			if flagDays > 0 {
				q.Since = time.Now().UTC().Add(-time.Duration(flagDays) * 24 * time.Hour)
			}

			if flagEvent != "" {
				q.Event = store.EventType(flagEvent)
			}

			q.Limit = flagLimit

			return RunHistory(ctx, deps, q)
		},
	}

	cmd.Flags().IntVar(&flagPort, "port", 0, "Only show events for this port (0 = all)")
	cmd.Flags().IntVar(&flagDays, "days", 7, "Show events from the last N days")
	cmd.Flags().StringVar(&flagEvent, "event", "", "Filter by event type: occupied|released|reserved|unreserved")
	cmd.Flags().IntVar(&flagLimit, "limit", 50, "Maximum number of events to show")

	return cmd
}
