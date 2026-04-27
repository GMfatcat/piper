package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/GMfatcat/piper/internal/scanner"
	"github.com/GMfatcat/piper/internal/server"
	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ─── ServeServer interface ────────────────────────────────────────────────────

// ServeServer is the slice of *server.Server that RunServe needs.
// Extracted to an interface so tests can inject a stub without binding a real port.
type ServeServer interface {
	ListenAndServe(ctx context.Context) error
	Handler() http.Handler
}

// ─── ServeDeps ────────────────────────────────────────────────────────────────

// ServeDeps bundles wiring so RunServe is unit-testable.
type ServeDeps struct {
	// DataDir is the resolved data directory path. Used for the startup banner.
	DataDir string

	// Store provides DB access for snapshots, history, and reservations.
	Store *store.Store

	// Scanner is the scanner instance. If nil, scanner.New() is used.
	Scanner *scanner.Scanner

	// Host and Port configure the HTTP listener.
	Host string
	Port int

	// Interval is the scan period (default 5m per design §4.3).
	Interval time.Duration

	// Logger receives scan/cleanup errors. Defaults to slog.Default() when nil.
	Logger *slog.Logger

	// Clock defaults to service.RealClock{} when nil.
	Clock service.Clock

	// NewServer is injectable for tests — defaults to server.New.
	NewServer func(host string, port int, deps server.Deps) ServeServer
}

// ─── cachedSnap ───────────────────────────────────────────────────────────────

// cachedSnap provides an in-memory SnapshotProvider used by the HTTP server.
//
// Phase 1 design choice: rather than reconstructing a service.ScanSnapshot from
// the DB on every /api/* request, we keep the last successful ScanResult in
// memory and update it atomically each scan pass. The first /api/scan/latest
// call before any scan completes returns an empty snapshot — this is documented
// behaviour per the spec note in §4.3 startup sequence.
type cachedSnap struct {
	mu   sync.RWMutex
	snap service.ScanSnapshot
}

func (c *cachedSnap) LatestSnapshot(_ context.Context) (service.ScanSnapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap, nil
}

func (c *cachedSnap) replace(s service.ScanSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap = s
}

// ─── reduceToSnapshotRows ─────────────────────────────────────────────────────

// reduceToSnapshotRows converts a raw ScanResult into the []SnapshotRow form
// persisted to scan_snapshots. It applies the fusion rules from design §7.2:
//
//  1. Docker entries are processed first (Docker takes precedence over ss).
//     If Inspected has richer port data for the container, that is used.
//     Running containers → StateUsedDocker; non-running → StateReservedImplicit.
//
//  2. SS entries are added only when the port is not already covered by Docker.
//
//  3. UFW rules are attached to rows where a matching port exists.
//
// Output is sorted ascending by port for deterministic storage and diffs.
func reduceToSnapshotRows(r scanner.ScanResult) []store.SnapshotRow {
	rows := map[int]store.SnapshotRow{}
	ufwByPort := indexUFWByPort(r.UFW)

	// --- Docker entries (highest priority) ---
	for _, c := range r.Docker {
		// Prefer Inspected[c.ID] for the full port set; fall back to docker ps entry.
		rich := c
		if insp, ok := r.Inspected[c.ID]; ok && len(insp.Ports) > 0 {
			rich = insp
			// Keep the docker-ps state/status fields, which docker inspect leaves blank.
			rich.State = c.State
			rich.Status = c.Status
		}
		for _, hp := range rich.Ports {
			if hp.HostPort == 0 {
				continue
			}
			row := store.SnapshotRow{
				Port:            hp.HostPort,
				ContainerID:     c.ID,
				ContainerName:   c.Name,
				ContainerImage:  c.Image,
				ContainerStatus: c.Status,
			}
			if c.State == "running" {
				row.State = store.StateUsedDocker
			} else {
				row.State = store.StateReservedImplicit
			}
			applyUFW(&row, ufwByPort, hp.HostPort)
			rows[hp.HostPort] = row
		}
	}

	// --- SS entries (lower priority — only add ports not already from Docker) ---
	for _, ss := range r.SS {
		if _, exists := rows[ss.Port]; exists {
			// Docker already claimed this port.
			continue
		}
		row := store.SnapshotRow{
			Port:        ss.Port,
			State:       store.StateUsedProcess,
			PID:         ss.PID,
			ProcessName: ss.ProcessName,
		}
		applyUFW(&row, ufwByPort, ss.Port)
		rows[ss.Port] = row
	}

	// --- Sort by port for deterministic output ---
	out := make([]store.SnapshotRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// indexUFWByPort builds a map[port]UFWRule from a UFW rule slice.
// When both v4 and v6 entries exist for the same port, the first encountered
// wins (no double-counting). Port=0 (complex rules) are excluded.
func indexUFWByPort(rules []scanner.UFWRule) map[int]scanner.UFWRule {
	m := make(map[int]scanner.UFWRule, len(rules))
	for _, r := range rules {
		if r.Port == 0 {
			continue
		}
		if _, exists := m[r.Port]; !exists {
			m[r.Port] = r
		}
	}
	return m
}

// applyUFW fills row.UFWAction and row.UFWRuleNum from the indexed UFW map
// when a matching port exists.
func applyUFW(row *store.SnapshotRow, ufwByPort map[int]scanner.UFWRule, port int) {
	if rule, ok := ufwByPort[port]; ok {
		row.UFWAction = rule.Action
		row.UFWRuleNum = rule.RuleNum
	}
}

// ─── RunServe ─────────────────────────────────────────────────────────────────

// RunServe is the long-running body of `piper serve`. It:
//  1. Builds a scanFunc closure that scans → diffs → persists → caches.
//  2. Builds a cleanupFunc closure for 30-day history retention.
//  3. Wires a Scheduler to drive both functions.
//  4. Starts the HTTP server in a goroutine.
//  5. Runs the scheduler synchronously until ctx is canceled.
func RunServe(ctx context.Context, d ServeDeps) error {
	// Default logger.
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Default clock.
	clock := d.Clock
	if clock == nil {
		clock = service.RealClock{}
	}

	// Default scanner.
	sc := d.Scanner
	if sc == nil {
		sc = scanner.New()
	}

	// Shared in-memory snapshot cache read by the HTTP server.
	cache := &cachedSnap{}

	// --- Build scanFunc ---
	scanFunc := func(ctx context.Context) error {
		res, err := sc.Scan(ctx)
		if err != nil {
			return fmt.Errorf("scan: %w", err)
		}

		// Read previous snapshot for diffing.
		prev, _, err := d.Store.GetSnapshot(ctx)
		if err != nil {
			return fmt.Errorf("scan: get previous snapshot: %w", err)
		}

		// Reduce raw ScanResult to snapshot rows (fusion / reducer).
		curr := reduceToSnapshotRows(res)

		// Compute history events from the diff.
		events := service.DiffSnapshots(prev, curr)

		scannedAt := res.FinishedAt

		// Persist the new snapshot.
		if err := d.Store.SaveSnapshot(ctx, curr, scannedAt); err != nil {
			return fmt.Errorf("scan: save snapshot: %w", err)
		}

		// Update the in-memory cache for the HTTP server.
		cache.replace(AdaptScanResult(res))

		// Append history events (errors are logged but non-fatal).
		for _, ev := range events {
			ev.Timestamp = scannedAt
			if _, err := d.Store.AppendEvent(ctx, ev); err != nil {
				logger.Error("append event failed", "port", ev.Port, "event", ev.Event, "err", err)
			}
		}

		return nil
	}

	// --- Build cleanupFunc ---
	cleanupFunc := func(ctx context.Context) error {
		cutoff := clock.Now().UTC().AddDate(0, 0, -30)
		_, err := d.Store.CleanupHistory(ctx, cutoff)
		return err
	}

	// --- Build Scheduler ---
	sched := &service.Scheduler{
		Clock:    clock,
		Scan:     scanFunc,
		Cleanup:  cleanupFunc,
		Interval: d.Interval,
		OnScanError: func(err error) {
			logger.Error("scan failed", "err", err)
		},
		OnCleanupError: func(err error) {
			logger.Error("cleanup failed", "err", err)
		},
	}

	// --- Build server.Deps ---
	deps := server.Deps{
		Snap:      cache,
		Reserves:  d.Store,
		History:   d.Store,
		OnRefresh: scanFunc,
		Now:       clock.Now,
	}

	// --- Resolve newServer factory ---
	newServer := d.NewServer
	if newServer == nil {
		newServer = func(host string, port int, deps server.Deps) ServeServer {
			return server.New(host, port, deps)
		}
	}

	srv := newServer(d.Host, d.Port, deps)

	// --- Startup banner ---
	dataPath := d.DataDir
	if dataPath == "" {
		dataPath = "(unknown)"
	}
	fmt.Printf("piper serving on http://%s:%d (data: %s)\n", d.Host, d.Port, dataPath)

	// --- One-shot UFW sudo probe ---
	// Reports a clear, actionable warning to stderr if `sudo -n ufw status`
	// cannot run without a password. The serve loop continues regardless —
	// the periodic scan will record the same diagnosis to slog, and the web
	// UI will surface a "UFW data unavailable" banner.
	probeUFWSudo(ctx, os.Stderr)

	// --- Start HTTP server in background ---
	go func() {
		if err := srv.ListenAndServe(ctx); err != nil {
			logger.Error("server error", "err", err)
		}
	}()

	// --- Run scheduler (blocks until ctx canceled) ---
	err := sched.Run(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Graceful shutdown via SIGTERM/SIGINT — not a command error.
		return nil
	}
	return err
}

// ─── probeUFWSudo ─────────────────────────────────────────────────────────────

// ufwProbeRunner is the interface RunServe uses to probe sudo -n ufw status.
// Tests can substitute a stub. probeUFWSudo uses exec.CommandContext directly
// in production; the var indirection lets test code wrap or replace it.
var probeUFWSudoExec = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "sudo", "-n", "ufw", "status").CombinedOutput()
}

// probeUFWSudo runs `sudo -n ufw status` once at startup. If it fails because
// the configured user is not in sudoers (or the binary is missing), it writes
// a multi-line warning to w with a copy-paste fix that uses the actual current
// user. It NEVER blocks startup: success and failure both return promptly.
func probeUFWSudo(ctx context.Context, w *os.File) {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := probeUFWSudoExec(probeCtx)
	if err == nil {
		// `Status: inactive` is success at the sudo layer — the firewall is
		// just turned off. The web UI distinguishes those cases on its own.
		return
	}

	combined := strings.ToLower(string(out)) + " " + strings.ToLower(err.Error())
	uname := currentUsername()
	ufwBin := "/usr/sbin/ufw" // most common path on Debian/Ubuntu

	switch {
	case strings.Contains(combined, "password is required"),
		strings.Contains(combined, "a password is required"),
		strings.Contains(combined, "is not allowed to execute"),
		strings.Contains(combined, "not allowed to run"):
		fmt.Fprint(w, formatUFWSudoersHint(uname, ufwBin))
	case strings.Contains(combined, "command not found"),
		strings.Contains(combined, "executable file not found"),
		strings.Contains(combined, "no such file or directory"):
		fmt.Fprintf(w, "\n⚠️  WARNING: ufw not found on PATH — UFW scanning will be disabled.\n"+
			"   Install ufw if you need firewall data:  sudo apt install ufw\n\n")
	default:
		fmt.Fprintf(w, "\n⚠️  WARNING: probe of `sudo -n ufw status` failed (%v). UFW scanning may not work until this is resolved.\n\n", err)
	}
}

// currentUsername returns the current OS user, or "<your-user>" if it can't
// be resolved (rare; unprivileged sandbox).
func currentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "<your-user>"
}

// formatUFWSudoersHint returns the exact warning printed when sudo refuses to
// run ufw without a password. The username is interpolated so the snippet is
// directly copy-pasteable.
func formatUFWSudoersHint(username, ufwBin string) string {
	return fmt.Sprintf(`
⚠️  WARNING: piper cannot read UFW rules without sudo.
   UFW scanning will be disabled until you configure sudoers.

   Quick fix:
     sudo tee /etc/sudoers.d/piper-ufw <<EOF
     %s ALL=(root) NOPASSWD: %s status, %s status numbered
     EOF
     sudo chmod 0440 /etc/sudoers.d/piper-ufw

   Then restart piper.

`, username, ufwBin, ufwBin)
}

// ─── NewServeCmd ──────────────────────────────────────────────────────────────

// NewServeCmd returns the cobra.Command for `piper serve`.
//
// depsFactory is called inside RunE after flags are parsed. In production
// (main.go) the factory opens the store and wires real dependencies. Tests
// may inject a stub factory.
func NewServeCmd(depsFactory func(host string, port int, interval time.Duration, dataDir string) ServeDeps) *cobra.Command {
	var (
		flagHost     string
		flagPort     int
		flagInterval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP server and background scanner",
		Long:  "Start the piper HTTP server on HOST:PORT with a background scan scheduler (default interval: 5m).",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			dataDir, _ := cmd.Flags().GetString("data-dir")

			deps := depsFactory(flagHost, flagPort, flagInterval, dataDir)
			return RunServe(ctx, deps)
		},
	}

	cmd.Flags().StringVar(&flagHost, "host", "0.0.0.0", "HTTP listen address")
	cmd.Flags().IntVar(&flagPort, "port", 7878, "HTTP listen port")
	cmd.Flags().DurationVar(&flagInterval, "scan-interval", 5*time.Minute, "Scan interval (e.g. 5m, 30s)")

	return cmd
}
