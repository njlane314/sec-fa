# Product Graph Framework Goal

This is the current implementation goal for the product-graph layer.

The objective is to keep a trading-safe product-graph framework around the
existing command/table/event system while using `.flow` files as the authored
workflow format.

Hard constraints:

```text
No YAML.
No XML.
No authored workflow JSON.
Preserve existing CLI behavior.
Preserve the pure C++ core boundary.
Do not introduce live broker submission.
Keep graph execution sequential until scheduling is explicitly designed.
Make every algorithm invocation auditable by hashes, timestamps, config,
inputs, outputs, and lineage.
Graph execution should call in-process operations, not fork the public CLI.
```

The current target shape is:

```text
typed products
typed layers/cells
algorithm registry
.flow workflow IR
invariant checker
sequential executor
product lineage
replay support
```

Workflow examples live in:

```text
docs/contracts/workflow.format
docs/contracts/workflow.example.flow
docs/contracts/workflow.full_analysis.flow
docs/contracts/workflow.stage_orders.flow
docs/contracts/workflow.paper_mock.flow
tests/fixtures/workflow_noop.flow
tests/fixtures/workflow_cycle.flow
tests/fixtures/workflow_broker_shadow.flow
```

The public workflow interface is:

```sh
sec --db <path> --validate <workflow.flow>
sec --db <path> --explain <workflow.flow>
sec --db <path> <workflow.flow>
```

The `.flow` parser compiles the authored workflow into the existing typed Rust
`WorkflowSpec`. The executor may canonicalize that internal representation for
hashing and replay, but operators should author and review `.flow` files.
