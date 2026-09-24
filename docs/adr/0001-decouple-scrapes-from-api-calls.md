# 1. Decouple scrapes from Scaleway API calls

- Status: accepted
- Date: 2026-09-24

## Context

Scaleway billing data is updated a few times a day and footprint data daily.
The API calls are slow (several paginated requests per billing period) and
subject to rate limits. Prometheus scrapes every 15 s to 2 min, and a scrape
must answer well within its timeout.

Calling the API from `Collect()` would multiply API calls by the scrape rate,
make scrape latency depend on Scaleway, and turn every API hiccup into a
failed scrape and a gap in every series.

## Decision

- Each source (billing, finops, footprint) has its own background
  **refresher** goroutine (`internal/refresher`) with its own interval and
  timeout (defaults: 1 h, 1 h, 12 h).
- A refresh calls the API with a `context.WithTimeout` propagated to the SDK
  through `scw.WithContext`, then publishes an **immutable snapshot** through
  an `atomic.Pointer[T]`.
- Collectors only read the current snapshot and emit
  `prometheus.MustNewConstMetric`. They never call the API.
- On failure, the previous snapshot is kept. Failures are visible through
  `scaleway_exporter_source_up`,
  `scaleway_exporter_source_last_success_timestamp_seconds` and
  `scaleway_exporter_source_errors_total{reason}`.
- Refreshes are spread with ±10 % jitter; consecutive failures back off
  exponentially from 30 s up to the refresh interval.
- A refresh either succeeds as a whole or fails as a whole: a snapshot is
  never built from a partial set of API responses.

`atomic.Pointer` is preferred over a `sync.RWMutex`: there is a single writer
that replaces the whole value, and readers must never wait for a refresh.

## Consequences

- Scrapes are fast and independent of Scaleway availability.
- Metrics can be up to one refresh interval old; staleness is observable and
  alerted on (`ScalewayExporterDataStale`).
- `/readyz` reports ready once any enabled source has published a snapshot.
- Refresh timing is tested deterministically with `testing/synctest`.
