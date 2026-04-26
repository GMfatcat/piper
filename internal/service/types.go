// Package service is the sole business-logic layer for Piper.
// It reads from scanner and store, applies the CheckPort decision tree,
// and produces typed results consumed by both CLI and HTTP handlers.
package service

import "time"

// PortState describes the disposition of a single host port.
type PortState string

const (
	// PortFree means no listener, no docker mapping, no reservation.
	PortFree PortState = "free"

	// PortUsedProcess means ss sees a listener whose process is NOT docker-proxy.
	PortUsedProcess PortState = "used_process"

	// PortUsedDocker means a running docker container holds this host port.
	PortUsedDocker PortState = "used_docker"

	// PortReservedImplicit means a stopped/exited docker container has this host
	// port mapped — the container can reclaim it on restart.
	PortReservedImplicit PortState = "reserved_implicit"

	// PortReservedExplicit means a user has manually reserved this port in the
	// store and no listener is currently active.
	PortReservedExplicit PortState = "reserved_explicit"
)

// Occupant describes whatever is currently holding a port.
// Exactly one of the process-specific or docker-specific fields is populated,
// depending on Occupant.Type.
type Occupant struct {
	Type string `json:"type"` // "process" | "docker"

	// Process fields (Type == "process")
	PID         int    `json:"pid,omitempty"`
	ProcessName string `json:"process_name,omitempty"`
	Cmdline     string `json:"cmdline,omitempty"`

	// Docker fields (Type == "docker")
	ContainerID     string `json:"container_id,omitempty"`
	ContainerName   string `json:"container_name,omitempty"`
	ContainerImage  string `json:"container_image,omitempty"`
	ContainerStatus string `json:"container_status,omitempty"` // "running" | "exited" | ...
	ContainerUptime string `json:"container_uptime,omitempty"` // human text from docker ps Status
}

// UFWInfo summarises the UFW rule that applies to a port.
type UFWInfo struct {
	Action  string `json:"action"`            // "allow" | "deny" | "limit" | "complex"
	RuleNum int    `json:"rule_num,omitempty"` // 0 when not applicable / unknown
}

// ContainerOtherPort is one sibling host port belonging to the same container
// as the primary result port. UFW is nil when no UFW rule covers it.
type ContainerOtherPort struct {
	Port int      `json:"port"`
	UFW  *UFWInfo `json:"ufw"` // nullable per §4.5
}

// ReservationInfo describes why a port is considered reserved.
type ReservationInfo struct {
	Source string `json:"source"` // "explicit" | "implicit"

	// Explicit-reservation fields (Source == "explicit")
	Name string `json:"name,omitempty"`
	Note string `json:"note,omitempty"`

	// Implicit-reservation fields (Source == "implicit")
	ContainerName   string `json:"container_name,omitempty"`
	ContainerStatus string `json:"container_status,omitempty"`
}

// PortStatus is the complete answer for one port in a CheckPorts call.
// The JSON field names mirror the design doc §4.5 exactly.
type PortStatus struct {
	Port  int       `json:"port"`
	State PortState `json:"state"`

	// Occupant is non-nil when State is used_process or used_docker.
	Occupant *Occupant `json:"occupant"` // nullable

	// UFW is non-nil when a UFW rule covers this port.
	UFW *UFWInfo `json:"ufw"` // nullable

	// ContainerOtherPorts lists every OTHER host port on the same container
	// (excluding the primary port itself). This field is nil (JSON null) for
	// non-docker states — not an empty slice.
	ContainerOtherPorts []ContainerOtherPort `json:"container_other_ports"` // nullable per §4.5

	// Reservation is non-nil when an explicit or implicit reservation applies.
	// Note: Reservation MAY be non-nil even when State is used_* — that
	// indicates a planning conflict (the listener is active AND a reservation
	// exists). Callers should display both pieces of information.
	Reservation *ReservationInfo `json:"reservation"` // nullable

	// DedupOf is set to the "owner" port when this port was already presented
	// in full via another result's ContainerOtherPorts. The current result then
	// carries only the minimal fields (Port, State, DedupOf).
	DedupOf int `json:"dedup_of,omitempty"`
}

// CheckResult is the top-level response returned by Checker.CheckPorts.
type CheckResult struct {
	ScannedAt time.Time    `json:"scanned_at"`
	Results   []PortStatus `json:"results"`
}
