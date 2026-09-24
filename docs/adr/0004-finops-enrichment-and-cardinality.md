# 4. FinOps enrichment in PromQL and cardinality guard

- Status: accepted
- Date: 2026-09-24

## Context

The FinOps API (`FinOpsAPI.ListCharges`) returns raw charges with a SKU,
a project and a resource, but, unlike the consumption API, **no category and
no product**. Per-resource charges can also produce a large number of series.

## Decision

### Enrichment

The billing source exports `scaleway_billing_sku_info{sku, category, product, unit} 1`
from the consumption rows. Category and product are added to charges at
query time:

```promql
scaleway_finops_charge_euros
  * on (sku) group_left (category, product) scaleway_billing_sku_info
```

Joining inside the exporter was rejected: it would couple two independent
sources and refreshers, and a FinOps refresh would depend on the freshness of
billing data. SKUs unknown to the consumption API simply stay without
category.

### Cardinality guard

- By default, charges are aggregated by project and SKU. Per-resource labels
  (`resource_id`, `resource_name`) are opt-in with `--finops.per-resource`.
- `--finops.max-series` (default 2000) bounds the series of each billing
  period. Above it, the `max-series - 1` series with the largest absolute
  amounts are kept and all the others are **summed** into one
  `sku="other"` series, so the total stays exact. Ties are broken on labels
  so that the result is deterministic.
- The exporter logs a warning, increments
  `scaleway_finops_series_limit_exceeded_total` and exposes the number of
  folded series in `scaleway_finops_folded_series`.

## Consequences

- The enrichment join fails with "many-to-many" only if a SKU has two
  descriptions; the billing source prevents it by keeping one description
  per SKU (the most recent period wins).
- Queries on `sku="other"` are meaningful only as a remainder.
