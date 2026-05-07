# Command Composition

The public programs are small, single-word commands. They compose like UNIX tools in the sense that each command has a narrow contract, writes useful stdout/stderr, returns a meaningful exit code, and can be chained by the shell. They are not byte-stream filters. Their primary handoff surfaces are the operational database, the immutable raw artifact store, the append-only event ledger, and explicit command-line paths.

That distinction is intentional. A financial control system should not pass trading state through ad hoc text pipes. It should materialize named objects, record decisions, and make the next command consume those recorded objects.

## 1. Handoff model

```text
init
  │
  ├── sym
  └── pos

watch ──► pull ──► xbrl ──► univ
                                  │
recon ─────────────────────────────┤
                                  ▼
                           plan or value
                                  ▼
                                gate
                                  ▼
                                stage
                                  ▼
                                send
                                  ▼
                           recon ──► report
                                  └──► ping
```

Each arrow is a state transition with an auditable record. The usual shared handles are:

```sh
DB=.fa.db
RAW=raw
LIB=build/libfolio.so
[ -f "$LIB" ] || LIB=build/libfolio.dylib
UA="Your Name your.email@example.com"
```

`DB` is the operational store and ledger. `RAW` is the immutable SEC artifact store. `LIB` is the deterministic C++ core. `UA` is the SEC User-Agent.

## 2. Command roles

```text
init    create the database and initial observe-mode state
sym     upsert issuer/security metadata
pos     upsert current internal position state
watch   discover recent SEC filings
pull    fetch immutable SEC filing artifacts
xbrl    parse an accession package and resolve observations
univ    construct the investable/research universe
recon   record external broker/cash truth
plan    run the baseline deterministic model
value   run the valuation model path
gate    convert intents into approved/rejected risk decisions
stage   persist approved orders before execution
send    send staged orders to the selected adapter
report  render state, decisions, and outcomes
ping    send an operator notification
stat    summarize current operating state
mode    change operating mode by explicit operator action
halt    prevent future trading action
```

The normal verb direction is left to right:

```text
discover -> fetch -> parse -> build -> recon -> plan -> gate -> stage -> send -> recon -> report
```

## 3. Initialize and seed one issuer

```sh
DB=.fa.db

make setup-db DB="$DB" &&
./sec sym --db "$DB" \
  --cik 0000320193 \
  --symbol AAPL \
  --price-usd 200 \
  --adv-usd 5000000000 \
  --investable 1 &&
./sec pos --db "$DB" \
  --cik 0000320193 \
  --quantity-shares 0 \
  --market-value-usd 0 \
  --weight-ratio 0
```

This is analogous to creating a working tree before using `git`: later commands assume the state root exists.

## 4. Ingest one issuer

```sh
DB=.fa.db
RAW=raw
export SEC_USER_AGENT="Your Name your.email@example.com"

./sec watch --db "$DB" \
  --cik 0000320193 \
  --limit 1 &&
./sec pull --db "$DB" \
  --raw-root "$RAW" \
  --cik 0000320193 \
  --limit 1 &&
./sec xbrl --db "$DB" \
  --latest \
  --cik 0000320193 \
  --raw-root "$RAW"
```

`watch` discovers filing metadata from the SEC submissions feed. `pull` fetches the raw package artifacts named by the accession `index.json`. `xbrl` uses the stored filing metadata, then turns one accession package into raw facts, periods, dimensions, and canonical observations.

## 5. Ingest several issuers, then build one universe

```sh
DB=.fa.db
RAW=raw
UA="Your Name your.email@example.com"

for cik in 0000320193 0000789019 0001652044; do
  ./sec watch --db "$DB" --cik "$cik" --user-agent "$UA"
done

./sec pull --db "$DB" --raw-root "$RAW" --user-agent "$UA" &&
./sec univ --db "$DB" --name us_core --min-adv-usd 0 --min-fact-count 2
```

This is fan-in composition. Many `watch` calls feed one pull pass and one universe snapshot.

## 6. Research-only run

```sh
DB=.fa.db
LIB=build/libfolio.so
[ -f "$LIB" ] || LIB=build/libfolio.dylib

./sec recon --db "$DB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 \
  --reconciled 1 &&
./sec plan --db "$DB" \
  --core-lib "$LIB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 &&
./sec value --db "$DB" \
  --core-lib "$LIB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 &&
./sec gate --db "$DB" \
  --core-lib "$LIB" &&
./sec report daily --db "$DB"
```

This chain is safe for observe or research operation because it stops at risk decisions. It does not call `stage` or `send`.

## 7. Stage approved intents without broker submission

```sh
DB=.fa.db
LIB=build/libfolio.so
[ -f "$LIB" ] || LIB=build/libfolio.dylib

./sec recon --db "$DB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 \
  --reconciled 1 &&
./sec plan --db "$DB" \
  --core-lib "$LIB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 &&
./sec gate --db "$DB" \
  --core-lib "$LIB" &&
./sec stage --db "$DB" &&
./sec report daily --db "$DB"
```

This is the shadow/paper boundary. `stage` consumes only approved risk decisions. It still does not speak to the broker adapter.

## 8. Mock adapter submission

```sh
DB=.fa.db

./sec stage --db "$DB" &&
./sec send --db "$DB" --adapter mock &&
./sec recon --db "$DB" \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 \
  --reconciled 1 &&
./sec report daily --db "$DB"
```

`send` is the only public program that crosses the execution boundary. In the current implementation, `--adapter mock` is the guarded adapter.

## 9. Report and Notify as Fan-Out

```sh
DB=.fa.db

./sec report daily --db "$DB" &&
./sec stat --db "$DB" &&
./sec ping \
  --method ntfy \
  --ntfy-topic your-secret-topic \
  --message "Daily run complete"
```

This is fan-out from the ledger. `report`, `stat`, and `ping` should not mutate trading decisions.

## 10. Failure behavior

Use `&&` when the next command must not run after a failed precondition:

```sh
./sec recon --db "$DB" --portfolio-value-usd 100000 --cash-usd 100000 --reconciled 1 &&
./sec plan --db "$DB" --core-lib "$LIB" --portfolio-value-usd 100000 --cash-usd 100000 &&
./sec gate --db "$DB" --core-lib "$LIB" &&
./sec stage --db "$DB"
```

Use `;` only when the commands are independent diagnostics:

```sh
./sec stat --db "$DB" ; ./sec report daily --db "$DB"
```

That distinction is the command-line safety contract: gates are chained with `&&`, observability can fan out.
