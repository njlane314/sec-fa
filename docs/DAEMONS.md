# Daemons

Daemons are durable state workers. They do not own hidden trading state and they do not define new business transitions. The rule is:

```text
commands define valid state transitions
daemons decide when to invoke those transitions
```

The database, raw store, and append-only ledger remain authoritative. Each daemon repeatedly reads durable state, claims one bounded unit of work when applicable, invokes an existing `sec` command, writes a durable result/event, and exits, sleeps, or continues.

## Workers

```text
sec-watchd   observes SEC feeds and enqueues pull_accession work
sec-pulld    consumes pull_accession, downloads raw accession packages, enqueues parse_accession
sec-xbrld    consumes parse_accession, parses XBRL packages, enqueues run_model
sec-pland    consumes run_model, runs plan/value, enqueues risk_check
sec-gated    consumes risk_check, writes risk_decisions, enqueues stage_orders
sec-staged   consumes stage_orders, writes staged_orders, may enqueue submit_orders
sec-submitd  consumes submit_orders and invokes send on an isolated broker host
sec-recond   periodically writes broker reconciliation snapshots
sec-notifyd  tails the event ledger and sends/logs notifications
```

The low-authority workers are `watchd`, `pulld`, `xbrld`, `recond`, and `notifyd`. The authority-advancing workers are `pland`, `gated`, `staged`, and `submitd`; these are guarded by trading mode, reconciliation freshness, and the command-level checks already present in the CLI.

## Durable queue contract

The daemon layer uses SQLite-backed leases:

```text
queued -> leased -> succeeded
queued -> leased -> failed -> leased ... -> dead
```

A crash during a command leaves the work item leased until `lease_until`; another daemon can retry it after the lease expires. A crash after the command succeeds but before the work item is marked succeeded can re-run the same command, so commands must remain idempotent or append-only with deterministic evidence.

## Example local process group

```sh
make build
DB=.fa.db
RAW=raw
UA="Your Name your.email@example.com"

./build/sec-watchd --db "$DB" --raw-root "$RAW" --user-agent "$UA"
./build/sec-pulld  --db "$DB" --raw-root "$RAW" --user-agent "$UA"
./build/sec-xbrld  --db "$DB" --raw-root "$RAW"
./build/sec-notifyd --db "$DB"
```

Paper or live deployment should not place `sec-submitd` in the same trust boundary as SEC ingestion or model execution. Run it on the broker host with broker credentials available only there.
