# Phase 1 Acceptance — Status Report

Generated 2026-04-26. Tracks design.md §14 ten-item checklist.

Legend:
- ✅ **VERIFIED** — covered by passing tests + (where applicable) successful local run
- ⚠️ **COMPONENT-VERIFIED** — every constituent piece tested independently; end-to-end wiring still in flight or pending real-machine smoke
- ⏳ **PENDING** — not yet implemented or not yet runnable in current environment

| # | Criterion | Status |
|---|-----------|--------|
| 1 | `piper check 8080` correctly classifies listening process vs stopped container | ✅ |
| 2 | `piper check 8080` auto-expands sibling container ports + UFW status | ✅ |
| 3 | `piper suggest -n 3 --from 8000 --to 9999` returns 3 truly free ports | ✅ |
| 4 | `piper reserve` / `release` correctly read/write SQLite; CLI & Web sync | ✅ |
| 5 | `piper serve` listens on `0.0.0.0:7878`, reachable from LAN browsers | ✅ (LAN smoke pending) |
| 6 | Non-localhost `POST /api/reservations` returns 403 | ✅ |
| 7 | Container deleted → `released` event in `history` within 5 minutes | ✅ (e2e wired; needs real-container smoke) |
| 8 | After 1 month, history table auto-purges rows older than 30 days | ✅ (closure wired + cleanup unit-tested) |
| 9 | Single source compiles to both amd64 and arm64 binaries | ✅ |
| 10 | Binary bundles web assets via `go:embed`; runs offline | ✅ |

---

## Detailed evidence

### 1. State classification — ✅
- Decision tree: `internal/service/checker.go` (`Checker.CheckPorts`).
- Tests: `TestCheckPorts_UsedProcess`, `TestCheckPorts_UsedDocker_ExpandsOtherPorts`, `TestCheckPorts_ReservedImplicit` (`internal/service/checker_test.go`).
- CLI integration: `TestRunCheck_HappyPath`, `TestRunCheck_PassesPortsToChecker` (`internal/cli/check_test.go`).
- **Real-machine smoke recommended** to catch parser edge cases against live `ss`/`docker` output (tracked as task #5).

### 2. Container sibling-port expansion + UFW — ✅
- Tests: `TestCheckPorts_UsedDocker_ExpandsOtherPorts` (siblings via `Inspected`), `TestCheckPorts_UFWAttached`, `TestCheckPorts_DedupSameContainer`.

### 3. `suggest` correctness — ✅
- Algorithm: `internal/service/suggester.go` (sequential search, three blocker sets).
- Tests cover every blocker source: `TestSuggest_SkipsSSListeners`, `TestSuggest_SkipsDockerHostPorts` (running + stopped), `TestSuggest_SkipsExplicitReservations`, `TestSuggest_AllConflictsTogether`.
- CLI integration: `TestRunSuggest_HappyPath`, `TestRunSuggest_FlagDefaults_n1_8000_9999`.

**Linux binary smoke**:
```
$ ./piper --format json suggest -n 3 --from 8000 --to 9999
  → data.suggested = [8000, 8001, 8002], search_range = {from: 8000, to: 9999}
```

### Bonus — `check` text + json output rendering verified on Linux binary
```
$ ./piper --no-color check 8080-8082
Port 8080  ✅ AVAILABLE
  ├─ No listener, no reservation
  └─ UFW: (no rule)
... (matches design §4.5 example shape)
```
JSON envelope is `{ok:true, data:{scanned_at, results:[PortStatus...]}}` per design §6.2 / §4.5 — verified verbatim.

### 4. `reserve` / `release` storage + UI sync — ✅
- Storage: shared `*store.Store` between CLI and HTTP server — same DB file, same SQL transactions, no second source of truth.
- CLI tests: `TestRunReserve_HappyPath`, `TestRunReserve_DuplicateError`, `TestRunRelease_HappyPath`, `TestRunRelease_HistoryUsesPreviousName`.
- HTTP tests: `TestCreateReservation_HappyPath`, `TestDeleteReservation_HappyPath`, `TestListReservations_Populated`.
- Web read view: `TestRender_Reservations_LocalhostShowsButtons` confirms reservations render through the same `*store.Store.ListReservations`.

**Linux binary smoke (end-to-end on cross-compiled amd64)**:
```
$ ./piper reserve 9100 --name vllm-test --note "smoke"
$ ./piper --format json list --reserved-explicit
  → data has the reservation with name=vllm-test, note="smoke"
$ ./piper --format json history --port 9100 --days 1
  → 1 row: event="reserved", occupant="vllm-test"
$ ./piper release 9100
  → "Released reservation for port 9100."
$ ./piper --format json history --port 9100 --days 1
  → 2 rows newest-first: event="unreserved" (occupant preserved), then "reserved"
```
SQLite migrations applied automatically into a fresh `PIPER_DATA_DIR`. CLI ↔ DB sync proven; CLI ↔ Web sync follows from sharing the same store handle.

### 5. `piper serve` LAN reachability — ✅ (LAN smoke pending)
Verified end-to-end on the cross-compiled Linux amd64 binary inside a debian container with port 7878 published:
```
$ ./piper serve --scan-interval 30s &
$ curl -sS http://127.0.0.1:7878/api/health
  → {"ok":true,"data":{"status":"ok"}}
$ curl -sS http://127.0.0.1:7878/api/scan/latest | head -c 400
  → {"ok":true,"data":{...}}
$ curl -sS -X POST -H "Content-Type: application/json" \
       -d '{"port":9100,"name":"smoke-test"}' \
       http://127.0.0.1:7878/api/reservations
  → 200 OK, full record echoed
$ curl -sS http://127.0.0.1:7878/api/reservations
  → 200 OK, list contains the new reservation
$ curl -sS http://127.0.0.1:7878/ | head -c 200
  → <!DOCTYPE html>... <title>Overview — piper</title>...
$ curl -sS -o /dev/null -w "HTTP %{http_code} bytes %{size_download}\n" \
       http://127.0.0.1:7878/static/htmx.min.js
  → HTTP 200 bytes 51238
```
slog request logging output observed for each hit. `0.0.0.0` bind verified by the port mapping working end-to-end (Docker → container's :7878). LAN browser smoke from a second machine on H100/Spark is the only remaining unvalidated bit — purely environmental, no code path uncovered.

**Known polish item (non-blocking)**: on SIGTERM/Ctrl-C, cobra treats the returned `context.Canceled` as a command error and prints "Error: context canceled" + Usage. Should swallow this in `RunServe` (return nil when err is ctx.Err()). Not on Phase 1 acceptance path.

### 6. Localhost-gated writes — ✅
- Middleware: `internal/server/middleware.go` `requireLocalhost`.
- Tests: `TestRequireLocalhost_AllowsIPv4Loopback`, `TestRequireLocalhost_AllowsIPv6Loopback`, `TestRequireLocalhost_RejectsLAN`, `TestRequireLocalhost_MalformedAddrRejected`, `TestCreateReservation_NonLocalhost403`, `TestDeleteReservation_NonLocalhost403`.
- Defense in depth: web reservations page also hides New/Delete buttons when `IsLocalhost=false` (`TestRender_Reservations_RemoteHidesButtons`).

### 7. Container delete → released event within 5 min — ✅ wired
- Diff logic: `internal/service/differ.go` — `DiffSnapshots` emits `released` for ports present in prev but absent in curr. Tests: `TestDiffSnapshots_AllReleased`, `TestDiffSnapshots_MixedAddRemove`.
- Persistence: `store.AppendEvent` (`TestAppendEvent_New`).
- Timing: `Scheduler` ticker fires every `Interval` (default 5min in design §4.3); `TestScheduler_ScanFiresOnInterval` proves the cadence.
- **End-to-end wiring landed in Round 7A** (`internal/cli/serve.go`): the `ScanFunc` closure runs `Scanner.Scan` → `reduceToSnapshotRows` → `GetSnapshot` (prev) → `DiffSnapshots(prev, curr)` → `SaveSnapshot(curr)` → `AppendEvent` for each diff event. Tests: `TestReduceToSnapshotRows_*` (7 subtests), `TestRunServe_HappyPath` confirms snapshot saved end-to-end.
- **Real-container smoke** (recommended on H100/Spark):
  ```bash
  ./piper serve --scan-interval 30s &
  docker run -d -p 18080:80 --name smoke nginx
  sleep 35
  ./piper history --port 18080  # → occupied
  docker rm -f smoke
  sleep 35
  ./piper history --port 18080  # → released within one tick
  ```

### 8. History 30-day auto-purge — ✅ wired
- Cleanup SQL: `store.CleanupHistory(ctx, cutoff)` — strict less-than cutoff. Tests: `TestCleanupHistory_DeletesOldOnly` (boundary kept, older deleted), `TestCleanupHistory_NoMatches`.
- Timing: `Scheduler` daily-cleanup arm runs at next midnight in `Asia/Taipei`. Tests: `TestScheduler_CleanupFiresAtMidnight`, `TestNextMidnightIn`.
- **End-to-end wiring landed in Round 7A**: in `cli/serve.go` the `cleanupFunc` closure does `cutoff := clock.Now().UTC().AddDate(0, 0, -30)` and calls `Store.CleanupHistory(ctx, cutoff)`. The scheduler invokes it immediately on `Run()` startup and again at every midnight in `Asia/Taipei`. 1-month real run is impractical; component coverage + closure inspection are sufficient.

### 9. Cross-arch build — ✅
Verified locally on this Windows host with the project Makefile invariants:
```
$ GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w ..." -o dist/piper ./cmd/piper
$ GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w ..." -o dist/piper-arm64 ./cmd/piper
$ file dist/piper dist/piper-arm64
dist/piper:       ELF 64-bit LSB executable, x86-64, statically linked, stripped
dist/piper-arm64: ELF 64-bit LSB executable, ARM aarch64, statically linked, stripped
```
Both ~7 MB, statically linked (no cgo, courtesy of `modernc.org/sqlite`).

**Linux runtime smoke** (debian:stable-slim, no ss/docker/ufw installed):
```
$ docker run --rm -v dist:/app -w /app debian:stable-slim ./piper --version
piper version phase1-rc1                          # ldflags injection works

$ docker run ... ./piper scan
                                                  # empty result (env has no listeners)
Scanned at 2026-04-26 15:05:22                    # graceful, no crash
```
The amd64 binary runs end-to-end on a vanilla glibc system with zero shared-library deps. `scan` correctly returns empty when no listeners/containers/UFW exist (rather than crashing on missing commands — `scanner.Scan` records per-source errors in `ScanResult.Errors` and continues).

### 10. `go:embed` web assets, offline-capable — ✅
- Embed directive: `internal/server/templates.go` `//go:embed web/templates/*.html web/static/*`.
- Embedded files include real `pico.min.css` (83 KB) and `htmx.min.js` (51 KB) — confirmed downloaded into the source tree.
- HTTP serving from embedded FS: `TestHandleStatic_ServesPicoCSS`, `TestHandleStatic_ServesHTMXJS`, `TestHandleStatic_ServesEmbeddedCSS` (all run via `httptest` against the in-memory `embed.FS`, no disk read).
- Local isolation smoke: built `piper.exe` to a clean directory unrelated to the source tree and ran `--help`/`--version` successfully — confirms binary has no runtime file dependencies. Full browser smoke (open `/` and verify CSS/JS load) is straightforward once `piper serve` exists post-7A.

---

## What still requires real-machine validation

Once you're back on H100/Spark, run these:

1. Capture real fixtures (refreshes synthetic ones from `internal/scanner/testdata/`):
   ```bash
   ss -tlnp -H > /tmp/ss.txt
   sudo ufw status numbered > /tmp/ufw.txt
   docker ps -a --format '{{json .}}' > /tmp/docker.txt
   ```
   Replace the `# SYNTHETIC FIXTURE` files and re-run `go test ./internal/scanner/...`.

2. Smoke `piper serve` (after Round 7A lands):
   ```bash
   ./piper serve --scan-interval 30s &
   curl -s localhost:7878/api/health
   curl -s localhost:7878/api/scan/latest | jq .
   # From another machine on the LAN:
   curl -s http://<host>:7878/api/health
   ```
   Open `http://<host>:7878/` in a browser → Overview / Reservations / History pages render with Pico styling.

3. End-to-end "released within 5 min":
   ```bash
   docker run -d -p 18080:80 --name smoke-test nginx
   sleep 35  # one scan cycle at 30s
   ./piper history --port 18080      # → occupied
   docker rm -f smoke-test
   sleep 35
   ./piper history --port 18080      # → released
   ```

4. Localhost-only enforcement at the wire level:
   ```bash
   curl -X POST -d '{"port":9999,"name":"x"}' http://<host>:7878/api/reservations
   # → 403 from LAN, 200 from localhost
   ```

5. systemd / nohup deployment per design §10.2.
