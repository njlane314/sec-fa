# Command Composition

The public programs are small, single-word commands. They compose like UNIX tools in the sense that each command has a narrow contract, writes useful stdout/stderr, returns a meaningful exit code, and can be chained by the shell. They are not byte-stream filters. Their primary handoff surfaces are the operational database, the immutable raw artifact store, the append-only event ledger, and explicit command-line paths.

That distinction is intentional. A financial control system should not pass trading state through ad hoc text pipes. It should materialize named objects, record decisions, and make the next command consume those recorded objects.

## 1. Handoff model

```text
bootstrap
  │
  ├── security
  └── position

filings ──► archive ──► xbrl ──► universe
                                  │
reconcile ────────────────────────┤
                                  ▼
                           model or value
                                  ▼
                                risk
                                  ▼
                                stage
                                  ▼
                               submit
                                  ▼
                         reconcile ──► report
                                  └──► notify
```

Each arrow is a state transition with an auditable record. The usual shared handles are:

```sh
DB=.folio.db
RAW=raw
LIB=build/libfolio.so
[ -f "$LIB" ] || LIB=build/libfolio.dylib
UA="Your Name your.email@example.com"
```

`DB` is the operational store and ledger. `RAW` is the immutable SEC artifact store. `LIB` is the deterministic C++ core. `UA` is the SEC User-Agent.

## 2. Command roles

```text
bootstrap   create the database and initial observe-mode state
security    upsert issuer/security metadata
position    upsert current internal position state
filings     discover recent SEC filings
archive     fetch immutable SEC filing artifacts
company     import SEC companyfacts fallback data
xbrl        parse an accession package and resolve observations
universe    construct the investable/research universe
reconcile   record external broker/cash truth
model       run the baseline deterministic model
value       run the valuation model path
risk        convert intents into approved/rejected risk decisions
stage       persist approved orders before execution
submit      send staged orders to the selected adapter
report      render state, decisions, and outcomes
notify      send an operator notification
status      summarize current operating state
mode        change operating mode by explicit operator action
halt        prevent future trading action
```

The normal verb direction is left to right:

```text
discover -> fetch -> parse -> build -> reconcile -> model -> check -> stage -> submit -> reconcile -> report
```

## 3. Initialize and seed one issuer

```sh
DB=.folio.db

bin/bootstrap --db "$DB" &&
bin/security --db "$DB" \
  --cik 0000320193 \
  --symbol AAPL \
  --price-usd 200 \
  --adv-usd 5000000000 \
  --investable 1 &&
bin/position --db "$DB" \
  --cik 0000320193 \
  --quantity-shares 0 \
  --market-value-usd 0 \
  --weight-ratio 0
```

This is analogous to creating a working tree before using `git`: later commands assume the state root exists.

## 4. Ingest one issuer

```sh
DB=.folio.db
RAW=raw
UA="Your Name your.email@example.com"

bin/filings --db "$DB" \
  --cik 0000320193 \
  --user-agent "$UA" &&
bin/archive --db "$DB" \
  --raw-root "$RAW" \
  --user-agent "$UA" &&
bin/xbrl --db "$DB" \
  --accession 0000320193-24-000123 \
  --cik 0000320193 \
  --symbol AAPL \
  --raw-root "$RAW" \
  --user-agent "$UA"
```

`filings` discovers filing metadata. `archive` fetches the raw artifacts. `xbrl` turns one accession package into raw facts, periods, dimensions, and canonical observations.

## 5. Ingest several issuers, then build one universe

```sh
DB=.folio.db
RAW=raw
UA="Your Name your.email@example.com"

for cik in 0000320193 0000789019 0001652044; do
  bin/filings --db "$DB" --cik "$cik" --user-agent "$UA"
done

bin/archive --db "$DB" --raw-root "$RAW" --user-agent "$UA" &&
bin/universe --db "$DB" --name us_core --min-adv-usd 0 --min-fact-count 2
```

This is fan-in composition. Many `filings` calls feed one archive pass and one universe snapshot.

## 6. Research-only run

```sh
DB=.folio.db
LIB=build/libfolio.so
[ -f "$LIB" ] || LIB=build/libfolio.dylib

bin/reconcile --db "$DB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 \
  --reconciled 1 &&
bin/model --db "$DB" \
  --core-lib "$LIB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 &&
bin/value --db "$DB" \
  --core-lib "$LIB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 &&
bin/risk --db "$DB" \
  --core-lib "$LIB" &&
bin/report daily --db "$DB"
```

This chain is safe for observe or research operation because it stops at risk decisions. It does not call `stage` or `submit`.

## 7. Stage approved intents without broker submission

```sh
DB=.folio.db
LIB=build/libfolio.so
[ -f "$LIB" ] || LIB=build/libfolio.dylib

bin/reconcile --db "$DB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 \
  --reconciled 1 &&
bin/model --db "$DB" \
  --core-lib "$LIB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 &&
bin/risk --db "$DB" \
  --core-lib "$LIB" &&
bin/stage --db "$DB" &&
bin/report daily --db "$DB"
```

This is the shadow/paper boundary. `stage` consumes only approved risk decisions. It still does not speak to the broker adapter.

## 8. Mock adapter submission

```sh
DB=.folio.db

bin/stage --db "$DB" &&
bin/submit --db "$DB" --adapter mock &&
bin/reconcile --db "$DB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 \
  --reconciled 1 &&
bin/report daily --db "$DB"
```

`submit` is the only public program that crosses the execution boundary. In the current implementation, `--adapter mock` is the guarded adapter.

## 9. Report and notify as fan-out

```sh
DB=.folio.db

bin/report daily --db "$DB" &&
bin/status --db "$DB" &&
bin/notify \
  --method ntfy \
  --ntfy-topic your-secret-topic \
  --message "Daily run complete"
```

This is fan-out from the ledger. `report`, `status`, and `notify` should not mutate trading decisions.

## 10. Failure behavior

Use `&&` when the next command must not run after a failed precondition:

```sh
bin/reconcile --db "$DB" --portfolio-value-usd 100000 --cash-usd 100000 --reconciled 1 &&
bin/model --db "$DB" --core-lib "$LIB" --portfolio-value-usd 100000 --cash-usd 100000 &&
bin/risk --db "$DB" --core-lib "$LIB" &&
bin/stage --db "$DB"
```

Use `;` only when the commands are independent diagnostics:

```sh
bin/status --db "$DB" ; bin/report daily --db "$DB"
```

That distinction is the command-line safety contract: gates are chained with `&&`, observability can fan out.
