package cli

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/scanner"
	"github.com/GMfatcat/piper/internal/server"
	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// stubServeServer is a no-op server for unit tests.
type stubServeServer struct{}

func (s *stubServeServer) ListenAndServe(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
func (s *stubServeServer) Handler() http.Handler { return http.NewServeMux() }

// noopNewServer always returns a stubServeServer.
func noopNewServer(_ string, _ int, _ server.Deps) ServeServer {
	return &stubServeServer{}
}

// openTestStore opens a fresh SQLite store in a temp dir and returns it
// alongside its path.
func openTestStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir + "/piper.db")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, dir
}

// ufwRule builds a simple UFWRule for test data.
func ufwRule(port, ruleNum int, action string) scanner.UFWRule {
	return scanner.UFWRule{
		RuleNum: ruleNum,
		Port:    port,
		Proto:   "tcp",
		Action:  action,
	}
}

// ─── reduceToSnapshotRows tests ───────────────────────────────────────────────

// Test 1: running docker container → StateUsedDocker
func TestReduceToSnapshotRows_DockerRunning(t *testing.T) {
	r := scanner.ScanResult{
		Docker: []scanner.DockerEntry{
			{
				ID:     "abc123",
				Name:   "my-container",
				Image:  "myimage:v1",
				State:  "running",
				Status: "Up 2 days",
				Ports:  []scanner.HostPort{{HostPort: 8080, ContainerPort: 80, Proto: "tcp"}},
			},
		},
		Inspected: map[string]scanner.DockerEntry{},
	}

	rows := reduceToSnapshotRows(r)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.Port != 8080 {
		t.Errorf("Port: got %d, want 8080", row.Port)
	}
	if row.State != store.StateUsedDocker {
		t.Errorf("State: got %q, want %q", row.State, store.StateUsedDocker)
	}
	if row.ContainerID != "abc123" {
		t.Errorf("ContainerID: got %q, want %q", row.ContainerID, "abc123")
	}
	if row.ContainerName != "my-container" {
		t.Errorf("ContainerName: got %q, want %q", row.ContainerName, "my-container")
	}
	if row.ContainerImage != "myimage:v1" {
		t.Errorf("ContainerImage: got %q, want %q", row.ContainerImage, "myimage:v1")
	}
	if row.ContainerStatus != "Up 2 days" {
		t.Errorf("ContainerStatus: got %q, want %q", row.ContainerStatus, "Up 2 days")
	}
}

// Test 2: stopped docker container → StateReservedImplicit
func TestReduceToSnapshotRows_DockerStopped(t *testing.T) {
	r := scanner.ScanResult{
		Docker: []scanner.DockerEntry{
			{
				ID:     "def456",
				Name:   "stopped-svc",
				Image:  "svcimage:latest",
				State:  "exited",
				Status: "Exited (0) 1 hour ago",
				Ports:  []scanner.HostPort{{HostPort: 9090, ContainerPort: 9090, Proto: "tcp"}},
			},
		},
		Inspected: map[string]scanner.DockerEntry{},
	}

	rows := reduceToSnapshotRows(r)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.State != store.StateReservedImplicit {
		t.Errorf("State: got %q, want %q", row.State, store.StateReservedImplicit)
	}
	if row.Port != 9090 {
		t.Errorf("Port: got %d, want 9090", row.Port)
	}
}

// Test 3: SS entry only → StateUsedProcess
func TestReduceToSnapshotRows_SSOnly(t *testing.T) {
	r := scanner.ScanResult{
		SS: []scanner.SSEntry{
			{Port: 8000, PID: 1234, ProcessName: "python3"},
		},
		Docker:    []scanner.DockerEntry{},
		Inspected: map[string]scanner.DockerEntry{},
	}

	rows := reduceToSnapshotRows(r)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.State != store.StateUsedProcess {
		t.Errorf("State: got %q, want %q", row.State, store.StateUsedProcess)
	}
	if row.PID != 1234 {
		t.Errorf("PID: got %d, want 1234", row.PID)
	}
	if row.ProcessName != "python3" {
		t.Errorf("ProcessName: got %q, want %q", row.ProcessName, "python3")
	}
}

// Test 4: SS entry for 8080 + Docker running container on 8080 → one row, StateUsedDocker (docker wins)
func TestReduceToSnapshotRows_DockerWinsOverSS(t *testing.T) {
	r := scanner.ScanResult{
		SS: []scanner.SSEntry{
			{Port: 8080, PID: 55555, ProcessName: "docker-proxy"},
		},
		Docker: []scanner.DockerEntry{
			{
				ID:     "ghi789",
				Name:   "api-server",
				Image:  "api:v2",
				State:  "running",
				Status: "Up 3 days",
				Ports:  []scanner.HostPort{{HostPort: 8080, ContainerPort: 80, Proto: "tcp"}},
			},
		},
		Inspected: map[string]scanner.DockerEntry{},
	}

	rows := reduceToSnapshotRows(r)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d (docker should win over ss)", len(rows))
	}
	row := rows[0]
	if row.State != store.StateUsedDocker {
		t.Errorf("State: got %q, want %q (docker should win)", row.State, store.StateUsedDocker)
	}
	if row.ContainerName != "api-server" {
		t.Errorf("ContainerName: got %q, want %q", row.ContainerName, "api-server")
	}
}

// Test 5: SS entry + matching UFW rule → UFWAction and UFWRuleNum populated
func TestReduceToSnapshotRows_UFWAttached(t *testing.T) {
	r := scanner.ScanResult{
		SS: []scanner.SSEntry{
			{Port: 8080, PID: 100, ProcessName: "python3"},
		},
		Docker:    []scanner.DockerEntry{},
		Inspected: map[string]scanner.DockerEntry{},
		UFW: []scanner.UFWRule{
			ufwRule(8080, 3, "allow"),
		},
	}

	rows := reduceToSnapshotRows(r)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.UFWAction != "allow" {
		t.Errorf("UFWAction: got %q, want %q", row.UFWAction, "allow")
	}
	if row.UFWRuleNum <= 0 {
		t.Errorf("UFWRuleNum: got %d, want > 0", row.UFWRuleNum)
	}
}

// Test 6: Docker docker ps shows 1 port; Inspected has 3 ports → 3 rows
func TestReduceToSnapshotRows_InspectedRicher(t *testing.T) {
	containerID := "rich001"
	r := scanner.ScanResult{
		Docker: []scanner.DockerEntry{
			{
				ID:     containerID,
				Name:   "rich-svc",
				Image:  "rich:v1",
				State:  "running",
				Status: "Up 1 day",
				Ports:  []scanner.HostPort{{HostPort: 8080, ContainerPort: 80, Proto: "tcp"}},
			},
		},
		Inspected: map[string]scanner.DockerEntry{
			containerID: {
				ID:    containerID,
				Name:  "rich-svc",
				Image: "rich:v1",
				// State/Status left blank — these come from docker ps
				Ports: []scanner.HostPort{
					{HostPort: 8080, ContainerPort: 80, Proto: "tcp"},
					{HostPort: 8443, ContainerPort: 443, Proto: "tcp"},
					{HostPort: 9090, ContainerPort: 9090, Proto: "tcp"},
				},
			},
		},
	}

	rows := reduceToSnapshotRows(r)

	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (inspected richer), got %d", len(rows))
	}
	// All should be used_docker (container is running).
	for _, row := range rows {
		if row.State != store.StateUsedDocker {
			t.Errorf("port %d: State: got %q, want %q", row.Port, row.State, store.StateUsedDocker)
		}
	}
}

// Test 7: random insertion order → output sorted ascending by port
func TestReduceToSnapshotRows_SortedByPort(t *testing.T) {
	r := scanner.ScanResult{
		SS: []scanner.SSEntry{
			{Port: 9000, PID: 1},
			{Port: 7000, PID: 2},
			{Port: 8000, PID: 3},
		},
		Docker:    []scanner.DockerEntry{},
		Inspected: map[string]scanner.DockerEntry{},
	}

	rows := reduceToSnapshotRows(r)

	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].Port <= rows[i-1].Port {
			t.Errorf("rows not sorted ascending: rows[%d].Port=%d <= rows[%d].Port=%d",
				i, rows[i].Port, i-1, rows[i-1].Port)
		}
	}
}

// ─── RunServe tests ──────────────────────────────────────────────────────────

// fakeScanner returns a fixed ScanResult on every call.
type fakeScanner struct {
	result scanner.ScanResult
	err    error
}

func (f *fakeScanner) Scan(_ context.Context) (scanner.ScanResult, error) {
	return f.result, f.err
}

// stubScannerWrapper wraps fakeScanner to satisfy *scanner.Scanner embedding.
// We cannot directly replace *scanner.Scanner (it's concrete), so we use a
// custom ServeDeps.Scanner replacement via an unexported wrapper.
//
// Since ServeDeps.Scanner is *scanner.Scanner (concrete), we patch RunServe by
// building the scanFunc in-line via a custom newServer. Instead, we rely on the
// Scanner field being nil + the default scanner path; but for test isolation
// we need to inject a fake. The cleanest approach for TDD without touching
// scanner internals: make the Scanner field nullable and always use the
// production Scanner in the nil case; for tests, we provide a customScanFunc
// wrapper via a closure.
//
// Implementation: the test overrides d.Scanner with a *scanner.Scanner that
// wraps our fakeScanner. We achieve this by having the Scanner Runner be a
// stub that returns pre-recorded output. However, the easiest approach per the
// spec is to expose a ScanFunc field or accept the fact that serve_test uses
// a real (tempdir) DB and a fake Runner.

// fakeRunner implements scanner.Runner for tests.
type fakeRunner struct {
	ssOut     []byte
	dockerOut []byte
	ufwOut    []byte
	err       error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	switch name {
	case "ss":
		return f.ssOut, nil
	case "docker":
		if len(args) > 0 && args[0] == "inspect" {
			return []byte("[]"), nil // empty array so inspect fails gracefully
		}
		return f.dockerOut, nil
	case "ufw":
		return f.ufwOut, nil
	default:
		return nil, nil
	}
}

// Test 8: RunServe happy path — scan executes and snapshot is saved.
func TestRunServe_HappyPath(t *testing.T) {
	st, dir := openTestStore(t)

	// Build a fake scanner that returns a simple SS result.
	runner := &fakeRunner{
		ssOut: []byte("LISTEN 0 128 0.0.0.0:8080 0.0.0.0:* users:((\"python3\",pid=1234,fd=6))\n"),
		// docker ps returns empty output (valid JSON: no lines)
		dockerOut: []byte(""),
		ufwOut:    []byte("Status: inactive\n"),
	}
	sc := &scanner.Scanner{Runner: runner}

	// Capture log output.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	deps := ServeDeps{
		DataDir:   dir,
		Store:     st,
		Scanner:   sc,
		Host:      "127.0.0.1",
		Port:      0, // no real binding needed
		Interval:  50 * time.Millisecond,
		Logger:    logger,
		Clock:     service.RealClock{},
		NewServer: noopNewServer,
	}

	// RunServe blocks until ctx is canceled.
	_ = RunServe(ctx, deps)

	// Assert at least one snapshot was saved.
	rows, _, err := st.GetSnapshot(context.Background())
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if len(rows) == 0 {
		t.Errorf("expected at least 1 snapshot row after scan, got 0")
	}
}

// Test 9: RunServe — persist error on every call; serve does not exit; errors
// are logged and the scheduler continues until ctx is canceled.
//
// To reliably trigger a scan error (logged via OnScanError), we close the
// store before RunServe starts. The scan itself may succeed (scanner just
// returns empty data when sources fail), but SaveSnapshot will always fail
// on a closed DB, causing scanFunc to return a non-nil error every pass.
func TestRunServe_ScannerErrorContinues(t *testing.T) {
	st, dir := openTestStore(t)

	// Close the store immediately so SaveSnapshot always errors.
	st.Close()

	// Reopen immediately to test we can open it again after close — but what
	// we really want is a closed DB to fail on write. Open a fresh broken store
	// by not re-opening (the store's DB is now closed; calling SaveSnapshot on
	// the closed *sql.DB will error).
	//
	// Since we closed st above, the t.Cleanup(Close) will double-close, which
	// is harmless (sql.DB.Close is idempotent with modernc/sqlite).

	// Use a runner that returns all-empty data (ss, docker, ufw all inactive),
	// which is fine — SaveSnapshot of empty rows against a closed DB still fails.
	runner := &fakeRunner{
		ssOut:     []byte(""),
		dockerOut: []byte(""),
		ufwOut:    []byte("Status: inactive\n"),
	}
	sc := &scanner.Scanner{Runner: runner}

	// Capture log output.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	deps := ServeDeps{
		DataDir:   dir,
		Store:     st,
		Scanner:   sc,
		Host:      "127.0.0.1",
		Port:      0,
		Interval:  50 * time.Millisecond,
		Logger:    logger,
		Clock:     service.RealClock{},
		NewServer: noopNewServer,
	}

	// RunServe swallows context.Canceled / context.DeadlineExceeded so cobra
	// doesn't print "Error: context canceled" on graceful shutdown.
	if err := RunServe(ctx, deps); err != nil {
		t.Errorf("expected nil on graceful shutdown, got %v", err)
	}

	// There should be some error log output (scan failed due to closed store).
	logOutput := logBuf.String()
	if logOutput == "" {
		t.Errorf("expected log output for scan errors (closed DB), got empty")
	}
}
