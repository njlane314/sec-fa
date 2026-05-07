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
broker submitter rechecks reconciliation freshness before adapter submission
order staging references risk decision
daily report displays reconciliation state
```

Evidence:

```text
tests/test_core.cpp
check stale reconciliation smoke path
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

## Accounting-period hazards

hazard: YTD revenue consumed as quarterly revenue
: control: period semantics and duration are part of canonical observation identity; the C++ ABI carries `period_semantics` and `duration_days`; model fact selection accepts only fiscal-quarter flow observations with quarter-like duration.

hazard: concept switch creates false growth signal
: control: canonical observations carry `basis_id`; the C++ kernel compares only observations with the same basis.

hazard: segment/product/geography revenue consumed as consolidated revenue
: control: observations carry `dimensions_hash` and `dimensional_scope`; model input loading selects consolidated-total observations and the C++ kernel compares only matching dimension signatures.

hazard: derived quarter created from incompatible YTD facts
: control: YTD subtraction is allowed only when metric, basis, unit, dimensions, fiscal year, and fiscal-year start are compatible; lineage is written for both operands.

## XBRL parser hazards

### Malformed inline-XBRL document produces partial facts

Controls:

- Raw source documents are stored before parsing.
- Parser warnings are included in `xbrl_package_parsed` events.
- `--strict` mode fails closed on parser warnings/errors.

### Segment revenue contaminates consolidated revenue

Controls:

- Context dimensions are hashed and classified before fact insertion.
- Canonical resolution rejects non-`consolidated_total` facts by default.
- CI includes a dimensional product-revenue fixture that is stored as a raw fact but not selected canonically.
