# Architecture Decision Records

Short records of the decisions that are not obvious from the code.

| ADR | Decision |
|---|---|
| [0001](0001-decouple-scrapes-from-api-calls.md) | Scrapes never call the API: background refreshers publish immutable snapshots. |
| [0002](0002-sdk-adapter-testing.md) | SDK adapters are tested against an `httptest` server with hand-written fixtures, not VCR cassettes. |
| [0003](0003-units-currency-and-time-windows.md) | Euros, grams of CO₂e and cubic meters; UTC monthly billing periods; daily footprint window. |
| [0004](0004-finops-enrichment-and-cardinality.md) | FinOps charges are enriched in PromQL; a cardinality guard folds the smallest series into `sku="other"`. |
| [0005](0005-manual-pagination.md) | Pages are walked manually because `scw.WithAllPages` drops response fields and does not support page tokens. |

To add a decision, copy the structure of an existing record (context,
decision, consequences), number it sequentially and add it to this table.
