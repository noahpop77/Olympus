# Olympus Grafana dashboards

Importable dashboard JSON for the Olympus load-testing stack.
These are **manual-import files** — nothing here is auto-provisioned, and
`docker-compose.yaml` was not modified.

## 1. Add the Prometheus datasource first

Grafana → **Connections → Data sources → Add data source → Prometheus**

| Field | Value |
|---|---|
| Name | `prometheus` |
| Prometheus server URL | `http://prometheus:9090` |

Click **Save & test** — it should report "Successfully queried the Prometheus API".

> The URL is `prometheus:9090` because Grafana queries Prometheus from inside the
> Docker network. Do **not** use `localhost:9090` here.

## 2. Import a dashboard

Grafana → **Dashboards → New → Import** → **Upload JSON file** → pick one of the
files below → when prompted, select the `prometheus` datasource → **Import**.

| File | Title | What it shows |
|---|---|---|
| `olympus-load.json` | Olympus — Load Test Overview | Total matches, active players (queue vs in-match), match spawn rate, requests/sec and p95 latency per service, requests/sec per endpoint |
| `olympus-health.json` | Olympus — Service Health | Blackbox probe success/duration for all four targets, per-endpoint request rate and p95 |
| `olympus-runtime.json` | Olympus — Runtime & Resources | Goroutines, heap, RSS, open FDs, CPU and GC rate per service |
| `00-recovered-old-dashboard.json` | New dashboard (recovered) | Your April 2025 dashboard, restored from `~/zaun/devenv/Olympus/grafana_data/grafana.db` |

The recovered dashboard had its old datasource UID (`defdsx2csk1dsb`) remapped to
the import-time datasource prompt. It also contains one empty panel
("Panel Title") that existed in the original.

## Useful queries

```promql
# matches per minute
sum(rate(matches_spawned{job="game_server_service"}[$__rate_interval])) * 60

# p95 latency, milliseconds
histogram_quantile(0.95,
  sum by (le) (rate(matchmaking_api_request_duration_seconds_bucket[$__rate_interval]))) * 1000

# healthy targets (should equal 4)
sum(probe_success{job="blackbox"})
```

## Notes / limitations

- `matches_spawned` is registered as a **gauge** (not a counter) in
  `game_server_service/metrics.go`. It only ever increases, so `rate()` works,
  but it resets to 0 when the game server restarts.
- There is **no Postgres exporter**, so DB connection/row metrics are not
  available. Add `postgres_exporter` if you want those panels.
- The services do not label responses by HTTP status code, so a true error-rate
  panel is not possible from the current metrics.
- Prometheus scrapes every 15s, so a 90s load test yields ~6 samples per series.
  For finer detail, lower `scrape_interval` in `prometheus.yml` (e.g. `5s`).
