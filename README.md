# piper

> *call once, all ports come marching*

A single-binary Go tool for managing port allocations on Linux hosts that mix Docker containers and host-level services. Piper fuses three sources of truth — `ss -tlnp` listeners, `docker ps -a` port bindings, and `ufw status numbered` rules — into one consistent view, persisted in SQLite, exposed via CLI and an embedded web UI.

[繁體中文 README](./README.zh-TW.md) · [Phase 1 Design](./2026-04-25-phase1-mvp-design.md) · [Acceptance Status](./ACCEPTANCE.md)

---

## Why piper

In a typical lab/server environment the four answers to "what's on this port?" disagree:

- the kernel's listener table,
- Docker's port bindings (running and stopped containers),
- the UFW rule set,
- the wiki page someone updates by hand.

When somebody adds a new container, ports collide. When somebody removes one, the port is technically free but nobody dares reuse it. Piper exists so you can ask once and get the truth.

## Status

**Phase 1 MVP — implementation complete, pending real-machine smoke.**

9 of 10 acceptance criteria from the design doc verified locally (cross-compile, embedded assets, end-to-end CLI smoke on the Linux binary). Remaining work: capturing real `ss`/`docker`/`ufw` output to validate parsers against production data, and a LAN browser smoke against `piper serve`.

## Install / build

Requires Go 1.26+. SQLite is pure Go (`modernc.org/sqlite`); no cgo, no toolchain hassle when cross-compiling.

```bash
git clone <repo>
cd piper
make build           # produces dist/piper (linux amd64) + dist/piper-arm64
make build-host      # current OS/arch (handy on dev machine)
make test            # full suite
make test-race       # with race detector
```

Targets:

| Target | Output |
|--------|--------|
| `make build-amd64` | `dist/piper` — Linux x86_64, statically linked, ~7 MB |
| `make build-arm64` | `dist/piper-arm64` — Linux aarch64 (DGX Spark, Jetson) |
| `make build-host` | `dist/piper[.exe]` — current OS/arch, dev convenience |
| `make test` / `test-race` / `lint` / `fmt` / `vet` | self-explanatory |

## Sudoers configuration (UFW)

`ufw status` requires root, so piper — which runs as a regular user — needs a
sudoers entry granting **passwordless** access to that one command. Without
it, `piper serve` will print a warning and the Web UI will show a "UFW data
unavailable" banner; `ss` and `docker` data still works.

Run this **once on the target host**, replacing `ymu` with your own username:

```bash
sudo tee /etc/sudoers.d/piper-ufw <<EOF
ymu ALL=(root) NOPASSWD: /usr/sbin/ufw status, /usr/sbin/ufw status numbered
EOF
sudo chmod 0440 /etc/sudoers.d/piper-ufw
```

The whitelist is **read-only** — piper still cannot add or remove UFW rules.
On non-Debian systems, swap `/usr/sbin/ufw` for the actual `which ufw` path.

## Quickstart

```bash
# Drop the binary on the target host
scp dist/piper user@h100:~/

# Configure passwordless `sudo ufw status` (see "Sudoers configuration" above)
# … then …

# Open the web port (one-time)
ssh user@h100 'sudo ufw allow 7878/tcp'

# Run the daemon (foreground; for systemd see design §10.4)
./piper serve --scan-interval 5m

# From any shell on the host:
piper check 8080
piper check 8080 9000-9005
piper suggest -n 3 --from 8000 --to 9999
piper reserve 9100 --name vllm-llama --note "next week"
piper history --port 9100 --days 7

# From a browser on the LAN:
http://h100:7878/
```

## CLI overview

```
piper [global flags] <command> [command flags]

Commands:
  check <ports...>     Inspect one or more ports (single, list, or range)
  list                 List ports filtered by state (--used / --free / --reserved / ...)
  scan                 Alias for `list --used --reserved`
  suggest              Suggest free ports in a range
  reserve <port>       Add an explicit reservation (--name, --note)
  release <port>       Remove an explicit reservation
  history              Query the event log
  serve                Run the HTTP daemon + scheduled scanner
  refresh              Trigger an immediate scan via the running daemon
                       (falls back to a local one-shot scan if not running)

Global flags:
  --data-dir PATH      Data directory (default ~/.piper, override via PIPER_DATA_DIR)
  --format text|json   Output format (default text)
  --output FILE        Tee output to FILE in addition to stdout
  --no-color           Disable ANSI color
```

Every subcommand emits both human-friendly text (with color + tree-drawing) and machine-readable JSON wrapped in a `{"ok":true,"data":...}` / `{"ok":false,"error":{...}}` envelope.

### Example: `piper check`

```
$ piper check 8080 8081 9000

Port 8080  ❌ IN USE (docker)
  ├─ Process: docker-proxy (pid 12453)
  ├─ Container: ai-translate-server
  │             image: translate:v2
  │             status: Up 3 days
  ├─ UFW: ALLOW (rule #5: 8080/tcp)
  └─ Other ports on this container:
      ├─ 8443  → UFW: ALLOW    ✅
      ├─ 9090  → UFW: (no rule, default deny)  ⚠️
      └─ 9091  → UFW: (no rule, default deny)  ⚠️

Port 8081  ⚠️  RESERVED (no listener)
  ├─ Source: implicit (stopped container)
  ├─ Container: subtitle-server (status: Exited 2 days ago)
  └─ This port is technically free; restart container to reclaim.

Port 9000  ✅ AVAILABLE
  └─ No listener, no reservation

Scanned at 2026-04-25 14:32:18
```

## Web UI

`piper serve` binds `0.0.0.0:7878` by default. Three pages:

- `/` — Overview: stateful ports table, "Check ports" / "Suggest free" forms, "Refresh now" button, "Last scan: X min ago".
- `/reservations` — explicit reservations with new/delete forms (write buttons render only for requests from `127.0.0.1`).
- `/history` — timeline of `occupied` / `released` / `reserved` / `unreserved` events.

Pico.css + HTMX, no build step, all assets embedded via `go:embed`. Works offline.

## Security model

Read access on the LAN, writes restricted to localhost — relies on UFW (or equivalent) for perimeter:

| From | Read | Write |
|------|------|-------|
| 127.0.0.1 | yes | yes |
| LAN (UFW-allowed) | yes | **no** (HTTP 403) |
| Internet | no (UFW deny) | no |

Write endpoints (`POST /api/reservations`, `DELETE /api/reservations/{port}`, `POST /api/scan/trigger`) are guarded by a `requireLocalhost` middleware on top of UFW. Web UI write buttons are also hidden when the request didn't come from `127.0.0.1`.

Piper never executes `sudo`, never modifies UFW rules, never starts/stops containers. It is read-only with respect to the system.

## Architecture

Strict layered dependency graph:

```
scanner ─┐
         ├─→ service ─→ cli
store   ─┘            ─→ server
```

- `internal/scanner/` — pure system reads (ss / docker / ufw) plus a thin orchestrator. Parsers are pure functions over `[]byte` so unit tests use captured fixtures and need no live system.
- `internal/store/` — SQLite via `modernc.org/sqlite`. CRUD for reservations, snapshots, history, plus `go:embed` migrations.
- `internal/service/` — only business-logic layer. Decision tree (`Checker`), suggestion algorithm (`Suggester`), snapshot diff (`DiffSnapshots`), scheduler.
- `internal/cli/` — cobra subcommand implementations.
- `internal/server/` — `net/http` handlers + HTMX/Pico templates (`go:embed`).

CLI and HTTP server share the same `service` layer, so behavior is identical regardless of entry point. See [`CLAUDE.md`](./CLAUDE.md) for the full architectural notes.

## Data layout

```
~/.piper/
└── piper.db          # SQLite — reservations, scan_snapshots, scan_meta, history
```

`piper.db` is mode `0600`, parent directory `0700`. History rows older than 30 days are auto-purged daily at midnight (`Asia/Taipei`).

## Phase 1 scope and non-goals

In scope: real-time queries, container expansion, explicit + implicit reservations, suggest free ports, web read panel, history.

Out of scope (deferred to later phases): cross-host federation, system mutations (no `ufw allow` / `docker rm`), authentication beyond UFW + localhost, port-range grouping, desktop GUI.

## License

TBD.
