# Product Graph Framework Goal

This file is intended to be passed to Codex with `/goal`.

The objective is to implement a trading-safe product-graph framework inside this repository without introducing YAML.

```text
/goal

Implement a trading-safe product-graph framework inside this repository.

Hard constraints:
1. Do not use YAML anywhere.
2. Use JSON, JSONL, JSON Schema, SQL, Rust, C/C++, and optionally Datalog-like `.dl` text contracts.
3. Preserve existing CLI behavior.
4. Preserve the existing pure C++ core boundary. No C++ exported function may perform IO, read wall clock, read environment variables, open sockets, or submit broker orders.
5. Preserve existing broker isolation. Live broker submission must not be introduced.
6. Add product graph infrastructure incrementally around existing commands.
7. The first implementation must run sequentially. Parallel scheduling may be specified but not required in the first pass.
8. All new state must be append-only or derivable unless explicitly marked as cache/index state.
9. Every algorithm invocation must be auditable by content hashes, config hashes, input product hashes, output product hashes, timestamps, and lineage.
10. Add tests.

Architectural goal:
Convert the existing command/table/event system into a typed immutable product graph.

Current shape:
  commands + SQLite tables + append-only events + daemon work items + pure C++ kernels

Target shape:
  typed products + typed layers/cells + algorithm registry + graph IR + invariant checker + executor + product lineage + replay support

Use the existing system as the substrate. Do not delete existing commands.
```

## 1. Product graph schema

Add this schema to `schema.sql`. Use `CREATE TABLE IF NOT EXISTS` so existing databases can be upgraded by `sec init`.

```sql
CREATE TABLE IF NOT EXISTS product_types (
    product_type TEXT PRIMARY KEY,
    schema_name TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    hazard_class TEXT NOT NULL CHECK (hazard_class IN ('A','B','C')),
    description TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS data_products (
    product_id TEXT PRIMARY KEY,
    product_type TEXT NOT NULL REFERENCES product_types(product_type),
    schema_name TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    cell_id TEXT NOT NULL,
    family_id TEXT,
    content_sha256 TEXT NOT NULL,
    storage_kind TEXT NOT NULL,
    storage_ref TEXT NOT NULL,
    created_by_algorithm TEXT NOT NULL,
    algorithm_version TEXT NOT NULL,
    config_sha256 TEXT NOT NULL,
    code_sha256 TEXT,
    valid_time_start TEXT,
    valid_time_end TEXT,
    source_time TEXT,
    accepted_at TEXT,
    ingested_at TEXT,
    available_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    hazard_class TEXT NOT NULL CHECK (hazard_class IN ('A','B','C')),
    UNIQUE(product_type, cell_id, content_sha256, created_by_algorithm, config_sha256)
);

CREATE INDEX IF NOT EXISTS idx_data_products_type_cell
    ON data_products(product_type, cell_id, available_at);

CREATE INDEX IF NOT EXISTS idx_data_products_available
    ON data_products(available_at);

CREATE TABLE IF NOT EXISTS data_product_lineage (
    child_product_id TEXT NOT NULL REFERENCES data_products(product_id),
    parent_product_id TEXT NOT NULL REFERENCES data_products(product_id),
    role TEXT NOT NULL,
    PRIMARY KEY(child_product_id, parent_product_id, role)
);

CREATE TABLE IF NOT EXISTS algorithm_registry (
    algorithm_id TEXT PRIMARY KEY,
    algorithm_name TEXT NOT NULL,
    algorithm_version TEXT NOT NULL,
    hazard_class TEXT NOT NULL CHECK (hazard_class IN ('A','B','C')),
    purity TEXT NOT NULL CHECK (purity IN (
        'pure',
        'deterministic_io',
        'external_read',
        'external_write'
    )),
    deterministic INTEGER NOT NULL CHECK (deterministic IN (0,1)),
    executable_kind TEXT NOT NULL CHECK (executable_kind IN (
        'builtin',
        'command',
        'c_abi',
        'noop_test'
    )),
    executable_ref TEXT NOT NULL,
    input_contract_json TEXT NOT NULL,
    output_contract_json TEXT NOT NULL,
    resource_contract_json TEXT NOT NULL,
    forbidden_resource_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(algorithm_name, algorithm_version)
);

CREATE TABLE IF NOT EXISTS workflow_runs (
    workflow_run_id TEXT PRIMARY KEY,
    workflow_name TEXT NOT NULL,
    workflow_spec_sha256 TEXT NOT NULL,
    driver_json TEXT NOT NULL,
    mode TEXT NOT NULL,
    decision_as_of TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN (
        'created',
        'running',
        'succeeded',
        'failed',
        'rejected'
    )),
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    diagnostics TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS workflow_nodes (
    node_run_id TEXT PRIMARY KEY,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(workflow_run_id),
    node_id TEXT NOT NULL,
    algorithm_id TEXT NOT NULL REFERENCES algorithm_registry(algorithm_id),
    cell_id TEXT NOT NULL,
    config_sha256 TEXT NOT NULL,
    input_product_ids_json TEXT NOT NULL,
    output_product_ids_json TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL CHECK (status IN (
        'created',
        'ready',
        'running',
        'succeeded',
        'failed',
        'rejected'
    )),
    started_at TEXT,
    finished_at TEXT,
    diagnostics TEXT NOT NULL DEFAULT '',
    UNIQUE(workflow_run_id, node_id, cell_id)
);

CREATE TABLE IF NOT EXISTS workflow_edges (
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(workflow_run_id),
    from_node_id TEXT NOT NULL,
    to_node_id TEXT NOT NULL,
    product_role TEXT NOT NULL,
    PRIMARY KEY(workflow_run_id, from_node_id, to_node_id, product_role)
);

CREATE TABLE IF NOT EXISTS resource_declarations (
    resource_id TEXT PRIMARY KEY,
    resource_kind TEXT NOT NULL CHECK (resource_kind IN (
        'cpu',
        'sqlite',
        'filesystem',
        'sec_http',
        'market_data_http',
        'broker',
        'notification',
        'clock'
    )),
    max_concurrent INTEGER NOT NULL DEFAULT 1,
    min_interval_ms INTEGER NOT NULL DEFAULT 0,
    allowed_modes_json TEXT NOT NULL DEFAULT '[]',
    hazard_class TEXT NOT NULL CHECK (hazard_class IN ('A','B','C')),
    description TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS graph_invariant_violations (
    violation_id TEXT PRIMARY KEY,
    workflow_run_id TEXT REFERENCES workflow_runs(workflow_run_id),
    invariant_id TEXT NOT NULL,
    subject_json TEXT NOT NULL,
    severity TEXT NOT NULL CHECK (severity IN ('error','warning')),
    explanation TEXT NOT NULL,
    created_at TEXT NOT NULL
);
```

## 2. Files to add

Add these Rust modules:

```text
cli/src/framework/mod.rs
cli/src/framework/canonical_json.rs
cli/src/framework/hash.rs
cli/src/framework/product.rs
cli/src/framework/registry.rs
cli/src/framework/graph.rs
cli/src/framework/invariants.rs
cli/src/framework/executor.rs
cli/src/framework/storage.rs
cli/src/framework/replay.rs
```

Add these contract/specification files:

```text
docs/contracts/product.schema.json
docs/contracts/algorithm.schema.json
docs/contracts/workflow.schema.json
docs/contracts/invariants.dl
docs/contracts/product_types.json
docs/contracts/algorithm_registry.bootstrap.json
docs/contracts/workflow.example.json
```

## 3. Deterministic identity logic

Implement canonical JSON with sorted object keys and no insignificant whitespace.

Mathematical definitions:

```text
canonical_json(x) =
  deterministic UTF-8 JSON encoding of x
  with lexicographically sorted object keys
  and stable number/string/bool/null encoding

sha256_hex(x) =
  lowercase hex SHA-256 digest of byte string x

content_sha256(product_payload) =
  sha256_hex(canonical_json(product_payload))

config_sha256(config_json) =
  sha256_hex(canonical_json(config_json))

product_id =
  "prod_" + sha256_hex(canonical_json({
    "product_type": product_type,
    "schema_name": schema_name,
    "schema_version": schema_version,
    "cell_id": cell_id,
    "family_id": family_id,
    "content_sha256": content_sha256,
    "created_by_algorithm": algorithm_id,
    "algorithm_version": algorithm_version,
    "config_sha256": config_sha256,
    "parents": sort(parent_product_ids),
    "available_at": available_at
  }))[0:48]

workflow_run_id =
  "wf_" + sha256_hex(canonical_json({
    "workflow_name": workflow_name,
    "workflow_spec_sha256": workflow_spec_sha256,
    "mode": mode,
    "decision_as_of": decision_as_of,
    "created_at": created_at
  }))[0:48]

node_run_id =
  "node_" + sha256_hex(canonical_json({
    "workflow_run_id": workflow_run_id,
    "node_id": node_id,
    "cell_id": cell_id,
    "algorithm_id": algorithm_id,
    "config_sha256": config_sha256,
    "input_product_ids": sort(input_product_ids)
  }))[0:48]
```

## 4. Product types

Create `docs/contracts/product_types.json`:

```json
{
  "spec_version": 1,
  "product_types": [
    {
      "product_type": "command_output.v1",
      "schema_name": "command_output",
      "schema_version": 1,
      "hazard_class": "C",
      "description": "Captured stdout/stderr/exit_code from a wrapped existing command."
    },
    {
      "product_type": "raw_filing_package.v1",
      "schema_name": "raw_filing_package",
      "schema_version": 1,
      "hazard_class": "B",
      "description": "Immutable SEC accession package payload and local object-store references."
    },
    {
      "product_type": "xbrl_fact_set.v1",
      "schema_name": "xbrl_fact_set",
      "schema_version": 1,
      "hazard_class": "B",
      "description": "Raw parsed XBRL facts, contexts, units, dimensions, and source document references."
    },
    {
      "product_type": "canonical_observation_set.v1",
      "schema_name": "canonical_observation_set",
      "schema_version": 1,
      "hazard_class": "B",
      "description": "Canonical accounting observations with period, basis, unit, dimensional scope, and quality semantics."
    },
    {
      "product_type": "statement_snapshot_set.v1",
      "schema_name": "statement_snapshot_set",
      "schema_version": 1,
      "hazard_class": "A",
      "description": "Model-consumable statement snapshots built from canonical observations."
    },
    {
      "product_type": "valuation_set.v1",
      "schema_name": "valuation_set",
      "schema_version": 1,
      "hazard_class": "A",
      "description": "Valuation outputs from pure model kernel."
    },
    {
      "product_type": "target_weight_set.v1",
      "schema_name": "target_weight_set",
      "schema_version": 1,
      "hazard_class": "A",
      "description": "Portfolio target weights generated by model/planner."
    },
    {
      "product_type": "order_intent_set.v1",
      "schema_name": "order_intent_set",
      "schema_version": 1,
      "hazard_class": "A",
      "description": "Intended portfolio actions before risk approval."
    },
    {
      "product_type": "risk_decision_set.v1",
      "schema_name": "risk_decision_set",
      "schema_version": 1,
      "hazard_class": "A",
      "description": "Approved/rejected risk decisions."
    },
    {
      "product_type": "staged_order_set.v1",
      "schema_name": "staged_order_set",
      "schema_version": 1,
      "hazard_class": "A",
      "description": "Approved orders persisted for possible broker submission."
    },
    {
      "product_type": "broker_event_set.v1",
      "schema_name": "broker_event_set",
      "schema_version": 1,
      "hazard_class": "A",
      "description": "Broker adapter events and acknowledgements."
    },
    {
      "product_type": "reconciliation_snapshot.v1",
      "schema_name": "reconciliation_snapshot",
      "schema_version": 1,
      "hazard_class": "A",
      "description": "Broker/account reconciliation state."
    },
    {
      "product_type": "report.v1",
      "schema_name": "report",
      "schema_version": 1,
      "hazard_class": "C",
      "description": "Human/operator report; never authority-bearing."
    }
  ]
}
```

## 5. Workflow JSON IR

Create `docs/contracts/workflow.schema.json`.

The first implementation may use Rust validation instead of a full JSON Schema engine, but the contract file must exist.

Create `docs/contracts/workflow.example.json`:

```json
{
  "spec_version": 1,
  "workflow_name": "shadow_value_pipeline",
  "driver": {
    "driver_id": "point_in_time_shadow.v1",
    "mode": "shadow",
    "decision_as_of": "2026-05-08T13:00:00.000Z",
    "allow_external_write": false,
    "allow_broker_resource": false
  },
  "layers": [
    {
      "layer": "job",
      "parent": null,
      "key_fields": ["workflow_name", "decision_as_of"]
    },
    {
      "layer": "issuer",
      "parent": "job",
      "key_fields": ["cik"]
    },
    {
      "layer": "accession",
      "parent": "issuer",
      "key_fields": ["cik", "accession_number"]
    },
    {
      "layer": "portfolio",
      "parent": "job",
      "key_fields": ["portfolio_id"]
    },
    {
      "layer": "decision_run",
      "parent": "portfolio",
      "key_fields": ["workflow_run_id"]
    }
  ],
  "resources": [
    {
      "resource_id": "sqlite.local",
      "resource_kind": "sqlite",
      "max_concurrent": 1,
      "min_interval_ms": 0,
      "allowed_modes": ["observe", "shadow", "stage", "paper", "live_limited", "live", "halted"],
      "hazard_class": "B"
    },
    {
      "resource_id": "sec.http",
      "resource_kind": "sec_http",
      "max_concurrent": 1,
      "min_interval_ms": 150,
      "allowed_modes": ["observe", "shadow", "stage", "paper", "live_limited", "live"],
      "hazard_class": "B"
    },
    {
      "resource_id": "broker.adapter",
      "resource_kind": "broker",
      "max_concurrent": 1,
      "min_interval_ms": 0,
      "allowed_modes": ["paper", "live_limited", "live"],
      "hazard_class": "A"
    }
  ],
  "nodes": [
    {
      "node_id": "reconcile",
      "algorithm": "cmd.recon.v1",
      "cell": {
        "layer": "portfolio",
        "key": {
          "portfolio_id": "default"
        }
      },
      "config": {
        "portfolio_value_usd": 100000.0,
        "cash_usd": 100000.0,
        "reconciled": true
      },
      "inputs": [],
      "outputs": [
        {
          "role": "reconciliation",
          "product_type": "reconciliation_snapshot.v1"
        }
      ]
    },
    {
      "node_id": "value",
      "algorithm": "cmd.value.v1",
      "cell": {
        "layer": "decision_run",
        "key": {
          "portfolio_id": "default"
        }
      },
      "config": {
        "core_lib": "build/libfolio.so",
        "portfolio_value_usd": 100000.0,
        "cash_usd": 100000.0,
        "target_gross_exposure_ratio": 0.50,
        "max_name_weight_ratio": 0.05,
        "max_order_notional_usd": 10000.0,
        "min_adv_usd": 1000000.0,
        "max_adv_participation_ratio": 0.01,
        "max_reconciliation_age_s": 3600
      },
      "inputs": [
        {
          "from": "reconcile",
          "role": "reconciliation"
        }
      ],
      "outputs": [
        {
          "role": "statement_snapshots",
          "product_type": "statement_snapshot_set.v1"
        },
        {
          "role": "valuations",
          "product_type": "valuation_set.v1"
        },
        {
          "role": "target_weights",
          "product_type": "target_weight_set.v1"
        },
        {
          "role": "order_intents",
          "product_type": "order_intent_set.v1"
        }
      ]
    },
    {
      "node_id": "gate",
      "algorithm": "cmd.gate.v1",
      "cell": {
        "layer": "decision_run",
        "key": {
          "portfolio_id": "default"
        }
      },
      "config": {
        "core_lib": "build/libfolio.so",
        "portfolio_value_usd": 100000.0,
        "cash_usd": 100000.0,
        "max_name_weight_ratio": 0.05,
        "max_order_notional_usd": 10000.0,
        "min_adv_usd": 1000000.0,
        "max_adv_participation_ratio": 0.01,
        "max_reconciliation_age_s": 3600
      },
      "inputs": [
        {
          "from": "value",
          "role": "order_intents"
        },
        {
          "from": "reconcile",
          "role": "reconciliation"
        }
      ],
      "outputs": [
        {
          "role": "risk_decisions",
          "product_type": "risk_decision_set.v1"
        }
      ]
    },
    {
      "node_id": "report",
      "algorithm": "cmd.report.daily.v1",
      "cell": {
        "layer": "job",
        "key": {
          "report": "daily"
        }
      },
      "config": {
        "date": "today"
      },
      "inputs": [
        {
          "from": "value",
          "role": "valuations"
        },
        {
          "from": "gate",
          "role": "risk_decisions"
        }
      ],
      "outputs": [
        {
          "role": "report",
          "product_type": "report.v1"
        }
      ]
    }
  ]
}
```

## 6. Algorithm registry bootstrap

Create `docs/contracts/algorithm_registry.bootstrap.json`:

```json
{
  "spec_version": 1,
  "algorithms": [
    {
      "algorithm_id": "cmd.recon.v1",
      "algorithm_name": "cmd.recon",
      "algorithm_version": "1",
      "hazard_class": "A",
      "purity": "deterministic_io",
      "deterministic": false,
      "executable_kind": "command",
      "executable_ref": "sec recon",
      "inputs": [],
      "outputs": ["reconciliation_snapshot.v1", "command_output.v1"],
      "resources": ["sqlite.local"],
      "forbidden_resources": ["broker.adapter"]
    },
    {
      "algorithm_id": "cmd.value.v1",
      "algorithm_name": "cmd.value",
      "algorithm_version": "1",
      "hazard_class": "A",
      "purity": "deterministic_io",
      "deterministic": true,
      "executable_kind": "command",
      "executable_ref": "sec value",
      "inputs": ["reconciliation_snapshot.v1"],
      "outputs": [
        "statement_snapshot_set.v1",
        "valuation_set.v1",
        "target_weight_set.v1",
        "order_intent_set.v1",
        "command_output.v1"
      ],
      "resources": ["sqlite.local"],
      "forbidden_resources": ["sec.http", "broker.adapter", "market_data_http"]
    },
    {
      "algorithm_id": "cmd.gate.v1",
      "algorithm_name": "cmd.gate",
      "algorithm_version": "1",
      "hazard_class": "A",
      "purity": "deterministic_io",
      "deterministic": true,
      "executable_kind": "command",
      "executable_ref": "sec gate",
      "inputs": ["order_intent_set.v1", "reconciliation_snapshot.v1"],
      "outputs": ["risk_decision_set.v1", "command_output.v1"],
      "resources": ["sqlite.local"],
      "forbidden_resources": ["sec.http", "broker.adapter", "market_data_http"]
    },
    {
      "algorithm_id": "cmd.stage.v1",
      "algorithm_name": "cmd.stage",
      "algorithm_version": "1",
      "hazard_class": "A",
      "purity": "deterministic_io",
      "deterministic": false,
      "executable_kind": "command",
      "executable_ref": "sec stage",
      "inputs": ["risk_decision_set.v1"],
      "outputs": ["staged_order_set.v1", "command_output.v1"],
      "resources": ["sqlite.local"],
      "forbidden_resources": ["broker.adapter"]
    },
    {
      "algorithm_id": "cmd.send.mock.v1",
      "algorithm_name": "cmd.send.mock",
      "algorithm_version": "1",
      "hazard_class": "A",
      "purity": "external_write",
      "deterministic": false,
      "executable_kind": "command",
      "executable_ref": "sec send --adapter mock",
      "inputs": ["staged_order_set.v1", "reconciliation_snapshot.v1"],
      "outputs": ["broker_event_set.v1", "command_output.v1"],
      "resources": ["sqlite.local", "broker.adapter"],
      "forbidden_resources": ["sec.http", "market_data_http"]
    },
    {
      "algorithm_id": "cmd.report.daily.v1",
      "algorithm_name": "cmd.report.daily",
      "algorithm_version": "1",
      "hazard_class": "C",
      "purity": "deterministic_io",
      "deterministic": false,
      "executable_kind": "command",
      "executable_ref": "sec report daily",
      "inputs": ["valuation_set.v1", "risk_decision_set.v1"],
      "outputs": ["report.v1", "command_output.v1"],
      "resources": ["sqlite.local"],
      "forbidden_resources": ["broker.adapter"]
    },
    {
      "algorithm_id": "noop.test.v1",
      "algorithm_name": "noop.test",
      "algorithm_version": "1",
      "hazard_class": "C",
      "purity": "pure",
      "deterministic": true,
      "executable_kind": "noop_test",
      "executable_ref": "noop",
      "inputs": [],
      "outputs": ["command_output.v1"],
      "resources": [],
      "forbidden_resources": ["broker.adapter", "sec.http", "market_data_http"]
    }
  ]
}
```

## 7. CLI commands

Add these commands to `cli/src/main.rs`:

```text
sec graph validate --db <path> --spec <workflow.json>
sec graph run      --db <path> --spec <workflow.json>
sec graph explain  --db <path> --spec <workflow.json>
sec product show   --db <path> --product-id <id>
sec product lineage --db <path> --product-id <id>
sec replay         --db <path> --workflow-run-id <id>
```

Semantics:

```text
graph validate:
  parse JSON
  canonicalize JSON
  validate required fields
  bootstrap product types and algorithms if absent
  construct nodes/edges
  reject cycles
  run invariant checker
  print JSON result

graph explain:
  same as validate
  print topological order, resource requirements, products expected

graph run:
  validate
  create workflow_runs row
  execute nodes sequentially in topological order
  for each node:
    resolve input products from previous node outputs
    run node-level invariants
    execute algorithm
    capture stdout/stderr/exit status
    persist command_output.v1 product
    persist declared domain products as storage refs if command output/domain table state can identify them
    persist data_product_lineage rows
    persist workflow_nodes rows
  mark workflow succeeded/failed/rejected

product show:
  query data_products by product_id
  print canonical JSON

product lineage:
  recursively walk data_product_lineage parents
  print DAG as JSON

replay:
  load workflow_run
  load workflow spec hash/driver
  verify products and lineage
  for deterministic algorithms, rerun if possible and compare output product hashes
```

## 8. Domain product persistence

In phase 1, domain product persistence can be conservative.

Every command wrapper must preserve `command_output.v1` with captured stdout, stderr, and exit code.

Known command storage references:

```text
cmd.recon.v1:
  output reconciliation_snapshot.v1
  storage_ref = "sqlite:broker_state:id=1"

cmd.value.v1:
  output statement_snapshot_set.v1
  storage_ref = "sqlite:statement_snapshots:latest_run_or_time"
  output valuation_set.v1
  storage_ref = "sqlite:valuations:run_id=<run_id>"
  output target_weight_set.v1
  storage_ref = "sqlite:target_weights:run_id=<run_id>"
  output order_intent_set.v1
  storage_ref = "sqlite:order_intents:run_id=<run_id>"

cmd.gate.v1:
  output risk_decision_set.v1
  storage_ref = "sqlite:risk_decisions:run_id=<run_id>"

cmd.stage.v1:
  output staged_order_set.v1
  storage_ref = "sqlite:staged_orders:run_id=<run_id>"

cmd.send.mock.v1:
  output broker_event_set.v1
  storage_ref = "sqlite:broker_events:after=<timestamp>"

cmd.report.daily.v1:
  output report.v1
  storage_ref = "stdout:<command_output_product_id>"
```

Run ID extraction:

```text
run_id_from_stdout(stdout):
  for each line:
    parse JSON
    if json.payload.run_id is string:
      return it
    if json.run_id is string:
      return it
  else return null
```

Cell identity:

```text
cell_id =
  layer + ":" + sha256_hex(canonical_json(key))[0:24]

family_id =
  parent_layer + ":" + sha256_hex(canonical_json(parent_key))[0:24]
  or null
```

## 9. Graph topology and scheduling

Graph definition:

```text
Let G = (V, E)
V = workflow nodes
E = dependency edges derived from node.inputs[*].from

valid_dag(G) ⇔ no directed cycle exists

topological_order(G) =
  sequence v_1...v_n such that
  ∀ edge (u → v), index(u) < index(v)
```

Kahn algorithm:

```text
Build adjacency from each node input:
  edge(input.from -> node.node_id)

indegree[v]
queue = nodes with indegree 0
pop, append to order, decrement children
if order.len != nodes.len:
  reject with invariant GRAPH_MUST_BE_ACYCLIC
```

Future parallel scheduling semantics:

```text
ready(v, t) ⇔
  status(v) = created
  ∧ ∀ parent u of v: status(u) = succeeded
  ∧ resources_available(v, t)
  ∧ invariants_hold(v)

resource_conflict(u, v) ⇔
  ∃ resource r:
    r ∈ resources(u)
    ∧ r ∈ resources(v)
    ∧ max_concurrent(r) = 1

parallelizable(u, v) ⇔
  no_path(u, v)
  ∧ no_path(v, u)
  ∧ ¬resource_conflict(u, v)
```

## 10. Invariant logic

Create `docs/contracts/invariants.dl`:

```prolog
% Facts to materialize from workflow JSON and database:
%
% node(Node).
% product(Product).
% consumes(Node, Product).
% produces(Node, Product).
% node_class(Node, Class).
% product_class(Product, Class).
% node_mode(Node, Mode).
% run_as_of(Run, Time).
% node_run(Node, Run).
% product_available_at(Product, Time).
% algorithm_resource(Node, Resource).
% resource_kind(Resource, Kind).
% resource_allowed_mode(Resource, Mode).
% edge(From, To).
% approved_risk_product(Product).
% staged_order_product(Product).
% broker_event_product(Product).
% reconciliation_product(Product).
% product_parent(Child, Parent).
% command_external_write(Node).

deny("NO_LOOKAHEAD", Node, Product) :-
    consumes(Node, Product),
    node_run(Node, Run),
    run_as_of(Run, DecisionTime),
    product_available_at(Product, AvailableTime),
    greater_than(AvailableTime, DecisionTime).

deny("NO_CLASS_A_FROM_CLASS_C", Node, Product) :-
    consumes(Node, Product),
    node_class(Node, "A"),
    product_class(Product, "C").

deny("BROKER_RESOURCE_MODE", Node, Resource) :-
    algorithm_resource(Node, Resource),
    resource_kind(Resource, "broker"),
    node_mode(Node, Mode),
    not resource_allowed_mode(Resource, Mode).

deny("NO_EXTERNAL_WRITE_IN_OBSERVE_OR_SHADOW", Node) :-
    command_external_write(Node),
    node_mode(Node, "observe").

deny("NO_EXTERNAL_WRITE_IN_SHADOW", Node) :-
    command_external_write(Node),
    node_mode(Node, "shadow").

deny("STAGED_ORDER_REQUIRES_APPROVED_RISK", Node, Product) :-
    produces(Node, Product),
    staged_order_product(Product),
    not has_approved_risk_parent(Product).

deny("BROKER_EVENT_REQUIRES_STAGED_ORDER", Node, Product) :-
    produces(Node, Product),
    broker_event_product(Product),
    not has_staged_order_parent(Product).

deny("GRAPH_MUST_BE_ACYCLIC", From, To) :-
    edge(From, To),
    path(To, From).
```

Equivalent mathematical invariants:

```text
I1. No lookahead
∀ node n, product p:
  consumes(n,p) ⇒ available_at(p) ≤ decision_as_of(run(n))

I2. Class authority
∀ node n, product p:
  class(n) = A ∧ consumes(n,p) ⇒ class(p) ∈ {A,B}

I3. C cannot create authority
∀ product p:
  class(p) = C ⇒ product_type(p) ∉ {
    order_intent_set.v1,
    risk_decision_set.v1,
    staged_order_set.v1,
    broker_event_set.v1,
    reconciliation_snapshot.v1
  }

I4. Broker mode
∀ node n:
  broker ∈ resources(n) ⇒ mode(run(n)) ∈ {paper, live_limited, live}

I5. Shadow/observe no external write
∀ node n:
  mode(run(n)) ∈ {observe, shadow} ⇒ purity(algorithm(n)) ≠ external_write

I6. Risk-before-stage
∀ staged_order_product s:
  ∃ risk_decision_product r:
    parent*(s,r) ∧ approved(r)

I7. Stage-before-broker
∀ broker_event_product b:
  ∃ staged_order_product s:
    parent*(b,s)

I8. DAG
No node may be reachable from itself:
  ∀ n: ¬path(n,n)

I9. Deterministic replay
For deterministic algorithm invocation a:
  same(inputs, config, code, algorithm_version) ⇒ same(output_content_sha256)

I10. Product lineage completeness
∀ product p where created_by_algorithm(p) ≠ "external":
  ∃ node n:
    produces(n,p)
  and all declared input products of n are linked as parents of p.
```

Risk/freshness formulas the graph invariant checker should understand:

```text
fresh(product, now, max_age_s) ⇔
  product.available_at is not null
  ∧ epoch(now) - epoch(product.available_at) ≤ max_age_s

reconciliation_fresh(recon, decision_as_of, max_reconciliation_age_s) ⇔
  recon.product_type = reconciliation_snapshot.v1
  ∧ fresh(recon, decision_as_of, max_reconciliation_age_s)

order_batch_cash_safe(batch, cash) ⇔
  Σ_{o ∈ batch, side(o)=buy} notional_usd(o) ≤ cash

adv_safe(order, adv_usd, max_adv_participation_ratio) ⇔
  notional_usd(order) ≤ adv_usd × max_adv_participation_ratio

name_weight_safe(order, max_name_weight_ratio) ⇔
  abs(target_weight_ratio(order)) ≤ max_name_weight_ratio
```

The graph framework must not replace the existing C++ risk gate. It must enforce topology and provenance. The existing C++ gate remains the authority for per-order deterministic hard risk decisions.

## 11. Rust data structures

Add these core Rust types.

```rust
// cli/src/framework/graph.rs

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct WorkflowSpec {
    pub spec_version: u32,
    pub workflow_name: String,
    pub driver: DriverSpec,
    pub layers: Vec<LayerSpec>,
    pub resources: Vec<ResourceSpec>,
    pub nodes: Vec<NodeSpec>,
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct DriverSpec {
    pub driver_id: String,
    pub mode: String,
    pub decision_as_of: String,
    pub allow_external_write: bool,
    pub allow_broker_resource: bool,
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct LayerSpec {
    pub layer: String,
    pub parent: Option<String>,
    pub key_fields: Vec<String>,
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct ResourceSpec {
    pub resource_id: String,
    pub resource_kind: String,
    pub max_concurrent: i64,
    pub min_interval_ms: i64,
    pub allowed_modes: Vec<String>,
    pub hazard_class: String,
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct NodeSpec {
    pub node_id: String,
    pub algorithm: String,
    pub cell: CellSpec,
    pub config: serde_json::Value,
    pub inputs: Vec<NodeInputSpec>,
    pub outputs: Vec<NodeOutputSpec>,
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct CellSpec {
    pub layer: String,
    pub key: serde_json::Value,
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct NodeInputSpec {
    pub from: String,
    pub role: String,
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct NodeOutputSpec {
    pub role: String,
    pub product_type: String,
}
```

```rust
// cli/src/framework/product.rs

#[derive(Debug, Clone)]
pub struct NewProduct {
    pub product_type: String,
    pub schema_name: String,
    pub schema_version: i64,
    pub cell_id: String,
    pub family_id: Option<String>,
    pub content_sha256: String,
    pub storage_kind: String,
    pub storage_ref: String,
    pub created_by_algorithm: String,
    pub algorithm_version: String,
    pub config_sha256: String,
    pub code_sha256: Option<String>,
    pub valid_time_start: Option<String>,
    pub valid_time_end: Option<String>,
    pub source_time: Option<String>,
    pub accepted_at: Option<String>,
    pub ingested_at: Option<String>,
    pub available_at: String,
    pub hazard_class: String,
    pub parent_product_ids: Vec<(String, String)>,
}
```

Command execution wrapper:

```rust
// cli/src/framework/executor.rs

pub struct CommandResult {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

pub fn run_sec_command(args: &[String]) -> anyhow::Result<CommandResult> {
    let exe = std::env::current_exe()?;
    let out = std::process::Command::new(exe)
        .args(args)
        .output()?;

    Ok(CommandResult {
        stdout: String::from_utf8_lossy(&out.stdout).to_string(),
        stderr: String::from_utf8_lossy(&out.stderr).to_string(),
        exit_code: out.status.code().unwrap_or(127),
    })
}
```

## 12. Command mapping

The executor must map algorithm IDs to existing CLI args.

```text
cmd.recon.v1:
  sec recon --db DB
            --portfolio-value-usd config.portfolio_value_usd
            --cash-usd config.cash_usd
            --reconciled 1|0

cmd.value.v1:
  sec value --db DB
            --core-lib config.core_lib
            --portfolio-value-usd config.portfolio_value_usd
            --cash-usd config.cash_usd
            --max-name-weight-ratio config.max_name_weight_ratio
            --target-gross-exposure-ratio config.target_gross_exposure_ratio
            --max-order-notional-usd config.max_order_notional_usd
            --min-adv-usd config.min_adv_usd
            --max-adv-participation-ratio config.max_adv_participation_ratio
            --max-reconciliation-age-s config.max_reconciliation_age_s

cmd.gate.v1:
  sec gate --db DB
           --core-lib config.core_lib
           --run-id resolved_run_id_from_value
           --portfolio-value-usd config.portfolio_value_usd
           --cash-usd config.cash_usd
           --max-name-weight-ratio config.max_name_weight_ratio
           --max-order-notional-usd config.max_order_notional_usd
           --min-adv-usd config.min_adv_usd
           --max-adv-participation-ratio config.max_adv_participation_ratio
           --max-reconciliation-age-s config.max_reconciliation_age_s

cmd.stage.v1:
  sec stage --db DB --run-id resolved_run_id

cmd.send.mock.v1:
  sec send --db DB --adapter mock
           --max-reconciliation-age-s config.max_reconciliation_age_s

cmd.report.daily.v1:
  sec report daily --db DB --date config.date
```

Graph run must reject stage/send in observe and shadow.

Mode order:

```text
halted       = 0
observe      = 1
shadow       = 2
stage        = 3
paper        = 4
live_limited = 5
live         = 6

can_run_external_write(mode) ⇔ mode ∈ {paper, live_limited, live}
can_stage(mode) ⇔ mode ∈ {stage, paper, live_limited, live}
can_submit(mode) ⇔ mode ∈ {paper, live_limited, live}
can_use_broker(mode) ⇔ mode ∈ {paper, live_limited, live}
```

## 13. Product extraction

Implement:

```text
persist_command_output_product:
  payload = {
    "stdout": stdout,
    "stderr": stderr,
    "exit_code": exit_code
  }
  content_sha256 = sha256(canonical_json(payload))
  storage_kind = "inline_json"
  storage_ref = canonical_json(payload)

persist_domain_product:
  payload = {
    "storage_ref": storage_ref,
    "algorithm_id": algorithm_id,
    "node_id": node_id,
    "workflow_run_id": workflow_run_id,
    "run_id": extracted_run_id_or_null,
    "created_at": now
  }
  content_sha256 = sha256(canonical_json(payload))
  storage_kind = "sqlite_ref"
  storage_ref = storage_ref
```

## 14. Bootstrap behavior

Modify `cmd_init`:

```text
After applying schema.sql:
  load docs/contracts/product_types.json
  insert product_types
  load docs/contracts/algorithm_registry.bootstrap.json
  insert algorithm_registry
  insert resource_declarations for known resources
```

## 15. Tests

Required tests:

```text
1. canonical_json_orders_keys
   Input objects with different key order produce identical bytes and identical hash.

2. product_id_is_deterministic
   Same product metadata and same parents produce same product_id.

3. product_id_changes_when_parent_changes
   Different parent hash produces different product_id.

4. graph_rejects_cycle
   workflow A->B->A fails validation.

5. graph_rejects_lookahead
   Product available_at > decision_as_of fails invariant checker.

6. graph_rejects_class_a_consuming_class_c
   A node with hazard_class A consuming report.v1 fails.

7. graph_rejects_broker_in_shadow
   Node using broker.adapter with mode shadow fails.

8. graph_allows_noop
   noop.test.v1 graph validates and runs, creating command_output.v1.

9. graph_run_records_lineage
   child products have parent rows in data_product_lineage.

10. graph_run_preserves_existing_cli
   Existing `sec` commands still parse and basic help output remains unchanged.

11. replay_deterministic_noop
   rerunning noop deterministic node produces identical product hash.
```

Add a local no-network fixture graph at `tests/fixtures/workflow_noop.json`:

```json
{
  "spec_version": 1,
  "workflow_name": "noop_fixture",
  "driver": {
    "driver_id": "test.v1",
    "mode": "observe",
    "decision_as_of": "2026-05-08T13:00:00.000Z",
    "allow_external_write": false,
    "allow_broker_resource": false
  },
  "layers": [
    {
      "layer": "job",
      "parent": null,
      "key_fields": ["workflow_name"]
    }
  ],
  "resources": [],
  "nodes": [
    {
      "node_id": "noop",
      "algorithm": "noop.test.v1",
      "cell": {
        "layer": "job",
        "key": {
          "workflow_name": "noop_fixture"
        }
      },
      "config": {
        "message": "test"
      },
      "inputs": [],
      "outputs": [
        {
          "role": "output",
          "product_type": "command_output.v1"
        }
      ]
    }
  ]
}
```

Noop algorithm behavior:

```text
input:
  config JSON

output payload:
  {
    "algorithm": "noop.test.v1",
    "config_sha256": sha256(canonical_json(config)),
    "message": config.message or ""
  }

No external command execution.
No IO except product persistence.
```

## 16. Replay semantics

```text
A workflow is replayable when every node algorithm has deterministic = true
or when non-deterministic products are treated as fixed external inputs.

For each deterministic node:
  replay_input_set = original input product ids and payload hashes
  replay_config_hash = original config hash
  replay_algorithm_version = original algorithm version

Replay success condition:
  ∀ deterministic node n:
    output_content_sha256_replay(n) = output_content_sha256_original(n)
```

## 17. Documentation

Add user-facing documentation in `docs/PRODUCT_GRAPH.md` with these sections:

```text
1. Purpose
2. Product identity
3. Layers and cells
4. Algorithm registry
5. Workflow JSON
6. Invariant logic
7. Execution modes
8. Replay
9. Relationship to existing commands
10. Safety boundary
```

## 18. Implementation order

```text
Step 1:
  Add SQL tables to schema.sql.
  Add product type and algorithm bootstrap JSON files.
  Modify cmd_init to seed them.

Step 2:
  Add canonical JSON and hash modules.
  Add tests for canonicalization and hashes.

Step 3:
  Add graph spec Rust types and JSON parser.
  Add graph validation:
    required fields
    duplicate node ids
    known algorithm ids
    known product types
    acyclic graph
    mode/resource compatibility
  Add tests.

Step 4:
  Add product persistence functions.
  Add product show and lineage commands.
  Add tests.

Step 5:
  Add noop executor.
  Add graph run for noop workflows.
  Add tests.

Step 6:
  Add command executor wrapper.
  Map cmd.recon.v1, cmd.value.v1, cmd.gate.v1, cmd.report.daily.v1.
  Keep stage/send mappings present but blocked unless mode permits.
  Add graph explain.

Step 7:
  Add domain product storage refs.
  Add lineage edges from input products to output products.

Step 8:
  Add replay command for deterministic noop and deterministic command-output hashes where possible.

Step 9:
  Add docs and update README with a small non-YAML graph example.

Step 10:
  Run:
    make check
    cargo test
    existing C++ tests
```

## 19. Acceptance criteria

```text
A. Existing quick-start commands still work.
B. `sec init` creates the new product graph tables.
C. `sec graph validate --db .fa.db --spec tests/fixtures/workflow_noop.json` succeeds.
D. A cyclic workflow fixture fails validation.
E. A broker-resource workflow in shadow mode fails validation.
F. `sec graph run --db .fa.db --spec tests/fixtures/workflow_noop.json` creates:
   - workflow_runs row
   - workflow_nodes row
   - data_products row
   - command_output.v1 product
G. `sec product show` prints deterministic JSON for the product.
H. `sec product lineage` prints a DAG JSON object.
I. `sec replay --workflow-run-id <id>` succeeds for noop deterministic runs.
J. No YAML files are introduced.
```

Preferred final CLI output format is JSONL:

```json
{"event":"workflow_validated","workflow_name":"noop_fixture","node_count":1,"edge_count":0}
{"event":"workflow_started","workflow_run_id":"wf_...","mode":"observe","decision_as_of":"2026-05-08T13:00:00.000Z"}
{"event":"node_succeeded","node_run_id":"node_...","node_id":"noop","output_count":1}
{"event":"workflow_succeeded","workflow_run_id":"wf_..."}
```

## 20. Design principle

Do not overfit to any one workflow syntax. Preserve the principles:

```text
data products, not hidden mutable state
layers/cells, not ad hoc loops
algorithm registration, not implicit command magic
higher-order workflow operators, not opaque process orchestration
resource declarations, not accidental concurrency
lineage and replay, not unverifiable side effects
```

The first pass should be boring. The valuable milestone is not parallel execution. The valuable milestone is that every decision artifact becomes a typed product with deterministic identity, lineage, and invariant-checked authority boundaries.
