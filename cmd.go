package main

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func cmdOrderStage(args []string) error {
	fs := newFlagSet("order-stage")
	dbPath := fs.String("db", "", "")
	runID := fs.String("run-id", "", "")
	limit := fs.Int("limit", 100, "")
	autonomous := fs.Bool("autonomous", false, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	_ = autonomous
	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return err
	}
	mode := setting(db, "trading_mode")
	if mode != "stage" && mode != "paper" && mode != "live_limited" && mode != "live" {
		return fail(3, "cannot stage orders in mode=%q; set mode explicitly", mode)
	}
	query := `SELECT rd.decision_id, rd.intent_id, rd.security_id, oi.side, rd.notional_usd
FROM risk_decisions rd JOIN order_intents oi ON oi.intent_id=rd.intent_id
LEFT JOIN staged_orders so ON so.decision_id=rd.decision_id
WHERE rd.approved=1 AND so.decision_id IS NULL`
	var qargs []any
	if *runID != "" {
		query += " AND oi.run_id=?"
		qargs = append(qargs, *runID)
	}
	query += " ORDER BY rd.created_at LIMIT ?"
	qargs = append(qargs, *limit)
	rows, err := db.Query(query, qargs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	staged := 0
	for rows.Next() {
		var decisionID, intentID, side string
		var securityID uint64
		var notional float64
		if err := rows.Scan(&decisionID, &intentID, &securityID, &side, &notional); err != nil {
			return err
		}
		stagedID := uuidV4()
		if _, err := tx.Exec(`INSERT INTO staged_orders(staged_order_id, decision_id, intent_id, security_id, side, notional_usd, staged_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, stagedID, decisionID, intentID, securityID, side, notional, utcNow()); err != nil {
			return err
		}
		event, err := appendEvent(tx, "order_staged", map[string]any{"staged_order_id": stagedID, "decision_id": decisionID, "intent_id": intentID, "security_id": securityID, "side": side, "notional_usd": notional})
		if err != nil {
			return err
		}
		if err := jsonLine(event); err != nil {
			return err
		}
		staged++
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "orders staged=%d\n", staged)
	return nil
}

func cmdBrokerSubmit(args []string) error {
	fs := newFlagSet("broker-submit")
	dbPath := fs.String("db", "", "")
	adapter := fs.String("adapter", "mock", "")
	limit := fs.Int("limit", 100, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *adapter != "mock" {
		return fail(3, "only --adapter mock is implemented")
	}
	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return err
	}
	mode := setting(db, "trading_mode")
	if mode != "paper" && mode != "live_limited" && mode != "live" {
		return fail(3, "cannot send broker orders in mode=%q", mode)
	}
	rows, err := db.Query(`SELECT so.staged_order_id, so.security_id, so.side, so.notional_usd
FROM staged_orders so LEFT JOIN broker_events be ON be.staged_order_id=so.staged_order_id AND be.event_type='mock_order_submitted'
WHERE be.broker_event_id IS NULL ORDER BY so.staged_at LIMIT ?`, *limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	sent := 0
	for rows.Next() {
		var stagedID, side string
		var securityID uint64
		var notional float64
		if err := rows.Scan(&stagedID, &securityID, &side, &notional); err != nil {
			return err
		}
		eventID := uuidV4()
		payload := map[string]any{"adapter": "mock", "staged_order_id": stagedID, "security_id": securityID, "side": side, "notional_usd": notional, "mode": mode}
		if _, err := tx.Exec(`INSERT INTO broker_events(broker_event_id, staged_order_id, event_type, payload_json, occurred_at)
VALUES (?, ?, 'mock_order_submitted', ?, ?)`, eventID, stagedID, mustCanonicalJSON(payload), utcNow()); err != nil {
			return err
		}
		payload["broker_event_id"] = eventID
		event, err := appendEvent(tx, "broker_order_submitted_mock", payload)
		if err != nil {
			return err
		}
		if err := jsonLine(event); err != nil {
			return err
		}
		sent++
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "mock broker submissions=%d\n", sent)
	return nil
}

func cmdSetMode(args []string) error {
	fs := newFlagSet("set-mode")
	dbPath := fs.String("db", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fail(2, "mode value is required")
	}
	mode := fs.Arg(0)
	allowed := map[string]bool{"observe": true, "shadow": true, "stage": true, "paper": true, "live_limited": true, "live": true, "halted": true}
	if !allowed[mode] {
		return fail(3, "invalid mode %q", mode)
	}
	return setMode(*dbPath, mode, "trading_mode_set", map[string]any{"mode": mode})
}

func cmdTradingHalt(args []string) error {
	fs := newFlagSet("trading-halt")
	dbPath := fs.String("db", "", "")
	reason := fs.String("reason", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	return setMode(*dbPath, "halted", "trading_halted", map[string]any{"reason": *reason})
}

func setMode(dbPath, mode, eventType string, payload map[string]any) error {
	db, err := openDB(dbPath)
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
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES ('trading_mode', ?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, mode, utcNow()); err != nil {
		return err
	}
	event, err := appendEvent(tx, eventType, payload)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return jsonLine(event)
}

func cmdStatus(args []string) error {
	fs := newFlagSet("status")
	dbPath := fs.String("db", "", "")
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
	counts := map[string]int{}
	for _, table := range []string{"events", "securities", "filings", "source_documents", "xbrl_facts", "canonical_observations", "canonical_observation_exceptions", "universe_snapshots", "universe_members", "model_runs", "statement_snapshots", "model_assumptions", "valuations", "forecast_outcomes", "order_intents", "risk_decisions", "staged_orders", "broker_events"} {
		counts[table] = countTable(db, table)
	}
	return jsonLine(map[string]any{"trading_mode": setting(db, "trading_mode"), "broker_state": brokerState(db), "counts": counts})
}

func cmdReport(args []string) error {
	if len(args) > 0 && args[0] == "daily" {
		args = args[1:]
	}
	fs := newFlagSet("report")
	dbPath := fs.String("db", "", "")
	date := fs.String("date", "today", "")
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
	if *date == "today" {
		*date = time.Now().UTC().Format("2006-01-02")
	}
	fmt.Printf("FA DAILY REPORT - %s UTC\n\n", *date)
	fmt.Println("state:")
	fmt.Printf("  trading_mode: %s\n", setting(db, "trading_mode"))
	fmt.Println()
	fmt.Println("data:")
	for _, table := range []string{"securities", "filings", "source_documents", "xbrl_facts", "canonical_observations"} {
		fmt.Printf("  %s: %d\n", table, countTable(db, table))
	}
	fmt.Println()
	fmt.Println("model/execution:")
	for _, table := range []string{"model_runs", "valuations", "order_intents", "risk_decisions", "staged_orders", "broker_events"} {
		fmt.Printf("  %s: %d\n", table, countTable(db, table))
	}
	return nil
}

func cmdNotify(args []string) error {
	fs := newFlagSet("notify")
	method := fs.String("method", "", "")
	title := fs.String("title", "sec-fa", "")
	message := fs.String("message", "", "")
	priority := fs.String("priority", "default", "")
	topic := fs.String("ntfy-topic", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *method == "ntfy" {
		t := *topic
		if t == "" {
			t = os.Getenv("NTFY_TOPIC")
		}
		if t == "" {
			return fail(1, "ntfy requires --ntfy-topic or NTFY_TOPIC")
		}
		req, err := http.NewRequest("POST", "https://ntfy.sh/"+t, strings.NewReader(*message))
		if err != nil {
			return err
		}
		req.Header.Set("Title", *title)
		req.Header.Set("Priority", *priority)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return jsonLine(map[string]any{"method": *method, "status": "sent", "response": string(body)})
	}
	return fail(1, "unsupported notify method %q", *method)
}

func cmdUniverseBuild(args []string) error {
	fs := newFlagSet("universe-build")
	dbPath := fs.String("db", "", "")
	name := fs.String("name", "default", "")
	minADV := fs.Float64("min-adv-usd", 0, "")
	minFacts := fs.Int("min-fact-count", 0, "")
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
	rows, err := db.Query(`SELECT s.security_id
FROM securities s
WHERE s.investable=1 AND s.adv_usd >= ?
AND (SELECT COUNT(*) FROM canonical_observations co WHERE co.security_id=s.security_id AND co.observation_status IN ('selected','derived')) >= ?
ORDER BY s.security_id`, *minADV, *minFacts)
	if err != nil {
		return err
	}
	defer rows.Close()
	snapshotID := stableID("universe", map[string]any{"name": *name, "created_at": utcNow()})
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rule := map[string]any{"min_adv_usd": *minADV, "min_fact_count": *minFacts}
	if _, err := tx.Exec(`INSERT INTO universe_snapshots(snapshot_id, name, created_at, rule_json) VALUES (?, ?, ?, ?)`, snapshotID, *name, utcNow(), mustCanonicalJSON(rule)); err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var sid uint64
		if err := rows.Scan(&sid); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO universe_members(snapshot_id, security_id) VALUES (?, ?)`, snapshotID, sid); err != nil {
			return err
		}
		count++
	}
	event, err := appendEvent(tx, "universe_snapshot_created", map[string]any{"snapshot_id": snapshotID, "name": *name, "member_count": count, "rule": rule})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return jsonLine(event)
}

func cmdCISeed(args []string) error {
	fs := newFlagSet("ci-seed")
	dbPath := fs.String("db", "", "")
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
	defer tx.Rollback()
	quarters := [][2]string{
		{"2022-07-01", "2022-09-30"},
		{"2022-10-01", "2022-12-31"},
		{"2023-01-01", "2023-03-31"},
		{"2023-04-01", "2023-06-30"},
		{"2023-07-01", "2023-09-30"},
	}
	revenueBySecurity := map[int][]float64{
		1: {90, 95, 100, 115, 125},
		2: {110, 105, 100, 95, 90},
	}
	for sec, revenues := range revenueBySecurity {
		for i, q := range quarters {
			revenue := revenues[i]
			if err := insertSyntheticObservation(tx, sec, metricRevenue, revenue, q[0], q[1], false); err != nil {
				return err
			}
			if err := insertSyntheticObservation(tx, sec, metricNetIncome, revenue/10, q[0], q[1], false); err != nil {
				return err
			}
			if err := insertSyntheticObservation(tx, sec, metricEPSDiluted, revenue/100, q[0], q[1], false); err != nil {
				return err
			}
			if err := insertSyntheticObservation(tx, sec, metricDilutedShares, 20, q[0], q[1], false); err != nil {
				return err
			}
			if err := insertSyntheticObservation(tx, sec, metricOperatingCashFlow, revenue*0.18, q[0], q[1], false); err != nil {
				return err
			}
			if err := insertSyntheticObservation(tx, sec, metricCapex, revenue*0.04, q[0], q[1], false); err != nil {
				return err
			}
		}
		if err := insertSyntheticObservation(tx, sec, metricCash, 50, "2023-09-30", "2023-09-30", true); err != nil {
			return err
		}
		if err := insertSyntheticObservation(tx, sec, metricDebt, 10, "2023-09-30", "2023-09-30", true); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return jsonLine(map[string]any{"status": "seeded"})
}

func insertSyntheticObservation(tx execer, sec, metric int, value float64, start, end string, instant bool) error {
	cik := fmt.Sprintf("%010d", sec)
	now := utcNow()
	periodKind := "duration"
	semantics := "fiscal_quarter"
	duration := durationDays(start, end)
	rawStart := any(start)
	rawEnd := any(end)
	rawInstant := any(nil)
	if instant {
		periodKind = "instant"
		semantics = "instant"
		duration = 0
		rawStart = nil
		rawEnd = nil
		rawInstant = end
	}
	pid := stableID("period", map[string]any{"sec": sec, "metric": metric, "start": start, "end": end, "instant": instant})
	if _, err := tx.Exec(`INSERT OR IGNORE INTO reporting_periods(
period_id, cik, accession_number, raw_start_date, raw_end_date, raw_instant_date,
start_date_inclusive, end_date_exclusive, duration_days, period_kind, period_semantics,
fiscal_year, fiscal_period, fiscal_period_ordinal, period_length_class, source, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 2023, 'Q1', 1, ?, 'synthetic', ?)`,
		pid, cik, fmt.Sprintf("acc-%d-%d-%s", sec, metric, end), rawStart, rawEnd, rawInstant,
		start, end, duration, periodKind, semantics, periodLengthClass(duration), now); err != nil {
		return err
	}
	obs := stableID("obs", map[string]any{"sec": sec, "metric": metric, "start": start, "end": end, "instant": instant})
	unit := "USD"
	if metric == metricEPSDiluted {
		unit = "USD/shares"
	}
	if metric == metricDilutedShares {
		unit = "shares"
	}
	_, err := tx.Exec(`INSERT OR IGNORE INTO canonical_observations(
observation_id, observation_hash, resolver_version, security_id, cik, metric_id, metric_name, metric_kind,
basis_id, period_id, period_semantics, duration_days, fiscal_year, fiscal_period, value_decimal,
unit_signature, dimensions_hash, dimensional_scope, observation_status, source_fact_id, source_accession,
taxonomy, concept_qname, selection_reason, quality_flags, quality_flags_json, quality_score,
accepted_at, available_at, created_at)
VALUES (?, ?, 'synthetic_resolver', ?, ?, ?, ?, ?, ?, ?, ?, ?, 2023, 'Q1', ?,
?, ?, 'consolidated_total', 'selected', NULL, ?, 'test', 'test', 'synthetic', 0, '[]', 1.0,
NULL, ?, ?)`,
		obs, strings.TrimPrefix(obs, "obs-"), sec, cik, metric, metricNames[metric], metricKinds[metric],
		basisByMetric[metric], pid, semantics, duration, value, unit, totalDimensionsHash,
		fmt.Sprintf("acc-%d-%d-%s", sec, metric, end), now, now)
	return err
}

func setting(db *sql.DB, key string) string {
	var value string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&value); err != nil {
		return "observe"
	}
	return value
}

func brokerState(db *sql.DB) any {
	row := db.QueryRow(`SELECT reconciled, checked_at, portfolio_value_usd, cash_usd FROM broker_state WHERE id=1`)
	var reconciled int
	var checked string
	var pv, cash float64
	if err := row.Scan(&reconciled, &checked, &pv, &cash); err != nil {
		return nil
	}
	return map[string]any{"reconciled": reconciled != 0, "checked_at": checked, "portfolio_value_usd": pv, "cash_usd": cash}
}

func countTable(db *sql.DB, table string) int {
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
	return n
}
