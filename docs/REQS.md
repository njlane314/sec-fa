# Requirements

This document is intentionally terse. Requirements should be testable.

## Safety and execution

```text
FA-SAF-001
  The system shall not submit any broker order unless the latest broker reconciliation has passed within the configured freshness interval.

FA-SAF-002
  The system shall reject all order intents if broker reconciliation is stale, missing, or failed.

FA-SAF-003
  The system shall not approve an order whose notional value exceeds max_order_notional_usd.

FA-SAF-004
  The system shall not approve an order whose target weight exceeds max_name_weight_ratio.

FA-SAF-005
  The system shall not approve an order for a non-investable security.

FA-SAF-006
  The system shall not approve aggregate buy notional greater than available cash.

FA-SAF-007
  The system shall make live broker submission impossible from any component except the broker submitter.

FA-SAF-008
  The system shall default to observe mode after database initialization.
```

## Data

```text
FA-DATA-001
  The system shall store raw SEC artefacts with content hashes.

FA-DATA-002
  The system shall distinguish filing_date, accepted_at, ingested_at, and available_at.

FA-DATA-003
  The system shall store canonical facts with source accession, tag, taxonomy, unit, and quality flags when available.

FA-DATA-004
  The system shall never treat ticker as a stable issuer identity. CIK/security_id is the primary identity in the current implementation.
```

## Model

```text
FA-MODEL-001
  The C++ model core shall be deterministic for identical inputs and configuration.

FA-MODEL-002
  The C++ model core shall not access network, filesystem, environment variables, or wall-clock time.

FA-MODEL-003
  The C++ ABI shall not expose STL containers.

FA-MODEL-004
  The model shall emit order intents, not broker orders.

FA-MODEL-012
  The C++ model core shall build valuation statement snapshots from canonical observations; Rust shall not derive TTM flows or statement-quality flags for valuation.
```

## Ledger

```text
FA-LEDGER-001
  Material state changes shall emit append-only events.

FA-LEDGER-002
  Risk decisions shall reference prior order intents.

FA-LEDGER-003
  Staged orders shall reference approved risk decisions.
```

## Verification matrix

```text
requirement_id | method                 | evidence
FA-SAF-001     | C++ unit + CI smoke     | tests/test_core.cpp, cli/src/main.rs, check
FA-SAF-002     | C++ unit + CI smoke     | tests/test_core.cpp, check
FA-SAF-003     | C++ risk gate           | engine/gate.cpp
FA-SAF-004     | C++ risk gate           | engine/gate.cpp
FA-SAF-005     | C++ risk gate           | engine/gate.cpp
FA-SAF-006     | C++ risk gate           | engine/gate.cpp
FA-SAF-008     | DB initialization check | schema.sql
FA-MODEL-002   | code review + checks    | engine has no IO imports/calls
FA-MODEL-003   | header inspection       | abi/core.h
FA-MODEL-004   | ABI inspection          | fa_order_intent_v1 only
FA-MODEL-012   | C++ unit + CLI path     | fa_build_statement_snapshots_v1, tests/test_valuation.cpp, check
```

## Accounting observation requirements

FA-DATA-010
: The system shall store raw source documents before any parsing or canonicalization.

FA-DATA-011
: The system shall store raw XBRL/companyfacts facts with concept, unit, period, context, accession, value, and source document lineage before resolving internal metrics.

FA-DATA-012
: The system shall represent reporting periods explicitly with period kind, period semantics, duration in days, fiscal year, fiscal period, and raw SEC dates.

FA-DATA-013
: The system shall represent canonical observations as versioned derived interpretations of raw facts, not as the source of truth.

FA-DATA-014
: The system shall preserve measurement basis, unit signature, and dimensional scope for every canonical observation.

FA-MODEL-010
: The C++ model kernel shall not compare flow metrics unless their period semantics, duration class, measurement basis, unit, and dimensional scope are compatible.

FA-MODEL-011
: The C++ model kernel shall reject ambiguous canonical observations as model-comparable inputs.

FA-DATA-015
: Any observation derived by arithmetic, including YTD subtraction, shall carry lineage to its parent observations and a quality flag identifying the derivation.

FA-DATA-016
: Feature snapshots shall be immutable, point-in-time materializations that reference source statement snapshots and carry input hashes plus quality flags.
