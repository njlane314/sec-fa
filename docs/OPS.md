# Operations

## 1. Build

```sh
make check
```

## 2. Database

`sec` opens the configured SQLite database and creates or upgrades the schema
before validating or running a workflow. Local development can still use the
make target:

```sh
make setup-db
```

The default database path is `.fa.db`. Use `--db` for an explicit operational
path:

```sh
./sec --db /var/lib/sec/sec.db --validate docs/contracts/workflow.full_analysis.flow
```

The initial trading mode is `observe`.

## 3. Workflow Execution

Operators interact through `.flow` files:

```sh
./sec --db /var/lib/sec/sec.db --validate docs/contracts/workflow.full_analysis.flow
./sec --db /var/lib/sec/sec.db --explain docs/contracts/workflow.full_analysis.flow
./sec --db /var/lib/sec/sec.db docs/contracts/workflow.full_analysis.flow
```

The full-analysis workflow runs reconciliation, planning, valuation, feature
materialization, risk gating, and reporting as in-process Rust operations. The
stage and mock-paper workflows are separate files so crossing from decision
evidence to staged or submitted orders remains explicit:

```sh
./sec --db /var/lib/sec/sec.db --validate docs/contracts/workflow.stage_orders.flow
./sec --db /var/lib/sec/sec.db --validate docs/contracts/workflow.paper_mock.flow
```

## 4. SEC Data

SEC ingestion still requires a real User-Agent and rate moderation:

```sh
export SEC_USER_AGENT="Your Company admin@example.com"
```

The public workflow surface is `.flow`. Ingestion-oriented operations should be
added to the graph as Rust operations before they are exposed to operators.
Source artifacts remain immutable under the raw store, and parsed observations
must stay downstream of filing/source-document evidence.

## 5. Broker Boundary

Live broker submission is intentionally not implemented. The current broker path
is the guarded mock adapter, and workflows with broker resources are rejected
unless the mode and driver flags explicitly allow them.

Broker submission must run on an isolated host before live authority is added.
The submitter must not ingest SEC data, run models, or make discretionary
decisions, and it must re-check fresh reconciliation immediately before adapter
submission.

## 6. Notifications

Notifications remain an operation boundary to add to workflows. Supported
delivery mechanisms are configured by environment variables such as
`PUSHOVER_TOKEN`, `PUSHOVER_USER`, or an `ntfy` topic.
