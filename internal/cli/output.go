// Package cli provides the CLI output layer for Piper: text and JSON
// formatters for all subcommands, plus a tee-capable Writer.
//
// Design references: §4.1, §4.5, §4.6, §4.7, §4.8, §4.9, §6.2 of
// 2026-04-25-phase1-mvp-design.md.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ─── Format type ─────────────────────────────────────────────────────────────

// Format selects the output serialisation mode.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// ─── Writer ───────────────────────────────────────────────────────────────────

// Writer fans output to Stdout AND optionally a file (tee mode).
// If FilePath is empty, only Stdout is written.
// Stdout defaults to os.Stdout when nil (set by NewWriter).
type Writer struct {
	Format   Format
	FilePath string
	NoColor  bool

	// Stdout is the primary output destination. Callers may inject a
	// *bytes.Buffer for testing. If this is nil the Write call will panic —
	// use NewWriter to get a safe default.
	Stdout io.Writer
}

// NewWriter returns a Writer whose Stdout defaults to os.Stdout.
func NewWriter(fmt Format, filePath string, noColor bool) *Writer {
	return &Writer{
		Format:   fmt,
		FilePath: filePath,
		NoColor:  noColor,
		Stdout:   os.Stdout,
	}
}

// Write picks the right format method and tees to FilePath when set.
// payload must be one of:
//   - service.CheckResult
//   - SuggestionPayload
//   - []store.Reservation
//   - []store.Event
//
// Any other type returns an "unsupported payload type" error.
func (w *Writer) Write(payload any) error {
	out := w.Stdout

	// Open tee file when requested.
	var f *os.File
	if w.FilePath != "" {
		var err error
		f, err = os.OpenFile(w.FilePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("cli.Writer: open tee file %q: %w", w.FilePath, err)
		}
		// Write goes to both stdout and the file.
		out = io.MultiWriter(w.Stdout, f)
	}

	var writeErr error
	switch p := payload.(type) {
	case service.CheckResult:
		if w.Format == FormatJSON {
			writeErr = FormatCheckJSON(out, p)
		} else {
			writeErr = FormatCheckText(out, p, w.NoColor)
		}
	case SuggestionPayload:
		if w.Format == FormatJSON {
			writeErr = FormatSuggestionJSON(out, p)
		} else {
			writeErr = FormatSuggestionText(out, p, w.NoColor)
		}
	case []store.Reservation:
		if w.Format == FormatJSON {
			writeErr = FormatReservationsJSON(out, p)
		} else {
			writeErr = FormatReservationsText(out, p, w.NoColor)
		}
	case []store.Event:
		if w.Format == FormatJSON {
			writeErr = FormatHistoryJSON(out, p)
		} else {
			writeErr = FormatHistoryText(out, p, w.NoColor)
		}
	default:
		if f != nil {
			f.Close()
		}
		return fmt.Errorf("unsupported payload type: %T", payload)
	}

	// Close the tee file (propagate close error only when write succeeded).
	if f != nil {
		closeErr := f.Close()
		if writeErr == nil {
			return closeErr
		}
	}
	return writeErr
}

// ─── SuggestionPayload ────────────────────────────────────────────────────────

// SuggestionPayload is the formatter input for `piper suggest`.
// Mirrors §6.3 /api/suggest response data exactly.
type SuggestionPayload struct {
	Suggested   []int     `json:"suggested"`
	SearchRange struct {
		From int `json:"from"`
		To   int `json:"to"`
	} `json:"search_range"`
	ScannedAt time.Time `json:"scanned_at"`
}

// ─── ANSI helper ─────────────────────────────────────────────────────────────

// colorize wraps text with an ANSI escape code when enabled is true.
// code should be a raw escape sequence parameter, e.g. "31" for red.
func colorize(text, code string, enabled bool) string {
	if !enabled {
		return text
	}
	return "\033[" + code + "m" + text + "\033[0m"
}

// ANSI color codes used in text output.
const (
	ansiRed    = "31"
	ansiYellow = "33"
	ansiGreen  = "32"
	ansiBlue   = "34"
)

// ─── State badge ─────────────────────────────────────────────────────────────

// stateBadge returns the display badge for a PortState.
// When noColor is true, ANSI escapes are omitted but emoji is kept.
func stateBadge(state service.PortState, noColor bool) string {
	switch state {
	case service.PortUsedProcess:
		return colorize("❌ IN USE (process)", ansiRed, !noColor)
	case service.PortUsedDocker:
		return colorize("❌ IN USE (docker)", ansiRed, !noColor)
	case service.PortReservedImplicit:
		return colorize("⚠️  RESERVED (no listener)", ansiYellow, !noColor)
	case service.PortReservedExplicit:
		return colorize("📌 RESERVED (planned)", ansiBlue, !noColor)
	case service.PortFree:
		return colorize("✅ AVAILABLE", ansiGreen, !noColor)
	default:
		return string(state)
	}
}

// ─── Asia/Taipei helper ───────────────────────────────────────────────────────

// taipeiTime returns t in Asia/Taipei. Falls back to UTC if the timezone
// cannot be loaded (e.g. on a system without IANA data).
func taipeiTime(t time.Time) time.Time {
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		return t.UTC()
	}
	return t.In(loc)
}

// scannedAtLine returns the footer line "Scanned at YYYY-MM-DD HH:MM:SS".
func scannedAtLine(t time.Time) string {
	return "Scanned at " + taipeiTime(t).Format("2006-01-02 15:04:05")
}

// ─── JSON envelope ────────────────────────────────────────────────────────────

// jsonEnvelope is the §6.2 success wrapper.
type jsonEnvelope struct {
	OK   bool `json:"ok"`
	Data any  `json:"data"`
}

func writeJSON(w io.Writer, data any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(jsonEnvelope{OK: true, Data: data})
}

// ─── FormatCheckJSON / FormatCheckText ───────────────────────────────────────

// FormatCheckJSON writes the §6.2-wrapped CheckResult as indented JSON.
func FormatCheckJSON(w io.Writer, r service.CheckResult) error {
	return writeJSON(w, r)
}

// FormatCheckText writes the §4.5 text format for a CheckResult.
func FormatCheckText(w io.Writer, r service.CheckResult, noColor bool) error {
	for i, ps := range r.Results {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if err := writePortStatusText(w, ps, noColor); err != nil {
			return err
		}
	}
	// Footer
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, scannedAtLine(r.ScannedAt))
	return err
}

// writePortStatusText renders one PortStatus in the §4.5 tree format.
func writePortStatusText(w io.Writer, ps service.PortStatus, noColor bool) error {
	badge := stateBadge(ps.State, noColor)

	// Header line
	if _, err := fmt.Fprintf(w, "Port %d  %s\n", ps.Port, badge); err != nil {
		return err
	}

	// Dedup shortcut
	if ps.DedupOf != 0 {
		_, err := fmt.Fprintf(w, "  └─ (see port %d)\n", ps.DedupOf)
		return err
	}

	// Build the tree lines depending on state.
	type line struct{ text string }
	var lines []string

	switch ps.State {
	case service.PortUsedProcess:
		if ps.Occupant != nil {
			lines = append(lines, fmt.Sprintf("Process: %s (pid %d)", ps.Occupant.ProcessName, ps.Occupant.PID))
		}
		if ps.UFW != nil {
			lines = append(lines, ufwLine(ps.UFW))
		} else {
			lines = append(lines, "UFW: (no rule)")
		}

	case service.PortUsedDocker:
		if ps.Occupant != nil {
			lines = append(lines, fmt.Sprintf("Process: docker-proxy (pid %d)", ps.Occupant.PID))
			lines = append(lines, fmt.Sprintf("Container: %s", ps.Occupant.ContainerName))
			lines = append(lines, fmt.Sprintf("           image: %s", ps.Occupant.ContainerImage))
			lines = append(lines, fmt.Sprintf("           status: %s", containerStatusText(ps.Occupant)))
		}
		if ps.UFW != nil {
			lines = append(lines, ufwLine(ps.UFW))
		} else {
			lines = append(lines, "UFW: (no rule)")
		}

	case service.PortReservedImplicit:
		lines = append(lines, "Source: implicit (stopped container)")
		if ps.Reservation != nil {
			lines = append(lines, fmt.Sprintf("Container: %s (status: %s)",
				ps.Reservation.ContainerName, ps.Reservation.ContainerStatus))
		}
		if ps.UFW != nil {
			lines = append(lines, ufwLine(ps.UFW))
		} else {
			lines = append(lines, "UFW: (no rule)")
		}
		lines = append(lines, "This port is technically free; restart container to reclaim.")

	case service.PortReservedExplicit:
		if ps.Reservation != nil {
			lines = append(lines, fmt.Sprintf("Reserved for: %s", ps.Reservation.Name))
			if ps.Reservation.Note != "" {
				lines = append(lines, fmt.Sprintf("Note: %s", ps.Reservation.Note))
			}
		}
		if ps.UFW != nil {
			lines = append(lines, ufwLine(ps.UFW))
		} else {
			lines = append(lines, "UFW: (no rule)")
		}

	case service.PortFree:
		lines = append(lines, "No listener, no reservation")
		if ps.UFW != nil {
			lines = append(lines, ufwLine(ps.UFW))
		} else {
			lines = append(lines, "UFW: (no rule)")
		}
	}

	// Write branch lines. The last line before sibling-ports section uses └─;
	// intermediate lines use ├─.
	//
	// If there are sibling ports, we write the text lines with ├─ and then the
	// sibling block with └─. Otherwise the last text line uses └─.

	hasSiblings := len(ps.ContainerOtherPorts) > 0

	for i, l := range lines {
		isLast := i == len(lines)-1 && !hasSiblings
		if isLast {
			if _, err := fmt.Fprintf(w, "  └─ %s\n", l); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(w, "  ├─ %s\n", l); err != nil {
				return err
			}
		}
	}

	// Sibling port block.
	if hasSiblings {
		if _, err := fmt.Fprintf(w, "  └─ Other ports on this container:\n"); err != nil {
			return err
		}
		for i, cop := range ps.ContainerOtherPorts {
			isLast := i == len(ps.ContainerOtherPorts)-1
			var ufwSuffix string
			if cop.UFW != nil {
				ufwSuffix = fmt.Sprintf("→ UFW: %s    ✅", strings.ToUpper(cop.UFW.Action))
			} else {
				ufwSuffix = "→ UFW: (no rule, default deny)  ⚠️"
			}
			if isLast {
				if _, err := fmt.Fprintf(w, "      └─ %d  %s\n", cop.Port, ufwSuffix); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintf(w, "      ├─ %d  %s\n", cop.Port, ufwSuffix); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// ufwLine returns a display line for a UFWInfo.
func ufwLine(u *service.UFWInfo) string {
	if u.RuleNum > 0 {
		return fmt.Sprintf("UFW: %s (rule #%d: %s/tcp)", strings.ToUpper(u.Action), u.RuleNum, "port")
	}
	return fmt.Sprintf("UFW: %s", strings.ToUpper(u.Action))
}

// containerStatusText returns a human-readable container status string.
func containerStatusText(o *service.Occupant) string {
	if o.ContainerUptime != "" {
		return "Up " + o.ContainerUptime
	}
	return o.ContainerStatus
}

// ─── FormatSuggestionJSON / FormatSuggestionText ─────────────────────────────

// FormatSuggestionJSON writes the §6.2-wrapped SuggestionPayload as JSON.
func FormatSuggestionJSON(w io.Writer, s SuggestionPayload) error {
	return writeJSON(w, s)
}

// FormatSuggestionText writes a human-readable suggestion result.
func FormatSuggestionText(w io.Writer, s SuggestionPayload, noColor bool) error {
	if _, err := fmt.Fprintf(w, "Suggested ports (search range %d–%d):\n", s.SearchRange.From, s.SearchRange.To); err != nil {
		return err
	}
	for _, p := range s.Suggested {
		if _, err := fmt.Fprintf(w, "  %s\n", colorize(fmt.Sprintf("%d", p), ansiGreen, !noColor)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w, scannedAtLine(s.ScannedAt))
	return err
}

// ─── FormatReservationsJSON / FormatReservationsText ─────────────────────────

// reservationJSON is the wire shape for a single Reservation in JSON output.
// Fields use explicit json tags without omitempty so empty strings serialise
// as "" rather than being omitted (per §6.2 spec note).
type reservationJSON struct {
	Port      int    `json:"port"`
	Name      string `json:"name"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
	CreatedBy string `json:"created_by"`
}

// FormatReservationsJSON writes the §6.2-wrapped reservations array as JSON.
func FormatReservationsJSON(w io.Writer, rs []store.Reservation) error {
	data := make([]reservationJSON, len(rs))
	for i, r := range rs {
		data[i] = reservationJSON{
			Port:      r.Port,
			Name:      r.Name,
			Note:      r.Note,
			CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
			CreatedBy: r.CreatedBy,
		}
	}
	return writeJSON(w, data)
}

// FormatReservationsText writes a human-readable reservations list.
func FormatReservationsText(w io.Writer, rs []store.Reservation, noColor bool) error {
	if len(rs) == 0 {
		_, err := fmt.Fprintln(w, "(no reservations)")
		return err
	}
	if _, err := fmt.Fprintf(w, "%-8s  %-20s  %-30s  %-20s  %s\n",
		"PORT", "NAME", "NOTE", "CREATED AT", "CREATED BY"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, strings.Repeat("─", 90)); err != nil {
		return err
	}
	for _, r := range rs {
		loc := taipeiTime(r.CreatedAt)
		if _, err := fmt.Fprintf(w, "%-8d  %-20s  %-30s  %-20s  %s\n",
			r.Port, r.Name, r.Note,
			loc.Format("2006-01-02 15:04"),
			r.CreatedBy); err != nil {
			return err
		}
	}
	return nil
}

// ─── FormatHistoryJSON / FormatHistoryText ────────────────────────────────────

// eventJSON is the wire shape for a history Event in JSON output.
type eventJSON struct {
	ID        int64  `json:"id"`
	Port      int    `json:"port"`
	Event     string `json:"event"`
	Occupant  string `json:"occupant"`
	Timestamp string `json:"timestamp"`
}

// FormatHistoryJSON writes the §6.2-wrapped history events array as JSON.
func FormatHistoryJSON(w io.Writer, evs []store.Event) error {
	data := make([]eventJSON, len(evs))
	for i, e := range evs {
		data[i] = eventJSON{
			ID:        e.ID,
			Port:      e.Port,
			Event:     string(e.Event),
			Occupant:  e.Occupant,
			Timestamp: e.Timestamp.UTC().Format(time.RFC3339),
		}
	}
	return writeJSON(w, data)
}

// FormatHistoryText writes a human-readable history list (newest-first; caller
// is responsible for ordering).
func FormatHistoryText(w io.Writer, evs []store.Event, noColor bool) error {
	if len(evs) == 0 {
		_, err := fmt.Fprintln(w, "(no history events)")
		return err
	}
	for _, e := range evs {
		loc := taipeiTime(e.Timestamp)
		ts := loc.Format("2006-01-02 15:04")
		occ := e.Occupant
		if _, err := fmt.Fprintf(w, "%s  port %-6d  %-12s  %s\n",
			ts, e.Port, string(e.Event), occ); err != nil {
			return err
		}
	}
	return nil
}
