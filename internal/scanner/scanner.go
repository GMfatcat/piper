package scanner

import (
	"context"
	"fmt"
	"os/exec"
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
	StartedAt  time.Time
	FinishedAt time.Time
	SS         []SSEntry
	Docker     []DockerEntry           // from `docker ps -a --format json`
	Inspected  map[string]DockerEntry  // key = container ID; richer Ports via `docker inspect`
	UFW        []UFWRule
	UFWActive  bool                   // false when `Status: inactive` or ufw missing
	Errors     map[string]error       // keys: "ss", "docker", "ufw", "inspect:<id>"
}

// Scanner runs the four data collectors in parallel.
type Scanner struct {
	Runner Runner
}

// New returns a Scanner using ExecRunner.
func New() *Scanner { return &Scanner{Runner: ExecRunner{}} }

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

	// --- ufw status numbered ---
	go func() {
		defer wg.Done()
		raw, err := s.Runner.Run(ctx, "ufw", "status", "numbered")
		if err != nil {
			mu.Lock()
			result.Errors["ufw"] = fmt.Errorf("ufw: %w", err)
			// UFWActive remains false
			mu.Unlock()
			return
		}
		rules, err := ParseUFWStatus(raw)
		if err != nil {
			mu.Lock()
			result.Errors["ufw"] = fmt.Errorf("ufw parse: %w", err)
			mu.Unlock()
			return
		}
		// Detect "Status: inactive" — ParseUFWStatus returns empty slice and no error.
		// Detect "Status: active" by checking if the raw output contains "Status: active".
		active := containsActiveStatus(raw)
		mu.Lock()
		result.UFW = rules
		result.UFWActive = active
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
