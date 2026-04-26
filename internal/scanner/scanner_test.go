package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// stubRunner — concurrency-safe test double
// ---------------------------------------------------------------------------

// stubRunner implements Runner using a lookup table keyed by
// "<name> <arg1> <arg2> ...".  A matching errTable entry returns that error
// instead of the byte payload.
type stubRunner struct {
	mu       sync.Mutex
	data     map[string][]byte
	errTable map[string]error
}

func newStubRunner(data map[string][]byte, errs map[string]error) *stubRunner {
	if errs == nil {
		errs = map[string]error{}
	}
	return &stubRunner{data: data, errTable: errs}
}

func (s *stubRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name
	if len(args) > 0 {
		key += " " + strings.Join(args, " ")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.errTable[key]; ok {
		return nil, err
	}
	if b, ok := s.data[key]; ok {
		return b, nil
	}
	// Default: empty response (no error).
	return []byte{}, nil
}

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

func loadFixture(t *testing.T, subdir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", subdir, name))
	if err != nil {
		t.Fatalf("failed to load fixture %s/%s: %v", subdir, name, err)
	}
	return data
}

// inspectJSON builds minimal valid docker-inspect JSON for a container ID/name.
func inspectJSONForID(id, name string) []byte {
	return []byte(`[{"Id":"` + id + `","Name":"/` + name + `","Config":{"Image":"test:v1"},"State":{"Status":"running"},"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"9999"}]}}}]`)
}

// ---------------------------------------------------------------------------
// Test 1: All sources succeed
// ---------------------------------------------------------------------------

func TestScan_AllSourcesSucceed(t *testing.T) {
	ssData := loadFixture(t, "ss", "simple.txt")
	psData := loadFixture(t, "docker", "ps_running_with_ports.txt")
	ufwData := loadFixture(t, "ufw", "active_simple.txt")

	// IDs from ps_running_with_ports.txt
	id1 := "a3f5b2c1d4e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2"
	id2 := "b4e7c3d2e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2"

	inspectData1 := loadFixture(t, "docker", "inspect_translate_server.json")
	inspectData2 := inspectJSONForID(id2, "metrics-collector")

	stub := newStubRunner(map[string][]byte{
		"ss -tlnp -H":                            ssData,
		"docker ps -a --format {{json .}}":        psData,
		"ufw status numbered":                      ufwData,
		"docker inspect " + id1:                   inspectData1,
		"docker inspect " + id2:                   inspectData2,
	}, nil)

	sc := &Scanner{Runner: stub}
	result, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan returned unexpected error: %v", err)
	}

	// Timestamps
	if result.StartedAt.IsZero() {
		t.Error("StartedAt must not be zero")
	}
	if result.FinishedAt.IsZero() {
		t.Error("FinishedAt must not be zero")
	}
	if result.FinishedAt.Before(result.StartedAt) {
		t.Errorf("FinishedAt (%v) must not be before StartedAt (%v)", result.FinishedAt, result.StartedAt)
	}

	// SS results
	if len(result.SS) != 4 {
		t.Errorf("SS: want 4 entries, got %d", len(result.SS))
	}

	// Docker results
	if len(result.Docker) != 2 {
		t.Errorf("Docker: want 2 entries, got %d", len(result.Docker))
	}

	// UFW results
	if !result.UFWActive {
		t.Error("UFWActive: want true")
	}
	if len(result.UFW) == 0 {
		t.Error("UFW: want non-empty rules")
	}

	// Inspected — both containers
	if len(result.Inspected) != 2 {
		t.Errorf("Inspected: want 2 entries, got %d", len(result.Inspected))
	}
	if _, ok := result.Inspected[id1]; !ok {
		t.Errorf("Inspected: missing entry for id1 %s", id1)
	}
	if _, ok := result.Inspected[id2]; !ok {
		t.Errorf("Inspected: missing entry for id2 %s", id2)
	}

	// No errors
	if len(result.Errors) != 0 {
		t.Errorf("Errors: want empty, got %v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Test 2: UFW inactive
// ---------------------------------------------------------------------------

func TestScan_UFWInactive(t *testing.T) {
	ufwData := loadFixture(t, "ufw", "inactive.txt")

	stub := newStubRunner(map[string][]byte{
		"ss -tlnp -H":                     []byte{},
		"docker ps -a --format {{json .}}": []byte{},
		"ufw status numbered":              ufwData,
	}, nil)

	sc := &Scanner{Runner: stub}
	result, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan returned unexpected error: %v", err)
	}

	if result.UFWActive {
		t.Error("UFWActive: want false for inactive status")
	}
	if len(result.UFW) != 0 {
		t.Errorf("UFW: want empty, got %d rules", len(result.UFW))
	}
	// No error entry for "ufw" — inactive is not an error condition.
	if _, hasErr := result.Errors["ufw"]; hasErr {
		t.Error("Errors[\"ufw\"]: should not be set when UFW is merely inactive")
	}
}

// ---------------------------------------------------------------------------
// Test 3: UFW missing (exec error)
// ---------------------------------------------------------------------------

func TestScan_UFWMissing(t *testing.T) {
	ssData := loadFixture(t, "ss", "simple.txt")
	psData := loadFixture(t, "docker", "ps_running_with_ports.txt")

	id1 := "a3f5b2c1d4e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2"
	id2 := "b4e7c3d2e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2"
	inspectData1 := loadFixture(t, "docker", "inspect_translate_server.json")
	inspectData2 := inspectJSONForID(id2, "metrics-collector")

	ufwErr := errors.New(`exec: "ufw": executable file not found in $PATH`)

	stub := newStubRunner(map[string][]byte{
		"ss -tlnp -H":                     ssData,
		"docker ps -a --format {{json .}}": psData,
		"docker inspect " + id1:            inspectData1,
		"docker inspect " + id2:            inspectData2,
	}, map[string]error{
		"ufw status numbered": ufwErr,
	})

	sc := &Scanner{Runner: stub}
	result, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan returned unexpected error: %v", err)
	}

	// UFW error must be recorded
	if result.Errors["ufw"] == nil {
		t.Error("Errors[\"ufw\"]: want non-nil error when ufw is missing")
	}
	if result.UFWActive {
		t.Error("UFWActive: want false when ufw is missing")
	}

	// SS and Docker must still complete
	if len(result.SS) == 0 {
		t.Error("SS: want non-empty results even when ufw fails")
	}
	if len(result.Docker) == 0 {
		t.Error("Docker: want non-empty results even when ufw fails")
	}
}

// ---------------------------------------------------------------------------
// Test 4: Docker error does not block SS
// ---------------------------------------------------------------------------

func TestScan_DockerErrorDoesNotBlockSS(t *testing.T) {
	ssData := loadFixture(t, "ss", "simple.txt")
	ufwData := loadFixture(t, "ufw", "active_simple.txt")
	dockerErr := errors.New("cannot connect to the Docker daemon")

	stub := newStubRunner(map[string][]byte{
		"ss -tlnp -H":         ssData,
		"ufw status numbered":  ufwData,
	}, map[string]error{
		"docker ps -a --format {{json .}}": dockerErr,
	})

	sc := &Scanner{Runner: stub}
	result, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan returned unexpected error: %v", err)
	}

	// Docker error recorded
	if result.Errors["docker"] == nil {
		t.Error("Errors[\"docker\"]: want non-nil error when docker ps fails")
	}

	// SS still parsed
	if len(result.SS) == 0 {
		t.Error("SS: want non-empty results even when docker fails")
	}

	// Inspected must be empty (no containers to inspect)
	if len(result.Inspected) != 0 {
		t.Errorf("Inspected: want empty when docker ps fails, got %d", len(result.Inspected))
	}

	// UFW still parsed
	if !result.UFWActive {
		t.Error("UFWActive: want true (ufw succeeded)")
	}
}

// ---------------------------------------------------------------------------
// Test 5: Inspect fails for one container
// ---------------------------------------------------------------------------

func TestScan_InspectFailsForOneContainer(t *testing.T) {
	// Build a docker-ps response with 2 containers
	id1 := "aaa1000000000000000000000000000000000000000000000000000000000000001"
	id2 := "bbb2000000000000000000000000000000000000000000000000000000000000002"

	psData := []byte(
		`{"ID":"` + id1 + `","Names":"container-ok","Image":"img:v1","State":"running","Status":"Up 1 day","Ports":"0.0.0.0:7001->80/tcp"}` + "\n" +
			`{"ID":"` + id2 + `","Names":"container-fail","Image":"img:v2","State":"running","Status":"Up 2 days","Ports":"0.0.0.0:7002->80/tcp"}` + "\n",
	)

	inspectOK := inspectJSONForID(id1, "container-ok")
	inspectErr := errors.New("no such container: " + id2)

	stub := newStubRunner(map[string][]byte{
		"ss -tlnp -H":                     []byte{},
		"docker ps -a --format {{json .}}": psData,
		"ufw status numbered":              []byte("Status: inactive\n"),
		"docker inspect " + id1:            inspectOK,
	}, map[string]error{
		"docker inspect " + id2: inspectErr,
	})

	sc := &Scanner{Runner: stub}
	result, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan returned unexpected error: %v", err)
	}

	// Successful inspect present
	if _, ok := result.Inspected[id1]; !ok {
		t.Errorf("Inspected: want entry for id1 (%s)", id1)
	}

	// Failed inspect must NOT be present in Inspected
	if _, ok := result.Inspected[id2]; ok {
		t.Errorf("Inspected: must not have entry for id2 (%s) whose inspect failed", id2)
	}

	// Error recorded for the failing inspect
	errKey := "inspect:" + id2
	if result.Errors[errKey] == nil {
		t.Errorf("Errors[%q]: want non-nil error", errKey)
	}

	// No error for the successful inspect
	if result.Errors["inspect:"+id1] != nil {
		t.Errorf("Errors[\"inspect:%s\"]: want nil, got %v", id1, result.Errors["inspect:"+id1])
	}
}

// ---------------------------------------------------------------------------
// Test 6: Respects context cancellation
// ---------------------------------------------------------------------------

func TestScan_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	// Use a stub that blocks forever if ctx is not checked.
	// Since the runner ignores context here, the key check is in Scan itself.
	stub := newStubRunner(map[string][]byte{}, nil)

	sc := &Scanner{Runner: stub}

	done := make(chan error, 1)
	go func() {
		_, err := sc.Scan(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Scan: want context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Scan did not return promptly after context cancellation")
	}
}

// ---------------------------------------------------------------------------
// Test 7: Concurrency / race detector
// ---------------------------------------------------------------------------

func TestScan_ConcurrencyDoesntRace(t *testing.T) {
	ssData := loadFixture(t, "ss", "simple.txt")
	psData := loadFixture(t, "docker", "ps_running_with_ports.txt")
	ufwData := loadFixture(t, "ufw", "active_simple.txt")

	id1 := "a3f5b2c1d4e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2"
	id2 := "b4e7c3d2e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2"
	inspectData1 := loadFixture(t, "docker", "inspect_translate_server.json")
	inspectData2 := inspectJSONForID(id2, "metrics-collector")

	stub := newStubRunner(map[string][]byte{
		"ss -tlnp -H":                     ssData,
		"docker ps -a --format {{json .}}": psData,
		"ufw status numbered":              ufwData,
		"docker inspect " + id1:            inspectData1,
		"docker inspect " + id2:            inspectData2,
	}, nil)

	sc := &Scanner{Runner: stub}

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = sc.Scan(context.Background())
		}()
	}
	wg.Wait()
	// If -race detects a race, the test binary itself will fail.
}
