# Hazard Analysis

## H-001 stale broker state causes unsafe order

Causes:

```text
broker connection loss
partial fill not recorded
reconciliation command not run
clock/configuration error
```

Controls:

```text
risk gate requires fresh reconciliation
risk gate rejects all intents if stale
order staging references risk decision
daily report displays reconciliation state
```

Evidence:

```text
tests/test_core.cpp
ci/check stale reconciliation smoke path
```

## H-002 wrong issuer identity

Causes:

```text
ticker reused
ticker changed
multiple share classes
manual security entry error
```

Controls:

```text
CIK/security_id primary identity
symbol is display metadata only
security-upsert requires explicit CIK
future security master must include IBKR contract id
```

## H-003 SEC fact canonicalization error

Causes:

```text
company-specific extension tag
unexpected unit
restatement
fiscal calendar change
missing comparable period
```

Controls:

```text
quality_flags stored with facts
canonical mapping is explicit
raw artefacts stored for inspection
model reduces confidence when quality flags are present
```

## H-004 duplicate event or duplicate filing

Controls:

```text
accession_number unique in filings table
event_id unique in events table
content hashes stored for raw artefacts
```

## H-005 model emits NaN or infinity

Controls:

```text
C++ core validates finite numerical inputs
risk gate validates finite order notionals and weights
invalid output should fail CI and command path
```

## H-006 aggregate buy notional exceeds cash

Controls:

```text
risk gate computes aggregate buy notional before approval
all buys are rejected when aggregate buy notional exceeds cash
```

## H-007 component accidentally submits live order

Controls:

```text
only broker submitter should contain broker credentials
current broker submitter is guarded mock only
operating mode is explicit in settings
future broker adapter must run on isolated host
```

## H-008 filing text or future assistant prompt attempts to issue instructions

Controls:

```text
filings are untrusted data
no LLM/MCP authority in this repository
future MCP tools must be read-only first
broker credentials are not exposed to assistant interfaces
```
