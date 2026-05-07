# sec-fa

A UNIX-shaped, NASA-influenced fundamentals-analysis and trading-control toolchain.

This repository is a production-oriented foundation, not a finished alpha model. The safety boundary is deliberate:
SEC ingestion, canonical fact construction, deterministic model execution, portfolio planning, hard risk checks, order staging, broker submission, and reconciliation are separate programs with explicit contracts. The default path does not submit live orders.

## System architecture

```text
SEC / market data
        |
        v
+--------------------+     +----------------------------+
| ingestion services | --> | raw immutable object store |
+--------------------+     +----------------------------+
        |                               |
        v                               v
+---------------------+     +------------------------+
| parser / normalizer | --> | SQLite canonical store |
+---------------------+     +------------------------+
        |                               |
        v                               v
+-----------------+         +-----------------------------------+
| feature builder | ------> | C++ modelling / portfolio library |
+-----------------+         +-----------------------------------+
        |                               |
        v                               v
+---------------------+     +-----------+     +--------------+
| order-intent engine | --> | risk gate | --> | IBKR adapter |
+---------------------+     +-----------+     +--------------+
        |                         |                   |
        v                         v                   v
+---------------+        +--------------+     +-----------------------+
| alerts/UI/MCP |        | audit ledger |     | broker reconciliation |
+---------------+        +--------------+     +-----------------------+
```

## Non-negotiable design choices

1. The C++ core is pure computation. It does not fetch data, open network sockets, submit orders, read environment variables, or read the wall clock.
2. Order submission is isolated. Only `send` is allowed to talk to a broker adapter.
3. The ledger is append-only. Mutable views are derived state.
4. There is no historical trading backtester. Validation is by deterministic replay, synthetic scenarios, shadow operation, and online forecast accounting.
5. The terminal is the first interface. The system emits machine-readable stdout, human-readable stderr, and meaningful exit codes.

## Current implementation

Implemented:

- C++17 shared library with a stable C ABI.
- Deterministic baseline fundamental model over canonical facts.
- Deterministic C++ statement-snapshot builder and valuation model over canonical observations and explicit scenario assumptions.
- Hard C++ risk gate for order intents.
- Go CLI shell for SQLite state movement, SEC/XBRL package parsing, model execution, risk checks, reports, and guarded mock submission.
- SQLite operational database for local/single-node operation.
- SEC submissions watcher and raw filing fetcher using SEC public endpoints.
- SEC accession-level inline-XBRL/classic-XBRL package parser that stores source documents, contexts, units, dimensions, and raw facts before canonical resolution.
- Go XBRL canonical observation resolution is explicit Class B data logic; it is not part of the order-execution authority.
- SEC `companyfacts` importer retained as a design path; the Go CLI currently uses accession XBRL as the primary ingestion path.
- CLI commands for securities, positions, model runs, risk checks, order staging, broker reconciliation snapshots, reports, and notifications.
- Auditable model assumptions, statement snapshots, valuations, forecast outcomes, and autonomy controls.
- CI check script, CMake build, unit test, and schema files.
- Philosophy, requirements, hazards, coding standard, naming standard, and operations documents.

Not implemented by design in this first drop:

- Live IBKR submission. `send` is a guarded mock adapter unless replaced by an isolated broker adapter.
- Web dashboard.
- MCP assistant.
- Historical trading backtester.

## Quick start

```sh
cd sec-fa
make check
make setup-db

DB=.fa.db
./sec sym --db "$DB" --cik 0000320193 --symbol AAPL --price-usd 200 --adv-usd 5000000000 --investable 1
./sec xbrl --db "$DB" --accession 0000320193-24-000123 --cik 0000320193 --symbol AAPL --raw-root raw --user-agent "Your Name your.email@example.com"
./sec univ --db "$DB" --name us_core --min-adv-usd 0 --min-fact-count 2
./sec recon --db "$DB" --portfolio-value-usd 100000 --cash-usd 100000 --reconciled 1
./sec plan --db "$DB" --core-lib build/libfolio.so --portfolio-value-usd 100000 --cash-usd 100000
./sec gate --db "$DB" --core-lib build/libfolio.so
./sec report daily --db "$DB"
```

On macOS the shared library is usually `build/libfolio.dylib`. On Linux it is `build/libfolio.so`.

For database setup only, use `make setup-db`. It creates or upgrades `.fa.db`.
The equivalent direct CLI command is `./sec init`; pass `--db /path/to/file.db`
when you need a specific database path.


## Accounting observation model

The database does not treat `revenue`, `cash`, or `EPS` as primitive scalar values. Raw source documents, XBRL contexts, XBRL units, inline-XBRL/classic-XBRL package facts, and companyfacts fallback facts are stored first. Canonical observations are derived, versioned interpretations with explicit metric kind, measurement basis, period semantics, duration, unit, dimensional scope, source fact, and quality flags.

The model-input ABI now carries these semantics. The C++ core rejects ambiguous observations and refuses to compare duration facts unless they are fiscal-quarter observations with comparable duration, basis, and dimensional scope. YTD and annual revenue remain stored and auditable, but they are not interchangeable with quarterly revenue.

## Tool names

The canonical entrypoint is `./sec`. Public commands use single lowercase words with no project prefix:

```text
init
sym
pos
watch
pull
xbrl
univ
recon
plan
value
gate
stage
send
report
ping
stat
mode
halt
```

The programs compose through the database, immutable raw store, and append-only ledger rather than through textual stdin/stdout filters. A normal operator sequence is:

```text
+------+
| init |
+------+
   |
   +--> sym
   |
   +--> pos

watch -> pull -> xbrl -> univ
                           |
recon ---------------------+
                           v
                    plan or value
                           |
                           v
                         gate
                           |
                           v
                         stage
                           |
                           v
                         send
                           |
                           v
                         recon
                        /     \
                       v       v
                    report   ping
```

`value` runs the valuation path.

## Composition examples

Initialize and seed state:

```sh
DB=.fa.db

./sec init --db "$DB" &&
./sec sym --db "$DB" \
  --cik 0000320193 \
  --symbol AAPL \
  --price-usd 200 \
  --adv-usd 5000000000 \
  --investable 1 &&
./sec pos --db "$DB" \
  --cik 0000320193 \
  --quantity-shares 0 \
  --market-value-usd 0 \
  --weight-ratio 0
```

Ingest data and build a universe:

```sh
DB=.fa.db
RAW=raw
UA="Your Name your.email@example.com"

./sec watch --db "$DB" --cik 0000320193 --user-agent "$UA" &&
./sec pull --db "$DB" --raw-root "$RAW" --user-agent "$UA" &&
./sec xbrl \
  --db "$DB" \
  --accession 0000320193-24-000123 \
  --cik 0000320193 \
  --symbol AAPL \
  --raw-root "$RAW" \
  --user-agent "$UA" &&
./sec univ --db "$DB" --name us_core --min-adv-usd 0 --min-fact-count 2
```

Research-only run. This stops at risk decisions; it does not stage or submit orders:

```sh
DB=.fa.db
LIB=build/libfolio.so
[ -f "$LIB" ] || LIB=build/libfolio.dylib

./sec recon --db "$DB" --portfolio-value-usd 100000 --cash-usd 100000 --reconciled 1 &&
./sec plan --db "$DB" --core-lib "$LIB" --portfolio-value-usd 100000 --cash-usd 100000 &&
./sec value --db "$DB" --core-lib "$LIB" --portfolio-value-usd 100000 --cash-usd 100000 &&
./sec gate --db "$DB" --core-lib "$LIB" &&
./sec report daily --db "$DB"
```

Shadow/staging run. This crosses from decision to staged action but still does not talk to a broker adapter:

```sh
./sec recon --db "$DB" --portfolio-value-usd 100000 --cash-usd 100000 --reconciled 1 &&
./sec plan --db "$DB" --core-lib "$LIB" --portfolio-value-usd 100000 --cash-usd 100000 &&
./sec gate --db "$DB" --core-lib "$LIB" &&
./sec stage --db "$DB" &&
./sec report daily --db "$DB"
```

Guarded mock submission:

```sh
./sec stage --db "$DB" &&
./sec send --db "$DB" --adapter mock &&
./sec recon --db "$DB" --portfolio-value-usd 100000 --cash-usd 100000 --reconciled 1 &&
./sec report daily --db "$DB"
```

## Repository map

```text
abi/core.h       C ABI boundary
engine/*.cpp     deterministic C++ model, valuation, and risk gate
cli/             Go orchestration package
schema.sql       local operational schema, raw facts, periods, canonical observations
docs/contracts/  line-oriented event/control and observation contracts
docs/            intent, requirements, hazards, naming, operations
tests/           C++ unit tests
check            mechanical repository check
```

## Operating modes

The system should move through these modes only by explicit operator action:

```text
observe -> shadow -> stage -> paper -> live_limited -> live
```

This repository initializes to `observe`. `halted` is always a valid terminal safety mode.
