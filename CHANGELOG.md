# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Billing source: invoice-aligned consumption per project, category and
  product (`scaleway_billing_consumption_euros`), SKU descriptions
  (`scaleway_billing_sku_info`), discounts, taxes and tax rates, and the last
  update time of the data, for the current and previous billing periods.
- FinOps source: raw charges per project and SKU, or per resource with
  `--finops.per-resource`, bounded by `--finops.max-series`
  (`scaleway_finops_charge_euros`, `scaleway_finops_folded_series`,
  `scaleway_finops_series_limit_exceeded_total`).
- Footprint source: estimated carbon (grams of CO₂e) and water (m³) per
  project, region, zone, service and product category, per month and for the
  last complete day.
- `scaleway_billing_period_info` and period start and end timestamps to
  select the current billing period in PromQL.
- Background refreshers with jitter, exponential backoff and timeouts;
  health metrics per source; `/healthz` and `/readyz`.
- Configuration through flags and `SCALEWAY_FINOPS_EXPORTER_*` environment
  variables; Scaleway credentials from the SDK profile, `SCW_*` variables or a
  secret key file.
- TLS and basic auth through the exporter-toolkit web configuration.
- Kubernetes manifests, Prometheus alert rules with unit tests and a Grafana
  dashboard.
- Distroless non-root container image, signed multi-arch releases with SBOMs
  and SLSA provenance.

[Unreleased]: https://github.com/SeeMyPing/scaleway-finops-exporter/commits/main
