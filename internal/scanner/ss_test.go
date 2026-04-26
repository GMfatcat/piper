package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func loadSSFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "ss", name))
	if err != nil {
		t.Fatalf("failed to load fixture %s: %v", name, err)
	}
	return data
}

func TestParseSS(t *testing.T) {
	t.Run("simple", func(t *testing.T) {
		raw := loadSSFixture(t,"simple.txt")
		entries, err := ParseSS(raw)
		if err != nil {
			t.Fatalf("ParseSS returned error: %v", err)
		}
		if len(entries) != 4 {
			t.Fatalf("expected 4 entries, got %d", len(entries))
		}

		// First entry: python3 on 0.0.0.0:8080
		e := entries[0]
		if e.Proto != "tcp" {
			t.Errorf("entries[0].Proto = %q, want %q", e.Proto, "tcp")
		}
		if e.LocalAddr != "0.0.0.0:8080" {
			t.Errorf("entries[0].LocalAddr = %q, want %q", e.LocalAddr, "0.0.0.0:8080")
		}
		if e.Port != 8080 {
			t.Errorf("entries[0].Port = %d, want %d", e.Port, 8080)
		}
		if e.IsIPv6 {
			t.Errorf("entries[0].IsIPv6 = true, want false")
		}
		if e.PID != 1234 {
			t.Errorf("entries[0].PID = %d, want %d", e.PID, 1234)
		}
		if e.ProcessName != "python3" {
			t.Errorf("entries[0].ProcessName = %q, want %q", e.ProcessName, "python3")
		}

		// Second entry: redis-server on 127.0.0.1:6379
		e = entries[1]
		if e.Port != 6379 {
			t.Errorf("entries[1].Port = %d, want %d", e.Port, 6379)
		}
		if e.LocalAddr != "127.0.0.1:6379" {
			t.Errorf("entries[1].LocalAddr = %q, want %q", e.LocalAddr, "127.0.0.1:6379")
		}
		if e.IsIPv6 {
			t.Errorf("entries[1].IsIPv6 = true, want false")
		}
		if e.PID != 5678 {
			t.Errorf("entries[1].PID = %d, want %d", e.PID, 5678)
		}
		if e.ProcessName != "redis-server" {
			t.Errorf("entries[1].ProcessName = %q, want %q", e.ProcessName, "redis-server")
		}

		// Third entry: node on 0.0.0.0:9090
		e = entries[2]
		if e.Port != 9090 {
			t.Errorf("entries[2].Port = %d, want %d", e.Port, 9090)
		}
		if e.ProcessName != "node" {
			t.Errorf("entries[2].ProcessName = %q, want %q", e.ProcessName, "node")
		}

		// Fourth entry: postgres on 127.0.0.1:5432
		e = entries[3]
		if e.Port != 5432 {
			t.Errorf("entries[3].Port = %d, want %d", e.Port, 5432)
		}
		if e.ProcessName != "postgres" {
			t.Errorf("entries[3].ProcessName = %q, want %q", e.ProcessName, "postgres")
		}
	})

	t.Run("ipv4_v6_same_port", func(t *testing.T) {
		raw := loadSSFixture(t,"ipv4_v6_same_port.txt")
		entries, err := ParseSS(raw)
		if err != nil {
			t.Fatalf("ParseSS returned error: %v", err)
		}
		// Parser must NOT dedup — both entries returned.
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries (no dedup), got %d", len(entries))
		}

		// IPv4 entry
		v4 := entries[0]
		if v4.Port != 8080 {
			t.Errorf("v4.Port = %d, want 8080", v4.Port)
		}
		if v4.IsIPv6 {
			t.Errorf("v4.IsIPv6 = true, want false")
		}
		if v4.LocalAddr != "0.0.0.0:8080" {
			t.Errorf("v4.LocalAddr = %q, want %q", v4.LocalAddr, "0.0.0.0:8080")
		}
		if v4.PID != 12453 {
			t.Errorf("v4.PID = %d, want 12453", v4.PID)
		}

		// IPv6 entry
		v6 := entries[1]
		if v6.Port != 8080 {
			t.Errorf("v6.Port = %d, want 8080", v6.Port)
		}
		if !v6.IsIPv6 {
			t.Errorf("v6.IsIPv6 = false, want true")
		}
		if v6.LocalAddr != "[::]:8080" {
			t.Errorf("v6.LocalAddr = %q, want %q", v6.LocalAddr, "[::]:8080")
		}
		if v6.PID != 12454 {
			t.Errorf("v6.PID = %d, want 12454", v6.PID)
		}
		if v6.ProcessName != "docker-proxy" {
			t.Errorf("v6.ProcessName = %q, want %q", v6.ProcessName, "docker-proxy")
		}
	})

	t.Run("reuseport", func(t *testing.T) {
		raw := loadSSFixture(t,"reuseport.txt")
		entries, err := ParseSS(raw)
		if err != nil {
			t.Fatalf("ParseSS returned error: %v", err)
		}
		// Phase 1: one SSEntry per row, first process taken.
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry (one per row), got %d", len(entries))
		}
		e := entries[0]
		if e.Port != 80 {
			t.Errorf("Port = %d, want 80", e.Port)
		}
		if e.PID != 100 {
			t.Errorf("PID = %d, want 100 (first process in SO_REUSEPORT list)", e.PID)
		}
		if e.ProcessName != "nginx" {
			t.Errorf("ProcessName = %q, want %q", e.ProcessName, "nginx")
		}
	})

	t.Run("no_pid", func(t *testing.T) {
		raw := loadSSFixture(t,"no_pid.txt")
		entries, err := ParseSS(raw)
		if err != nil {
			t.Fatalf("ParseSS returned error: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		e := entries[0]
		if e.Port != 8080 {
			t.Errorf("Port = %d, want 8080", e.Port)
		}
		if e.PID != 0 {
			t.Errorf("PID = %d, want 0 (no users segment)", e.PID)
		}
		if e.ProcessName != "" {
			t.Errorf("ProcessName = %q, want empty string", e.ProcessName)
		}
	})

	t.Run("docker_proxy", func(t *testing.T) {
		raw := loadSSFixture(t,"docker_proxy.txt")
		entries, err := ParseSS(raw)
		if err != nil {
			t.Fatalf("ParseSS returned error: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries (IPv4 + IPv6 docker-proxy pair), got %d", len(entries))
		}

		// Both should be docker-proxy on port 8080.
		for i, e := range entries {
			if e.Port != 8080 {
				t.Errorf("entries[%d].Port = %d, want 8080", i, e.Port)
			}
			if e.ProcessName != "docker-proxy" {
				t.Errorf("entries[%d].ProcessName = %q, want %q", i, e.ProcessName, "docker-proxy")
			}
			if e.Proto != "tcp" {
				t.Errorf("entries[%d].Proto = %q, want %q", i, e.Proto, "tcp")
			}
		}

		// IPv4 first, IPv6 second.
		if entries[0].IsIPv6 {
			t.Errorf("entries[0] should be IPv4 (IsIPv6=false)")
		}
		if !entries[1].IsIPv6 {
			t.Errorf("entries[1] should be IPv6 (IsIPv6=true)")
		}
	})

	t.Run("skip_non_listen_lines", func(t *testing.T) {
		// Ensure header lines, blank lines, comment lines and non-LISTEN rows are skipped.
		raw := []byte(`# this is a comment
ESTAB  0   0   127.0.0.1:12345  127.0.0.1:6379
LISTEN 0   128 0.0.0.0:22       0.0.0.0:*  users:(("sshd",pid=777,fd=3))

TIME-WAIT 0 0  127.0.0.1:44444  127.0.0.1:80
`)
		entries, err := ParseSS(raw)
		if err != nil {
			t.Fatalf("ParseSS returned error: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry (only the LISTEN row), got %d", len(entries))
		}
		if entries[0].Port != 22 {
			t.Errorf("Port = %d, want 22", entries[0].Port)
		}
	})

	t.Run("empty_input", func(t *testing.T) {
		entries, err := ParseSS([]byte{})
		if err != nil {
			t.Fatalf("ParseSS returned error on empty input: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("expected 0 entries on empty input, got %d", len(entries))
		}
	})
}
