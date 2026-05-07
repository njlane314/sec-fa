# Operations

## 1. Build

```sh
make check
```

## 2. Initialize database

```sh
bin/init --db /var/lib/sec/sec.db
```

The initial mode is `observe`.

## 3. Add securities

```sh
bin/sym --db /var/lib/sec/sec.db \
  --cik 0000320193 --symbol AAPL --price-usd 200 --adv-usd 5000000000 --investable 1
```

In the starter implementation, `security_id` is the integer CIK. A production security master should add exchange, currency, share class, IBKR contract id, listing status, and corporate-action data.

## 4. SEC ingestion

Declare a real User-Agent. The SEC fair-access guidance requires a declared User-Agent and rate moderation.

```sh
bin/watch --db /var/lib/sec/sec.db \
  --cik 0000320193 \
  --user-agent "Your Company admin@example.com"

bin/pull --db /var/lib/sec/sec.db \
  --raw-root /var/lib/sec/raw \
  --user-agent "Your Company admin@example.com"
```

## 5. XBRL package parsing

```sh
bin/xbrl --db /var/lib/sec/sec.db \
  --accession 0000320193-24-000123 \
  --cik 0000320193 \
  --symbol AAPL \
  --raw-root /var/lib/sec/raw \
  --user-agent "Your Company admin@example.com"
```

The parser downloads the SEC accession `index.json`, stores XBRL-relevant package artifacts unchanged, parses inline-XBRL and classic-XBRL contexts, units, dimensions, and raw facts, then invokes the canonical observation resolver. The `comp` command remains available as a fallback/reconciliation source, not as the primary production ingestion path.

## 6. Reconciliation snapshot

```sh
bin/recon --db /var/lib/sec/sec.db \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 \
  --reconciled 1
```

In production this command should be replaced or backed by an actual broker-state pull from the isolated broker host.

## 7. Model and risk

```sh
bin/plan --db /var/lib/sec/sec.db \
  --core-lib /opt/sec/lib/libfolio.so \
  --portfolio-value-usd 100000 \
  --cash-usd 100000

bin/gate --db /var/lib/sec/sec.db \
  --core-lib /opt/sec/lib/libfolio.so
```

Risk decisions are append-only. Stale reconciliation rejects all intents.

## 8. Staging and submission

```sh
bin/stage --db /var/lib/sec/sec.db
```

Live broker submission is intentionally not implemented. The current command supports only guarded mock submission:

```sh
bin/send --db /var/lib/sec/sec.db --adapter mock
```

## 9. Reports

```sh
bin/report daily --db /var/lib/sec/sec.db
bin/stat --db /var/lib/sec/sec.db
```

## 10. Notifications

Pushover:

```sh
PUSHOVER_TOKEN=... PUSHOVER_USER=... \
  bin/ping --method pushover --title "Risk alert" --message "Risk gate rejected orders"
```

ntfy:

```sh
bin/ping --method ntfy --ntfy-topic your-secret-topic --message "New filing"
```
