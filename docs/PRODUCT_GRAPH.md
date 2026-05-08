# Product Graph

## 1. Purpose

The product graph makes operation outputs, model artifacts, risk decisions, and reports explicit data products. A workflow run records the algorithms invoked, the configuration hashes, the input and output product hashes, timestamps, and parent lineage needed to reconstruct a decision path.

The public operator interface is a `.flow` file passed to `sec`. Graph
execution calls in-process Rust operations directly instead of spawning `sec`
subcommands.

## 2. Product identity

Products are immutable records in `data_products`. Identity is derived from canonical JSON and SHA-256 hashes:

```text
content_sha256 = sha256(canonical_json(payload))
config_sha256 = sha256(canonical_json(config))
product_id    = prod_ + sha256(canonical_json(identity_fields))[0:48]
```

The identity fields include product type, schema, cell, content hash, algorithm id, algorithm version, config hash, parent product ids, and `available_at`.

`workflow_runs` and `workflow_nodes` are operational run indexes over immutable products, lineage rows, workflow edges, and invariant violations. Their statuses are convenience state for execution and replay inspection; authority-bearing artifacts remain in typed products and lineage.

## 3. Layers And Cells

Workflow nodes run in cells. A cell is a layer name plus a canonicalized key:

```text
cell_id = layer + ":" + sha256(canonical_key(key))[0:24]
```

Layers give the workflow a typed coordinate system such as `job`, `issuer`, `accession`, `portfolio`, or `decision_run`.

## 4. Algorithm Registry

Algorithms are registered in `algorithm_registry` and bootstrapped from `docs/contracts/algorithm_registry.bootstrap.json`. Each entry declares:

```text
algorithm id
version
hazard class
purity
determinism
executable kind, usually `rust_operation` for SEC-FA operations
input and output product types
resources and forbidden resources
```

The registry makes operation execution explicit instead of relying on command
names or shell-visible verbs.

## 5. Workflow .flow

Workflows are authored as line-oriented `.flow` files with a driver, layers,
resources, and nodes. The format is documented in
`docs/contracts/workflow.format`; the no-network fixture is
`tests/fixtures/workflow_noop.flow`. A full shadow analysis example that wraps
the old `recon -> plan -> value -> feat -> gate -> report` sequence lives in
`docs/contracts/workflow.full_analysis.flow`. Controlled stage and guarded mock
paper examples live in `docs/contracts/workflow.stage_orders.flow` and
`docs/contracts/workflow.paper_mock.flow`.

```sh
./sec --db .fa.db --validate tests/fixtures/workflow_noop.flow
./sec --db .fa.db tests/fixtures/workflow_noop.flow
```

Minimal example:

```text
workflow noop_fixture
driver test.v1
mode observe
decision_as_of 2026-05-08T13:00:00.000Z
allow_external_write false
allow_broker_resource false

layer job key workflow_name

node noop
  alg noop.test.v1
  cell job workflow_name=noop_fixture
  set message test
  output output command_output.v1
end
```

Output is JSONL:

```json
{"event":"workflow_validated","workflow_name":"noop_fixture","node_count":1,"edge_count":0,"topological_order":["noop"],"violations":[]}
```

## 6. Invariant Logic

The invariant contract is `docs/contracts/invariants.dl`. The Rust checker enforces the first-pass invariants directly:

```text
workflow graph must be acyclic
Class A algorithms cannot consume Class C products
Class C algorithms cannot produce authority-bearing products
broker resources are blocked outside paper/live_limited/live
external writes are blocked in observe and shadow
runtime inputs must not be available after decision_as_of
declared node inputs must match the algorithm input contract
staged-order products must have a risk-decision parent
broker-event products must have staged-order and reconciliation parents
```

The graph framework does not replace the C++ risk gate. It enforces topology, provenance, and authority boundaries around the existing risk gate.

## 7. Execution Modes

Modes are ordered operational states:

```text
halted
observe
shadow
stage
paper
live_limited
live
```

Graph validation rejects broker-resource and external-write algorithms unless the workflow mode and driver flags allow them. Stage and send mappings exist, but observe and shadow workflows cannot use them for side effects.

## 8. Replay

Replay loads the original workflow spec and node records, then re-checks
deterministic outputs where the framework can do so without causing side
effects. It recomputes pure `noop.test.v1` payload hashes. For deterministic
Rust-operation nodes, it does not re-run mutating operations; it verifies the
persisted operation-output hash, zero exit code, product config hash, and
parent lineage. Replay is now an internal graph verification routine, not a
public command surface.

Replay succeeds only when those replay checks match the original products.

## 9. Relationship To Existing Operations

The graph executor calls internal Rust operations such as:

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

For Rust-operation algorithms, the executor still persists `command_output.v1`
with the stdout/stderr/exit-code shape for compatibility with existing product
contracts. Domain products are persisted conservatively as SQLite storage
references where the operation output or existing tables identify the artifact.

## 10. Safety Boundary

The pure C++ core boundary is unchanged. Exported C++ functions must not perform IO, read wall clock, read environment variables, open sockets, or submit broker orders.

Live broker submission is still not introduced. The current broker path remains the guarded mock adapter, and broker resources are rejected in observe and shadow graph workflows.
