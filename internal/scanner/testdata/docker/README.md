# testdata/docker — Synthetic Fixtures

> **SYNTHETIC FIXTURE — verify against real H100/Spark output before Phase 1 sign-off**
>
> All files in this directory are hand-crafted from the design doc spec and docker man pages.
> A real-fixture capture pass on the H100 and DGX Spark servers is tracked as a follow-up task.

## docker ps fixtures (`.txt` — one JSON object per line)

Each line is one JSON object as emitted by `docker ps -a --format '{{json .}}'`.

| File | Description |
|------|-------------|
| `ps_running_with_ports.txt` | 2 running containers: one with single TCP port mapping (8080->80), one with multi-port including UDP (9100->9100/tcp, 9200->9200/udp) |
| `ps_stopped.txt` | 1 exited container with a bound port mapping (8081->8081/tcp); used to test implicit reservation detection |
| `ps_no_ports.txt` | 1 running container with an empty Ports field; parser must emit a DockerEntry with empty Ports slice (not filter the container out) |
| `ps_expose_only.txt` | 1 running container with only `9090/tcp` (pure EXPOSE, no `->` binding); parser must emit a DockerEntry with empty Ports slice |
| `ps_mixed.txt` | 1 running container with a mix: `0.0.0.0:8080->80/tcp` (bound IPv4), `9090/tcp` (EXPOSE-only, dropped), `[::]:8080->80/tcp` (bound IPv6, HostIP="::"); verifies IPv6 bracket-stripping and EXPOSE filtering |

## docker inspect fixtures (`.json` — array with one container object)

Each file is the JSON output of `docker inspect <container>`, which is always an array.

| File | Description |
|------|-------------|
| `inspect_translate_server.json` | Full inspect for `ai-translate-server` (image `translate:v2`, state `running`). NetworkSettings.Ports has bound entries for 80->8080 and 443->8443, plus unbound null entries for 9090 and 9091. |
| `inspect_no_ports.json` | Container with `NetworkSettings.Ports = {}` (no port mappings at all); parser must return a DockerEntry with empty Ports slice. |
