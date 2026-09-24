# 2. Test SDK adapters against an httptest server with hand-written fixtures

- Status: accepted
- Date: 2026-09-24

## Context

The adapters in `internal/scaleway` are the only code that touches the
Scaleway SDK. Their tests must exercise the real SDK: request building,
query parameters, JSON decoding and error mapping, without network access
and without credentials.

Two options were evaluated:

1. **The SDK VCR helpers** (`internal/testhelpers/httprecorder`, built on
   `go-vcr`). They live under the SDK's `internal/` directory and **cannot be
   imported** by another module. Using `go-vcr` directly would require
   recording cassettes against the real API with real credentials, then
   scrubbing identifiers and amounts from them: hard to review, easy to leak,
   and impossible to use for edge cases (5xx, malformed JSON, pagination
   loops) that the real API will not produce on demand.
2. **An `httptest.Server`** serving hand-written JSON fixtures, with the SDK
   client pointed at it through `scw.WithAPIURL` and `scw.WithHTTPClient`.

## Decision

Use `httptest.Server` (option 2):

- Fixtures live in `internal/scaleway/testdata/` and are anonymised by
  construction (fake UUIDs, `Example Org`, round amounts).
- Handlers reproduce the pagination of each endpoint (page numbers for
  billing, page tokens for FinOps) so that multi-page behaviour is tested.
- Error cases (401, 403, 404, 429, 5xx without JSON, invalid JSON, expired
  and canceled contexts, refused connections) are covered, and each is
  checked against the `reason` label it maps to.
- Real API validation is done by opt-in integration tests
  (`-tags integration`, `make test-integration`), never run in CI.

The fixtures were built from the SDK JSON field tags, because the public API
documentation was not reachable from the development environment. They must
be compared with real responses when the integration tests are first run.

## Consequences

- Tests are fast, deterministic, parallel and need no credentials.
- A change of the API wire format not reflected in the SDK types is not
  detected by unit tests; the integration tests cover that risk.
