# Interface Control

## 1. C ABI

The public C ABI is `abi/core.h`. All public structs include `abi_version` and all functions return explicit status codes.

The C++ `fa_build_portfolio_plan_v1` entrypoint consumes:

```text
canonical facts
securities
positions
model configuration
risk-limit context
```

and produces:

```text
forecasts
target weights
order intents
diagnostics
```

The C++ `fa_build_statement_snapshots_v1` entrypoint consumes:

```text
canonical observations
securities
model configuration
risk-limit time context
```

and produces:

```text
periodized statement snapshots
diagnostics
```

The Rust `value` command loads canonical observations and persists returned snapshots. It must not construct TTM flows, choose valuation statement anchors, or assign statement-quality flags itself.

The Rust `feat` command consumes persisted statement snapshots and writes
point-in-time feature snapshots. It may compute ratios and growth fields from
those statement rows, but it must preserve source snapshot IDs, input hashes, and
quality flags.

The C++ `fa_build_valuation_plan_v1` entrypoint consumes:

```text
statement snapshots
securities
positions
valuation scenarios
model configuration
risk-limit context
```

and produces:

```text
valuations
target weights
order intents
diagnostics
```

The C++ `fa_check_risk_limits_v1` entrypoint consumes:

```text
order intents
securities
risk limits
```

and produces:

```text
risk decisions
diagnostics
```

No exported function submits orders or performs IO.

## 2. Event interface

Events are JSON payloads stored in the `events` table. They are append-only evidence. The minimum event envelope is:

```json
{
  "event_id": "uuid",
  "event_type": "name",
  "event_version": 1,
  "occurred_at": "UTC instant",
  "payload": {}
}
```

Schema contract files live in `docs/contracts/`. They are plain, line-oriented
contracts; the format is documented in `docs/contracts/FORMAT`.

## 3. Workflow interface

The public Rust executable consumes `.flow` workflow files:

```text
sec [--db PATH] [--validate|--explain] WORKFLOW.flow
```

Stdout is JSONL machine-readable workflow evidence, stderr is human-readable
diagnostics, and the exit code is the operational status. Individual operations
such as reconciliation, model execution, risk gating, staging, and guarded mock
submission are internal Rust operations addressed by algorithm ids in the
workflow graph, not public CLI verbs.

## 4. Database interface

SQLite is the single implemented database interface for local/single-node operation. Do not treat it as a broker-host coordination mechanism for a multi-node live deployment.

## 5. Broker adapter replacement point

The current broker submitter implements only a guarded mock adapter. The live adapter must preserve the same upstream contract:

```text
input: staged_orders joined to approved risk_decisions and order_intents
output: broker_events and append-only events
precondition: trading_mode in paper/live_limited/live
precondition: fresh reconciliation exists
```

It must not call the model or canonicalizer.
