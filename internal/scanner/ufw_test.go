package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func loadUFWFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "ufw", name))
	if err != nil {
		t.Fatalf("failed to load fixture %s: %v", name, err)
	}
	return data
}

func TestParseUFW_ActiveSimple(t *testing.T) {
	raw := loadUFWFixture(t, "active_simple.txt")
	rules, err := ParseUFWStatus(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect 5 rules
	if len(rules) != 5 {
		t.Fatalf("expected 5 rules, got %d: %+v", len(rules), rules)
	}

	tests := []struct {
		name    string
		ruleNum int
		port    int
		proto   string
		action  string
	}{
		{"rule1-ssh-tcp", 1, 22, "tcp", "allow"},
		{"rule2-8080-tcp", 2, 8080, "tcp", "allow"},
		{"rule3-7878-tcp", 3, 7878, "tcp", "allow"},
		{"rule4-9000-udp", 4, 9000, "udp", "allow"},
		{"rule5-9100-deny", 5, 9100, "tcp", "deny"},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := rules[i]
			if r.RuleNum != tt.ruleNum {
				t.Errorf("RuleNum: got %d, want %d", r.RuleNum, tt.ruleNum)
			}
			if r.Port != tt.port {
				t.Errorf("Port: got %d, want %d", r.Port, tt.port)
			}
			if r.Proto != tt.proto {
				t.Errorf("Proto: got %q, want %q", r.Proto, tt.proto)
			}
			if r.Action != tt.action {
				t.Errorf("Action: got %q, want %q", r.Action, tt.action)
			}
		})
	}
}

func TestParseUFW_Inactive(t *testing.T) {
	raw := loadUFWFixture(t, "inactive.txt")
	rules, err := ParseUFWStatus(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules == nil {
		t.Fatal("expected empty slice, got nil")
	}
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules for inactive status, got %d: %+v", len(rules), rules)
	}
}

func TestParseUFW_ComplexRules(t *testing.T) {
	raw := loadUFWFixture(t, "complex_rules.txt")
	rules, err := ParseUFWStatus(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Fixture has 7 bracketed rule lines:
	// [1] 22/tcp ALLOW IN Anywhere  -> simple allow
	// [2] 8080/tcp ALLOW IN Anywhere -> simple allow
	// [3] 8080/tcp ALLOW IN 192.168.1.0/24 -> complex (not Anywhere)
	// [4] 9000 on eth0 ALLOW IN Anywhere -> complex (interface-bound)
	// [5] 443/tcp DENY IN Anywhere -> simple deny
	// [6] Nginx Full ALLOW IN Anywhere -> complex (named service, no port/proto)
	// [7] 9090/udp LIMIT IN Anywhere -> simple limit
	if len(rules) != 7 {
		t.Fatalf("expected 7 rules, got %d: %+v", len(rules), rules)
	}

	tests := []struct {
		name    string
		ruleNum int
		action  string
		port    int    // 0 means port couldn't be extracted (complex)
		proto   string // "" means complex/unknown
	}{
		{"rule1-simple-allow", 1, "allow", 22, "tcp"},
		{"rule2-simple-allow-8080", 2, "allow", 8080, "tcp"},
		{"rule3-complex-from-ip", 3, "complex", 8080, "tcp"},
		{"rule4-complex-interface", 4, "complex", 9000, ""},
		{"rule5-simple-deny-443", 5, "deny", 443, "tcp"},
		{"rule6-complex-named-service", 6, "complex", 0, ""},
		{"rule7-simple-limit", 7, "limit", 9090, "udp"},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := rules[i]
			if r.RuleNum != tt.ruleNum {
				t.Errorf("RuleNum: got %d, want %d", r.RuleNum, tt.ruleNum)
			}
			if r.Action != tt.action {
				t.Errorf("Action: got %q, want %q", r.Action, tt.action)
			}
			if r.Port != tt.port {
				t.Errorf("Port: got %d, want %d", r.Port, tt.port)
			}
			if tt.proto != "" && r.Proto != tt.proto {
				t.Errorf("Proto: got %q, want %q", r.Proto, tt.proto)
			}
		})
	}
}

func TestParseUFW_V6Duplicate(t *testing.T) {
	raw := loadUFWFixture(t, "v6_duplicate.txt")
	rules, err := ParseUFWStatus(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 6 bracketed lines: 3 v4 + 3 v6 — parser emits ALL, no dedup
	if len(rules) != 6 {
		t.Fatalf("expected 6 rules (3 v4 + 3 v6), got %d: %+v", len(rules), rules)
	}

	// First 3 are v4
	v4Ports := []int{22, 8080, 7878}
	for i, port := range v4Ports {
		t.Run("v4", func(t *testing.T) {
			if rules[i].Port != port {
				t.Errorf("v4 rule[%d] Port: got %d, want %d", i, rules[i].Port, port)
			}
			if rules[i].Proto != "tcp" {
				t.Errorf("v4 rule[%d] Proto: got %q, want tcp", i, rules[i].Proto)
			}
			if rules[i].Action != "allow" {
				t.Errorf("v4 rule[%d] Action: got %q, want allow", i, rules[i].Action)
			}
		})
	}

	// Last 3 are v6 — same ports, parser must NOT silently drop them
	for i, port := range v4Ports {
		idx := i + 3
		t.Run("v6", func(t *testing.T) {
			if rules[idx].Port != port {
				t.Errorf("v6 rule[%d] Port: got %d, want %d", idx, rules[idx].Port, port)
			}
			if rules[idx].Proto != "tcp" {
				t.Errorf("v6 rule[%d] Proto: got %q, want tcp", idx, rules[idx].Proto)
			}
			if rules[idx].Action != "allow" {
				t.Errorf("v6 rule[%d] Action: got %q, want allow", idx, rules[idx].Action)
			}
		})
	}
}

// TestParseUFW_CommentLines verifies that lines starting with '#' are skipped
// (fixture header lines must not interfere with parsing).
func TestParseUFW_CommentLines(t *testing.T) {
	raw := []byte("# this is a comment\nStatus: inactive\n")
	rules, err := ParseUFWStatus(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected 0 rules, got %d", len(rules))
	}
}
