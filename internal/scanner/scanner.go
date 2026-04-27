package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Runner abstracts command execution so tests can stub it.
// Implementations must respect ctx cancellation.
// Returning a non-nil error MUST NOT prevent partial results — Scan handles errors per-source.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is the production Runner backed by os/exec.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// ScanResult is the aggregated output of one scan pass. Per-source errors are
// reported in the Errors map (key = source name) so the caller can degrade
// gracefully (e.g., UFW unavailable → still show ss + docker info).
type ScanResult struct {
	StartedAt   time.Time
	FinishedAt  time.Time
	SS          []SSEntry
	Docker      []DockerEntry          // from `docker ps -a --format json`
	Inspected   map[string]DockerEntry // key = container ID; richer Ports via `docker inspect`
	UFW         []UFWRule
	UFWActive   bool             // Status line said "active"
	UFWReadable bool             // ufw command itself succeeded (regardless of active/inactive)
	Errors      map[string]error // keys: "ss", "docker", "ufw", "inspect:<id>"
}

// Scanner runs the four data collectors in parallel.
type Scanner struct {
	Runner Runner
	Logger *slog.Logger
}

// New returns a Scanner using ExecRunner and slog.Default().
func New() *Scanner { return &Scanner{Runner: ExecRunner{}, Logger: slog.Default()} }

// log returns the configured logger or slog.Default() if nil.
func (s *Scanner) log() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

// Scan kicks off ss / docker ps / ufw status concurrently. Once docker ps
// completes, it issues `docker inspect <id>` calls (sequentially is fine for
// Phase 1) for each container so Inspected gets richer port info than docker ps.
//
// On any per-source error, the corresponding ScanResult slice/map is left
// empty/nil and the error is recorded in ScanResult.Errors. Scan itself
// returns an error ONLY if ctx is canceled or all four sources fail.
func (s *Scanner) Scan(ctx context.Context) (ScanResult, error) {
	// Check for pre-canceled context immediately.
	select {
	case <-ctx.Done():
		return ScanResult{}, ctx.Err()
	default:
	}

	result := ScanResult{
		StartedAt: time.Now().UTC(),
		Errors:    make(map[string]error),
		Inspected: make(map[string]DockerEntry),
	}

	// mu protects all writes to result.
	var mu sync.Mutex

	// Run ss, docker ps, and ufw concurrently.
	var wg sync.WaitGroup
	wg.Add(3)

	// Collect docker entries via a channel so inspect can proceed after docker ps.
	type dockerResult struct {
		entries []DockerEntry
		err     error
	}
	dockerCh := make(chan dockerResult, 1)

	// --- ss -tlnp -H ---
	go func() {
		defer wg.Done()
		raw, err := s.Runner.Run(ctx, "ss", "-tlnp", "-H")
		if err != nil {
			mu.Lock()
			result.Errors["ss"] = fmt.Errorf("ss: %w", err)
			mu.Unlock()
			return
		}
		entries, err := ParseSS(raw)
		if err != nil {
			mu.Lock()
			result.Errors["ss"] = fmt.Errorf("ss parse: %w", err)
			mu.Unlock()
			return
		}
		mu.Lock()
		result.SS = entries
		mu.Unlock()
	}()

	// --- docker ps -a --format {{json .}} ---
	go func() {
		defer wg.Done()
		raw, err := s.Runner.Run(ctx, "docker", "ps", "-a", "--format", "{{json .}}")
		if err != nil {
			mu.Lock()
			result.Errors["docker"] = fmt.Errorf("docker ps: %w", err)
			mu.Unlock()
			dockerCh <- dockerResult{err: err}
			return
		}
		entries, err := ParseDockerPS(raw)
		if err != nil {
			mu.Lock()
			result.Errors["docker"] = fmt.Errorf("docker ps parse: %w", err)
			mu.Unlock()
			dockerCh <- dockerResult{err: err}
			return
		}
		mu.Lock()
		result.Docker = entries
		mu.Unlock()
		dockerCh <- dockerResult{entries: entries}
	}()

	// --- sudo -n ufw status numbered ---
	// `sudo -n` (non-interactive) fails immediately if a password would be
	// required, instead of blocking the scan. Configure sudoers (see README)
	// to grant the piper user passwordless access to `ufw status`.
	go func() {
		defer wg.Done()
		raw, err := s.Runner.Run(ctx, "sudo", "-n", "ufw", "status", "numbered")
		if err != nil {
			stderr := extractStderr(err)
			s.diagnoseUFWError(err, stderr)
			mu.Lock()
			result.Errors["ufw"] = fmt.Errorf("ufw: %w", err)
			// UFWActive remains false; UFWReadable remains false.
			mu.Unlock()
			return
		}
		rules, perr := ParseUFWStatus(raw)
		if perr != nil {
			mu.Lock()
			result.Errors["ufw"] = fmt.Errorf("ufw parse: %w", perr)
			// Command succeeded but parser failed — still readable, but contents
			// are unusable; conservatively leave UFWReadable=false.
			mu.Unlock()
			return
		}
		// Detect "Status: inactive" — ParseUFWStatus returns empty slice and no error.
		// Detect "Status: active" by checking if the raw output contains "Status: active".
		active := containsActiveStatus(raw)
		if !active {
			s.log().Warn("ufw is inactive — port firewall data will be unavailable",
				"hint", "run: sudo ufw enable")
		}
		mu.Lock()
		result.UFW = rules
		result.UFWActive = active
		result.UFWReadable = true
		mu.Unlock()
	}()

	// Wait for all three concurrent collectors to finish.
	wg.Wait()

	// --- docker inspect (sequential, per Phase 1 spec) ---
	// Retrieve docker entries from the channel (already drained by the goroutine).
	dr := <-dockerCh
	if dr.err == nil && len(dr.entries) > 0 {
		for _, container := range dr.entries {
			id := container.ID
			raw, err := s.Runner.Run(ctx, "docker", "inspect", id)
			if err != nil {
				mu.Lock()
				result.Errors["inspect:"+id] = fmt.Errorf("docker inspect %s: %w", id, err)
				mu.Unlock()
				continue
			}
			entry, err := ParseDockerInspect(raw)
			if err != nil {
				mu.Lock()
				result.Errors["inspect:"+id] = fmt.Errorf("docker inspect %s parse: %w", id, err)
				mu.Unlock()
				continue
			}
			mu.Lock()
			result.Inspected[id] = entry
			mu.Unlock()
		}
	}

	result.FinishedAt = time.Now().UTC()

	// Return context error if ctx was canceled during the scan.
	select {
	case <-ctx.Done():
		return result, ctx.Err()
	default:
	}

	// Return error only if ALL four sources failed.
	mu.Lock()
	defer mu.Unlock()
	if len(result.Errors) >= 4 {
		return result, fmt.Errorf("all scan sources failed: ss=%v, docker=%v, ufw=%v",
			result.Errors["ss"], result.Errors["docker"], result.Errors["ufw"])
	}

	return result, nil
}

// extractStderr pulls the captured stderr bytes from an error returned by
// exec.Cmd.Output(). Falls back to err.Error() so unit-test stub errors with
// stderr-like substrings ("password is required", "command not found") still
// trigger the matching diagnostics.
func extractStderr(err error) string {
	if err == nil {
		return ""
	}
	if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
		return string(exitErr.Stderr)
	}
	return err.Error()
}

// diagnoseUFWError logs a slog warning that names the most likely cause of the
// `sudo -n ufw status` failure and points the operator at the fix. The full
// error is preserved in result.Errors["ufw"] for the caller to surface.
func (s *Scanner) diagnoseUFWError(err error, stderr string) {
	logger := s.log()
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "password is required"),
		strings.Contains(low, "a password is required"):
		logger.Warn("ufw read failed — sudo wants a password under -n",
			"err", err,
			"stderr", strings.TrimSpace(stderr),
			"hint", "configure sudoers: grant NOPASSWD for /usr/sbin/ufw status (see README)")
	case strings.Contains(low, "command not found"),
		strings.Contains(low, "no such file or directory"),
		strings.Contains(low, "executable file not found"):
		logger.Warn("ufw read failed — ufw not found on PATH",
			"err", err,
			"stderr", strings.TrimSpace(stderr),
			"hint", "install ufw: apt install ufw (or skip ufw integration)")
	case strings.Contains(low, "is not allowed to execute"),
		strings.Contains(low, "not allowed to run"):
		logger.Warn("ufw read failed — sudoers does not permit this command",
			"err", err,
			"stderr", strings.TrimSpace(stderr),
			"hint", "extend sudoers to allow `ufw status numbered` for this user (see README)")
	default:
		logger.Warn("ufw read failed",
			"err", err,
			"stderr", strings.TrimSpace(stderr))
	}
}

// containsActiveStatus checks whether the UFW output contains "Status: active".
// ParseUFWStatus returns empty rules for inactive status; we need to distinguish
// "active with no rules" from "inactive" by re-checking the raw bytes.
func containsActiveStatus(raw []byte) bool {
	// Scan line by line looking for "Status: active".
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == '\n' {
			line := string(raw[start:i])
			// Trim spaces and carriage returns.
			trimmed := trimSpace(line)
			if len(trimmed) > 8 && trimmed[:8] == "Status: " {
				statusVal := trimmed[8:]
				return statusVal == "active"
			}
			start = i + 1
		}
	}
	return false
}

// trimSpace trims ASCII spaces, tabs, and carriage returns from both ends.
func trimSpace(s string) string {
	// Trim leading whitespace.
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\r') {
		s = s[1:]
	}
	// Trim trailing whitespace.
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
