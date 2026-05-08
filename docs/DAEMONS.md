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

The queue must remain a durable authority surface, not an in-memory coordination trick. Useful near-term queue additions are:

```text
transition_runs       one row per claimed transition, with input/output hashes
dead or blocked work  operator-visible failures that should not retry blindly
daemon_cursors        durable read positions for event-tail or polling workers
```

`blocked` is distinct from `dead`: stale reconciliation, halted mode, missing broker credentials, missing core libraries, or schema mismatch may become valid after state changes. `dead` means operator repair is required.

## Control-plane primitives

The adjacent technologies worth adding are the ones that make background work safe, observable, and bounded:

```text
supervision      systemd service/timer units before Kubernetes
leases           SQLite short transactions first; Postgres SKIP LOCKED later if needed
rate limits      explicit token buckets for SEC, broker, notification, and model pressure
circuit breakers pause dependency-specific work after repeated failures
kill switches    durable config checked inside commands, not only daemons
observability    structured logs, queue metrics, heartbeat age, and transition durations
schema migration versioned SQL with binary min/max supported schema checks
```

Do not add Kafka, Kubernetes, Temporal, or a separate stream broker until there is a concrete pressure that SQLite-backed work items and a single supervised host cannot handle. The first scaling move should be Postgres-backed work items with notification wakeups; durable streams are a later multi-host concern.

## Feature snapshots

Model and risk daemons should not scan raw XBRL facts or re-infer accounting semantics at execution time. The slow evidence path should materialize immutable model-ready inputs:

```text
canonical_observations -> feature_snapshots -> model_runs
```

A feature snapshot should be point-in-time safe and hashable:

```text
feature_snapshot_id
universe_snapshot_id
as_of_time
feature_version
input_statement_hash
created_at
```

The current feature operation materializes rows from audited statement snapshots
and records `feature_snapshot_created`. `pland` and value operations should
eventually consume a `feature_snapshot_id`. SEC parsing, canonicalization, and
feature building stay on the slow path; staging and submission stay on the fast
authority path.

## Broker lifecycle

`submitd` is a trust boundary, not just another worker. It should run with broker credentials on an isolated host and should not ingest SEC data, run models, or make discretionary decisions.

Before live broker integration, add explicit evidence tables for:

```text
broker_orders
broker_fills
broker_account_snapshots
broker_position_snapshots
```

Broker submission must use stable client order identifiers so retries are safe after crashes. Reconciliation must be able to discover broker-side orders and fills even if `submitd` dies after placing an order but before writing the local acknowledgement.

## Verification and replay

Daemon reliability depends on executable invariants, not only documented intent. Add verification commands before adding live authority:

```text
sec verify db
sec verify raw-store
sec verify work
sec verify broker
sec verify lineage
```

The important checks are:

```text
no staged order without an approved risk decision
no broker order without a staged order
no fill without a broker order
no canonical observation without source fact lineage
no model run without an input hash
no live submission without fresh reconciliation
no duplicate client order id
no dead work item that still holds a lease
```

Replay/rebuild workflows should reconstruct derived state from raw evidence
after parser, feature, or model fixes:

```text
workflow.replay_accession.flow
workflow.rebuild_canonical.flow
workflow.rebuild_features.flow
workflow.replay_model_run.flow
```

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
