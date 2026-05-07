# Operations

## 1. Build

```sh
make check
```

## 2. Initialize database

```sh
./sec init --db /var/lib/sec/sec.db
```

The initial mode is `observe`.

## 3. Add securities

```sh
./sec sym --db /var/lib/sec/sec.db \
  --cik 0000320193 --symbol AAPL --price-usd 200 --adv-usd 5000000000 --investable 1
```

In the starter implementation, `security_id` is the integer CIK. A production security master should add exchange, currency, share class, IBKR contract id, listing status, and corporate-action data.

## 4. SEC ingestion

Declare a real User-Agent. The SEC fair-access guidance requires a declared User-Agent and rate moderation.

```sh
./sec watch --db /var/lib/sec/sec.db \
  --cik 0000320193 \
  --user-agent "Your Company admin@example.com"

./sec pull --db /var/lib/sec/sec.db \
  --raw-root /var/lib/sec/raw \
  --user-agent "Your Company admin@example.com"
```

## 5. XBRL package parsing

```sh
./sec xbrl --db /var/lib/sec/sec.db \
  --accession 0000320193-24-000123 \
  --cik 0000320193 \
  --symbol AAPL \
  --raw-root /var/lib/sec/raw \
  --user-agent "Your Company admin@example.com"
```

The parser reads the SEC accession `index.json`, stores XBRL-relevant package artifacts unchanged, parses inline-XBRL and classic-XBRL contexts, units, dimensions, and raw facts, then invokes the canonical observation resolver. Companyfacts remains a design fallback, but the Go CLI's primary path is accession XBRL.

## 6. Reconciliation snapshot

```sh
./sec recon --db /var/lib/sec/sec.db \
  --portfolio-value-usd 100000 \
  --cash-usd 100000 \
  --reconciled 1
```

In production this command should be replaced or backed by an actual broker-state pull from the isolated broker host.

## 7. Model and risk

```sh
./sec plan --db /var/lib/sec/sec.db \
  --core-lib /opt/sec/lib/libfolio.so \
  --portfolio-value-usd 100000 \
  --cash-usd 100000

./sec gate --db /var/lib/sec/sec.db \
  --core-lib /opt/sec/lib/libfolio.so
```

Risk decisions are append-only. Stale reconciliation rejects all intents.

## 8. Staging and submission

```sh
./sec stage --db /var/lib/sec/sec.db
```

Live broker submission is intentionally not implemented. The current command supports only guarded mock submission:

```sh
./sec send --db /var/lib/sec/sec.db --adapter mock
```

## 9. Reports

```sh
./sec report daily --db /var/lib/sec/sec.db
./sec stat --db /var/lib/sec/sec.db
```

## 10. Notifications

Pushover:

```sh
PUSHOVER_TOKEN=... PUSHOVER_USER=... \
  ./sec ping --method pushover --title "Risk alert" --message "Risk gate rejected orders"
```

ntfy:

```sh
./sec ping --method ntfy --ntfy-topic your-secret-topic --message "New filing"
```
