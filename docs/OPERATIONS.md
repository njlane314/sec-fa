# Operations

## 1. Build

```sh
make check
```

## 2. Initialize database

```sh
bin/fa init-db --db /var/lib/fa/fa.db
```

The initial mode is `observe`.

## 3. Add securities

```sh
bin/fa security-upsert --db /var/lib/fa/fa.db \
  --cik 0000320193 --symbol AAPL --price-usd 200 --adv-usd 5000000000 --investable 1
```

In the starter implementation, `security_id` is the integer CIK. A production security master should add exchange, currency, share class, IBKR contract id, listing status, and corporate-action data.

## 4. SEC ingestion

Declare a real User-Agent. The SEC fair-access guidance requires a declared User-Agent and rate moderation.

```sh
bin/fa sec-watch --db /var/lib/fa/fa.db \
  --cik 0000320193 \
  --user-agent "Your Company admin@example.com"

bin/fa sec-fetch --db /var/lib/fa/fa.db \
  --raw-root /var/lib/fa/raw \
  --user-agent "Your Company admin@example.com"
```

## 5. XBRL package parsing

```sh
bin/fa xbrl-parse --db /var/lib/fa/fa.db \
  --accession 0000320193-24-000123 \
  --cik 0000320193 \
  --symbol AAPL \
  --raw-root /var/lib/fa/raw \
  --user-agent "Your Company admin@example.com"
```

The parser downloads the SEC accession `index.json`, stores XBRL-relevant package artifacts unchanged, parses inline-XBRL and classic-XBRL contexts, units, dimensions, and raw facts, then invokes the canonical observation resolver. The `facts-companyfacts` command remains available as a fallback/reconciliation source, not as the primary production ingestion path.

## 6. Reconciliation snapshot

```sh
bin/fa broker-reconcile --db /var/lib/fa/fa.db \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 \
  --reconciled 1
```

In production this command should be replaced or backed by an actual broker-state pull from the isolated broker host.

## 7. Model and risk

```sh
bin/fa model-run --db /var/lib/fa/fa.db \
  --core-lib /opt/fa/lib/libfa_core.so \
  --portfolio-value-usd 100000 \
  --cash-usd 100000

bin/fa risk-check --db /var/lib/fa/fa.db \
  --core-lib /opt/fa/lib/libfa_core.so
```

Risk decisions are append-only. Stale reconciliation rejects all intents.

## 8. Staging and submission

```sh
bin/fa order-stage --db /var/lib/fa/fa.db
```

Live broker submission is intentionally not implemented. The current command supports only guarded mock submission:

```sh
bin/fa broker-submit --db /var/lib/fa/fa.db --adapter mock
```

## 9. Reports

```sh
bin/fa report daily --db /var/lib/fa/fa.db
bin/fa status --db /var/lib/fa/fa.db
```

## 10. Notifications

Pushover:

```sh
PUSHOVER_TOKEN=... PUSHOVER_USER=... \
  bin/fa notify --method pushover --title "FA alert" --message "Risk gate rejected orders"
```

ntfy:

```sh
bin/fa notify --method ntfy --ntfy-topic your-secret-topic --message "New filing"
```
