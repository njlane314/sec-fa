# Interface Control

## 1. C ABI

The public C ABI is `abi/core.h`. All public structs include `abi_version` and all functions return explicit status codes.

The C++ `fa_plan_v1` entrypoint consumes:

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

The C++ `fa_value_v1` entrypoint consumes:

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

The C++ `fa_gate_v1` entrypoint consumes:

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

## 3. Command interface

Commands obey:

```text
stdout = machine-readable result
stderr = human-readable diagnostics
exit code = operational status
```

Commands that mutate state have names that expose the side effect: `upsert`, `fetch`, `import`, `reconcile`, `stage`, `submit`, `halt`.

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
