# Naming Standard

## 1. Units and time semantics

Use suffixes that encode semantics:

```text
*_id        internal stable identifier
*_key       external or natural key
*_ref       reference to another object
*_at        UTC instant
*_date      calendar date
*_day       epoch day integer
*_period    accounting interval
*_count     cardinality
*_limit     hard bound
*_ratio     unitless decimal ratio
*_bps       basis points
*_usd       US-dollar amount
*_shares    share quantity
*_hash      content hash
*_uri       storage/network location
```

Good:

```text
accepted_at
ingested_at
available_at
filing_date
period_end_day
revenue_usd
gross_margin_ratio
target_weight_ratio
max_order_notional_usd
raw_filing_sha256
```

Bad:

```text
data
info
value
amount
manager
handler
processor
helper
util
latest
current
```

A bad word can become acceptable only when it is domain-qualified, such as `latest_filing_for_cik`.

## 2. Verbs

```text
fetch        retrieve external bytes
parse        syntax to structure
normalize    make schema/unit/format consistent
canonicalize choose the domain interpretation
build        construct a derived object
plan         compute intended action
stage        persist approved action before execution
submit       send to external broker
reconcile    compare internal state to external truth
halt         prevent future trading action
```

Prefer:

```text
parse_xbrl_document
normalize_fact_units
canonicalize_revenue_fact
build_universe_snapshot
build_portfolio_plan
check_risk_limits
stage_order_intents
submit_staged_orders
reconcile_broker_positions
```

Avoid:

```text
process_filing
handle_order
update_data
run_model_stuff
```

## 3. C/C++ symbol convention

```text
C ABI symbols:     fa_ prefix, lower_snake_case
Types:             fa_name_v1 for ABI structs, PascalCase only inside private C++ if needed
Functions:         lower_snake_case
Variables:         lower_snake_case
Constants/macros:  FA_UPPER_SNAKE_CASE
```

## 4. Executables

Use single lowercase words. Do not use the project name as a binary prefix, and do not make command names by hyphenating a sentence.

Use nouns for persisted objects, verbs for gates and side effects, and keep the normal operating sequence readable left to right:

```text
bootstrap
security
position
filings
archive
company
xbrl
universe
reconcile
model
value
risk
stage
submit
report
notify
status
mode
halt
```

Prefer composition through the operational store over long command names. Example:

```text
filings -> archive -> xbrl -> universe -> reconcile -> model -> risk -> stage -> submit -> report
```

## Observation naming

Use `*_observation` for a versioned interpretation of one or more raw facts. Use `*_fact` only for lossless source-level data.

Required suffixes:

```text
*_basis_id          measurement definition, not merely concept name
*_period_semantics  fiscal_quarter, fiscal_ytd, fiscal_year, instant, etc.
*_duration_days     inclusive reporting-period length
*_dimensions_hash   stable signature of XBRL dimensions
*_scope             consolidated_total, segment, product, geography, etc.
```

Avoid generic accessors such as `get_revenue`. Prefer names that encode semantics, for example `load_revenue_fiscal_quarter_observations` or `derive_quarter_from_ytd`.
