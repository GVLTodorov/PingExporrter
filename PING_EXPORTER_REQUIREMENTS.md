# ping_exporter — Requirements (for a self-built replacement)

Source of truth for these requirements: the running `czerwonk/ping_exporter:latest`
container on **l11** (`l11/diagnostic/docker-compose.yml`, service `pingexporter`),
its live config at `/srv/pingexporter/config.yml`, and its actual `/metrics` output
captured 2026-09-06. Anything not directly observed is marked *(assumed / upstream
behavior, verify before relying on it)*.

## 1. Goal

Build a drop-in replacement for `czerwonk/ping_exporter` that can be swapped into
the existing compose stack with no changes to `docker-compose.yml` other than the
`image:` line — same config file format, same port, same metric names/labels.

## 2. Current deployment (must remain compatible)

```yaml
pingexporter:
  image: czerwonk/ping_exporter:latest
  container_name: pingexporter
  restart: unless-stopped
  ports:
    - 9427:9427
  volumes:
    - /srv/pingexporter:/config:ro
  networks:
    - proxy
```

- Config file is mounted read-only at `/config/config.yml` (directory `/config` in
  the container, single file `config.yml` inside it).
- No `privileged`, no explicit `cap_add`, no `NET_RAW` grant, no `user:` override in
  the compose file — so the image must be able to send ICMP echo without extra
  Docker privileges (upstream does this via `setcap cap_net_raw+ep` on the binary
  at image build time, or by running as root inside the container by default).
- HTTP port `9427` exposed for Prometheus to scrape `/metrics`.
- Prometheus scrapes it as a normal target on the `proxy` network.

## 3. Config file format

Observed live config (`/srv/pingexporter/config.yml`):

```yaml
targets:
  - 46.229.202.209
  - 8.8.4.4
  - 8.8.8.8
  - 1.1.1.1
  - 1.0.0.1
  - 192.168.1.1

dns:
  refresh: 2m15s
  nameserver: 1.1.1.1

ping:
  interval: 2s
  timeout: 3s
  history-size: 42
  size: 120

options:
  disableIPv6: false
```

### 3.1 `targets`
- A YAML list. Each entry is a plain string: an IPv4/IPv6 literal or a hostname.
- Must support a mix of IP literals and DNS names in the same list (hostnames get
  resolved; IP literals are used as-is).
- *(Upstream also supports a map form per target — e.g. `{target: host, name: alias}`
  — to override the Prometheus `target` label with a friendly name. Not exercised
  by this deployment; treat as a nice-to-have, not a hard requirement.)*

### 3.2 `dns`
- `refresh`: Go-style duration (`2m15s`). Interval at which hostnames in `targets`
  are re-resolved, so DNS changes (e.g. dynamic DNS) are picked up without a
  restart. IP-literal targets are unaffected.
- `nameserver`: resolver to use for that re-resolution, overriding the system
  resolver (here pinned to `1.1.1.1`).

### 3.3 `ping`
- `interval`: how often each target is pinged (`2s`).
- `timeout`: per-probe timeout before counting a reply as lost (`3s`).
- `history-size`: number of most-recent RTT samples kept per target to compute the
  rolling best/worst/mean/stddev/loss stats exposed as metrics (`42`).
- `size`: ICMP echo payload size in bytes (`120`).

### 3.4 `options`
- `disableIPv6`: `true`/`false`. When `false`, hostnames that resolve to AAAA
  records are pinged over ICMPv6 as well (see `ip_version` label below).

### 3.5 Reload behavior
- *(Assumed upstream behavior — not verified here)*: config changes require a
  container restart under this deployment (no `SIGHUP`-based reload is used in
  the current setup). A self-built version only needs to reload on startup; live
  reload is a stretch goal, not a requirement.

## 4. Metrics — must match exactly

Captured from the running exporter's `/metrics` endpoint (labels/values elided
where not structurally relevant):

```
ping_loss_ratio{ip="<ip>",ip_version="<4|6>",target="<target>"} <0.0-1.0>
ping_rtt_best_seconds{ip="<ip>",ip_version="<4|6>",target="<target>"} <seconds, float>
ping_rtt_mean_seconds{ip="<ip>",ip_version="<4|6>",target="<target>"} <seconds, float>
ping_rtt_std_deviation_seconds{ip="<ip>",ip_version="<4|6>",target="<target>"} <seconds, float>
ping_rtt_worst_seconds{ip="<ip>",ip_version="<4|6>",target="<target>"} <seconds, float>
ping_up{version="<exporter version>"} 1
```

Requirements per metric:

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `ping_loss_ratio` | gauge | `ip`, `ip_version`, `target` | Fraction of probes lost in the current `history-size` window, `0.0`–`1.0`. |
| `ping_rtt_best_seconds` | gauge | `ip`, `ip_version`, `target` | Minimum RTT in the current window, in seconds (not ms). |
| `ping_rtt_mean_seconds` | gauge | `ip`, `ip_version`, `target` | Mean RTT in the current window, seconds. |
| `ping_rtt_std_deviation_seconds` | gauge | `ip`, `ip_version`, `target` | Standard deviation of RTT in the window, seconds. |
| `ping_rtt_worst_seconds` | gauge | `ip`, `ip_version`, `target` | Maximum RTT in the current window, seconds. |
| `ping_up` | gauge | `version` | Always `1` while the exporter process is alive; `version` label carries the exporter's own build version string. Used as a scrape/liveness sentinel, distinct from per-target reachability (that's what `ping_loss_ratio == 1` signals). |

Label semantics:
- `target` — the value as given in `targets:` (IP or hostname), i.e. what the user
  configured.
- `ip` — the resolved IP actually pinged (equal to `target` when `target` is
  already a literal IP).
- `ip_version` — `"4"` or `"6"`, one series per resolved address family for
  hostnames that resolve to both.

Standard Prometheus Go-runtime/process metrics (`go_*`, `process_*`,
`promhttp_*`) are also expected on `/metrics`, as with any `promhttp`-instrumented
Go exporter — not itemized here since they come free from the client library.

## 5. HTTP server

- Listens on `0.0.0.0:9427` *(port is hardcoded by this deployment's port mapping;
  make it configurable with `9427` as the default so the existing compose file
  needs no changes)*.
- Serves Prometheus text-format metrics at `GET /metrics`.
- *(Assumed)*: a landing page at `GET /` with a link to `/metrics` — cosmetic,
  not required for Prometheus scraping to work.

## 6. Runtime / container requirements

- Must be able to send raw ICMP echo requests (IPv4) and ICMP6 echo requests
  (IPv6, when `disableIPv6: false`) **without** the compose file granting
  `privileged: true`, `cap_add: [NET_RAW]`, or running as a specific `user:`.
  Concretely, the built image must either:
  - run its process as root inside the container (simplest), or
  - `setcap cap_net_raw+ep` on the compiled binary at image-build time and run as
    a non-root user (matches upstream's approach and is preferable for a
    from-scratch rebuild).
- Reads its config from `/config/config.yml` inside the container (path is fixed
  by the existing bind mount `/srv/pingexporter:/config:ro`).
- No writes needed anywhere in the container filesystem — the existing mount is
  read-only.
- Should start and begin serving `/metrics` within a couple of seconds of
  container start (Prometheus scrape interval on this host is short; a slow
  cold-start would show as gaps in `ping_up`).
- Restart policy in the stack is `unless-stopped`; the exporter should exit
  non-zero on fatal config errors (bad YAML, unresolvable target at startup)
  rather than hang, so Docker's restart policy can surface the failure via
  `docker ps` status/repeated restarts.

## 7. Non-functional requirements

- **Resource footprint**: comparable to the upstream binary — this is a small Go
  static binary pinging 6 targets every 2s; should run comfortably in a few MB of
  RAM and negligible CPU on l11.
- **Logging**: plain stdout/stderr logs (so `docker logs pingexporter` works),
  since that's how every other container in this stack is diagnosed.
- **Image size**: prefer a small final image (multi-stage build, scratch/distroless
  or alpine base) — consistent with the other lightweight exporters in this stack
  (node-exporter, smartctl-exporter).
- **Architecture**: must run on whatever architecture l11 is (verify host arch —
  this stack currently assumes linux/amd64 images across the board).

## 8. Explicit non-goals (for v1 of the rebuild)

- Config hot-reload via signal.
- TLS/basic-auth on the `/metrics` endpoint (not used by any exporter in this
  stack; Prometheus scrapes everything in plaintext over the internal `proxy`
  network).
- Per-target label aliasing (`name:` override) — nice-to-have, not required to
  match current behavior since the live config doesn't use it.
- IPv6-only targets beyond what `disableIPv6: false` already covers — the current
  target list is all IPv4 addresses.

## 9. Acceptance criteria

A replacement image is considered a valid drop-in when, using the **exact same**
`/srv/pingexporter/config.yml` and the **exact same** compose service block
(only `image:` changed):

1. `docker logs pingexporter` shows no errors and the container stays `Up`
   (not restarting).
2. `curl http://l11:9427/metrics` returns Prometheus text format containing all
   six metric families listed in §4, with one series per target/IP-version
   combination present in the current config (6 targets × 1 IP version = 6 series
   per RTT/loss metric, plus one `ping_up`).
3. Existing Prometheus scrape config and any Grafana panels built against
   `ping_loss_ratio`, `ping_rtt_*_seconds`, and `ping_up` continue to render with
   no query changes.
4. Stopping a target's connectivity (e.g. blocking `192.168.1.1` at the router)
   drives that target's `ping_loss_ratio` toward `1` within `history-size *
   interval` seconds, without affecting other targets' series or crashing the
   exporter.
