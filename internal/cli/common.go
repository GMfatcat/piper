// Package cli contains the cobra subcommand implementations for piper.
//
// Each subcommand follows the pattern:
//   1. NewXxxCmd() returns a *cobra.Command — pure cobra wiring, hard to unit test.
//   2. RunXxx(ctx, deps, args) does the work given injected dependencies — easy to unit test.
//   3. Tests target RunXxx with stub Snap/Reserves/Writer; the cobra glue is exercised by smoke runs.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/GMfatcat/piper/internal/scanner"
	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ResolveDataDir picks the data directory in this priority:
//  1. flagValue (from --data-dir)
//  2. PIPER_DATA_DIR env var
//  3. ~/.piper (from os.UserHomeDir)
func ResolveDataDir(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if env := os.Getenv("PIPER_DATA_DIR"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".piper"), nil
}

// OpenStore opens piper.db inside dataDir, creating dataDir + applying migrations as needed.
func OpenStore(dataDir string) (*store.Store, error) {
	return store.Open(filepath.Join(dataDir, "piper.db"))
}

// AdaptScanResult converts a raw scanner.ScanResult into the service.ScanSnapshot
// shape consumed by Checker / Suggester. Used by every read-side command after
// running a fresh scan.
func AdaptScanResult(r scanner.ScanResult) service.ScanSnapshot {
	return service.ScanSnapshot{
		ScannedAt:   r.FinishedAt,
		SS:          r.SS,
		Docker:      r.Docker,
		Inspected:   r.Inspected,
		UFW:         r.UFW,
		UFWActive:   r.UFWActive,
		UFWReadable: r.UFWReadable,
	}
}

// InMemorySnapProvider implements service.SnapshotProvider over a fixed snapshot.
// Phase 1 CLI commands run a fresh scan per invocation and feed it through this.
type InMemorySnapProvider struct {
	Snap service.ScanSnapshot
}

// LatestSnapshot returns the embedded snapshot.
func (p InMemorySnapProvider) LatestSnapshot(_ context.Context) (service.ScanSnapshot, error) {
	return p.Snap, nil
}

// ScanOnce runs a one-shot scan via the production scanner and returns it
// adapted to a service.ScanSnapshot. Returns the raw scanner.ScanResult too
// in case the caller wants per-source error info (UFW unavailable, etc.).
func ScanOnce(ctx context.Context) (service.ScanSnapshot, scanner.ScanResult, error) {
	s := scanner.New()
	res, err := s.Scan(ctx)
	if err != nil {
		return service.ScanSnapshot{}, res, err
	}
	return AdaptScanResult(res), res, nil
}

// ParsePortArgs parses CLI port arguments accepting:
//   - bare ports: "8080"
//   - ranges:     "8080-8085"
//   - mixed:      ["8080", "9000-9005", "9100"]
//
// Returns ports in the order they appear (no dedup — caller decides). Range
// bounds inclusive. Returns an error on:
//   - non-numeric input
//   - port < 1 or > 65535
//   - range with start > end
func ParsePortArgs(args []string) ([]int, error) {
	if len(args) == 0 {
		return nil, errors.New("at least one port or port range required")
	}
	var out []int
	for _, raw := range args {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "-") {
			parts := strings.SplitN(raw, "-", 2)
			start, err := parsePort(parts[0])
			if err != nil {
				return nil, fmt.Errorf("invalid range %q: %w", raw, err)
			}
			end, err := parsePort(parts[1])
			if err != nil {
				return nil, fmt.Errorf("invalid range %q: %w", raw, err)
			}
			if start > end {
				return nil, fmt.Errorf("invalid range %q: start > end", raw)
			}
			for p := start; p <= end; p++ {
				out = append(out, p)
			}
			continue
		}
		p, err := parsePort(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("no valid ports parsed")
	}
	return out, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("port %q is not a number", s)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("port %d out of range [1, 65535]", n)
	}
	return n, nil
}
