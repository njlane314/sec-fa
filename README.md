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
- Security peer metadata for universe analysis (`peer_group`, `sector`, `industry`) from explicit CLI, CSV, or seed data fields.
- Canonical observations for gross profit, operating income, R&D, stock-based compensation, interest expense, and buybacks.
- Auditable model assumptions, statement snapshots, valuations, forecast outcomes, and autonomy controls.
- Product graph infrastructure for typed products, workflow validation, sequential noop execution, lineage, and deterministic replay checks.
- Immutable point-in-time feature snapshots for revenue growth, margins, free cash flow, leverage, shares, and quality flags.
- Daily reports that show peer-group counts, canonical metric coverage, feature snapshots, and statement quality flags.
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
./sec --db "$DB" --validate docs/contracts/workflow.full_analysis.flow
./sec --db "$DB" docs/contracts/workflow.full_analysis.flow
```

On macOS the shared library is usually `build/libfolio.dylib`. On Linux it is `build/libfolio.so`.

For database setup only, use `make setup-db`. Running a workflow also creates or
upgrades the selected SQLite database path before validation/execution.

For SEC ingestion, use a real SEC User-Agent via `SEC_USER_AGENT`. Public
operator workflows are `.flow` files; new ingestion or data-load behavior should
be added as graph operations rather than new public CLI verbs.

## Product graph

`sec` runs `.flow` files with typed products, lineage, and invariant checks.
Graph execution calls in-process Rust operations; it does not spawn `./sec`
subcommands. The first no-network fixture runs a deterministic noop workflow:

```sh
DB=.fa.db
./sec --db "$DB" --validate tests/fixtures/workflow_noop.flow
./sec --db "$DB" tests/fixtures/workflow_noop.flow
```

Workflow runs print JSONL events and persist product/lineage rows in SQLite.
Replay and product inspection are internal graph verification capabilities
rather than public CLI verbs.

The full shadow analysis example wraps the research path without staging or
broker submission:

```sh
./sec --db "$DB" docs/contracts/workflow.full_analysis.flow
```

Stage and guarded mock-paper graph examples are also available for explicit
operator-mode runs:

```sh
./sec --db "$DB" --validate docs/contracts/workflow.stage_orders.flow
./sec --db "$DB" --validate docs/contracts/workflow.paper_mock.flow
```

See `docs/PRODUCT_GRAPH.md` for the schema, invariants, modes, and safety
boundary.

## Accounting observation model

The database does not treat `revenue`, `cash`, or `EPS` as primitive scalar values. Raw source documents, XBRL contexts, XBRL units, inline-XBRL/classic-XBRL package facts, and companyfacts fallback facts are stored first. Canonical observations are derived, versioned interpretations with explicit metric kind, measurement basis, period semantics, duration, unit, dimensional scope, source fact, and quality flags.

The model-input ABI now carries these semantics. The C++ core rejects ambiguous observations and refuses to compare duration facts unless they are fiscal-quarter observations with comparable duration, basis, and dimensional scope. YTD and annual revenue remain stored and auditable, but they are not interchangeable with quarterly revenue.

## Operation names

The canonical entrypoint is `./sec`, and the public interface is a `.flow` file.
Workflow nodes reference operation-like algorithm ids for internal graph steps:

```text
op.recon
op.plan
op.value
op.feat
op.gate
op.stage
op.send.mock
op.report.daily
```

The operations compose through the database, immutable raw store, append-only
ledger, and graph products rather than through textual stdin/stdout filters. A
normal decision sequence is:

```text
recon
  |
  +--> plan ----------+
  |                   v
  +--> value ------> feat
                       |
                       v
                     gate
                       |
              +--------+--------+
              v                 v
            report            stage
                                |
                                v
                            send.mock
```

`value` runs the valuation path. `feat` materializes immutable feature snapshots
from the statement snapshots written by `value`.

## Workflow examples

Validate the no-network fixture:

```sh
./sec --db .fa.db --validate tests/fixtures/workflow_noop.flow
```

Run research analysis without staging or broker submission:

```sh
./sec --db .fa.db docs/contracts/workflow.full_analysis.flow
```

Validate higher-authority workflows before deliberately running them:

```sh
./sec --db .fa.db --validate docs/contracts/workflow.stage_orders.flow
./sec --db .fa.db --validate docs/contracts/workflow.paper_mock.flow
```

The issuer/security seed, SEC ingestion, Databento universe, notification, and
broker-adapter behaviors are operation boundaries. They should be registered in
the graph and exposed through `.flow` files before becoming public operator
behavior.

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
