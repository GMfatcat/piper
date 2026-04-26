package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GMfatcat/piper/internal/cli"
	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ─── helpers ────────────────────────────────────────────────────────────────

func makeCheckResult(ports []service.PortStatus, scannedAt time.Time) service.CheckResult {
	return service.CheckResult{
		ScannedAt: scannedAt,
		Results:   ports,
	}
}

// ─── 1. DefaultsStdoutWhenNil ────────────────────────────────────────────────

// TestWriter_DefaultsStdoutWhenNil verifies that NewWriter with no Stdout
// defaults to os.Stdout (i.e. does not panic / error when Stdout is nil and
// NewWriter is used).  We can't easily assert the actual os.Stdout bytes, but
// we verify the call succeeds without error when the Writer is properly
// initialised via NewWriter.
func TestWriter_DefaultsStdoutWhenNil(t *testing.T) {
	r := makeCheckResult([]service.PortStatus{
		{Port: 9000, State: service.PortFree},
	}, time.Now())

	w := cli.NewWriter(cli.FormatText, "", true)
	// Redirect its stdout to a buffer so we don't pollute test output.
	buf := &bytes.Buffer{}
	w.Stdout = buf

	if err := w.Write(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected non-empty output")
	}
}

// ─── 2. TestFormatCheckJSON_Envelope ────────────────────────────────────────

func TestFormatCheckJSON_Envelope(t *testing.T) {
	scannedAt := time.Date(2026, 4, 25, 14, 32, 18, 0, time.UTC)
	r := makeCheckResult([]service.PortStatus{
		{Port: 8080, State: service.PortUsedDocker},
	}, scannedAt)

	buf := &bytes.Buffer{}
	if err := cli.FormatCheckJSON(buf, r); err != nil {
		t.Fatalf("FormatCheckJSON error: %v", err)
	}

	var env map[string]any
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v\nOutput:\n%s", err, buf.String())
	}

	if ok, _ := env["ok"].(bool); !ok {
		t.Errorf("expected ok=true, got %v", env["ok"])
	}

	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not a map: %T", env["data"])
	}
	if data["scanned_at"] == nil {
		t.Error("data.scanned_at missing")
	}
	results, ok := data["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatal("data.results missing or empty")
	}
	first, ok := results[0].(map[string]any)
	if !ok {
		t.Fatal("results[0] is not a map")
	}
	if port, _ := first["port"].(float64); int(port) != 8080 {
		t.Errorf("expected results[0].port=8080, got %v", first["port"])
	}
}

// ─── 3. TestFormatCheckJSON_NullableFields ───────────────────────────────────

func TestFormatCheckJSON_NullableFields(t *testing.T) {
	r := makeCheckResult([]service.PortStatus{
		{
			Port:                9000,
			State:               service.PortFree,
			Occupant:            nil,
			UFW:                 nil,
			ContainerOtherPorts: nil,
			Reservation:         nil,
		},
	}, time.Now())

	buf := &bytes.Buffer{}
	if err := cli.FormatCheckJSON(buf, r); err != nil {
		t.Fatalf("FormatCheckJSON error: %v", err)
	}

	// The JSON output must explicitly contain `"occupant":null` etc.,
	// not omit these keys.
	out := buf.String()
	for _, key := range []string{`"occupant":null`, `"ufw":null`, `"container_other_ports":null`, `"reservation":null`} {
		// strip whitespace variant: "occupant": null
		compact := strings.ReplaceAll(out, " ", "")
		compact = strings.ReplaceAll(compact, "\n", "")
		if !strings.Contains(compact, strings.ReplaceAll(key, " ", "")) {
			t.Errorf("expected %q in JSON output; got:\n%s", key, out)
		}
	}
}

// ─── 4. TestFormatCheckText_UsedDockerWithSiblings ───────────────────────────

func TestFormatCheckText_UsedDockerWithSiblings(t *testing.T) {
	scannedAt := time.Date(2026, 4, 25, 14, 32, 18, 0, time.UTC)
	r := makeCheckResult([]service.PortStatus{
		{
			Port:  8080,
			State: service.PortUsedDocker,
			Occupant: &service.Occupant{
				Type:            "docker",
				ContainerName:   "ai-translate-server",
				ContainerImage:  "translate:v2",
				ContainerStatus: "running",
				ContainerUptime: "3 days",
			},
			UFW: &service.UFWInfo{Action: "allow", RuleNum: 5},
			ContainerOtherPorts: []service.ContainerOtherPort{
				{Port: 8443, UFW: &service.UFWInfo{Action: "allow"}},
				{Port: 9090, UFW: nil},
			},
		},
	}, scannedAt)

	buf := &bytes.Buffer{}
	if err := cli.FormatCheckText(buf, r, false); err != nil {
		t.Fatalf("FormatCheckText error: %v", err)
	}

	out := buf.String()
	mustContain := []string{
		"Port 8080",
		"IN USE (docker)",
		"ai-translate-server",
		"Other ports on this container:",
		"8443",
		"9090",
		"├─",
		"└─",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\nFull output:\n%s", s, out)
		}
	}
}

// ─── 5. TestFormatCheckText_FreePort ────────────────────────────────────────

func TestFormatCheckText_FreePort(t *testing.T) {
	r := makeCheckResult([]service.PortStatus{
		{Port: 9000, State: service.PortFree},
	}, time.Now())

	buf := &bytes.Buffer{}
	if err := cli.FormatCheckText(buf, r, true); err != nil {
		t.Fatalf("FormatCheckText error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "AVAILABLE") {
		t.Errorf("expected 'AVAILABLE' in output; got:\n%s", out)
	}
}

// ─── 6. TestFormatCheckText_NoColorStripsANSI ────────────────────────────────

func TestFormatCheckText_NoColorStripsANSI(t *testing.T) {
	r := makeCheckResult([]service.PortStatus{
		{Port: 9000, State: service.PortFree},
	}, time.Now())

	colored := &bytes.Buffer{}
	if err := cli.FormatCheckText(colored, r, false); err != nil {
		t.Fatalf("FormatCheckText(color) error: %v", err)
	}
	plain := &bytes.Buffer{}
	if err := cli.FormatCheckText(plain, r, true); err != nil {
		t.Fatalf("FormatCheckText(nocolor) error: %v", err)
	}

	if !strings.Contains(colored.String(), "\033[") {
		t.Error("expected ANSI escapes in colored output")
	}
	if strings.Contains(plain.String(), "\033[") {
		t.Errorf("expected NO ANSI escapes in no-color output; got:\n%s", plain.String())
	}
}

// ─── 7. TestFormatCheckText_ScannedAtFooter ──────────────────────────────────

func TestFormatCheckText_ScannedAtFooter(t *testing.T) {
	r := makeCheckResult([]service.PortStatus{
		{Port: 9000, State: service.PortFree},
	}, time.Now())

	buf := &bytes.Buffer{}
	if err := cli.FormatCheckText(buf, r, true); err != nil {
		t.Fatalf("FormatCheckText error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Scanned at ") {
		t.Errorf("expected 'Scanned at ' in output; got:\n%s", out)
	}
	re := regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}`)
	if !re.MatchString(out) {
		t.Errorf("expected date pattern in output; got:\n%s", out)
	}
}

// ─── 8. TestFormatSuggestionJSON ─────────────────────────────────────────────

func TestFormatSuggestionJSON(t *testing.T) {
	scannedAt := time.Date(2026, 4, 25, 14, 32, 18, 0, time.UTC)
	s := cli.SuggestionPayload{
		Suggested: []int{8000, 8001, 8002},
		ScannedAt: scannedAt,
	}
	s.SearchRange.From = 8000
	s.SearchRange.To = 9999

	buf := &bytes.Buffer{}
	if err := cli.FormatSuggestionJSON(buf, s); err != nil {
		t.Fatalf("FormatSuggestionJSON error: %v", err)
	}

	var env map[string]any
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}

	if ok, _ := env["ok"].(bool); !ok {
		t.Errorf("expected ok=true, got %v", env["ok"])
	}

	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data not a map: %T", env["data"])
	}

	suggested, ok := data["suggested"].([]any)
	if !ok {
		t.Fatalf("data.suggested not an array: %T", data["suggested"])
	}
	if len(suggested) != 3 {
		t.Errorf("expected 3 suggested ports, got %d", len(suggested))
	}
	for i, want := range []float64{8000, 8001, 8002} {
		if suggested[i].(float64) != want {
			t.Errorf("suggested[%d] = %v, want %v", i, suggested[i], want)
		}
	}
}

// ─── 9. TestFormatSuggestionText ─────────────────────────────────────────────

func TestFormatSuggestionText(t *testing.T) {
	s := cli.SuggestionPayload{
		Suggested: []int{8000, 8001, 8002},
		ScannedAt: time.Now(),
	}
	s.SearchRange.From = 8000
	s.SearchRange.To = 9999

	buf := &bytes.Buffer{}
	if err := cli.FormatSuggestionText(buf, s, true); err != nil {
		t.Fatalf("FormatSuggestionText error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"8000", "8001", "8002", "8000", "9999"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nFull output:\n%s", want, out)
		}
	}
}

// ─── 10. TestFormatReservationsJSON ──────────────────────────────────────────

func TestFormatReservationsJSON(t *testing.T) {
	ts := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	rs := []store.Reservation{
		{Port: 9100, Name: "vllm-llama", Note: "DGX dep", CreatedAt: ts, CreatedBy: "ymu"},
		{Port: 9200, Name: "ollama", Note: "", CreatedAt: ts, CreatedBy: ""},
	}

	buf := &bytes.Buffer{}
	if err := cli.FormatReservationsJSON(buf, rs); err != nil {
		t.Fatalf("FormatReservationsJSON error: %v", err)
	}

	var env map[string]any
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}

	if ok, _ := env["ok"].(bool); !ok {
		t.Errorf("expected ok=true")
	}
	data, ok := env["data"].([]any)
	if !ok {
		t.Fatalf("data not an array: %T", env["data"])
	}
	if len(data) != 2 {
		t.Errorf("expected 2 reservations, got %d", len(data))
	}

	// Verify empty Note serialises as "" not omitted.
	second := data[1].(map[string]any)
	if note, exists := second["note"]; !exists {
		t.Error("note key missing from second reservation")
	} else if note != "" {
		t.Errorf("expected empty note \"\", got %v", note)
	}
}

// ─── 11. TestFormatReservationsText_Empty ────────────────────────────────────

func TestFormatReservationsText_Empty(t *testing.T) {
	buf := &bytes.Buffer{}
	if err := cli.FormatReservationsText(buf, []store.Reservation{}, true); err != nil {
		t.Fatalf("FormatReservationsText error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "no reservation") {
		t.Errorf("expected 'no reservation' marker in empty output; got:\n%s", out)
	}
}

// ─── 12. TestFormatHistoryJSON ────────────────────────────────────────────────

func TestFormatHistoryJSON(t *testing.T) {
	ts := time.Date(2026, 4, 25, 12, 3, 0, 0, time.UTC)
	evs := []store.Event{
		{ID: 1, Port: 9100, Event: store.EventOccupied, Occupant: "vllm-llama", Timestamp: ts},
	}

	buf := &bytes.Buffer{}
	if err := cli.FormatHistoryJSON(buf, evs); err != nil {
		t.Fatalf("FormatHistoryJSON error: %v", err)
	}

	var env map[string]any
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}

	if ok, _ := env["ok"].(bool); !ok {
		t.Errorf("expected ok=true")
	}

	data, ok := env["data"].([]any)
	if !ok {
		t.Fatalf("data not an array")
	}
	if len(data) != 1 {
		t.Fatalf("expected 1 event, got %d", len(data))
	}

	ev := data[0].(map[string]any)
	// timestamp must be RFC3339
	tsStr, ok := ev["timestamp"].(string)
	if !ok {
		t.Fatal("timestamp missing or not string")
	}
	if _, err := time.Parse(time.RFC3339, tsStr); err != nil {
		t.Errorf("timestamp %q is not RFC3339: %v", tsStr, err)
	}
}

// ─── 13. TestFormatHistoryText ────────────────────────────────────────────────

func TestFormatHistoryText(t *testing.T) {
	ts1 := time.Date(2026, 4, 25, 12, 3, 0, 0, time.UTC)
	ts2 := time.Date(2026, 4, 25, 11, 47, 0, 0, time.UTC)
	evs := []store.Event{
		{ID: 2, Port: 9100, Event: store.EventOccupied, Occupant: "vllm-llama", Timestamp: ts1},
		{ID: 1, Port: 9100, Event: store.EventReleased, Occupant: "", Timestamp: ts2},
	}

	buf := &bytes.Buffer{}
	if err := cli.FormatHistoryText(buf, evs, true); err != nil {
		t.Fatalf("FormatHistoryText error: %v", err)
	}

	out := buf.String()
	mustContain := []string{"9100", "occupied", "vllm-llama", "released"}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q in history output:\n%s", s, out)
		}
	}
}

// ─── 14. TestWriter_TeeWritesToFileAndStdout ──────────────────────────────────

func TestWriter_TeeWritesToFileAndStdout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	r := makeCheckResult([]service.PortStatus{
		{Port: 9000, State: service.PortFree},
	}, time.Now())

	stdoutBuf := &bytes.Buffer{}
	w := &cli.Writer{
		Format:   cli.FormatText,
		FilePath: path,
		NoColor:  true,
		Stdout:   stdoutBuf,
	}

	if err := w.Write(r); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	if stdoutBuf.Len() == 0 {
		t.Error("stdout buffer is empty")
	}

	fileBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read tee file: %v", err)
	}
	if len(fileBytes) == 0 {
		t.Error("tee file is empty")
	}
	if !bytes.Equal(stdoutBuf.Bytes(), fileBytes) {
		t.Errorf("stdout and file differ\nstdout=%q\nfile=%q", stdoutBuf.String(), string(fileBytes))
	}
}

// ─── 15. TestWriter_TeeFileCreatedWithMode ────────────────────────────────────

func TestWriter_TeeFileCreatedWithMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "piper-query.txt")

	r := makeCheckResult([]service.PortStatus{
		{Port: 9000, State: service.PortFree},
	}, time.Now())

	buf := &bytes.Buffer{}
	w := &cli.Writer{
		Format:   cli.FormatText,
		FilePath: path,
		NoColor:  true,
		Stdout:   buf,
	}

	if err := w.Write(r); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("tee file was not created")
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0644 {
			t.Errorf("expected mode 0644, got %o", info.Mode().Perm())
		}
	}
}

// ─── 16. TestWriter_UnknownPayloadType ───────────────────────────────────────

func TestWriter_UnknownPayloadType(t *testing.T) {
	buf := &bytes.Buffer{}
	w := &cli.Writer{
		Format:  cli.FormatText,
		NoColor: true,
		Stdout:  buf,
	}

	err := w.Write(struct{ X int }{X: 42})
	if err == nil {
		t.Fatal("expected error for unknown payload type, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported payload type") {
		t.Errorf("expected 'unsupported payload type' in error, got: %v", err)
	}
}
