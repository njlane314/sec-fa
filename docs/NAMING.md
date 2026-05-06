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

Use lower-kebab-case:

```text
fa-sec-watch
fa-sec-fetch
fa-facts-companyfacts
fa-model-run
fa-risk-check
fa-order-stage
fa-broker-submit
fa-broker-reconcile
fa-report
```
