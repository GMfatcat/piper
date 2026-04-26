package scanner

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// SSEntry represents a single TCP listener row from `ss -tlnp -H`.
type SSEntry struct {
	Proto       string // "tcp" (Phase 1 only handles tcp listeners)
	LocalAddr   string // raw local addr text, e.g. "0.0.0.0:8080" or "[::]:8080"
	Port        int    // extracted from LocalAddr
	IsIPv6      bool   // true if LocalAddr was an IPv6 form, e.g. "[::]:8080"
	PID         int    // 0 if not present
	ProcessName string // e.g. "docker-proxy", "python3"; empty if no users segment
}

// ParseSS parses `ss -tlnp -H` stdout.
// Header is suppressed by -H, so every non-empty, non-`#` line is a row.
//
// SO_REUSEPORT: the same port can appear multiple times with different PIDs.
// We emit one SSEntry per row (no dedup at parse time — the fusion layer decides).
// When a users:(...) segment lists multiple processes, we take the FIRST one.
// TODO: Phase 2 — expand multi-process SO_REUSEPORT rows into multiple SSEntry items
// and annotate the presentation layer with "(+N more)" per design §13.2.
//
// IPv4/IPv6 dedup is also NOT done here; caller decides (design §13.1).
func ParseSS(raw []byte) ([]SSEntry, error) {
	var entries []SSEntry

	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()

		// Skip blank lines and comment lines (fixture header).
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Only process LISTEN rows.
		if !strings.HasPrefix(line, "LISTEN") {
			continue
		}

		// Fields are whitespace-separated. Minimum meaningful columns:
		//   [0] State      "LISTEN"
		//   [1] Recv-Q     e.g. "0"
		//   [2] Send-Q     e.g. "128"
		//   [3] Local Addr e.g. "0.0.0.0:8080" or "[::]:8080"
		//   [4] Peer Addr  e.g. "0.0.0.0:*" or "[::]:*"
		//   [5] (optional) users:(...) segment
		fields := strings.Fields(line)
		if len(fields) < 5 {
			// Malformed line — skip silently.
			continue
		}

		localAddr := fields[3]
		port, isIPv6, ok := parseLocalAddr(localAddr)
		if !ok {
			// Cannot parse port — skip silently.
			continue
		}

		entry := SSEntry{
			Proto:     "tcp",
			LocalAddr: localAddr,
			Port:      port,
			IsIPv6:    isIPv6,
		}

		// Parse optional users:(...) segment — may be in field [5] or absent.
		if len(fields) >= 6 {
			usersField := fields[5]
			if strings.HasPrefix(usersField, "users:(") {
				name, pid := parseUsersFirst(usersField)
				entry.ProcessName = name
				entry.PID = pid
			}
		}

		entries = append(entries, entry)
	}

	if err := sc.Err(); err != nil {
		return nil, err
	}

	return entries, nil
}

// parseLocalAddr extracts the port number and IPv6 flag from a local address
// string of the form "1.2.3.4:PORT" (IPv4) or "[addr]:PORT" (IPv6).
func parseLocalAddr(addr string) (port int, isIPv6 bool, ok bool) {
	if strings.HasPrefix(addr, "[") {
		// IPv6 form: "[::]:8080" or "[::1]:8080"
		closeBracket := strings.LastIndex(addr, "]")
		if closeBracket < 0 {
			return 0, false, false
		}
		// After "]" there must be ":PORT"
		rest := addr[closeBracket+1:]
		if !strings.HasPrefix(rest, ":") {
			return 0, false, false
		}
		portStr := rest[1:]
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return 0, false, false
		}
		return p, true, true
	}

	// IPv4 form: "1.2.3.4:PORT" — find last colon.
	lastColon := strings.LastIndex(addr, ":")
	if lastColon < 0 {
		return 0, false, false
	}
	portStr := addr[lastColon+1:]
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, false, false
	}
	return p, false, true
}

// parseUsersFirst extracts the process name and PID of the first process entry
// in a users:(...) segment. The segment looks like:
//
//	users:(("nginx",pid=100,fd=6),("nginx",pid=101,fd=6))
//
// For SO_REUSEPORT with multiple entries we take only the first.
// Returns ("", 0) if the segment cannot be parsed.
func parseUsersFirst(usersField string) (name string, pid int) {
	// Strip "users:((" prefix and find first complete process entry.
	// We look for the pattern: ("procname",pid=N,fd=M)
	//
	// Locate the first opening paren of the entry list, then the inner "(".
	// users:((  "procname"  ,pid=N,fd=M),...))
	//        ^^ start here
	const prefix = `users:((`
	if !strings.HasPrefix(usersField, prefix) {
		return "", 0
	}
	inner := usersField[len(prefix):]

	// inner now starts at: "procname",pid=N,fd=M),...))
	// Extract process name: between the leading `"` and the next `"`.
	if !strings.HasPrefix(inner, `"`) {
		return "", 0
	}
	inner = inner[1:] // skip leading "
	nameEnd := strings.Index(inner, `"`)
	if nameEnd < 0 {
		return "", 0
	}
	name = inner[:nameEnd]
	rest := inner[nameEnd+1:] // rest starts with ",pid=N,fd=M)..."

	// Extract pid value.
	const pidPrefix = ",pid="
	pidIdx := strings.Index(rest, pidPrefix)
	if pidIdx < 0 {
		// Name found but no PID — return name with pid=0.
		return name, 0
	}
	rest = rest[pidIdx+len(pidPrefix):]
	// rest now starts with the numeric PID, followed by ",fd=..."
	commaOrEnd := strings.IndexAny(rest, ",)")
	if commaOrEnd < 0 {
		commaOrEnd = len(rest)
	}
	pidStr := rest[:commaOrEnd]
	p, err := strconv.Atoi(pidStr)
	if err != nil {
		return name, 0
	}
	return name, p
}
