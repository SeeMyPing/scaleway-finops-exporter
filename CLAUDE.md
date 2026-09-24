# CLAUDE.md

Guidance for AI assistants and contributors working on `scaleway-finops-exporter`.

## Project

A Prometheus exporter, written in Go, that exposes Scaleway billing (FinOps) and
environmental footprint (GreenOps) data.

## Hard rules

- Talk to Scaleway **only** through `github.com/scaleway/scaleway-sdk-go`. No
  `net/http` client to `api.scaleway.com` in production code. If an endpoint is
  missing from the SDK, stop and write an ADR first.
- Verify every SDK type, field and enum with `go doc` before using it. The SDK is
  `v1.0.0-beta.x` and is pinned in `go.mod`; its Go API can break between versions.
- Never call the Scaleway API during a scrape. Collectors only read immutable
  snapshots published by the refreshers.
- No global state, no `init()`, no `prometheus.DefaultRegisterer`.
- Never log, label or return secrets (secret keys, tokens).
- Code, comments, commit messages and docs are in English.

## Layout

```
cmd/scaleway-finops-exporter/  wiring only: config, dependencies, run
internal/config/               flags + env, validation
internal/scaleway/             thin adapters over the SDK (the only package importing it)
internal/source/{billing,finops,footprint}/  fetch + normalise into domain snapshots
internal/refresher/            background refresh loop, backoff, jitter
internal/collector/            prometheus.Collector implementations reading snapshots
internal/server/               HTTP server, /metrics, /healthz, /readyz
docs/adr/                      Architecture Decision Records
deploy/                        Kubernetes manifests, alert rules and their tests, dashboard
```

## Conventions

- Small interfaces, declared by the consumer package.
- Errors are wrapped with `fmt.Errorf("...: %w", err)` at package boundaries.
- Logging uses `log/slog` with snake_case attribute keys and static messages.
- Tests are table-driven, use `t.Parallel()` when safe, and never call `time.Sleep`:
  time-dependent code is tested with `testing/synctest`.
- SDK adapters are tested against `httptest.Server` fixtures in `testdata/`.
- Collectors are tested with golden exposition files (`testutil.CollectAndCompare`)
  and `testutil.CollectAndLint`. Regenerate goldens with `go test ./... -update`.
- Commits follow Conventional Commits (`feat(billing): ...`, `fix: ...`, `docs: ...`).
- Non-obvious decisions go into a short ADR in `docs/adr/`.

## Workflow

```
make fmt          # format
make lint         # golangci-lint v2
make test-race    # unit tests with race detector
make cover        # coverage on internal/, fails under 80 %
make ci           # everything the CI runs, except the image build and scan
```

Run `make lint test-race` before every commit.
