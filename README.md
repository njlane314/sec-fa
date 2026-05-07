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
- Rust CLI shell for SQLite state movement, SEC/XBRL package parsing, model execution, risk checks, reports, and guarded mock submission.
- Go `secd` HTTP service for network-facing SEC fetch/proxy work.
- SQLite operational database for local/single-node operation.
- SEC submissions watcher and raw filing fetcher using SEC public endpoints.
- Rust SEC accession-level inline-XBRL/classic-XBRL package parser that stores source documents, contexts, units, dimensions, and raw facts before canonical resolution.
- Rust XBRL canonical observation resolution is explicit Class B data logic; it is not part of the order-execution authority.
- SEC `companyfacts` importer retained as a design path; the Rust CLI currently uses accession XBRL as the primary ingestion path.
- CLI commands for securities, positions, model runs, risk checks, order staging, broker reconciliation snapshots, reports, and notifications.
- Bulk security-master CSV import for issuer-level universes.
- Batch issuer ingest that discovers, fetches, and parses financial 10-K/10-Q filings only.
- Databento-backed seed CSV generation for limited explicit stock symbol lists.
- Auditable model assumptions, statement snapshots, valuations, forecast outcomes, and autonomy controls.
- SQLite-backed durable daemon workers for SEC discovery, raw pulls, XBRL parsing, planning, risk gating, staging, guarded submission, reconciliation, and notification.
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
export SEC_USER_AGENT="Your Name your.email@example.com"

DB=.fa.db
./sec sym --db "$DB" --cik 0000320193 --symbol AAPL --price-usd 200 --adv-usd 5000000000 --investable 1
./sec watch --db "$DB" --cik 0000320193 --limit 1
./sec pull --db "$DB" --cik 0000320193 --limit 1
./sec xbrl --db "$DB" --latest --cik 0000320193
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

For SEC ingestion, `watch` chooses accessions from the official SEC submissions
feed, `pull` chooses package files from the accession `index.json`, and `xbrl`
uses the stored filing metadata. A real SEC User-Agent can be supplied once with
`SEC_USER_AGENT` or per command with `--user-agent`.


## Accounting observation model

The database does not treat `revenue`, `cash`, or `EPS` as primitive scalar values. Raw source documents, XBRL contexts, XBRL units, inline-XBRL/classic-XBRL package facts, and companyfacts fallback facts are stored first. Canonical observations are derived, versioned interpretations with explicit metric kind, measurement basis, period semantics, duration, unit, dimensional scope, source fact, and quality flags.

The model-input ABI now carries these semantics. The C++ core rejects ambiguous observations and refuses to compare duration facts unless they are fiscal-quarter observations with comparable duration, basis, and dimensional scope. YTD and annual revenue remain stored and auditable, but they are not interchangeable with quarterly revenue.

## Tool names

The canonical entrypoint is `./sec`. Public commands use single lowercase words with no project prefix:

```text
init
sym
import-securities
ingest-universe
databento
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

make setup-db DB="$DB" &&
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

Scale to a larger issuer universe:

```sh
DB=.fa.db
RAW=raw
UA="Your Name your.email@example.com"

cat > universe.csv <<'CSV'
cik,ticker,price_usd,adv_usd,investable
0000320193,AAPL,200,5000000000,1
0001045810,NVDA,900,20000000000,1
0000789019,MSFT,400,7000000000,1
CSV

./sec import-securities \
  --db "$DB" \
  --csv universe.csv &&
./sec ingest-universe \
  --db "$DB" \
  --csv universe.csv \
  --raw-root "$RAW" \
  --user-agent "$UA" \
  --forms 10-K,10-Q,10-K/A,10-Q/A \
  --annual-limit-per-cik 1 \
  --quarterly-limit-per-cik 4 \
  --universe-name us_core \
  --min-adv-usd 0 \
  --min-fact-count 2
```

`ingest-universe` is intentionally issuer-level because the local schema uses CIK as `security_id`. By default it ingests the latest annual 10-K/10-K/A package plus the latest four quarterly 10-Q/10-Q/A packages for each CIK, then parses every fetched accession. This prevents a latest 10-K from crowding out quarterly comparables. If a CSV has several tickers for one CIK, the importer keeps the row with the highest `adv_usd` and records the collapsed count in the event ledger.

Build a security seed CSV from Databento market/reference data:

```sh
export DATABENTO_API_KEY="..."

./sec databento \
  --symbols AAPL,MSFT,NVDA \
  --start 2026-05-01 \
  --end 2026-05-07 \
  --out databento_universe.csv
```

The Databento command calls `security_master.get_last` and `timeseries.get_range` using `EQUS.MINI` / `ohlcv-1d` by default. It writes the same `cik,ticker,price_usd,adv_usd,investable` seed shape accepted by `import-securities` and `ingest-universe`. `adv_usd` is computed as average daily `close * volume` over the requested bar window. The command rejects `ALL_SYMBOLS` by default so accidental broad paid data pulls do not happen.

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
cli/             Rust CLI orchestration package
filings/         Rust SEC filing parser crate
server.go        Go SEC HTTP proxy implementation, built as secd
daemon.go        Go durable state worker implementation, built as sec-*d
main.go          Go entrypoint dispatch by executable name
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
