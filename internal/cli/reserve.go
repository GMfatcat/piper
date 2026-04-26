package cli

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"strconv"

	"github.com/GMfatcat/piper/internal/store"
	"github.com/spf13/cobra"
)

// ─── Consumer interfaces ──────────────────────────────────────────────────────

// ReservationStore is the minimal store interface needed by reserve/release.
type ReservationStore interface {
	InsertReservation(ctx context.Context, r store.Reservation) error
	GetReservation(ctx context.Context, port int) (store.Reservation, error)
	DeleteReservation(ctx context.Context, port int) error
}

// EventAppender is the minimal store interface needed to record history events.
type EventAppender interface {
	AppendEvent(ctx context.Context, e store.Event) (store.Event, error)
}

// ─── ReserveDeps ─────────────────────────────────────────────────────────────

// ReserveDeps bundles dependencies so RunReserve is testable with stubs.
type ReserveDeps struct {
	Reservations ReservationStore
	Events       EventAppender
	Out          *Writer
	// UserFn returns the username for created_by. In production it calls
	// os/user.Current().Username; in tests it is overridden to avoid OS calls.
	UserFn func() string
}

// defaultUserFn returns the current OS user's Username, falling back to
// "unknown" on error. This is the production default.
func defaultUserFn() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return "unknown"
	}
	return u.Username
}

// RunReserve is the testable inner function. cobra glue lives in NewReserveCmd.
//
//  1. Inserts the reservation (CreatedBy from UserFn).
//  2. Appends a history event EventReserved with Occupant=name.
//  3. If InsertReservation fails with ErrReservationExists, returns friendly error.
//  4. Prints confirmation via Writer.
func RunReserve(ctx context.Context, d ReserveDeps, port int, name, note string) error {
	createdBy := d.UserFn()

	r := store.Reservation{
		Port:      port,
		Name:      name,
		Note:      note,
		CreatedBy: createdBy,
	}

	if err := d.Reservations.InsertReservation(ctx, r); err != nil {
		if errors.Is(err, store.ErrReservationExists) {
			return fmt.Errorf("port %d already reserved", port)
		}
		return fmt.Errorf("reserve: %w", err)
	}

	// Append history event.
	if _, err := d.Events.AppendEvent(ctx, store.Event{
		Port:     port,
		Event:    store.EventReserved,
		Occupant: name,
	}); err != nil {
		return fmt.Errorf("reserve: append event: %w", err)
	}

	// Read back the stored reservation to get the filled-in CreatedAt.
	stored, err := d.Reservations.GetReservation(ctx, port)
	if err != nil {
		// Fall back to the original if read-back fails.
		stored = r
	}

	return d.Out.Write([]store.Reservation{stored})
}

// NewReserveCmd builds the cobra command for `piper reserve`.
func NewReserveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reserve <port>",
		Short: "Reserve a port with an explicit reservation",
		Long: `Register an explicit port reservation in the database.

This marks the port as reserved, preventing piper suggest from recommending it.
If the port is currently in use, a warning note is recorded but the reservation
is still created.

Examples:
  piper reserve 9100 --name vllm-llama
  piper reserve 9100 --name vllm-llama --note "next week deployment"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			port, err := strconv.Atoi(args[0])
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("reserve: invalid port %q", args[0])
			}

			name, _ := cmd.Flags().GetString("name")
			note, _ := cmd.Flags().GetString("note")

			// Resolve data dir and open store.
			dataDir, _ := cmd.Flags().GetString("data-dir")
			dir, err := ResolveDataDir(dataDir)
			if err != nil {
				return fmt.Errorf("reserve: %w", err)
			}
			st, err := OpenStore(dir)
			if err != nil {
				return fmt.Errorf("reserve: open store: %w", err)
			}
			defer st.Close()

			// Build Writer from flags.
			format, _ := cmd.Flags().GetString("format")
			output, _ := cmd.Flags().GetString("output")
			noColor, _ := cmd.Flags().GetBool("no-color")
			w := NewWriter(Format(format), output, noColor)

			deps := ReserveDeps{
				Reservations: st,
				Events:       st,
				Out:          w,
				UserFn:       defaultUserFn,
			}

			return RunReserve(ctx, deps, port, name, note)
		},
	}

	cmd.Flags().String("name", "", "service name for the reservation (required)")
	cmd.Flags().String("note", "", "optional note for this reservation")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}
