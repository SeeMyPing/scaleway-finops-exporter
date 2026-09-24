# scaleway-finops-exporter

A Prometheus exporter for **Scaleway FinOps and GreenOps**: what your Scaleway
organization costs, per project, category, product and SKU, and what it emits,
in estimated carbon and water, per project, region and service.

- **Invoice-aligned consumption and taxes** from the Billing API.
- **Raw charges** from the FinOps API, per SKU or per resource, with a
  cardinality guard.
- **Environmental footprint** (carbon and water) from the Environmental
  Footprint API, per month and per day.
- Talks to Scaleway only through the official
  [Go SDK](https://github.com/scaleway/scaleway-sdk-go). Never calls the API
  during a scrape: data is refreshed in the background and served from memory.
- Ready for production: TLS and basic auth through the Prometheus
  exporter-toolkit, distroless non-root image, signed releases, alert rules
  with tests, and a Grafana dashboard.

Deltas, projections and comparisons are left to PromQL: see
[PromQL examples](#promql-examples).

## Contents

- [Quickstart](#quickstart)
- [IAM permissions](#iam-permissions)
- [Configuration](#configuration)
- [Metrics](#metrics)
- [PromQL examples](#promql-examples)
- [Alerts and dashboard](#alerts-and-dashboard)
- [Known limitations](#known-limitations)
- [Development](#development)

## Quickstart

Create an API key with the [permissions below](#iam-permissions), then export
the usual Scaleway variables. A profile in `~/.config/scw/config.yaml` works too.

```sh
export SCW_ACCESS_KEY=SCWXXXXXXXXXXXXXXXXX
export SCW_SECRET_KEY=<secret key>
export SCW_DEFAULT_ORGANIZATION_ID=<organization ID>
```

### Binary

Download an archive from the
[releases](https://github.com/SeeMyPing/scaleway-finops-exporter/releases),
or build from source with Go 1.26 or newer:

```sh
make build
./bin/scaleway-finops-exporter
curl -s localhost:10056/metrics | grep ^scaleway_
```

### Docker

```sh
docker run --rm -p 10056:10056 \
  -e SCW_ACCESS_KEY -e SCW_SECRET_KEY -e SCW_DEFAULT_ORGANIZATION_ID \
  ghcr.io/seemyping/scaleway-finops-exporter:latest
```

The image is `distroless/static` running as `nonroot` (UID 65532). It is signed
with cosign; verify it with:

```sh
cosign verify ghcr.io/seemyping/scaleway-finops-exporter:<version> \
  --certificate-identity-regexp 'https://github.com/SeeMyPing/scaleway-finops-exporter/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Kubernetes

```sh
kubectl create namespace monitoring
kubectl -n monitoring create secret generic scaleway-finops-exporter \
  --from-literal=access-key=SCWXXXXXXXXXXXXXXXXX \
  --from-literal=organization-id=<organization ID> \
  --from-file=secret-key=./secret-key
kubectl apply -k deploy/kubernetes
```

The manifests in [`deploy/kubernetes`](deploy/kubernetes) run a single replica
with a read-only root filesystem, no capabilities and no service account
token, mount the secret key as a file (`--scaleway.secret-key-file`), and
include a `ServiceMonitor` for the Prometheus Operator. Pin the image tag in
`kustomization.yaml`.

`/healthz` answers as soon as the process runs. `/readyz` answers once at
least one enabled source has completed a successful refresh.

## IAM permissions

Create a dedicated IAM application with an API key, and a policy on the
**organization** with these permission sets:

| Permission set | Needed by |
|---|---|
| `BillingReadOnly` | Billing source (consumption, taxes) and FinOps source (charges) |
| `EnvironmentalImpactReadOnly` | Footprint source |

Disable a source (`--no-footprint.enabled`) to drop its permission set. The
exporter never writes anything.

## Configuration

Every flag can also be set with an environment variable: upper-case the flag,
replace `.` and `-` with `_`, and prefix with `SCALEWAY_FINOPS_EXPORTER_`. For
example `--billing.interval` becomes `SCALEWAY_FINOPS_EXPORTER_BILLING_INTERVAL`.
Flags win over the environment. The configuration is validated at startup and
every problem is reported at once.

**Credentials** are resolved like the Scaleway CLI does: the profile from the
configuration file (`SCW_CONFIG_PATH`, `SCW_PROFILE`, `--scaleway.profile`),
overridden by `SCW_*` variables, overridden by the flags below. Secrets are
never logged, and SDK errors that could quote them are redacted.

| Flag | Default | Description |
|---|---|---|
| `--scaleway.organization-id` | SDK default | Organization to export. Falls back to `SCW_DEFAULT_ORGANIZATION_ID` or the profile. |
| `--scaleway.project-id` | all | Only export these projects. Repeatable. Taxes and discounts stay organization-wide. |
| `--scaleway.profile` | active profile | Profile of the Scaleway configuration file. |
| `--scaleway.secret-key-file` | | File holding the secret key, for Kubernetes secrets. |
| `--scaleway.http-timeout` | `60s` | Timeout of a single HTTP request. |
| `--[no-]billing.enabled` | `true` | Consumption and taxes source. |
| `--billing.interval` | `1h` | Refresh interval (minimum `1m`). |
| `--billing.timeout` | `2m` | Timeout of one refresh (at most the interval). |
| `--billing.lookback-periods` | `1` | Previous months exported in addition to the current one (0 to 12). |
| `--[no-]finops.enabled` | `true` | FinOps raw charges source. |
| `--finops.interval` | `1h` | Refresh interval. |
| `--finops.timeout` | `5m` | Timeout of one refresh. |
| `--finops.lookback-periods` | `0` | Previous months exported. |
| `--[no-]finops.per-resource` | `false` | Add `resource_id` and `resource_name` labels. |
| `--finops.max-series` | `2000` | Series per billing period before aggregating under `sku="other"`. |
| `--[no-]footprint.enabled` | `true` | Environmental footprint source. |
| `--footprint.interval` | `12h` | Refresh interval. |
| `--footprint.timeout` | `2m` | Timeout of one refresh. |
| `--footprint.lookback-periods` | `1` | Previous months exported. |
| `--footprint.daily-offset-days` | `1` | Day of the daily metrics, counted back from today (1 = yesterday). |
| `--web.listen-address` | `:10056` | Listen address. Repeatable. |
| `--web.config.file` | | [exporter-toolkit web configuration](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md) for TLS and basic auth. |
| `--web.telemetry-path` | `/metrics` | Metrics path. |
| `--web.shutdown-timeout` | `10s` | Grace period for in-flight requests on shutdown. |
| `--log.level` | `info` | `debug`, `info`, `warn` or `error`. |
| `--log.format` | `logfmt` | `logfmt` or `json`. |

Port 10056 is not allocated in the Prometheus
[default port allocations](https://github.com/prometheus/prometheus/wiki/Default-port-allocations)
(9503 is used by another Scaleway exporter).

## Metrics

All amounts are **untaxed euros** unless stated. Money and footprint values are
**cumulative over a billing period** (a UTC calendar month, `billing_period="YYYY-MM"`)
and can decrease after credits or corrections, so they are gauges. The
exporter serves the last successful snapshot; the "Refresh" column is the
default interval.

### Billing (Billing API, `ListConsumptions` and `ListTaxes`)

| Metric | Type | Labels | Description | Refresh |
|---|---|---|---|---|
| `scaleway_billing_consumption_euros` | gauge | `organization_id`, `project_id`, `project_name`, `category`, `product`, `billing_period` | Consumption of the period so far, as on the invoice. | 1h |
| `scaleway_billing_sku_info` | gauge (1) | `sku`, `category`, `product`, `unit` | Category, product and unit of each SKU, to enrich FinOps charges. | 1h |
| `scaleway_billing_discount_euros` | gauge | `organization_id`, `billing_period` | Organization-wide discounts, as reported by the API. | 1h |
| `scaleway_billing_tax_euros` | gauge | `organization_id`, `billing_period`, `description` | Taxes of the period so far. | 1h |
| `scaleway_billing_tax_rate_ratio` | gauge | `organization_id`, `billing_period`, `description` | Tax rate (0.2 = 20 %). | 1h |
| `scaleway_billing_last_update_timestamp_seconds` | gauge | `organization_id`, `billing_period` | Last update of the data by Scaleway. | 1h |
| `scaleway_billing_period_info` | gauge (1) | `billing_period`, `offset` | Exported periods; `offset="0"` is the current one. Computed from the clock. | scrape |
| `scaleway_billing_period_start_timestamp_seconds` | gauge | `billing_period` | Start of the period (inclusive, UTC). | scrape |
| `scaleway_billing_period_end_timestamp_seconds` | gauge | `billing_period` | End of the period (exclusive, UTC). | scrape |

The Billing API reports taxes per organization only, not per project or category.

### FinOps (FinOps API, `ListCharges`)

| Metric | Type | Labels | Description | Refresh |
|---|---|---|---|---|
| `scaleway_finops_charge_euros` | gauge | `organization_id`, `project_id`, `project_name`, `sku`, `billing_period`, plus `resource_id`, `resource_name` with `--finops.per-resource` | Raw charges of the period so far, clamped to the period. | 1h |
| `scaleway_finops_folded_series` | gauge | `organization_id`, `billing_period` | Series aggregated under `sku="other"` by `--finops.max-series`. | 1h |
| `scaleway_finops_series_limit_exceeded_total` | counter | | Period refreshes that hit `--finops.max-series`. | 1h |
| `scaleway_finops_last_update_timestamp_seconds` | gauge | `organization_id`, `billing_period` | Most recent charge update. | 1h |

The FinOps API returns no category nor product: join with
`scaleway_billing_sku_info` (see [examples](#promql-examples)).

### GreenOps (Environmental Footprint API, `GetImpactData`)

| Metric | Type | Labels | Description | Refresh |
|---|---|---|---|---|
| `scaleway_footprint_carbon_grams` | gauge | `organization_id`, `project_id`, `region`, `zone`, `service_category`, `product_category`, `billing_period` | Estimated emissions of the month in grams of CO₂e. The current month covers the complete days so far. | 12h |
| `scaleway_footprint_water_cubic_meters` | gauge | same | Estimated water consumption of the month in m³. | 12h |
| `scaleway_footprint_daily_carbon_grams` | gauge | same without `billing_period` | Emissions of the last complete UTC day (`--footprint.daily-offset-days`). | 12h |
| `scaleway_footprint_daily_water_cubic_meters` | gauge | same without `billing_period` | Water consumption of that day. | 12h |
| `scaleway_footprint_period_end_timestamp_seconds` | gauge | `organization_id`, `billing_period` | End (exclusive) of the data covered by the monthly metrics. | 12h |
| `scaleway_footprint_daily_window_start_timestamp_seconds` | gauge | `organization_id` | Start of the day covered by the daily metrics. | 12h |

`zone` is empty for regional products such as Object Storage. The API reports
kgCO₂e; the exporter converts to grams, the Prometheus base unit.

### Exporter health

| Metric | Type | Labels | Description |
|---|---|---|---|
| `scaleway_exporter_source_up` | gauge | `source` | 1 if the last refresh succeeded, 0 otherwise (and before the first refresh). |
| `scaleway_exporter_source_last_success_timestamp_seconds` | gauge | `source` | Time of the last successful refresh. |
| `scaleway_exporter_source_refresh_duration_seconds` | histogram | `source` | Duration of refreshes. |
| `scaleway_exporter_source_errors_total` | counter | `source`, `reason` | Errors by reason: `timeout`, `canceled`, `auth`, `rate_limited`, `server`, `client`, `decode`, `network`, `unknown`, and `unexpected_currency` for skipped rows. |
| `scaleway_exporter_build_info` | gauge (1) | `version`, `revision`, `branch`, `goversion`, `goos`, `goarch`, `tags` | Build information. |

The Go runtime (`go_*`) and process (`process_*`) metrics are exported too.

## PromQL examples

The examples below use this selector for the **current billing period**:

```promql
scaleway_billing_consumption_euros and on (billing_period) scaleway_billing_period_info{offset="0"}
```

**Consumption this month, per project:**

```promql
sum by (project_name) (
  scaleway_billing_consumption_euros and on (billing_period) scaleway_billing_period_info{offset="0"}
)
```

**Cost of the day** (spend over the last 24 hours). Use `delta()`, not
`increase()`: these are gauges, and `increase()` would read a credit as a
counter reset. The value is negative right after a period change.

```promql
delta(
  (sum(scaleway_billing_consumption_euros and on (billing_period) scaleway_billing_period_info{offset="0"}))[1d:1h]
)
```

**End-of-month projection** with `predict_linear` over the last 3 days:

```promql
predict_linear(
  (sum(scaleway_billing_consumption_euros and on (billing_period) scaleway_billing_period_info{offset="0"}))[3d:1h],
  scalar(max(scaleway_billing_period_end_timestamp_seconds and on (billing_period) scaleway_billing_period_info{offset="0"})) - time()
)
```

or, linearly from the start of the month:

```promql
sum(scaleway_billing_consumption_euros and on (billing_period) scaleway_billing_period_info{offset="0"})
/ scalar(max(
    (time() - scaleway_billing_period_start_timestamp_seconds)
    / (scaleway_billing_period_end_timestamp_seconds - scaleway_billing_period_start_timestamp_seconds)
    and on (billing_period) scaleway_billing_period_info{offset="0"}
))
```

**This month versus last month.** The previous period is complete, so compare
it with the projection, or compare month-to-date values at the same point of
the month with `offset`:

```promql
  sum(scaleway_billing_consumption_euros and on (billing_period) scaleway_billing_period_info{offset="0"})
/ sum(
    scaleway_billing_consumption_euros offset 30d
    and on (billing_period) scaleway_billing_period_info{offset="0"} offset 30d
  )
```

The `offset 30d` modifier applies to a single selector, so both sides of
`and` need it: 30 days ago, `offset="0"` designated the previous month.

**Top 5 most expensive products this month:**

```promql
topk(5, sum by (product) (
  scaleway_billing_consumption_euros and on (billing_period) scaleway_billing_period_info{offset="0"}
))
```

**FinOps charges with their category and product:**

```promql
sum by (project_name, sku) (scaleway_finops_charge_euros)
  * on (sku) group_left (category, product) scaleway_billing_sku_info
```

**Carbon intensity in kgCO₂e per euro**, on the last complete month:

```promql
  sum(scaleway_footprint_carbon_grams and on (billing_period) scaleway_billing_period_info{offset="1"}) / 1000
/ sum(scaleway_billing_consumption_euros and on (billing_period) scaleway_billing_period_info{offset="1"})
```

**Emissions by service category yesterday**, in kgCO₂e:

```promql
sum by (service_category) (scaleway_footprint_daily_carbon_grams) / 1000
```

## Alerts and dashboard

- [`deploy/prometheus/rules.yml`](deploy/prometheus/rules.yml): recording rules
  for the current-period consumption and its projection, and alerts on budget
  exceeded, budget forecast, burn rate (twice the daily budget), failing or
  stale sources, a missing exporter, billing data not updated by Scaleway,
  the FinOps series limit and non-EUR amounts. **Set your budget** in
  `scaleway:billing_monthly_budget_euros`. The rules are unit tested in
  [`rules_test.yml`](deploy/prometheus/rules_test.yml)
  (`make rules-test`). With the Prometheus Operator, wrap the `groups` in a
  `PrometheusRule` resource.
- [`deploy/grafana/scaleway-finops.json`](deploy/grafana/scaleway-finops.json):
  a dashboard with FinOps, GreenOps and exporter health rows, filterable by
  organization and project.

## Known limitations

- **Not real time.** Scaleway updates consumption a few times a day and
  footprint data daily. Values lag real usage by hours; projections are lower
  bounds. `scaleway_billing_last_update_timestamp_seconds` shows the lag.
- **FinOps and Billing differ.** The FinOps API returns raw charges, the
  Billing API invoice-aligned consumption. Rounding and aggregation differ,
  so their totals do not match exactly. Use Billing for budgets and invoices,
  FinOps for per-resource analysis.
- **The Environmental Footprint API is alpha** (`v1alpha1`) and only covers
  part of the products. Values are estimates (ADEME methodology) and can be
  revised by Scaleway. A warning is logged if the SKU values of a project do
  not add up to its total.
- **The Scaleway SDK is beta** (`v1.0.0-beta.x`); it is pinned and updated
  deliberately.
- **EUR only.** Amounts in another currency are skipped and counted (see
  [ADR 3](docs/adr/0003-units-currency-and-time-windows.md)).
- **UTC months.** Billing periods are computed in UTC.
- `resource_name` is only reported for products that forward it.

## Development

```sh
make help             # list targets
make lint test-race   # before every commit
make ci               # everything the CI runs, except the image build and scan
make test-integration # against the real API, with SCW_* credentials
```

See [CONTRIBUTING.md](CONTRIBUTING.md), the
[architecture decision records](docs/adr), and [SECURITY.md](SECURITY.md) to
report a vulnerability.

## License

[Apache License 2.0](LICENSE).
