# Product Graph

## 1. Purpose

The product graph makes command outputs, model artifacts, risk decisions, and reports explicit data products. A workflow run records the algorithms invoked, the configuration hashes, the input and output product hashes, timestamps, and parent lineage needed to reconstruct a decision path.

The framework is additive. Existing commands remain the authority for their current work. The graph executor wraps those commands and persists typed products around them.

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

Workflow nodes run in cells. A cell is a layer name plus a canonical JSON key:

```text
cell_id = layer + ":" + sha256(canonical_json(key))[0:24]
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
executable kind
input and output product types
resources and forbidden resources
```

The registry makes command execution explicit instead of relying on implicit command naming.

## 5. Workflow JSON

Workflows are JSON documents with a driver, layers, resources, and nodes. The no-network fixture is `tests/fixtures/workflow_noop.json`.

```sh
./sec graph validate --db .fa.db --spec tests/fixtures/workflow_noop.json
./sec graph run --db .fa.db --spec tests/fixtures/workflow_noop.json
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

Replay loads the original workflow spec and node records, then re-checks deterministic outputs where the framework can reproduce them. The first implementation supports deterministic `noop.test.v1` replay:

```sh
./sec replay --db .fa.db --workflow-run-id wf_...
```

Replay succeeds only when the recomputed content hash matches the original product hash.

## 9. Relationship To Existing Commands

The graph executor wraps existing commands such as:

```text
recon
value
gate
stage
send --adapter mock
report daily
```

For command algorithms, the wrapper always persists `command_output.v1` with captured stdout, stderr, and exit code. Domain products are persisted conservatively as SQLite storage references where the command output or existing tables identify the artifact.

## 10. Safety Boundary

The pure C++ core boundary is unchanged. Exported C++ functions must not perform IO, read wall clock, read environment variables, open sockets, or submit broker orders.

Live broker submission is still not introduced. The current broker path remains the guarded mock adapter, and broker resources are rejected in observe and shadow graph workflows.
