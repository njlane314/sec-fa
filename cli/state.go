package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func loadSchemaSQL() (string, error) {
	var candidates []string
	if root := os.Getenv("SEC_FA_ROOT"); root != "" {
		candidates = append(candidates, filepath.Join(root, "schema.sql"))
	}
	candidates = append(candidates, "schema.sql", filepath.Join("..", "schema.sql"))
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "..", "schema.sql"))
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data), nil
		}
	}
	return "", fail(1, "schema.sql not found")
}

func ensureDB(db *sql.DB) error {
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='events'").Scan(&name)
	if err != nil {
		return fail(1, "database is not initialized; run `init --db <path>` first")
	}
	return nil
}

func cmdInitDB(args []string) error {
	fs := newFlagSet("init-db")
	dbPath := fs.String("db", "", "SQLite database path")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *dbPath == "" {
		return fail(2, "--db is required")
	}
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil && filepath.Dir(*dbPath) != "." {
		return err
	}
	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	schema, err := loadSchemaSQL()
	if err != nil {
		return err
	}
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	if err := seedMetricConceptCandidates(db); err != nil {
		return err
	}
	if err := insertTotalDimensionSignature(db); err != nil {
		return err
	}
	return jsonLine(map[string]any{"db": *dbPath, "status": "initialized"})
}

func cmdSecurityUpsert(args []string) error {
	fs := newFlagSet("security-upsert")
	dbPath := fs.String("db", "", "")
	cik := fs.String("cik", "", "")
	symbol := fs.String("symbol", "", "")
	price := fs.Float64("price-usd", 0, "")
	adv := fs.Float64("adv-usd", 0, "")
	investable := fs.Int("investable", 0, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return err
	}
	cik10 := normalizeCIK(*cik)
	securityID, _ := strconv.ParseInt(cik10, 10, 64)
	now := utcNow()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO securities(security_id, cik, symbol, investable, price_usd, adv_usd, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(security_id) DO UPDATE SET cik=excluded.cik, symbol=excluded.symbol,
investable=excluded.investable, price_usd=excluded.price_usd, adv_usd=excluded.adv_usd, updated_at=excluded.updated_at`,
		securityID, cik10, strings.ToUpper(*symbol), boolInt(*investable != 0), *price, *adv, now)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	event, err := appendEvent(tx, "security_upserted", map[string]any{
		"security_id": securityID, "cik": cik10, "symbol": strings.ToUpper(*symbol),
		"investable": *investable != 0, "price_usd": *price, "adv_usd": *adv,
	})
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return jsonLine(event)
}

func cmdPositionUpsert(args []string) error {
	fs := newFlagSet("position-upsert")
	dbPath := fs.String("db", "", "")
	cik := fs.String("cik", "", "")
	quantity := fs.Float64("quantity-shares", 0, "")
	mv := fs.Float64("market-value-usd", 0, "")
	weight := fs.Float64("weight-ratio", 0, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return err
	}
	securityID, _ := strconv.ParseInt(normalizeCIK(*cik), 10, 64)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO positions(security_id, quantity_shares, market_value_usd, weight_ratio, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(security_id) DO UPDATE SET quantity_shares=excluded.quantity_shares,
market_value_usd=excluded.market_value_usd, weight_ratio=excluded.weight_ratio, updated_at=excluded.updated_at`,
		securityID, *quantity, *mv, *weight, utcNow())
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	event, err := appendEvent(tx, "position_upserted", map[string]any{
		"security_id": securityID, "quantity_shares": *quantity, "market_value_usd": *mv, "weight_ratio": *weight,
	})
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return jsonLine(event)
}
func cmdBrokerReconcile(args []string) error {
	fs := newFlagSet("broker-reconcile")
	dbPath := fs.String("db", "", "")
	portfolioValue := fs.Float64("portfolio-value-usd", 0, "")
	cash := fs.Float64("cash-usd", 0, "")
	reconciled := fs.Int("reconciled", 0, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO broker_state(id, reconciled, checked_at, portfolio_value_usd, cash_usd)
VALUES (1, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET reconciled=excluded.reconciled, checked_at=excluded.checked_at,
portfolio_value_usd=excluded.portfolio_value_usd, cash_usd=excluded.cash_usd`,
		boolInt(*reconciled != 0), utcNow(), *portfolioValue, *cash)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	event, err := appendEvent(tx, "broker_reconciliation_snapshot", map[string]any{
		"reconciled": *reconciled != 0, "portfolio_value_usd": *portfolioValue, "cash_usd": *cash, "source": "manual_or_mock",
	})
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return jsonLine(event)
}

func seedMetricConceptCandidates(db execer) error {
	for concept, c := range conceptCandidates {
		parts := strings.SplitN(concept, ":", 2)
		payload := map[string]any{"metric_id": c.metricID, "basis_id": c.basisID, "taxonomy": parts[0], "concept": parts[1]}
		_, err := db.Exec(`INSERT OR IGNORE INTO metric_concept_candidates(
candidate_id, metric_id, basis_id, taxonomy, concept_qname, priority, allowed_units_json, dimension_policy, allowed_forms_json, notes)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			stableID("mcc", payload), c.metricID, c.basisID, parts[0], parts[1], c.priority,
			mustCanonicalJSON([]string{c.unit}), "consolidated_total_only",
			mustCanonicalJSON([]string{"10-K", "10-Q", "10-K/A", "10-Q/A"}), "seeded by sec-fa constants")
		if err != nil {
			return err
		}
	}
	return nil
}

func insertTotalDimensionSignature(db execer) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO dimension_signatures(
dimensions_hash, dimensions_json, has_dimensions, scope_class, axis_count, created_at)
VALUES (?, ?, 0, 'consolidated_total', 0, ?)`, totalDimensionsHash, totalDimensionsJSON, utcNow())
	return err
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func appendEvent(tx execer, eventType string, payload map[string]any) (map[string]any, error) {
	eventID := uuidV4()
	now := utcNow()
	payloadJSON := mustCanonicalJSON(payload)
	sum := sha256Hex([]byte(payloadJSON))
	_, err := tx.Exec(`INSERT INTO events(event_id, event_type, event_version, occurred_at, payload_json, payload_sha256)
VALUES (?, ?, 1, ?, ?, ?)`, eventID, eventType, now, payloadJSON, sum)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"event_id": eventID, "event_type": eventType, "event_version": 1, "occurred_at": now, "payload": payload,
	}, nil
}
