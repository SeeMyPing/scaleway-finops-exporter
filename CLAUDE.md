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
- Logging uses `log/slog` with snake_case attribute keys and static messages. Never use
  the `source` key: promslog uses it for the caller location. Use `data_source`.
- Tests are table-driven, use `t.Parallel()` when safe, and never call `time.Sleep`:
  time-dependent code is tested with `testing/synctest`.
- SDK adapters are tested against `httptest.Server` fixtures in `testdata/`.
- Collectors are tested with golden exposition files (`testutil.CollectAndCompare`)
  and `testutil.CollectAndLint`. Regenerate goldens with `go test ./... -update`.
- Commits follow Conventional Commits (`feat(billing): ...`, `fix: ...`, `docs: ...`).
- Non-obvious decisions go into a short ADR in `docs/adr/`.

## Metrics

- Prefix `scaleway_`, unit suffix, snake_case labels. Money is `_euros` (EUR
  only, other currencies are skipped and counted), carbon `_grams` (CO2e),
  water `_cubic_meters`, time `_timestamp_seconds`.
- Amounts are cumulative per `billing_period="YYYY-MM"` (UTC month) and are
  gauges. Never compute deltas or projections in the exporter: that is PromQL.
- Every metric is documented in the README table. Golden files in
  `internal/collector/testdata/` are checked by `promtool check metrics`.
- promlinter cannot resolve descriptors stored in struct fields; that
  specific message is excluded in `.golangci.yml`.

## SDK pitfalls found so far (SDK v1.0.0-beta.37)

- `scw.WithAllPages()` drops every non-list response field (`updated_at`,
  `total_discount_untaxed_value`), and does not support page tokens (FinOps).
  Adapters walk pages manually (ADR 0005).
- `scw.NewClient` validation errors quote the secret key: redact them.
- `ListTaxes` is organization-wide (no project, no category). `Charge` has
  no category nor product (join with `scaleway_billing_sku_info` in PromQL).
- Footprint values are `float32` in kgCO2e and m³; convert with
  `scaledFloat32` to avoid float32 artifacts.

## Workflow

```
make fmt          # format
make lint         # golangci-lint v2
make test-race    # unit tests with race detector
make cover        # coverage on internal/, fails under 80 %
make ci           # everything the CI runs, except the image build and scan
```

Run `make lint test-race` before every commit.

- Go version: `go.mod` (`go 1.26.0`, `toolchain go1.26.8`). The Docker build
  uses `GOTOOLCHAIN=local`, so bump the base image with the toolchain.
- `govet` `shadow` is disabled on purpose (it flags `if err := ...`).
- Actions are pinned by commit SHA with a version comment; resolve new SHAs
  with `git ls-remote https://github.com/<owner>/<repo> 'refs/tags/<tag>^{}'`.
- CI runs on pushes to `main` and on pull requests.
- Default port: 10056.
