# Live Readiness Gates

This repository defaults to non-live operation. A live deployment must pass these gates before broker credentials are introduced.

## Gate 1: data authority

```text
raw SEC artefacts are stored immutably
canonical fact rules are reviewed
accession-aware XBRL parsing is reviewed against real 10-K/10-Q packages across the investable universe
security master includes CIK, ticker, exchange, currency, share class, listing status, and IBKR contract id
corporate-action handling is implemented
```

## Gate 2: deterministic operation

```text
same production input snapshot reproduces same model output
model output contains no NaN or infinity
all numerical inputs are unit-tagged
all timestamps distinguish accepted_at, ingested_at, and available_at
```

## Gate 3: risk authority

```text
fresh broker reconciliation required for approval
aggregate buy notional cannot exceed cash
per-name, per-order, ADV, and mode limits enforced
risk decisions reference prior order intents
staged orders reference approved risk decisions
```

## Gate 4: broker isolation

```text
broker adapter runs on isolated host
broker credentials exist only on broker host
broker submitter cannot ingest SEC data or run models
all broker responses are persisted
reconciler compares positions, cash, open orders, fills, and commissions
```

## Gate 5: operating modes

```text
observe runs without model order intents
shadow creates order intents but cannot stage
stage requires explicit operator mode
paper uses broker paper account only
live_limited enforces very small notional caps
live requires signed release artefact and current reconciliation
halted blocks staging and submission
```

## Gate 6: evidence

```text
requirements matrix updated
hazard table updated
check passes
sanitizer builds pass
static analysis passes
synthetic risk scenarios pass
operator runbook reviewed
rollback procedure tested
```
