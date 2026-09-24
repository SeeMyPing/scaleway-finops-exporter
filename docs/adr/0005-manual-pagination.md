# 5. Walk API pages manually instead of using scw.WithAllPages

- Status: accepted
- Date: 2026-09-24

## Context

`scw.WithAllPages()` is the SDK mechanism to fetch every page of a list call.
Two limitations were found in SDK `v1.0.0-beta.37`:

1. For page-number lists (`ListConsumptions`, `ListTaxes`), the SDK merges the
   pages through the generated `UnsafeAppend` method, which only appends the
   list and adds to `total_count`. **All other response fields are dropped**:
   `updated_at` and `total_discount_untaxed_value` always come back empty.
   A unit test against a paginated fixture caught it.
2. `ListChargesResponse` (FinOps) paginates with an opaque `next_page_token`
   and implements no `UnsafeAppend`, so `WithAllPages()` returns an error.

## Decision

The adapters walk the pages themselves with the SDK request fields (`Page`,
`PageSize`, `PageToken`), still through the SDK client:

- Billing: request pages 1, 2, ... with the maximum page size (100) until a
  page is empty or the number of items reaches `total_count`. The first
  page's scalar fields are kept.
- FinOps: follow `next_page_token` until it is empty. A repeated token is
  an error, to avoid an infinite loop.
- Both stop with an error after 10 000 pages.

## Consequences

- No custom HTTP client: the SDK still builds, authenticates and decodes
  every request.
- To revisit when the SDK merges scalar fields or supports token pagination.
