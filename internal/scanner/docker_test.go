package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

// loadDockerFixture reads a fixture file from testdata/docker/.
func loadDockerFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "docker", name))
	if err != nil {
		t.Fatalf("failed to load fixture %s: %v", name, err)
	}
	return data
}

// ---------------------------------------------------------------------------
// ParseDockerPS tests
// ---------------------------------------------------------------------------

func TestParseDockerPS(t *testing.T) {
	t.Run("running_with_ports", func(t *testing.T) {
		raw := loadDockerFixture(t, "ps_running_with_ports.txt")
		entries, err := ParseDockerPS(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(entries))
		}

		// First container: ai-translate-server
		e := entries[0]
		if e.Name != "ai-translate-server" {
			t.Errorf("Name: got %q, want %q", e.Name, "ai-translate-server")
		}
		if e.Image != "translate:v2" {
			t.Errorf("Image: got %q, want %q", e.Image, "translate:v2")
		}
		if e.State != "running" {
			t.Errorf("State: got %q, want %q", e.State, "running")
		}
		if e.Status != "Up 3 days" {
			t.Errorf("Status: got %q, want %q", e.Status, "Up 3 days")
		}
		if len(e.Ports) != 2 {
			t.Fatalf("entries[0]: expected 2 Ports, got %d: %+v", len(e.Ports), e.Ports)
		}
		// Port 0: 0.0.0.0:8080->80/tcp
		p0 := e.Ports[0]
		if p0.HostIP != "0.0.0.0" {
			t.Errorf("Ports[0].HostIP: got %q, want %q", p0.HostIP, "0.0.0.0")
		}
		if p0.HostPort != 8080 {
			t.Errorf("Ports[0].HostPort: got %d, want %d", p0.HostPort, 8080)
		}
		if p0.ContainerPort != 80 {
			t.Errorf("Ports[0].ContainerPort: got %d, want %d", p0.ContainerPort, 80)
		}
		if p0.Proto != "tcp" {
			t.Errorf("Ports[0].Proto: got %q, want %q", p0.Proto, "tcp")
		}
		// Port 1: 0.0.0.0:8443->443/tcp
		p1 := e.Ports[1]
		if p1.HostPort != 8443 {
			t.Errorf("Ports[1].HostPort: got %d, want %d", p1.HostPort, 8443)
		}
		if p1.ContainerPort != 443 {
			t.Errorf("Ports[1].ContainerPort: got %d, want %d", p1.ContainerPort, 443)
		}
		if p1.Proto != "tcp" {
			t.Errorf("Ports[1].Proto: got %q, want %q", p1.Proto, "tcp")
		}

		// Second container: metrics-collector with UDP
		e2 := entries[1]
		if e2.Name != "metrics-collector" {
			t.Errorf("entries[1].Name: got %q, want %q", e2.Name, "metrics-collector")
		}
		if len(e2.Ports) != 2 {
			t.Fatalf("entries[1]: expected 2 Ports, got %d: %+v", len(e2.Ports), e2.Ports)
		}
		// TCP port
		if e2.Ports[0].Proto != "tcp" {
			t.Errorf("entries[1].Ports[0].Proto: got %q, want tcp", e2.Ports[0].Proto)
		}
		if e2.Ports[0].HostPort != 9100 {
			t.Errorf("entries[1].Ports[0].HostPort: got %d, want 9100", e2.Ports[0].HostPort)
		}
		// UDP port
		if e2.Ports[1].Proto != "udp" {
			t.Errorf("entries[1].Ports[1].Proto: got %q, want udp", e2.Ports[1].Proto)
		}
		if e2.Ports[1].HostPort != 9200 {
			t.Errorf("entries[1].Ports[1].HostPort: got %d, want 9200", e2.Ports[1].HostPort)
		}
	})

	t.Run("stopped_container", func(t *testing.T) {
		raw := loadDockerFixture(t, "ps_stopped.txt")
		entries, err := ParseDockerPS(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		e := entries[0]
		if e.Name != "subtitle-server" {
			t.Errorf("Name: got %q, want %q", e.Name, "subtitle-server")
		}
		if e.State != "exited" {
			t.Errorf("State: got %q, want %q", e.State, "exited")
		}
		if len(e.Ports) != 1 {
			t.Fatalf("expected 1 Port, got %d: %+v", len(e.Ports), e.Ports)
		}
		if e.Ports[0].HostPort != 8081 {
			t.Errorf("Ports[0].HostPort: got %d, want 8081", e.Ports[0].HostPort)
		}
	})

	t.Run("no_ports_empty_field", func(t *testing.T) {
		// Container with empty Ports field must still produce a DockerEntry
		// with an empty (not nil) Ports slice.
		raw := loadDockerFixture(t, "ps_no_ports.txt")
		entries, err := ParseDockerPS(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry (container not filtered out), got %d", len(entries))
		}
		e := entries[0]
		if e.Name != "internal-worker" {
			t.Errorf("Name: got %q, want %q", e.Name, "internal-worker")
		}
		if e.Ports == nil {
			t.Errorf("Ports must be non-nil empty slice, got nil")
		}
		if len(e.Ports) != 0 {
			t.Errorf("expected 0 Ports, got %d: %+v", len(e.Ports), e.Ports)
		}
	})

	t.Run("expose_only_no_host_binding", func(t *testing.T) {
		// Pure EXPOSE (no ->) must be dropped from Ports; container still returned.
		raw := loadDockerFixture(t, "ps_expose_only.txt")
		entries, err := ParseDockerPS(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		e := entries[0]
		if e.Name != "expose-only-service" {
			t.Errorf("Name: got %q, want %q", e.Name, "expose-only-service")
		}
		if len(e.Ports) != 0 {
			t.Errorf("expected 0 Ports (EXPOSE-only dropped), got %d: %+v", len(e.Ports), e.Ports)
		}
	})

	t.Run("mixed_bound_and_expose", func(t *testing.T) {
		// Mixed: bound IPv4, EXPOSE-only (dropped), bound IPv6.
		raw := loadDockerFixture(t, "ps_mixed.txt")
		entries, err := ParseDockerPS(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		e := entries[0]
		if e.Name != "mixed-service" {
			t.Errorf("Name: got %q, want %q", e.Name, "mixed-service")
		}
		// Must have exactly 2 bound ports (IPv4 and IPv6); EXPOSE-only dropped.
		if len(e.Ports) != 2 {
			t.Fatalf("expected 2 Ports (1 IPv4 bound + 1 IPv6 bound), got %d: %+v", len(e.Ports), e.Ports)
		}

		// IPv4 entry: HostIP="0.0.0.0", HostPort=8080, ContainerPort=80, Proto="tcp"
		ipv4 := e.Ports[0]
		if ipv4.HostIP != "0.0.0.0" {
			t.Errorf("Ports[0].HostIP: got %q, want %q", ipv4.HostIP, "0.0.0.0")
		}
		if ipv4.HostPort != 8080 {
			t.Errorf("Ports[0].HostPort: got %d, want 8080", ipv4.HostPort)
		}
		if ipv4.ContainerPort != 80 {
			t.Errorf("Ports[0].ContainerPort: got %d, want 80", ipv4.ContainerPort)
		}
		if ipv4.Proto != "tcp" {
			t.Errorf("Ports[0].Proto: got %q, want tcp", ipv4.Proto)
		}

		// IPv6 entry: HostIP="::" (brackets stripped), HostPort=8080, ContainerPort=80, Proto="tcp"
		ipv6 := e.Ports[1]
		if ipv6.HostIP != "::" {
			t.Errorf("Ports[1].HostIP: got %q, want %q (brackets must be stripped)", ipv6.HostIP, "::")
		}
		if ipv6.HostPort != 8080 {
			t.Errorf("Ports[1].HostPort: got %d, want 8080", ipv6.HostPort)
		}
		if ipv6.Proto != "tcp" {
			t.Errorf("Ports[1].Proto: got %q, want tcp", ipv6.Proto)
		}
	})

	t.Run("empty_input", func(t *testing.T) {
		entries, err := ParseDockerPS([]byte{})
		if err != nil {
			t.Fatalf("ParseDockerPS returned error on empty input: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("expected 0 entries on empty input, got %d", len(entries))
		}
	})

	t.Run("invalid_json_line", func(t *testing.T) {
		raw := []byte(`{"ID":"abc","Names":"good","Image":"img","State":"running","Status":"Up","Ports":""}
{not valid json}
`)
		_, err := ParseDockerPS(raw)
		if err == nil {
			t.Fatal("expected error for invalid JSON line, got nil")
		}
	})
}

// ---------------------------------------------------------------------------
// ParseDockerInspect tests
// ---------------------------------------------------------------------------

func TestParseDockerInspect(t *testing.T) {
	t.Run("translate_server_full", func(t *testing.T) {
		raw := loadDockerFixture(t, "inspect_translate_server.json")
		entry, err := ParseDockerInspect(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if entry.Name != "ai-translate-server" {
			t.Errorf("Name: got %q, want %q", entry.Name, "ai-translate-server")
		}
		if entry.Image != "translate:v2" {
			t.Errorf("Image: got %q, want %q", entry.Image, "translate:v2")
		}
		if entry.State != "running" {
			t.Errorf("State: got %q, want %q", entry.State, "running")
		}

		// Only the 2 bound ports should appear (9090 and 9091 are null/unbound).
		if len(entry.Ports) != 2 {
			t.Fatalf("expected 2 Ports, got %d: %+v", len(entry.Ports), entry.Ports)
		}

		// Build a map for order-independent assertion.
		portMap := make(map[int]HostPort, len(entry.Ports))
		for _, p := range entry.Ports {
			portMap[p.HostPort] = p
		}

		p8080, ok := portMap[8080]
		if !ok {
			t.Fatal("expected HostPort 8080 in Ports")
		}
		if p8080.HostIP != "0.0.0.0" {
			t.Errorf("p8080.HostIP: got %q, want %q", p8080.HostIP, "0.0.0.0")
		}
		if p8080.ContainerPort != 80 {
			t.Errorf("p8080.ContainerPort: got %d, want 80", p8080.ContainerPort)
		}
		if p8080.Proto != "tcp" {
			t.Errorf("p8080.Proto: got %q, want tcp", p8080.Proto)
		}

		p8443, ok := portMap[8443]
		if !ok {
			t.Fatal("expected HostPort 8443 in Ports")
		}
		if p8443.ContainerPort != 443 {
			t.Errorf("p8443.ContainerPort: got %d, want 443", p8443.ContainerPort)
		}
		if p8443.Proto != "tcp" {
			t.Errorf("p8443.Proto: got %q, want tcp", p8443.Proto)
		}
	})

	t.Run("no_ports", func(t *testing.T) {
		raw := loadDockerFixture(t, "inspect_no_ports.json")
		entry, err := ParseDockerInspect(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if entry.Name != "internal-worker" {
			t.Errorf("Name: got %q, want %q", entry.Name, "internal-worker")
		}
		if entry.Ports == nil {
			t.Errorf("Ports must be non-nil empty slice, got nil")
		}
		if len(entry.Ports) != 0 {
			t.Errorf("expected 0 Ports, got %d: %+v", len(entry.Ports), entry.Ports)
		}
	})

	t.Run("empty_array_error", func(t *testing.T) {
		_, err := ParseDockerInspect([]byte("[]"))
		if err == nil {
			t.Fatal("expected error for empty array, got nil")
		}
	})

	t.Run("multi_element_array_error", func(t *testing.T) {
		// docker inspect always returns exactly one element per container invocation;
		// if someone passes multi-container output, we reject it.
		raw := []byte(`[{"Id":"a","Name":"/foo","Config":{"Image":"img"},"State":{"Status":"running"},"NetworkSettings":{"Ports":{}}},{"Id":"b","Name":"/bar","Config":{"Image":"img2"},"State":{"Status":"running"},"NetworkSettings":{"Ports":{}}}]`)
		_, err := ParseDockerInspect(raw)
		if err == nil {
			t.Fatal("expected error for multi-element array, got nil")
		}
	})

	t.Run("invalid_json_error", func(t *testing.T) {
		_, err := ParseDockerInspect([]byte("{not json}"))
		if err == nil {
			t.Fatal("expected error for invalid JSON, got nil")
		}
	})
}
