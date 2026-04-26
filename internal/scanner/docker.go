package scanner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// DockerEntry represents a single container row from `docker ps -a --format '{{json .}}'`
// or from `docker inspect <container>`.
type DockerEntry struct {
	ID     string     // full container ID (or short — whatever docker emits)
	Name   string     // container name (without leading slash)
	Image  string     // e.g. "translate:v2"
	State  string     // "running" | "exited" | "created" | "paused" | "dead" | "restarting" | "removing"
	Status string     // human text: "Up 3 days", "Exited (0) 2 hours ago"
	Ports  []HostPort // ONLY entries with a non-zero HostPort (i.e. actual -p mappings)
}

// HostPort represents a single host-to-container port binding.
type HostPort struct {
	HostIP        string // "0.0.0.0", "::", "127.0.0.1" — empty string means "all"
	HostPort      int    // the host-side port number
	ContainerPort int    // the in-container port
	Proto         string // "tcp" | "udp"
}

// dockerPSLine is the JSON shape emitted by `docker ps -a --format '{{json .}}'`.
// Each line is a separate JSON object.
type dockerPSLine struct {
	ID     string `json:"ID"`
	Names  string `json:"Names"`
	Image  string `json:"Image"`
	State  string `json:"State"`
	Status string `json:"Status"`
	Ports  string `json:"Ports"`
}

// ParseDockerPS parses `docker ps -a --format '{{json .}}'` output.
// Each line is a separate JSON object. The relevant fields per line are:
//
//	{"ID": "abc123", "Names": "ai-translate-server", "Image": "translate:v2",
//	 "State": "running", "Status": "Up 3 days",
//	 "Ports": "0.0.0.0:8080->80/tcp, 0.0.0.0:8443->443/tcp, 9090/tcp"}
//
// The "Ports" field is a STRING (not JSON) in `docker ps` format. Parse it.
// Entries without a `->` (i.e. just `9090/tcp` = pure EXPOSE) are dropped per
// design §7.4: "只解析有 HostPort 綁定的條目".
func ParseDockerPS(raw []byte) ([]DockerEntry, error) {
	var entries []DockerEntry

	sc := bufio.NewScanner(bytes.NewReader(raw))
	lineNum := 0
	for sc.Scan() {
		line := sc.Text()
		lineNum++

		// Skip blank lines and comment lines.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		var row dockerPSLine
		if err := json.Unmarshal([]byte(trimmed), &row); err != nil {
			return nil, fmt.Errorf("ParseDockerPS: invalid JSON on line %d: %w", lineNum, err)
		}

		ports := parseDockerPSPortsString(row.Ports)

		entry := DockerEntry{
			ID:     row.ID,
			Name:   strings.TrimPrefix(row.Names, "/"),
			Image:  row.Image,
			State:  row.State,
			Status: row.Status,
			Ports:  ports,
		}
		entries = append(entries, entry)
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ParseDockerPS: scanner error: %w", err)
	}

	if entries == nil {
		entries = []DockerEntry{}
	}
	return entries, nil
}

// parseDockerPSPortsString parses the comma-separated Ports string from
// `docker ps` output into a slice of HostPort.
//
// Each token is one of:
//   - "0.0.0.0:8080->80/tcp"   — IPv4 bound
//   - "[::]:8080->80/tcp"      — IPv6 bound (brackets stripped from HostIP)
//   - "127.0.0.1:9000->9000/tcp" — loopback bound
//   - "9090/tcp"               — pure EXPOSE (no "->") — DROPPED
//
// Tokens that cannot be parsed are silently skipped.
func parseDockerPSPortsString(s string) []HostPort {
	ports := make([]HostPort, 0)
	if s == "" {
		return ports
	}

	for tok := range strings.SplitSeq(s, ", ") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		// Only parse tokens that contain "->".
		if !strings.Contains(tok, "->") {
			// Pure EXPOSE — drop per design §7.4.
			continue
		}
		hp, ok := parseDockerPSPortToken(tok)
		if !ok {
			continue
		}
		ports = append(ports, hp)
	}
	return ports
}

// parseDockerPSPortToken parses a single port token of the form:
//
//	[IP]:hostPort->containerPort/proto
//
// for both IPv4 ("0.0.0.0:8080->80/tcp") and IPv6 ("[::]:8080->80/tcp").
func parseDockerPSPortToken(tok string) (HostPort, bool) {
	// Split on "->": left=hostSpec, right=containerPortProto
	arrowIdx := strings.Index(tok, "->")
	if arrowIdx < 0 {
		return HostPort{}, false
	}
	hostSpec := tok[:arrowIdx]
	containerSpec := tok[arrowIdx+2:]

	// Parse containerPort/proto
	containerPort, proto, ok := parsePortProtoStr(containerSpec)
	if !ok {
		return HostPort{}, false
	}

	// Parse hostSpec: could be "[::]:8080", "0.0.0.0:8080", "127.0.0.1:9000"
	hostIP, hostPortNum, ok := parseHostSpec(hostSpec)
	if !ok {
		return HostPort{}, false
	}

	return HostPort{
		HostIP:        hostIP,
		HostPort:      hostPortNum,
		ContainerPort: containerPort,
		Proto:         proto,
	}, true
}

// parseHostSpec parses the host-side portion of a docker port token.
// Handles both IPv4 "1.2.3.4:8080" and IPv6 "[::]:8080" forms.
// Returns (hostIP, hostPort, ok).
func parseHostSpec(spec string) (string, int, bool) {
	if strings.HasPrefix(spec, "[") {
		// IPv6: "[::1]:8080" or "[::]:8080"
		closeBracket := strings.LastIndex(spec, "]")
		if closeBracket < 0 {
			return "", 0, false
		}
		ip := spec[1:closeBracket] // strip "[" and "]"
		rest := spec[closeBracket+1:]
		if !strings.HasPrefix(rest, ":") {
			return "", 0, false
		}
		portStr := rest[1:]
		p, err := strconv.Atoi(portStr)
		if err != nil || p <= 0 {
			return "", 0, false
		}
		return ip, p, true
	}

	// IPv4: last colon separates IP from port
	lastColon := strings.LastIndex(spec, ":")
	if lastColon < 0 {
		return "", 0, false
	}
	ip := spec[:lastColon]
	portStr := spec[lastColon+1:]
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 {
		return "", 0, false
	}
	return ip, p, true
}

// parsePortProtoStr parses "80/tcp" or "9200/udp" into (port, proto, ok).
func parsePortProtoStr(s string) (int, string, bool) {
	slashIdx := strings.LastIndex(s, "/")
	if slashIdx < 0 {
		// No slash — try bare number
		p, err := strconv.Atoi(s)
		if err != nil || p <= 0 {
			return 0, "", false
		}
		return p, "", true
	}
	portStr := s[:slashIdx]
	proto := strings.ToLower(s[slashIdx+1:])
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 {
		return 0, "", false
	}
	return p, proto, true
}

// ---------------------------------------------------------------------------
// ParseDockerInspect
// ---------------------------------------------------------------------------

// dockerInspectContainer is the top-level shape of a single container in
// `docker inspect <container>` output (which is a JSON array).
type dockerInspectContainer struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
	State struct {
		Status string `json:"Status"`
	} `json:"State"`
	NetworkSettings struct {
		// Ports is a map of "containerPort/proto" -> array of host bindings (or null when unbound).
		Ports map[string]json.RawMessage `json:"Ports"`
	} `json:"NetworkSettings"`
}

// dockerInspectBinding is a single element in the host-binding array.
type dockerInspectBinding struct {
	HostIp   string `json:"HostIp"`
	HostPort string `json:"HostPort"` // string in docker inspect output
}

// ParseDockerInspect parses the JSON output of `docker inspect <container>` (an array
// of one container object). Reads `NetworkSettings.Ports`, which is a map of
//
//	"80/tcp" -> [ {"HostIp": "0.0.0.0", "HostPort": "8080"}, ... ]  // null when not bound
//
// Returns the same DockerEntry shape as ParseDockerPS, but with full port info.
// Used for the "expand all ports of this container" feature (design §4.5).
func ParseDockerInspect(raw []byte) (DockerEntry, error) {
	var containers []dockerInspectContainer
	if err := json.Unmarshal(raw, &containers); err != nil {
		return DockerEntry{}, fmt.Errorf("ParseDockerInspect: invalid JSON: %w", err)
	}

	if len(containers) == 0 {
		return DockerEntry{}, fmt.Errorf("ParseDockerInspect: input array is empty, expected exactly one container")
	}
	if len(containers) > 1 {
		return DockerEntry{}, fmt.Errorf("ParseDockerInspect: input array has %d elements, expected exactly one", len(containers))
	}

	c := containers[0]

	ports := make([]HostPort, 0)
	for portKey, bindingsRaw := range c.NetworkSettings.Ports {
		// portKey is "80/tcp" or "9090/tcp" etc.
		containerPort, proto, ok := parsePortProtoStr(portKey)
		if !ok {
			continue
		}

		// Decode the binding array (null means unbound).
		if bindingsRaw == nil || string(bindingsRaw) == "null" {
			// Unbound (pure EXPOSE) — skip per design §7.4.
			continue
		}

		var bindings []dockerInspectBinding
		if err := json.Unmarshal(bindingsRaw, &bindings); err != nil {
			// Unexpected format — skip this entry.
			continue
		}

		for _, b := range bindings {
			hostPortNum, err := strconv.Atoi(b.HostPort)
			if err != nil || hostPortNum <= 0 {
				continue
			}
			// Strip brackets from IPv6 HostIp if present.
			hostIP := stripIPv6Brackets(b.HostIp)
			ports = append(ports, HostPort{
				HostIP:        hostIP,
				HostPort:      hostPortNum,
				ContainerPort: containerPort,
				Proto:         proto,
			})
		}
	}

	entry := DockerEntry{
		ID:    c.ID,
		Name:  strings.TrimPrefix(c.Name, "/"),
		Image: c.Config.Image,
		State: c.State.Status,
		// docker inspect does not expose a Status string like docker ps does;
		// leave Status empty — callers that need it should use docker ps output.
		Status: "",
		Ports:  ports,
	}
	return entry, nil
}

// stripIPv6Brackets removes surrounding "[" and "]" from an IPv6 address
// if present (e.g. "[::1]" → "::1"). Pass-through for IPv4 strings.
func stripIPv6Brackets(ip string) string {
	if strings.HasPrefix(ip, "[") && strings.HasSuffix(ip, "]") {
		return ip[1 : len(ip)-1]
	}
	return ip
}
