# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project: Piper

Piper is a single-binary Go tool for managing port allocations on a Linux host that runs a mix of Docker containers and host-level services (FastAPI, vLLM, Ollama, etc.). It fuses three sources of truth — `ss -tlnp` listeners, `docker ps -a` port bindings, and `ufw status numbered` rules — into a unified view, persisted in SQLite, exposed via CLI and an embedded Web UI.

The full Phase 1 specification lives in `2026-04-25-phase1-mvp-design.md`. **Read it before making non-trivial changes** — it defines the CLI contract, JSON API, schema, and acceptance criteria.

Status: design complete, **no implementation yet** (greenfield).

Module path: `github.com/GMfatcat/piper` (this overrides the `github.com/ymu/piper` shown in the design doc's ldflags example).

## Development Environment

- **Host**: Windows 11, project lives at `D:\CLAUDE_CODE_PIPER\`. Go 1.26.2 installed natively on Windows.
- **Claude Code session**: launched from PowerShell. The `Bash` tool invokes Git Bash, so use Unix syntax (forward slashes, `/dev/null`); `go.exe` is on PATH so `go test`, `go mod`, etc. run fine from Bash.
- **No WSL Go toolchain.** Cross-arch builds and Linux-specific test runs go through Docker:
  ```bash
  docker run --rm -v D:/CLAUDE_CODE_PIPER:/app -v piper-go-cache:/root/go -w /app golang:1.26 go test -race -cover ./...
  ```
  Create the cache volume once: `docker volume create piper-go-cache`.
- **No real-system fixtures available** during home-office sessions. All scanner parser fixtures (`internal/scanner/testdata/`) are **synthetic**, hand-crafted from the design doc spec + man pages. Each synthetic fixture file must start with a comment marker `# SYNTHETIC FIXTURE — verify against real H100/Spark output before Phase 1 sign-off`. A real-fixture verification pass is tracked as a follow-up task.
- **Line endings**: `.gitattributes` forces LF for all source/text files; `testdata/**` is treated as binary to preserve byte-exact captures.

## Development Methodology

- **TDD via Sonnet 4.6 subagents.** Each round dispatches up to 3 subagents in parallel. Run independent tasks concurrently; serialize only on real dependencies.
- **Layer order for Phase 1 implementation**: `scanner` → `store` → `service` → `cli` → `server`. Each layer must be independently testable before the next layer depends on it.
- **Parsers are pure functions.** `scanner/{ss,docker,ufw}.go` should accept `[]byte` (command stdout) and return typed structs. This keeps unit tests OS-independent and lets integration tests use fixtures captured from real hosts.

## Architecture

Strict layered dependency graph (enforce in code review):

```
scanner  ──┐
           ├──>  service  ──>  cli
store    ──┘                ──>  server
```

- `internal/scanner/` — pure system reads (ss / docker / ufw / lsof). Must NOT import store, service, or server.
- `internal/store/` — SQLite operations (reservations, snapshots, history, migrations via go:embed). Must NOT import scanner, service, or server.
- `internal/service/` — the only business-logic layer. Reads from scanner + store, produces answers. Used by both CLI and HTTP handlers (this is why both layers stay thin).
- `internal/server/` — `net/http` handlers + embedded HTMX/Pico templates (`go:embed`). Imports service.
- `internal/cli/` — cobra subcommands. Imports service. Must NOT import server (and vice versa).

The `service` layer being the single source of business logic is load-bearing — never duplicate logic between CLI and HTTP handlers.

### Scan data flow

Each scan (scheduled every 5 min, or manually triggered) runs three collectors in parallel (`sync.WaitGroup`), fuses them into a `port → state` map (Docker takes precedence over raw `ss` because docker-proxy entries belong to containers), diffs against the previous snapshot to emit `occupied`/`released` history events, then writes everything in a single SQLite transaction. See §7.2 of the design doc for the full pipeline.

### Security model

- HTTP server binds `0.0.0.0:7878` for read access from the LAN (UFW provides perimeter).
- All write endpoints (`POST /api/reservations`, `DELETE /api/reservations/:port`, `POST /api/scan/trigger`) are gated by a `requireLocalhost` middleware that checks `r.RemoteAddr`. Web UI buttons must not even render when the request comes from non-localhost.
- Piper never executes `sudo`, never modifies UFW rules, never starts/stops containers. It is read-only with respect to the system.

## Build & Test Commands

Targets defined in the planned `Makefile` (§10.1 of design):

```bash
make build           # build amd64 + arm64
make build-amd64     # GOOS=linux GOARCH=amd64 CGO_ENABLED=0
make build-arm64     # GOOS=linux GOARCH=arm64 CGO_ENABLED=0
make test            # go test -race -cover ./...
make lint            # go vet + golangci-lint run
```

`CGO_ENABLED=0` is required — the SQLite driver is `modernc.org/sqlite` (pure Go) specifically so cross-compilation to aarch64 (DGX Spark) needs no toolchain. Do not switch to `mattn/go-sqlite3`.

Run a single test: `go test -run TestName ./internal/scanner/...`

## Key Constraints

- **Go 1.23+** with standard-library `net/http` ServeMux (no gin/chi/echo — only ~10 routes).
- **No cgo anywhere.** Breaks cross-compile.
- **No WebSocket / no auto-polling** in Web UI — 5-minute scan cadence makes push pointless. HTMX for partial updates only.
- **`lsof` is NOT in the periodic scan path.** It runs only on explicit `piper check <port>` deep-check requests.
- **Phase 1 UFW parser handles only the simple `PORT/PROTO ALLOW IN Anywhere` form.** Complex rules (specific from-IP, interface-bound) get marked `ufw_action = 'complex'` — do not try to parse them in Phase 1.
- **Time storage is UTC; display is Asia/Taipei.** Don't store local time.

## Deviations from design.md

- **Migrations location**: design.md §8 places SQL files at project-root `migrations/`, but `//go:embed` cannot escape its package directory (no `..` allowed). Migrations therefore live at `internal/store/migrations/001_init.sql` and are embedded from `internal/store/migrate.go`. The project-root `migrations/` folder is not used.
- **Scanner types layout**: design.md §8 mentions `internal/scanner/types.go` as a single types file. For parallel-friendly TDD, each scanner file currently owns its own struct (UFWRule in `ufw.go`, SSEntry in `ss.go`, etc.). Consolidation into `types.go` is deferred unless it becomes painful.

## Phase 1 Acceptance Criteria

See §14 of the design doc — these are the gates for declaring Phase 1 done. Notably:

- `piper check <port>` correctly identifies running container, stopped container (implicit reservation), explicit reservation, and free states; auto-expands all sibling ports of an occupying container.
- Container deletion → `released` event in `history` within one scan cycle.
- Cross-architecture: same source compiles to both amd64 and arm64 binaries.
- Single binary contains all web assets via `go:embed` and runs offline.
