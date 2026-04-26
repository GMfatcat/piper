package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/GMfatcat/piper/internal/store"
	"github.com/spf13/cobra"
)

// ReleaseDeps bundles dependencies so RunRelease is testable with stubs.
// Note: ReservationStore and EventAppender are defined in reserve.go.
type ReleaseDeps struct {
	Reservations ReservationStore
	Events       EventAppender
	Out          *Writer
}

// RunRelease is the testable inner function. cobra glue lives in NewReleaseCmd.
//
//  1. Looks up the reservation (to retrieve the occupant name for history).
//     If ErrReservationNotFound, returns a friendly error.
//  2. Deletes the reservation.
//  3. Appends a history event EventUnreserved with Occupant=reservation.Name.
//  4. Prints confirmation via Writer.
func RunRelease(ctx context.Context, d ReleaseDeps, port int) error {
	// Step 1: Look up reservation to get the name.
	reservation, err := d.Reservations.GetReservation(ctx, port)
	if err != nil {
		if errors.Is(err, store.ErrReservationNotFound) {
			return fmt.Errorf("port %d has no reservation to release", port)
		}
		return fmt.Errorf("release: get reservation: %w", err)
	}

	// Step 2: Delete the reservation.
	if err := d.Reservations.DeleteReservation(ctx, port); err != nil {
		return fmt.Errorf("release: delete reservation: %w", err)
	}

	// Step 3: Append history event.
	if _, err := d.Events.AppendEvent(ctx, store.Event{
		Port:     port,
		Event:    store.EventUnreserved,
		Occupant: reservation.Name,
	}); err != nil {
		return fmt.Errorf("release: append event: %w", err)
	}

	// Step 4: Print confirmation. Write directly to the Writer's output
	// rather than going through Writer.Write (which only handles registered types).
	if d.Out.Format == FormatJSON {
		type releaseData struct {
			Port     int  `json:"port"`
			Released bool `json:"released"`
		}
		return writeJSON(d.Out.Stdout, releaseData{Port: port, Released: true})
	}
	_, err = fmt.Fprintf(d.Out.Stdout, "Released reservation for port %d.\n", port)
	return err
}

// NewReleaseCmd builds the cobra command for `piper release`.
func NewReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release <port>",
		Short: "Release an explicit port reservation",
		Long: `Remove an explicit port reservation from the database.

This records an 'unreserved' event in history and allows piper suggest to
recommend the port again.

Note: releasing a port does not affect the currently running service on that
port — it only removes the explicit reservation.

Examples:
  piper release 9100`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			port, err := strconv.Atoi(args[0])
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("release: invalid port %q", args[0])
			}

			// Resolve data dir and open store.
			dataDir, _ := cmd.Flags().GetString("data-dir")
			dir, err := ResolveDataDir(dataDir)
			if err != nil {
				return fmt.Errorf("release: %w", err)
			}
			st, err := OpenStore(dir)
			if err != nil {
				return fmt.Errorf("release: open store: %w", err)
			}
			defer st.Close()

			// Build Writer from flags.
			format, _ := cmd.Flags().GetString("format")
			output, _ := cmd.Flags().GetString("output")
			noColor, _ := cmd.Flags().GetBool("no-color")
			w := NewWriter(Format(format), output, noColor)

			deps := ReleaseDeps{
				Reservations: st,
				Events:       st,
				Out:          w,
			}

			return RunRelease(ctx, deps, port)
		},
	}

	return cmd
}
