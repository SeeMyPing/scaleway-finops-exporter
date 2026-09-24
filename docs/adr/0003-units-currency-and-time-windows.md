# 3. Units, currency and time windows

- Status: accepted
- Date: 2026-09-24

## Context

Prometheus conventions require base units as metric name suffixes. The
Scaleway APIs return money as `scw.Money` (billing, FinOps) or as a float with
a separate currency (taxes), carbon in kilograms of CO₂ equivalent and water
in cubic meters, both as `float32`.

## Decision

### Money

- Metric names use the `_euros` suffix (`scaleway_billing_consumption_euros`).
  Scaleway bills in EUR; a `currency` label would make every query carry a
  label that never varies.
- Amounts are converted with `scw.Money.ToFloat()`.
- A row in any other currency is **skipped**, counted in
  `scaleway_exporter_source_errors_total{reason="unexpected_currency"}` and
  logged, so that a euro total is never silently wrong. Zero amounts are
  accepted whatever their currency: free usage has no value at all in the
  API, and a zero changes no sum.
- Amounts are cumulative over a billing period and can decrease (credits,
  corrections), so they are **gauges**, not counters.

### Environmental footprint

- Carbon is exported in **grams** of CO₂ equivalent
  (`scaleway_footprint_carbon_grams`): the gram is the Prometheus base unit
  for mass. The API returns kilograms.
- Water is exported in **cubic meters** (`scaleway_footprint_water_cubic_meters`),
  the SI unit returned by the API. Prometheus defines no base unit for volume.
- `float32` values are converted through their shortest decimal
  representation (`strconv.FormatFloat(v, 'e', -1, 32)`) with the power of ten
  shifted in the exponent. A plain `float64(v) * 1000` would turn 0.123 kg into
  123.00000339746475 g.

### Time windows

- Billing periods are UTC calendar months, labeled `billing_period="YYYY-MM"`.
  Every source uses the same label so that PromQL can join them (for example
  carbon intensity per euro).
- Billing and FinOps values cover the period so far. FinOps charges are
  requested with `clamp_to_time_range` so that a charge spanning several
  months only counts for the requested one.
- Footprint data is daily. The monthly metrics cover the **complete days**
  of the month so far (`[1st 00:00 UTC, today 00:00 UTC)`); on the first day
  of a month the current month has no data yet. The daily metrics
  (`scaleway_footprint_daily_*`) cover one complete UTC day, yesterday by
  default (`--footprint.daily-offset-days`). They are separate metric names so
  that `sum()` never mixes a day with a month.
- `scaleway_billing_period_info{billing_period, offset}` and the period
  start and end timestamps are computed from the clock, so PromQL can select
  the current period (`offset="0"`) and compute the elapsed fraction of the
  month.

## Consequences

- Deltas, projections and comparisons are computed in PromQL, not in the
  exporter.
- An organization billed in another currency would export no amounts until
  the naming decision is revisited; the alert `ScalewayUnexpectedCurrency`
  makes this visible.
- Scaleway may define billing periods in its own time zone; around midnight
  on the first of the month, the exporter can query the previous period for
  up to a couple of hours.
