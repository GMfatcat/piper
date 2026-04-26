package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/scanner"
	"github.com/GMfatcat/piper/internal/store"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// stubSnap implements SnapshotProvider using a fixed ScanSnapshot.
type stubSnap struct {
	snap ScanSnapshot
	err  error
}

func (s stubSnap) LatestSnapshot(_ context.Context) (ScanSnapshot, error) {
	return s.snap, s.err
}

// stubReserves implements ReservationGetter using a pre-populated map.
type stubReserves struct {
	byPort map[int]store.Reservation
}

func (s stubReserves) GetReservation(_ context.Context, port int) (store.Reservation, error) {
	if r, ok := s.byPort[port]; ok {
		return r, nil
	}
	return store.Reservation{}, store.ErrReservationNotFound
}

// ---------------------------------------------------------------------------
// Helper: build a minimal Checker from stubs.
// ---------------------------------------------------------------------------

func newChecker(snap ScanSnapshot, reserves map[int]store.Reservation) *Checker {
	return &Checker{
		Snap:     stubSnap{snap: snap},
		Reserves: stubReserves{byPort: reserves},
	}
}

// scannedAt is a fixed timestamp for all test snapshots.
var scannedAt = time.Date(2026, 4, 25, 14, 32, 18, 0, time.UTC)

// emptySnap returns a minimal ScanSnapshot with ScannedAt set.
func emptySnap() ScanSnapshot {
	return ScanSnapshot{
		ScannedAt: scannedAt,
		Inspected: map[string]scanner.DockerEntry{},
	}
}

// ---------------------------------------------------------------------------
// Test 1: Free port — empty snapshot + no reservation
// ---------------------------------------------------------------------------

func TestCheckPorts_FreePort(t *testing.T) {
	c := newChecker(emptySnap(), nil)

	result, err := c.CheckPorts(context.Background(), []int{9000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(result.Results))
	}

	ps := result.Results[0]
	assertPort(t, ps, 9000)
	assertState(t, ps, PortFree)

	if ps.Occupant != nil {
		t.Errorf("Occupant: want nil, got %+v", ps.Occupant)
	}
	if ps.UFW != nil {
		t.Errorf("UFW: want nil, got %+v", ps.UFW)
	}
	if ps.ContainerOtherPorts != nil {
		t.Errorf("ContainerOtherPorts: want nil, got %+v", ps.ContainerOtherPorts)
	}
	if ps.Reservation != nil {
		t.Errorf("Reservation: want nil, got %+v", ps.Reservation)
	}
	if ps.DedupOf != 0 {
		t.Errorf("DedupOf: want 0, got %d", ps.DedupOf)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Used by a regular process (no docker)
// ---------------------------------------------------------------------------

func TestCheckPorts_UsedProcess(t *testing.T) {
	snap := emptySnap()
	snap.SS = []scanner.SSEntry{
		{Port: 8080, PID: 1234, ProcessName: "python3"},
	}

	c := newChecker(snap, nil)
	result, err := c.CheckPorts(context.Background(), []int{8080})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(result.Results))
	}

	ps := result.Results[0]
	assertPort(t, ps, 8080)
	assertState(t, ps, PortUsedProcess)

	if ps.Occupant == nil {
		t.Fatal("Occupant: want non-nil")
	}
	if ps.Occupant.Type != "process" {
		t.Errorf("Occupant.Type: want %q, got %q", "process", ps.Occupant.Type)
	}
	if ps.Occupant.PID != 1234 {
		t.Errorf("Occupant.PID: want 1234, got %d", ps.Occupant.PID)
	}
	if ps.Occupant.ProcessName != "python3" {
		t.Errorf("Occupant.ProcessName: want %q, got %q", "python3", ps.Occupant.ProcessName)
	}
	if ps.ContainerOtherPorts != nil {
		t.Errorf("ContainerOtherPorts: want nil for non-docker, got %+v", ps.ContainerOtherPorts)
	}
	if ps.Reservation != nil {
		t.Errorf("Reservation: want nil, got %+v", ps.Reservation)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Docker container — running, expands sibling ports, UFW for 8080 only
// ---------------------------------------------------------------------------

func TestCheckPorts_UsedDocker_ExpandsOtherPorts(t *testing.T) {
	const (
		containerID   = "a3f5b2c1d4e6f7a8b9c0d1e2f3a4b5c60"
		containerName = "ai-translate-server"
		containerImg  = "translate:v2"
	)

	dockerPS := scanner.DockerEntry{
		ID:     containerID,
		Name:   containerName,
		Image:  containerImg,
		State:  "running",
		Status: "Up 3 days",
		Ports: []scanner.HostPort{
			{HostPort: 8080, ContainerPort: 80, Proto: "tcp"},
			{HostPort: 8443, ContainerPort: 443, Proto: "tcp"},
			{HostPort: 9090, ContainerPort: 9090, Proto: "tcp"},
		},
	}

	// Inspected entry (from docker inspect) has the same ports.
	inspectedEntry := scanner.DockerEntry{
		ID:    containerID,
		Name:  containerName,
		Image: containerImg,
		State: "running",
		Ports: []scanner.HostPort{
			{HostPort: 8080, ContainerPort: 80, Proto: "tcp"},
			{HostPort: 8443, ContainerPort: 443, Proto: "tcp"},
			{HostPort: 9090, ContainerPort: 9090, Proto: "tcp"},
		},
	}

	snap := emptySnap()
	snap.Docker = []scanner.DockerEntry{dockerPS}
	snap.Inspected = map[string]scanner.DockerEntry{
		containerID: inspectedEntry,
	}
	// SS: docker-proxy entries for all three ports.
	snap.SS = []scanner.SSEntry{
		{Port: 8080, ProcessName: "docker-proxy", PID: 12453},
		{Port: 8443, ProcessName: "docker-proxy", PID: 12454},
		{Port: 9090, ProcessName: "docker-proxy", PID: 12455},
	}
	// UFW: only allow rule for 8080.
	snap.UFW = []scanner.UFWRule{
		{RuleNum: 5, Port: 8080, Proto: "tcp", Action: "allow"},
	}
	snap.UFWActive = true

	c := newChecker(snap, nil)
	result, err := c.CheckPorts(context.Background(), []int{8080})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(result.Results))
	}

	ps := result.Results[0]
	assertPort(t, ps, 8080)
	assertState(t, ps, PortUsedDocker)

	// Occupant checks.
	if ps.Occupant == nil {
		t.Fatal("Occupant: want non-nil")
	}
	if ps.Occupant.Type != "docker" {
		t.Errorf("Occupant.Type: want %q, got %q", "docker", ps.Occupant.Type)
	}
	if ps.Occupant.ContainerID != containerID {
		t.Errorf("Occupant.ContainerID: want %q, got %q", containerID, ps.Occupant.ContainerID)
	}
	if ps.Occupant.ContainerName != containerName {
		t.Errorf("Occupant.ContainerName: want %q, got %q", containerName, ps.Occupant.ContainerName)
	}
	if ps.Occupant.ContainerImage != containerImg {
		t.Errorf("Occupant.ContainerImage: want %q, got %q", containerImg, ps.Occupant.ContainerImage)
	}
	if ps.Occupant.ContainerStatus != "running" {
		t.Errorf("Occupant.ContainerStatus: want %q, got %q", "running", ps.Occupant.ContainerStatus)
	}
	if ps.Occupant.ContainerUptime != "Up 3 days" {
		t.Errorf("Occupant.ContainerUptime: want %q, got %q", "Up 3 days", ps.Occupant.ContainerUptime)
	}

	// UFW for primary port.
	if ps.UFW == nil {
		t.Fatal("UFW: want non-nil for port 8080")
	}
	if ps.UFW.Action != "allow" {
		t.Errorf("UFW.Action: want %q, got %q", "allow", ps.UFW.Action)
	}
	if ps.UFW.RuleNum != 5 {
		t.Errorf("UFW.RuleNum: want 5, got %d", ps.UFW.RuleNum)
	}

	// ContainerOtherPorts: 8443 and 9090 (not 8080 itself).
	if ps.ContainerOtherPorts == nil {
		t.Fatal("ContainerOtherPorts: want non-nil for docker port")
	}
	if len(ps.ContainerOtherPorts) != 2 {
		t.Fatalf("ContainerOtherPorts: want 2, got %d: %+v", len(ps.ContainerOtherPorts), ps.ContainerOtherPorts)
	}

	// Check 8443 has no UFW.
	copPorts := make(map[int]*UFWInfo)
	for _, cop := range ps.ContainerOtherPorts {
		copPorts[cop.Port] = cop.UFW
	}
	if _, ok := copPorts[8443]; !ok {
		t.Error("ContainerOtherPorts: missing port 8443")
	}
	if ufw := copPorts[8443]; ufw != nil {
		t.Errorf("ContainerOtherPorts[8443].UFW: want nil (no rule), got %+v", ufw)
	}
	if _, ok := copPorts[9090]; !ok {
		t.Error("ContainerOtherPorts: missing port 9090")
	}
	if ufw := copPorts[9090]; ufw != nil {
		t.Errorf("ContainerOtherPorts[9090].UFW: want nil (no rule), got %+v", ufw)
	}
	// Primary port must NOT appear in other ports.
	if _, ok := copPorts[8080]; ok {
		t.Error("ContainerOtherPorts: must not contain the primary port 8080")
	}

	if ps.Reservation != nil {
		t.Errorf("Reservation: want nil, got %+v", ps.Reservation)
	}
}

// ---------------------------------------------------------------------------
// Test 4: Reserved implicit — exited container, no SS entry
// ---------------------------------------------------------------------------

func TestCheckPorts_ReservedImplicit(t *testing.T) {
	const (
		containerID   = "bb1234567890abcdef1234567890abcdef12345678"
		containerName = "subtitle-server"
	)

	snap := emptySnap()
	snap.Docker = []scanner.DockerEntry{
		{
			ID:     containerID,
			Name:   containerName,
			Image:  "subtitle:latest",
			State:  "exited",
			Status: "Exited (0) 2 hours ago",
			Ports: []scanner.HostPort{
				{HostPort: 8081, ContainerPort: 80, Proto: "tcp"},
			},
		},
	}
	// No SS entry for 8081.

	c := newChecker(snap, nil)
	result, err := c.CheckPorts(context.Background(), []int{8081})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(result.Results))
	}

	ps := result.Results[0]
	assertPort(t, ps, 8081)
	assertState(t, ps, PortReservedImplicit)

	if ps.Occupant != nil {
		t.Errorf("Occupant: want nil for reserved_implicit, got %+v", ps.Occupant)
	}
	if ps.Reservation == nil {
		t.Fatal("Reservation: want non-nil")
	}
	if ps.Reservation.Source != "implicit" {
		t.Errorf("Reservation.Source: want %q, got %q", "implicit", ps.Reservation.Source)
	}
	if ps.Reservation.ContainerName != containerName {
		t.Errorf("Reservation.ContainerName: want %q, got %q", containerName, ps.Reservation.ContainerName)
	}
	if ps.Reservation.ContainerStatus != "exited" {
		t.Errorf("Reservation.ContainerStatus: want %q, got %q", "exited", ps.Reservation.ContainerStatus)
	}
}

// ---------------------------------------------------------------------------
// Test 5: Reserved explicit — store has reservation, no SS, no docker
// ---------------------------------------------------------------------------

func TestCheckPorts_ReservedExplicit(t *testing.T) {
	reserves := map[int]store.Reservation{
		9100: {
			Port: 9100,
			Name: "vllm-llama",
			Note: "next week deployment",
		},
	}

	c := newChecker(emptySnap(), reserves)
	result, err := c.CheckPorts(context.Background(), []int{9100})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(result.Results))
	}

	ps := result.Results[0]
	assertPort(t, ps, 9100)
	assertState(t, ps, PortReservedExplicit)

	if ps.Occupant != nil {
		t.Errorf("Occupant: want nil, got %+v", ps.Occupant)
	}
	if ps.Reservation == nil {
		t.Fatal("Reservation: want non-nil")
	}
	if ps.Reservation.Source != "explicit" {
		t.Errorf("Reservation.Source: want %q, got %q", "explicit", ps.Reservation.Source)
	}
	if ps.Reservation.Name != "vllm-llama" {
		t.Errorf("Reservation.Name: want %q, got %q", "vllm-llama", ps.Reservation.Name)
	}
	if ps.Reservation.Note != "next week deployment" {
		t.Errorf("Reservation.Note: want %q, got %q", "next week deployment", ps.Reservation.Note)
	}
	if ps.ContainerOtherPorts != nil {
		t.Errorf("ContainerOtherPorts: want nil for explicit reservation, got %+v", ps.ContainerOtherPorts)
	}
}

// ---------------------------------------------------------------------------
// Test 6: Explicit reservation overridden by active listener
//
// When BOTH an explicit reservation AND an active listener exist, the listener
// wins (state = used_process or used_docker). The Reservation field is still
// populated so callers can surface the planning conflict.
// ---------------------------------------------------------------------------

func TestCheckPorts_ExplicitReservationOverridesFree(t *testing.T) {
	// Port 9100 has an explicit reservation AND an active process listener.
	reserves := map[int]store.Reservation{
		9100: {Port: 9100, Name: "planned-service", Note: "reserving for deployment"},
	}

	snap := emptySnap()
	snap.SS = []scanner.SSEntry{
		{Port: 9100, PID: 5678, ProcessName: "fastapi"},
	}

	c := newChecker(snap, reserves)
	result, err := c.CheckPorts(context.Background(), []int{9100})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(result.Results))
	}

	ps := result.Results[0]
	assertPort(t, ps, 9100)

	// Listener wins over reservation.
	assertState(t, ps, PortUsedProcess)

	// But reservation is still reported (planning conflict).
	if ps.Reservation == nil {
		t.Fatal("Reservation: want non-nil (conflict signal even though listener wins)")
	}
	if ps.Reservation.Source != "explicit" {
		t.Errorf("Reservation.Source: want %q, got %q", "explicit", ps.Reservation.Source)
	}
	if ps.Reservation.Name != "planned-service" {
		t.Errorf("Reservation.Name: want %q, got %q", "planned-service", ps.Reservation.Name)
	}

	// Occupant reflects the actual listener, not the reservation.
	if ps.Occupant == nil {
		t.Fatal("Occupant: want non-nil (listener is active)")
	}
	if ps.Occupant.Type != "process" {
		t.Errorf("Occupant.Type: want %q, got %q", "process", ps.Occupant.Type)
	}
	if ps.Occupant.ProcessName != "fastapi" {
		t.Errorf("Occupant.ProcessName: want %q, got %q", "fastapi", ps.Occupant.ProcessName)
	}
}

// ---------------------------------------------------------------------------
// Test 7: UFW info is attached when there is an SS listener + UFW rule
// ---------------------------------------------------------------------------

func TestCheckPorts_UFWAttached(t *testing.T) {
	snap := emptySnap()
	snap.SS = []scanner.SSEntry{
		{Port: 8080, PID: 1001, ProcessName: "nginx"},
	}
	snap.UFW = []scanner.UFWRule{
		{RuleNum: 3, Port: 8080, Proto: "tcp", Action: "allow"},
	}
	snap.UFWActive = true

	c := newChecker(snap, nil)
	result, err := c.CheckPorts(context.Background(), []int{8080})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(result.Results))
	}

	ps := result.Results[0]
	assertPort(t, ps, 8080)
	assertState(t, ps, PortUsedProcess)

	if ps.UFW == nil {
		t.Fatal("UFW: want non-nil")
	}
	if ps.UFW.Action != "allow" {
		t.Errorf("UFW.Action: want %q, got %q", "allow", ps.UFW.Action)
	}
	if ps.UFW.RuleNum != 3 {
		t.Errorf("UFW.RuleNum: want 3, got %d", ps.UFW.RuleNum)
	}
}

// ---------------------------------------------------------------------------
// Test 8: Dedup — two ports on the same docker container
// ---------------------------------------------------------------------------

func TestCheckPorts_DedupSameContainer(t *testing.T) {
	const (
		containerID   = "dedup-container-id-001"
		containerName = "dedup-server"
	)

	dockerPS := scanner.DockerEntry{
		ID:     containerID,
		Name:   containerName,
		Image:  "dedup:v1",
		State:  "running",
		Status: "Up 1 hour",
		Ports: []scanner.HostPort{
			{HostPort: 8080, ContainerPort: 80, Proto: "tcp"},
			{HostPort: 8443, ContainerPort: 443, Proto: "tcp"},
		},
	}

	snap := emptySnap()
	snap.Docker = []scanner.DockerEntry{dockerPS}
	snap.Inspected = map[string]scanner.DockerEntry{
		containerID: dockerPS,
	}
	snap.SS = []scanner.SSEntry{
		{Port: 8080, ProcessName: "docker-proxy", PID: 9001},
		{Port: 8443, ProcessName: "docker-proxy", PID: 9002},
	}

	c := newChecker(snap, nil)
	// Query both ports — 8080 is the "primary" that expands other ports,
	// 8443 should be deduplicated.
	result, err := c.CheckPorts(context.Background(), []int{8080, 8443})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(result.Results))
	}

	// First result: full detail for 8080.
	first := result.Results[0]
	assertPort(t, first, 8080)
	assertState(t, first, PortUsedDocker)
	if first.DedupOf != 0 {
		t.Errorf("Result[0].DedupOf: want 0 (not a dedup), got %d", first.DedupOf)
	}
	// 8443 must appear in container_other_ports of the first result.
	hasOther := false
	for _, cop := range first.ContainerOtherPorts {
		if cop.Port == 8443 {
			hasOther = true
		}
	}
	if !hasOther {
		t.Errorf("Result[0].ContainerOtherPorts: want 8443, got %+v", first.ContainerOtherPorts)
	}

	// Second result: dedup record for 8443.
	second := result.Results[1]
	assertPort(t, second, 8443)
	if second.DedupOf != 8080 {
		t.Errorf("Result[1].DedupOf: want 8080, got %d", second.DedupOf)
	}
	// State is still meaningful for dedup records.
	assertState(t, second, PortUsedDocker)
}

// ---------------------------------------------------------------------------
// Test 9: Order preserved
// ---------------------------------------------------------------------------

func TestCheckPorts_OrderPreserved(t *testing.T) {
	snap := emptySnap()
	snap.SS = []scanner.SSEntry{
		{Port: 8080, PID: 100, ProcessName: "nginx"},
	}

	c := newChecker(snap, nil)
	// Request ports in a deliberate non-sequential order.
	ports := []int{9000, 8080, 8500}
	result, err := c.CheckPorts(context.Background(), ports)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Results) != len(ports) {
		t.Fatalf("want %d results, got %d", len(ports), len(result.Results))
	}

	for i, want := range ports {
		if result.Results[i].Port != want {
			t.Errorf("Result[%d].Port: want %d, got %d", i, want, result.Results[i].Port)
		}
	}

	// Spot-check states.
	assertState(t, result.Results[0], PortFree)    // 9000
	assertState(t, result.Results[1], PortUsedProcess) // 8080
	assertState(t, result.Results[2], PortFree)    // 8500
}

// ---------------------------------------------------------------------------
// Test 10: SnapshotProvider error propagates
// ---------------------------------------------------------------------------

func TestCheckPorts_SnapshotProviderError(t *testing.T) {
	scanErr := errors.New("scanner: ss -tlnp exited with status 1")

	c := &Checker{
		Snap:     stubSnap{err: scanErr},
		Reserves: stubReserves{byPort: nil},
	}

	_, err := c.CheckPorts(context.Background(), []int{8080})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, scanErr) {
		t.Errorf("want scanErr, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

func assertPort(t *testing.T, ps PortStatus, want int) {
	t.Helper()
	if ps.Port != want {
		t.Errorf("Port: want %d, got %d", want, ps.Port)
	}
}

func assertState(t *testing.T, ps PortStatus, want PortState) {
	t.Helper()
	if ps.State != want {
		t.Errorf("State: want %q, got %q", want, ps.State)
	}
}
