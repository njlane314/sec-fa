# Intent and Principles

## 1. Mission statement

This system exists to convert public fundamental information into controlled, auditable portfolio actions.

It is not a dashboard. It is not an assistant. It is not a research notebook. It is a mission-control toolchain: small programs, explicit files, deterministic state transitions, append-only evidence, and hard risk gates.

The construction principle is:

```text
UNIX outside.
NASA inside.
```

UNIX outside means small programs, plain interfaces, composability, stable command-line behaviour, explicit stdout/stderr discipline, and tools that can be inspected under pressure.

NASA inside means bounded control flow, explicit requirements, hazard analysis, coding standards, static analysis, deterministic replay, objective evidence, fail-closed execution, and suspicion of hidden state.

## 2. What correctness means here

Correctness is not the same as profitable prediction.

A correct system can still make bad forecasts. An incorrect system can sometimes make money. The former is improvable; the latter is dangerous.

For this software, correctness means:

```text
same input + same code + same configuration = same output
```

and:

```text
no broker action can occur without a traceable chain of prior evidence
```

For every trade, the system must be able to reconstruct:

```text
raw filing
  -> parsed facts
  -> canonical facts
  -> model input snapshot
  -> forecast snapshot
  -> target weights
  -> order intent
  -> risk decision
  -> staged order
  -> broker event
  -> fill/reconciliation
```

If that chain is broken, the system is not fit to trade.

## 3. No backtesting by design

This system deliberately excludes historical trading backtesting. A backtest can be added only as a separate research program that cannot write to the trading ledger.

The accepted validation mechanisms are:

```text
deterministic replay
  Prior production events are replayed to verify that identical inputs produce identical outputs.

synthetic scenario testing
  Artificial filings, facts, positions, cash states, and broker failures are constructed to attack edge cases.

shadow operation
  The full pipeline runs against live data without order submission.

online forecast accounting
  New realised fundamentals are compared against prior forecasts after the fact.
```

The absence of backtesting requires lower live exposure, stricter abstention, and more conservative risk gates. The system must not infer confidence from untested historical simulation.

## 4. Separation of authority

No single component should have enough authority to observe, decide, and execute.

```text
SEC ingestion observes.
Canonicalization interprets accounting facts.
The model proposes.
The portfolio planner converts proposals into intended exposure.
The risk gate approves or rejects.
The broker adapter transmits only approved staged orders.
The reconciler determines whether the internal ledger still agrees with external truth.
```

The model is never the execution authority.

The broker adapter is never the investment authority.

The notification system is never the approval authority.

## 5. Append-only evidence

Events are facts about what happened. They are not mutable status fields.

Do not update an order from `pending` to `filled` as if the intermediate states never existed. Record:

```text
order_intent_created
risk_decision_approved
order_staged
broker_order_submitted
broker_order_acknowledged
fill_received
broker_reconciliation_passed
```

Mutable tables may exist as convenience views, caches, or indexes. They do not replace the event log.

## 6. Failure posture

The system fails closed.

If filing ingestion fails, do not trade on stale facts.

If canonicalization confidence is low, abstain.

If model output contains NaN or infinity, reject the run.

If broker reconciliation is stale, reject all order intents.

If cash, positions, or open orders disagree with the broker, halt trading.

If the execution mode is ambiguous, assume `halted`.

## 7. Side-effect discipline

Every component must answer four questions:

```text
What exactly do I consume?
What exactly do I produce?
What state am I allowed to mutate?
What invariant must remain true if I fail halfway?
```

A component that cannot answer those questions should not exist.

Stdout is for machine-readable output. Stderr is for diagnostics. Exit code is for operational state.

A command that mutates external state must have a name that says so:

```text
fetch
stage
submit
reconcile
halt
```

A command named `show`, `report`, or `plan` must not submit an order.

## 8. Class boundaries

The software is divided by hazard level.

```text
Class A: trading-control code
  C++ core, risk gate, broker submitter, reconciler, kill switch, ledger invariants.

Class B: correctness-critical data code
  SEC fetcher, filing parser, fact canonicalizer, universe builder, model input builder.

Class C: observation and convenience
  reports, notifications, export tools, future UI, future MCP interface.
```

Class A code is constrained most heavily. Class B code is allowed to use more libraries because SEC filings and XBRL are messy, but it cannot submit orders. Class C code must never become part of the execution authority.

## 9. Naming as control

Names are part of the safety mechanism. Ambiguous names hide state and side effects.

Use precise verbs:

```text
fetch       external retrieval, no interpretation
parse       syntax to structure
normalize   format/unit/schema consistency
canonicalize domain choice of canonical fact
build       derived object construction
plan        intended action, no external side effect
stage       persist approved action before execution
submit      transmit to external broker
reconcile   compare internal state to external truth
halt        prevent future trading action
```

Avoid names such as `process`, `handle`, `manager`, `helper`, `util`, and `do_stuff`.

## 10. What this repository should become

The mature form of this system is not larger in spirit. It should remain a chain of named tools with stable contracts.

Future additions should respect the existing shape:

```text
XBRL package parser    -> writes raw facts and canonical observations
IBKR adapter           -> reads staged orders, writes broker events
web dashboard          -> reads events/views, never becomes authority
MCP interface          -> read-only initially, never direct broker execution
research code          -> separate from trading ledger
```

The most important product is not the alpha model. It is the auditable path from evidence to action.

## 11. Primary influences

- Doug McIlroy's UNIX philosophy: programs should do one thing well, work together, and use streams as a universal interface.
- Gerard Holzmann's NASA/JPL Power of Ten discipline: restrict control flow, bound resource use, check return values, keep functions small, use assertions, avoid excessive preprocessing, and build code that tools can analyze.
- NASA software assurance practice: requirements, coding standards, static analysis, test evidence, and traceability are engineering artefacts, not paperwork.

## Accounting observations are not scalars

A financial metric name is not a value. `revenue`, `cash`, and `EPS` are families of observations distinguished by measurement basis, reporting interval, period semantics, unit, dimensional scope, and source lineage.

Therefore the system shall not collapse raw SEC facts directly into model features. The required chain is:

```text
source_documents
  -> xbrl_contexts / xbrl_units / xbrl_facts
  -> canonical_observations
  -> derived observations with lineage
  -> model features
```

A quarterly revenue fact, a six-month YTD revenue fact, and an annual revenue fact may share the same accounting concept. They are not interchangeable. The canonical layer must preserve `period_semantics`, `duration_days`, `basis_id`, `unit_signature`, and `dimensions_hash`; the C++ core must reject model comparisons whose period, basis, or dimensional scope is incompatible.

The design preference is conservative failure. An ambiguous candidate set is evidence, not permission. The resolver may store an ambiguous observation with flags for audit, but the model kernel must not treat that observation as comparable input.
