# Operations

## 1. Build

```sh
make check
```

## 2. Initialize database

Local development:

```sh
make setup-db
```

This creates or upgrades `.fa.db`. The direct CLI equivalent is:

```sh
./sec init
```

For an explicit production path:

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
export SEC_USER_AGENT="Your Company admin@example.com"

./sec watch --db /var/lib/sec/sec.db \
  --cik 0000320193 \
  --limit 1

./sec pull --db /var/lib/sec/sec.db \
  --raw-root /var/lib/sec/raw \
  --cik 0000320193 \
  --limit 1
```

## 5. XBRL package parsing

```sh
./sec xbrl --db /var/lib/sec/sec.db \
  --latest \
  --cik 0000320193 \
  --raw-root /var/lib/sec/raw
```

`watch` records filing metadata from SEC submissions, `pull` reads the SEC accession `index.json` and stores XBRL-relevant package artifacts unchanged, and `xbrl` infers form, filing date, acceptance time, and primary document from the stored filing row. The parser then parses inline-XBRL and classic-XBRL contexts, units, dimensions, and raw facts before invoking the canonical observation resolver. Companyfacts remains a design fallback, but the Go CLI's primary path is accession XBRL.

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

`send` rechecks broker reconciliation freshness immediately before adapter submission. A previously staged order is not sufficient authority if reconciliation has gone stale.

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
