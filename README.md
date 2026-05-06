# sec-fa

A UNIX-shaped, NASA-influenced fundamentals-analysis and trading-control toolchain.

This repository is a production-oriented foundation, not a finished alpha model. The safety boundary is deliberate:
SEC ingestion, canonical fact construction, deterministic model execution, portfolio planning, hard risk checks, order staging, broker submission, and reconciliation are separate programs with explicit contracts. The default path does not submit live orders.

## Non-negotiable design choices

1. The C++ core is pure computation. It does not fetch data, open network sockets, submit orders, read environment variables, or read the wall clock.
2. Order submission is isolated. Only `fa-broker-submit` is allowed to talk to a broker adapter.
3. The ledger is append-only. Mutable views are derived state.
4. There is no historical trading backtester. Validation is by deterministic replay, synthetic scenarios, shadow operation, and online forecast accounting.
5. The terminal is the first interface. The system emits machine-readable stdout, human-readable stderr, and meaningful exit codes.

## Current implementation

Implemented:

- C++17 shared library with a stable C ABI.
- Deterministic baseline fundamental model over canonical facts.
- Hard C++ risk gate for order intents.
- SQLite operational database for local/single-node operation.
- PostgreSQL schema for production deployment.
- SEC submissions watcher and raw filing fetcher using SEC public endpoints.
- SEC `companyfacts` importer with conservative canonical fact mapping.
- CLI commands for securities, positions, model runs, risk checks, order staging, broker reconciliation snapshots, reports, and notifications.
- CI check script, CMake build, unit test, and schema files.
- Philosophy, requirements, hazards, coding standard, naming standard, and operations documents.

Not implemented by design in this first drop:

- Live IBKR submission. `fa-broker-submit` is a guarded mock adapter unless replaced by an isolated broker adapter.
- Web dashboard.
- MCP assistant.
- Historical trading backtester.
- Full XBRL package parser. The starter importer uses SEC `companyfacts`; a production parser should be added behind the same canonical fact schema before live trading.

## Quick start

```sh
cd sec-fa
make check
bin/fa init-db --db .fa.db
bin/fa security-upsert --db .fa.db --cik 0000320193 --symbol AAPL --price-usd 200 --adv-usd 5000000000 --investable 1
bin/fa facts-companyfacts --db .fa.db --cik 0000320193 --symbol AAPL --raw-root raw --user-agent "Your Name your.email@example.com"
bin/fa universe-build --db .fa.db --name us_core --min-adv-usd 0 --min-fact-count 2
bin/fa broker-reconcile --db .fa.db --portfolio-value-usd 100000 --cash-usd 100000 --reconciled 1
bin/fa model-run --db .fa.db --core-lib build/libfa_core.so --portfolio-value-usd 100000 --cash-usd 100000
bin/fa risk-check --db .fa.db --core-lib build/libfa_core.so
bin/fa report daily --db .fa.db
```

On macOS the shared library is usually `build/libfa_core.dylib`. On Linux it is `build/libfa_core.so`.

## Tool names

The canonical entrypoint is `bin/fa`. Wrapper scripts also exist for UNIX-style composition:

```text
fa-sec-watch
fa-sec-fetch
fa-facts-companyfacts
fa-universe-build
fa-model-run
fa-risk-check
fa-order-stage
fa-broker-reconcile
fa-broker-submit
fa-notify
fa-report
```

Each wrapper calls the matching `bin/fa` subcommand.

## Repository map

```text
include/fa_core.h          C ABI boundary
core/fa_core.cpp           deterministic C++ kernel and risk gate
tools/fa_cli.py            CLI/service shell using only Python stdlib
db/sqlite/001_schema.sql   local operational schema
db/postgres/001_schema.sql production schema sketch
schemas/                   event/control schemas
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
