# Workflow Composition

The public operational surface is a `.flow` file. The executable is a workflow
host, not a collection of operator-facing subcommands:

```sh
./sec --db .fa.db --validate docs/contracts/workflow.full_analysis.flow
./sec --db .fa.db --explain docs/contracts/workflow.full_analysis.flow
./sec --db .fa.db docs/contracts/workflow.full_analysis.flow
```

`.flow` files are line-oriented and reviewable. They compile into the typed Rust
`WorkflowSpec` used by validation, execution, lineage, and replay checks. The
format is documented in `docs/contracts/workflow.format`.

## Handoff Model

Each node produces explicit products in the product graph. State is handed off
through the operational database, immutable raw/source artifacts, append-only
events, and typed graph products, not through ad hoc shell pipes.

```text
recon
  │
  ├── plan ──────┐
  │              ▼
  └── value ──► feat
                 │
                 ▼
               gate
                 │
          ┌──────┴──────┐
          ▼             ▼
        report        stage
                         │
                         ▼
                    send.mock
```

The decision path is split into separate workflows:

```text
workflow.full_analysis.flow   research, valuation, features, gate, report
workflow.stage_orders.flow    controlled transition to staged orders
workflow.paper_mock.flow      guarded mock adapter submission
```

## Authoring Rules

Use `.flow` for operator-facing workflows. Do not add a new public CLI verb for a
new operation; register a Rust operation in the algorithm registry and reference
that algorithm from a workflow node.

Set the workflow mode and driver flags to match authority:

```text
mode observe
allow_external_write false
allow_broker_resource false
```

Broker resources require an explicit paper or live mode plus an explicit driver
allowance. Observe and shadow workflows may build decisions and reports, but
they must not stage or submit orders.

## Examples

Validate and run the no-network fixture:

```sh
./sec --db .fa.db --validate tests/fixtures/workflow_noop.flow
./sec --db .fa.db tests/fixtures/workflow_noop.flow
```

Run the current research workflow:

```sh
./sec --db .fa.db docs/contracts/workflow.full_analysis.flow
```

Validate higher-authority workflows before an operator deliberately runs them:

```sh
./sec --db .fa.db --validate docs/contracts/workflow.stage_orders.flow
./sec --db .fa.db --validate docs/contracts/workflow.paper_mock.flow
```

## Evidence

Workflow execution emits JSONL events such as `workflow_validated`,
`workflow_started`, `node_succeeded`, and `workflow_succeeded`. The database
records `workflow_runs`, `workflow_nodes`, `workflow_edges`, immutable
`data_products`, product lineage, and invariant violations.

The C++ core remains a pure calculation boundary. SEC network access, raw
artifact storage, observation resolution, risk-gated staging, broker adapters,
and notifications are Rust operation boundaries that must be represented as
graph nodes before becoming public workflow behavior.
