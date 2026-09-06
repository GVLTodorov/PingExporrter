# PingExporter

Self-built Prometheus ICMP ping exporter — a drop-in replacement for [`czerwonk/ping_exporter`](https://github.com/czerwonk/ping_exporter): same `config.yml` format, same port (`9427`), same metric names/labels, no changes needed to an existing compose file beyond the `image:` line. Built to close it out as a dependency on an unmaintained third-party image while keeping every existing Prometheus scrape config and Grafana panel working unchanged — see [PING_EXPORTER_REQUIREMENTS.md](./PING_EXPORTER_REQUIREMENTS.md) for the full background and acceptance criteria this was built against.

- One process, N targets — a YAML config file (`targets`, `dns`, `ping`, `options`), not env vars per target.
- Raw ICMP echo (IPv4 and, unless `disableIPv6: true`, IPv6), one shared socket per IP family rather than one per target.
- Targets can be IP literals or hostnames in the same list; hostnames are re-resolved on `dns.refresh` so dynamic DNS changes are picked up without a restart, and a resolved IP that disappears drops out of `/metrics` instead of leaving a stale series.
- Runs unprivileged: the image grants the compiled binary `cap_net_raw+ep` via `setcap` at build time instead of running as root or needing `cap_add: [NET_RAW]` in the compose file.

## Quick start

```bash
mkdir -p /srv/pingexporter
cat > /srv/pingexporter/config.yml <<'EOF'
targets:
  - 8.8.8.8
  - 1.1.1.1

dns:
  refresh: 5m
  nameserver: 1.1.1.1

ping:
  interval: 2s
  timeout: 3s
  history-size: 42
  size: 120

options:
  disableIPv6: false
EOF

docker run -d \
  --name pingexporter \
  -p 9427:9427 \
  -v /srv/pingexporter:/config:ro \
  ghcr.io/gvltodorov/pingexporter:latest
```

Then scrape:

```bash
curl http://localhost:9427/metrics
```

## docker-compose

The exact block this was built to slot into (only `image:` changes from `czerwonk/ping_exporter:latest`):

```yaml
  pingexporter:
    image: ghcr.io/gvltodorov/pingexporter:latest
    container_name: pingexporter
    restart: unless-stopped
    ports:
      - 9427:9427
    volumes:
      - /srv/pingexporter:/config:ro
    networks:
      - proxy
```

## Configuration

| Variable      | Default              | Description                                              |
|---------------|-----------------------|------------------------------------------------------------|
| `CONFIG_FILE` | `/config/config.yml` | Path to the YAML config file.                               |
| `LISTEN_ADDR` | `:9427`               | Address the `/metrics` HTTP server listens on.              |

### Config file (`config.yml`)

```yaml
targets:
  - 46.229.202.209   # IP literal
  - some.host.name   # hostname, re-resolved every dns.refresh

dns:
  refresh: 2m15s      # how often hostname targets are re-resolved
  nameserver: 1.1.1.1  # resolver used for that re-resolution

ping:
  interval: 2s         # how often each target is pinged
  timeout: 3s          # per-probe timeout before it counts as lost
  history-size: 42     # rolling window size (in probes) for the stats below
  size: 120             # ICMP echo payload size, in bytes

options:
  disableIPv6: false    # when false, hostnames resolving to AAAA are also pinged over ICMPv6
```

`targets` accepts a mix of IP literals and hostnames in the same list. Every field has a sane built-in default (see `config.go`), so a minimal file only needs `targets`.

## Exposed metrics

| Metric                            | Type  | Labels                        | Notes                                                              |
|------------------------------------|-------|---------------------------------|-----------------------------------------------------------------------|
| `ping_loss_ratio`                  | gauge | `ip`, `ip_version`, `target`   | Fraction of probes lost in the current `history-size` window, `0.0`–`1.0`. |
| `ping_rtt_best_seconds`            | gauge | `ip`, `ip_version`, `target`   | Minimum RTT in the current window, in seconds.                        |
| `ping_rtt_mean_seconds`            | gauge | `ip`, `ip_version`, `target`   | Mean RTT in the current window, in seconds.                           |
| `ping_rtt_std_deviation_seconds`   | gauge | `ip`, `ip_version`, `target`   | Standard deviation of RTT in the window, in seconds.                  |
| `ping_rtt_worst_seconds`           | gauge | `ip`, `ip_version`, `target`   | Maximum RTT in the current window, in seconds.                        |
| `ping_up`                          | gauge | `version`                       | Always `1` while the process is alive; liveness sentinel, distinct from per-target reachability (`ping_loss_ratio == 1` signals that). |

`target` is the value as given in `targets:`; `ip` is the address actually pinged (equal to `target` for IP literals); `ip_version` is `"4"` or `"6"`. Standard `go_*`/`process_*`/`promhttp_*` runtime metrics are also present, as with any `promhttp`-instrumented Go exporter.

## Development

```bash
go build ./...
go vet ./...
go test ./... -v -cover
```

Tests stub the raw-socket `pinger` interface and, for hostname resolution, the resolver's `LookupIPAddr` call, so no real network access or `CAP_NET_RAW` is required to run the suite. `manager` spawns one long-lived goroutine per resolved target/IP; `go.uber.org/goleak` backs the whole package via `TestMain` to catch anything left running past the test that started it.

## Releases

Every push to `main` bumps a semver tag (patch by default) and publishes `ghcr.io/gvltodorov/pingexporter:<version>` plus `:latest`. Tests must pass first.
