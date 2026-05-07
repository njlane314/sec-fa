# sec-fa

A UNIX-shaped, NASA-influenced fundamentals-analysis and trading-control toolchain.

This repository is a production-oriented foundation, not a finished alpha model. The safety boundary is deliberate:
SEC ingestion, canonical fact construction, deterministic model execution, portfolio planning, hard risk checks, order staging, broker submission, and reconciliation are separate programs with explicit contracts. The default path does not submit live orders.

## System architecture

```text
SEC / market data
      │
      ▼
[ingestion services] ──► [raw immutable object store]
      │                         │
      ▼                         ▼
[parser / normalizer] ──► [PostgreSQL canonical store]
      │                         │
      ▼                         ▼
[feature builder] ──► [C++ modelling / portfolio library]
      │                         │
      ▼                         ▼
[order-intent engine] ─► [risk gate] ─► [IBKR adapter]
      │                         │              │
      ▼                         ▼              ▼
[alerts/UI/MCP]       [audit ledger]     [broker reconciliation]
```

## Non-negotiable design choices

1. The C++ core is pure computation. It does not fetch data, open network sockets, submit orders, read environment variables, or read the wall clock.
2. Order submission is isolated. Only `submit` is allowed to talk to a broker adapter.
3. The ledger is append-only. Mutable views are derived state.
4. There is no historical trading backtester. Validation is by deterministic replay, synthetic scenarios, shadow operation, and online forecast accounting.
5. The terminal is the first interface. The system emits machine-readable stdout, human-readable stderr, and meaningful exit codes.

## Current implementation

Implemented:

- C++17 shared library with a stable C ABI.
- Deterministic baseline fundamental model over canonical facts.
- Deterministic valuation model over periodized statement snapshots and explicit scenario assumptions.
- Hard C++ risk gate for order intents.
- SQLite operational database for local/single-node operation.
- PostgreSQL schema for production deployment.
- SEC submissions watcher and raw filing fetcher using SEC public endpoints.
- SEC accession-level inline-XBRL/classic-XBRL package parser that stores source documents, contexts, units, dimensions, and raw facts before canonical resolution.
- SEC `companyfacts` importer retained as a fallback/reconciliation path.
- CLI commands for securities, positions, model runs, risk checks, order staging, broker reconciliation snapshots, reports, and notifications.
- Auditable model assumptions, statement snapshots, valuations, forecast outcomes, and autonomy controls.
- CI check script, CMake build, unit test, and schema files.
- Philosophy, requirements, hazards, coding standard, naming standard, and operations documents.

Not implemented by design in this first drop:

- Live IBKR submission. `submit` is a guarded mock adapter unless replaced by an isolated broker adapter.
- Web dashboard.
- MCP assistant.
- Historical trading backtester.

## Quick start

```sh
cd sec-fa
make check
bin/bootstrap --db .folio.db
bin/security --db .folio.db --cik 0000320193 --symbol AAPL --price-usd 200 --adv-usd 5000000000 --investable 1
bin/xbrl --db .folio.db --accession 0000320193-24-000123 --cik 0000320193 --symbol AAPL --raw-root raw --user-agent "Your Name your.email@example.com"
bin/universe --db .folio.db --name us_core --min-adv-usd 0 --min-fact-count 2
bin/reconcile --db .folio.db --portfolio-value-usd 100000 --cash-usd 100000 --reconciled 1
bin/model --db .folio.db --core-lib build/libfolio.so --portfolio-value-usd 100000 --cash-usd 100000
bin/risk --db .folio.db --core-lib build/libfolio.so
bin/report daily --db .folio.db
```

On macOS the shared library is usually `build/libfolio.dylib`. On Linux it is `build/libfolio.so`.


## Accounting observation model

The database does not treat `revenue`, `cash`, or `EPS` as primitive scalar values. Raw source documents, XBRL contexts, XBRL units, inline-XBRL/classic-XBRL package facts, and companyfacts fallback facts are stored first. Canonical observations are derived, versioned interpretations with explicit metric kind, measurement basis, period semantics, duration, unit, dimensional scope, source fact, and quality flags.

The model-input ABI now carries these semantics. The C++ core rejects ambiguous observations and refuses to compare duration facts unless they are fiscal-quarter observations with comparable duration, basis, and dimensional scope. YTD and annual revenue remain stored and auditable, but they are not interchangeable with quarterly revenue.

## Tool names

The canonical entrypoint is `bin/folio`. Public programs use single lowercase words with no project prefix:

```text
bootstrap
security
position
filings
archive
company
xbrl
universe
reconcile
model
value
risk
stage
submit
report
notify
status
mode
halt
```

The programs compose through the database, immutable raw store, and append-only ledger rather than through textual stdin/stdout filters. A normal operator sequence is:

```text
bootstrap
  │
  ├── security
  └── position

filings ──► archive ──► xbrl ──► universe
                                  │
reconcile ────────────────────────┤
                                  ▼
                           model or value
                                  ▼
                                risk
                                  ▼
                                stage
                                  ▼
                               submit
                                  ▼
                         reconcile ──► report
                                  └──► notify
```

`company` remains a fallback/reconciliation importer for SEC companyfacts data. `value` runs the newer valuation path.

## Composition examples

Initialize and seed state:

```sh
DB=.folio.db

bin/bootstrap --db "$DB" &&
bin/security --db "$DB" \
  --cik 0000320193 \
  --symbol AAPL \
  --price-usd 200 \
  --adv-usd 5000000000 \
  --investable 1 &&
bin/position --db "$DB" \
  --cik 0000320193 \
  --quantity-shares 0 \
  --market-value-usd 0 \
  --weight-ratio 0
```

Ingest data and build a universe:

```sh
DB=.folio.db
RAW=raw
UA="Your Name your.email@example.com"

bin/filings --db "$DB" --cik 0000320193 --user-agent "$UA" &&
bin/archive --db "$DB" --raw-root "$RAW" --user-agent "$UA" &&
bin/xbrl \
  --db "$DB" \
  --accession 0000320193-24-000123 \
  --cik 0000320193 \
  --symbol AAPL \
  --raw-root "$RAW" \
  --user-agent "$UA" &&
bin/universe --db "$DB" --name us_core --min-adv-usd 0 --min-fact-count 2
```

Research-only run. This stops at risk decisions; it does not stage or submit orders:

```sh
DB=.folio.db
LIB=build/libfolio.so
[ -f "$LIB" ] || LIB=build/libfolio.dylib

bin/reconcile --db "$DB" --portfolio-value-usd 100000 --cash-usd 100000 --reconciled 1 &&
bin/model --db "$DB" --core-lib "$LIB" --portfolio-value-usd 100000 --cash-usd 100000 &&
bin/value --db "$DB" --core-lib "$LIB" --portfolio-value-usd 100000 --cash-usd 100000 &&
bin/risk --db "$DB" --core-lib "$LIB" &&
bin/report daily --db "$DB"
```

Shadow/staging run. This crosses from decision to staged action but still does not talk to a broker adapter:

```sh
bin/reconcile --db "$DB" --portfolio-value-usd 100000 --cash-usd 100000 --reconciled 1 &&
bin/model --db "$DB" --core-lib "$LIB" --portfolio-value-usd 100000 --cash-usd 100000 &&
bin/risk --db "$DB" --core-lib "$LIB" &&
bin/stage --db "$DB" &&
bin/report daily --db "$DB"
```

Guarded mock submission:

```sh
bin/stage --db "$DB" &&
bin/submit --db "$DB" --adapter mock &&
bin/reconcile --db "$DB" --portfolio-value-usd 100000 --cash-usd 100000 --reconciled 1 &&
bin/report daily --db "$DB"
```

The executable templates encode those patterns directly:

```text
examples/observe
examples/shadow
examples/mock
```

## Repository map

```text
include/fa_core.h          C ABI boundary
core/fa_core.cpp           deterministic C++ kernel and risk gate
tools/folio.py             CLI/service shell using only Python stdlib
db/sqlite/001_schema.sql   local operational schema, raw facts, periods, canonical observations
db/postgres/001_schema.sql production schema sketch
schemas/                   event/control and observation schemas
docs/                      intent, requirements, hazards, naming, operations
tests/                     C++ unit tests
ci/check                   mechanical repository check
```

## Operating modes

The system should move through these modes only by explicit operator action:

```text
observe -> shadow -> stage -> paper -> live_limited -> live
```

This repository initializes to `observe`. `halted` is always a valid terminal safety mode.
