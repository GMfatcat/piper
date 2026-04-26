package scanner

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// UFWRule represents a single rule from `ufw status numbered`.
type UFWRule struct {
	RuleNum int    // [N] in `ufw status numbered`
	Port    int    // 0 when port cannot be extracted (complex rule)
	Proto   string // "tcp" | "udp" | "" for complex rules
	Action  string // "allow" | "deny" | "limit" | "complex"
}

// ParseUFWStatus parses `ufw status numbered` stdout.
// Returns empty slice (not nil) and no error when status is "inactive".
// Lines starting with '#' are skipped (fixture header support).
// Rules that don't match the simple `PORT/PROTO ACTION IN Anywhere` form
// are emitted with Action="complex" and Port=0 if port can't be extracted.
func ParseUFWStatus(raw []byte) ([]UFWRule, error) {
	rules := make([]UFWRule, 0)

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	active := false
	statusSeen := false

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Skip comment lines (fixture headers)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Detect status line
		if strings.HasPrefix(trimmed, "Status:") {
			statusStr := strings.TrimSpace(strings.TrimPrefix(trimmed, "Status:"))
			statusSeen = true
			if statusStr == "inactive" {
				return rules, nil
			}
			if statusStr == "active" {
				active = true
			}
			continue
		}

		if !active || !statusSeen {
			continue
		}

		// Rule lines look like: [ 1] 22/tcp   ALLOW IN   Anywhere
		// They start with '[' after trimming.
		if !strings.HasPrefix(trimmed, "[") {
			continue
		}

		rule, err := parseRuleLine(trimmed)
		if err != nil {
			// Malformed rule line — skip silently
			continue
		}
		rules = append(rules, rule)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ParseUFWStatus: scanner error: %w", err)
	}

	return rules, nil
}

// parseRuleLine parses a single numbered rule line such as:
//
//	[ 1] 22/tcp                     ALLOW IN    Anywhere
//	[ 3] 8080/tcp                   ALLOW IN    192.168.1.0/24
//	[ 4] 9000 on eth0               ALLOW IN    Anywhere
//	[ 6] Nginx Full                  ALLOW IN    Anywhere
func parseRuleLine(line string) (UFWRule, error) {
	// Extract rule number from leading [N]
	closeBracket := strings.Index(line, "]")
	if closeBracket < 0 {
		return UFWRule{}, fmt.Errorf("no closing bracket in: %q", line)
	}
	numStr := strings.TrimSpace(line[1:closeBracket])
	ruleNum, err := strconv.Atoi(numStr)
	if err != nil {
		return UFWRule{}, fmt.Errorf("invalid rule number %q: %w", numStr, err)
	}

	// Remainder after ']'
	rest := strings.TrimSpace(line[closeBracket+1:])

	// Split into fields — UFW uses at least two spaces between columns.
	// We can split on runs of whitespace; the "To" field may contain spaces
	// (e.g. "Nginx Full"), so we look for the action keyword to find the boundary.
	fields := splitFields(rest)
	if len(fields) < 3 {
		return UFWRule{}, fmt.Errorf("too few fields in: %q", rest)
	}

	// Find action index: look for ALLOW, DENY, LIMIT (case-insensitive).
	actionIdx := -1
	var rawAction string
	for i, f := range fields {
		upper := strings.ToUpper(f)
		if upper == "ALLOW" || upper == "DENY" || upper == "LIMIT" {
			actionIdx = i
			rawAction = upper
			break
		}
	}
	if actionIdx < 0 {
		return UFWRule{}, fmt.Errorf("no action keyword found in: %q", rest)
	}

	// "To" portion is everything before the action keyword.
	toFields := fields[:actionIdx]
	toStr := strings.Join(toFields, " ")

	// "From" portion is everything after "ACTION IN" (skip "IN" token).
	// After actionIdx: fields[actionIdx] = action, fields[actionIdx+1] = "IN" (usually)
	fromStr := ""
	if actionIdx+2 < len(fields) {
		fromStr = strings.Join(fields[actionIdx+2:], " ")
	} else if actionIdx+1 < len(fields) {
		// No "IN" token present — treat everything after action as from
		fromStr = strings.Join(fields[actionIdx+1:], " ")
	}

	action := strings.ToLower(rawAction)

	// Determine if this is a simple rule: To must be PORT/PROTO (or PORT/PROTO (v6))
	// and From must be "Anywhere" or "Anywhere (v6)".
	port, proto, isSimpleTo := parseToField(toStr)
	isSimpleFrom := isAnywhereFrom(fromStr)

	if !isSimpleTo || !isSimpleFrom {
		// Complex rule — try best-effort port extraction from toStr
		return UFWRule{
			RuleNum: ruleNum,
			Port:    port,  // may be 0
			Proto:   proto, // may be ""
			Action:  "complex",
		}, nil
	}

	return UFWRule{
		RuleNum: ruleNum,
		Port:    port,
		Proto:   proto,
		Action:  action,
	}, nil
}

// splitFields splits a string on runs of whitespace.
func splitFields(s string) []string {
	return strings.Fields(s)
}

// parseToField attempts to parse the "To" column of a UFW rule.
// Simple form: "PORT/PROTO" or "PORT/PROTO (v6)".
// Returns (port, proto, true) on success, (0, "", false) on failure.
// For interface-bound rules like "9000 on eth0", returns (9000, "", false)
// since that's still complex (interface-bound).
func parseToField(to string) (port int, proto string, simple bool) {
	// Strip trailing " (v6)" suffix — v6 rules are still parseable
	stripped := strings.TrimSuffix(to, " (v6)")
	stripped = strings.TrimSpace(stripped)

	// Check for "on <iface>" pattern — complex
	if idx := strings.Index(strings.ToLower(stripped), " on "); idx >= 0 {
		// Try to parse the port portion before " on "
		portProto := strings.TrimSpace(stripped[:idx])
		p, pr := parsePortProto(portProto)
		return p, pr, false // always complex
	}

	// Simple: must match PORT/PROTO exactly
	p, pr := parsePortProto(stripped)
	if p == 0 {
		return 0, "", false
	}
	return p, pr, true
}

// parsePortProto parses "PORT/PROTO" returning (port, proto).
// If the string has no '/', treats it as a bare port number with no proto.
// Returns (0, "") on parse failure.
func parsePortProto(s string) (int, string) {
	if idx := strings.Index(s, "/"); idx >= 0 {
		portStr := s[:idx]
		proto := strings.ToLower(s[idx+1:])
		p, err := strconv.Atoi(portStr)
		if err != nil || p <= 0 {
			return 0, ""
		}
		return p, proto
	}
	// Bare number (no proto)
	p, err := strconv.Atoi(s)
	if err != nil || p <= 0 {
		return 0, ""
	}
	return p, ""
}

// isAnywhereFrom returns true if the From column represents the simple
// "Anywhere" or "Anywhere (v6)" (case-insensitive).
func isAnywhereFrom(from string) bool {
	lower := strings.ToLower(strings.TrimSpace(from))
	return lower == "anywhere" || lower == "anywhere (v6)"
}
