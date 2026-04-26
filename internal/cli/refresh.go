package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// ─── RefreshDeps ──────────────────────────────────────────────────────────────

// RefreshDeps bundles injectable dependencies for `piper refresh`.
type RefreshDeps struct {
	// HTTPClient is used to POST /api/scan/trigger. Defaults to http.DefaultClient.
	HTTPClient *http.Client

	// Host and Port identify the running piper serve instance.
	Host string
	Port int

	// Out is the writer for fallback output.
	Out *Writer

	// Fallback is called when the server is not reachable (connection refused).
	// If nil, a default fallback that runs ScanOnce is used.
	// Returning non-nil causes RunRefresh to return that error.
	Fallback func(ctx context.Context) error
}

// ─── RunRefresh ───────────────────────────────────────────────────────────────

// RunRefresh implements `piper refresh` (design §4.10):
//
//  1. POST http://HOST:PORT/api/scan/trigger.
//  2. 200 OK → print response, return nil.
//  3. Connection refused → call Fallback, return Fallback error.
//  4. Other HTTP error → return error with status code.
func RunRefresh(ctx context.Context, d RefreshDeps) error {
	client := d.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	url := fmt.Sprintf("http://%s:%d/api/scan/trigger", d.Host, d.Port)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("refresh: build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		// Detect connection refused — the server is not running.
		if isConnectionRefused(err) {
			return d.runFallback(ctx)
		}
		return fmt.Errorf("refresh: http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		fmt.Fprintln(d.Out.Stdout, "ok")
		return nil
	}

	return fmt.Errorf("refresh: server returned status %d %s", resp.StatusCode, resp.Status)
}

// runFallback executes the RefreshDeps.Fallback function, or the default
// ScanOnce-based fallback when Fallback is nil.
func (d *RefreshDeps) runFallback(ctx context.Context) error {
	if d.Fallback != nil {
		return d.Fallback(ctx)
	}

	// Default fallback: run a local scan and print a brief summary.
	snap, res, err := ScanOnce(ctx)
	if err != nil {
		return fmt.Errorf("refresh: fallback scan: %w", err)
	}

	// Count interesting ports.
	nPorts := len(snap.SS) + len(snap.Docker)

	out := d.Out
	if out == nil {
		out = NewWriter(FormatText, "", false)
	}

	// Brief message per spec §4.10: "scanned N ports, did not persist".
	_ = res // raw result available if needed for future enrichment
	fmt.Fprintf(out.Stdout, "scanned %d ports, did not persist (piper serve is not running)\n", nPorts)
	return nil
}

// isConnectionRefused returns true when err indicates the remote host actively
// refused the connection (i.e. piper serve is not running).
//
// Detection strategy:
//  1. Unwrap to check for *net.OpError → *net.AddrError or OS-level errno.
//  2. String match on "connection refused" (Linux) and "actively refused" (Windows).
//
// Note: syscall.ECONNREFUSED is not reliably comparable cross-platform when
// errors are wrapped, so we fall back to string matching as a portable heuristic.
func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}

	// Walk the error chain looking for *net.OpError.
	unwrapped := err
	for unwrapped != nil {
		if opErr, ok := unwrapped.(*net.OpError); ok {
			// Check the inner error string.
			inner := opErr.Err.Error()
			if strings.Contains(inner, "connection refused") ||
				strings.Contains(inner, "actively refused") {
				return true
			}
		}
		// Try to unwrap one level.
		type unwrapper interface{ Unwrap() error }
		if u, ok := unwrapped.(unwrapper); ok {
			unwrapped = u.Unwrap()
		} else {
			break
		}
	}

	// Fallback: check the top-level error string.
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "actively refused")
}

// ─── NewRefreshCmd ────────────────────────────────────────────────────────────

// NewRefreshCmd returns the cobra.Command for `piper refresh`.
//
// depsFactory is called inside RunE after flags are parsed.
func NewRefreshCmd(depsFactory func(host string, port int, dataDir string) RefreshDeps) *cobra.Command {
	var (
		flagHost string
		flagPort int
	)

	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Trigger an immediate scan (via piper serve, or locally as fallback)",
		Long: "POST /api/scan/trigger to a running piper serve instance. " +
			"If piper serve is not running, falls back to a local one-shot scan (does not persist).",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			dataDir, _ := cmd.Flags().GetString("data-dir")
			deps := depsFactory(flagHost, flagPort, dataDir)
			return RunRefresh(ctx, deps)
		},
	}

	cmd.Flags().StringVar(&flagHost, "host", "127.0.0.1", "Host of the piper serve instance")
	cmd.Flags().IntVar(&flagPort, "port", 7878, "Port of the piper serve instance")

	return cmd
}
