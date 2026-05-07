package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultDBPath   = ".fa.db"
	defaultRawRoot  = "raw"
	defaultForms    = "10-K,10-Q,10-K/A,10-Q/A"
	defaultInterval = 30 * time.Second
	defaultLease    = 5 * time.Minute
)

type config struct {
	role                string
	dbPath              string
	secBin              string
	rawRoot             string
	userAgent           string
	forms               string
	coreLib             string
	adapter             string
	modelCommand        string
	ntfyTopic           string
	watchLimit          int
	workLimit           int
	maxAttempts         int
	once                bool
	interval            time.Duration
	lease               time.Duration
	portfolioValueUSD   float64
	cashUSD             float64
	maxNameWeightRatio  float64
	targetGrossExposure float64
	maxOrderNotionalUSD float64
	minADVUSD           float64
	maxADVParticipation float64
	maxReconAgeSeconds  int64
	secSleepSeconds     float64
}
type daemon struct {
	cfg  config
	db   *sql.DB
	name string
	host string
	pid  int
}
type workItem struct {
	id        string
	typeName  string
	subjectID string
	priority  int
	attempts  int
}
type commandOutput struct {
	stdout string
	stderr string
}

var errNoWork = errors.New("no work")

func daemonMain() {
	cfg := parseFlags()
	db, err := sql.Open("sqlite3", cfg.dbPath+"?_foreign_keys=on")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		log.Fatal(err)
	}
	if err := ensureDaemonSchema(db); err != nil {
		log.Fatal(err)
	}
	host, _ := os.Hostname()
	d := &daemon{cfg: cfg, db: db, host: host, pid: os.Getpid(), name: cfg.role + ":" + host + ":" + strconv.Itoa(os.Getpid())}
	if err := d.heartbeat("started"); err != nil {
		log.Fatal(err)
	}
	defer d.heartbeat("stopped")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("%s started db=%s sec=%s", cfg.role, cfg.dbPath, cfg.secBin)
	for {
		err := d.runOnce(ctx)
		if err != nil && !errors.Is(err, errNoWork) {
			log.Printf("%s: %v", cfg.role, err)
		}
		if cfg.once {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.interval):
		}
	}
}
func parseFlags() config {
	programRole := roleFromProgram(os.Args[0])
	cfg := config{}
	flag.StringVar(&cfg.role, "role", programRole, "daemon role: watchd, pulld, xbrld, pland, gated, staged, submitd, recond, notifyd")
	flag.StringVar(&cfg.dbPath, "db", getenv("SEC_FA_DB", defaultDBPath), "SQLite database path")
	flag.StringVar(&cfg.secBin, "sec-bin", getenv("SEC_FA_SEC_BIN", discoverSecBin()), "sec CLI binary path")
	flag.StringVar(&cfg.rawRoot, "raw-root", getenv("SEC_FA_RAW_ROOT", defaultRawRoot), "raw filing store root")
	flag.StringVar(&cfg.userAgent, "user-agent", getenv("SEC_USER_AGENT", ""), "SEC User-Agent")
	flag.StringVar(&cfg.forms, "forms", defaultForms, "SEC forms for watchd")
	flag.StringVar(&cfg.coreLib, "core-lib", getenv("SEC_FA_CORE_LIB", ""), "C++ core shared library for model/risk daemons")
	flag.StringVar(&cfg.adapter, "adapter", "mock", "broker adapter for submitd")
	flag.StringVar(&cfg.modelCommand, "model-command", "value", "pland command: plan, value, or both")
	flag.StringVar(&cfg.ntfyTopic, "ntfy-topic", getenv("NTFY_TOPIC", ""), "ntfy topic for notifyd")
	flag.IntVar(&cfg.watchLimit, "watch-limit", 10, "filings inspected per CIK by watchd")
	flag.IntVar(&cfg.workLimit, "work-limit", 100, "work rows handled per cycle")
	flag.IntVar(&cfg.maxAttempts, "max-attempts", 5, "attempts before marking work dead")
	flag.BoolVar(&cfg.once, "once", false, "run one cycle and exit")
	flag.DurationVar(&cfg.interval, "interval", defaultInterval, "daemon sleep interval")
	flag.DurationVar(&cfg.lease, "lease", defaultLease, "work lease duration")
	flag.Float64Var(&cfg.portfolioValueUSD, "portfolio-value-usd", 0, "portfolio value override")
	flag.Float64Var(&cfg.cashUSD, "cash-usd", 0, "cash override")
	flag.Float64Var(&cfg.maxNameWeightRatio, "max-name-weight-ratio", 0.05, "max name weight")
	flag.Float64Var(&cfg.targetGrossExposure, "target-gross-exposure-ratio", 0.50, "target gross exposure")
	flag.Float64Var(&cfg.maxOrderNotionalUSD, "max-order-notional-usd", 10000, "max order notional")
	flag.Float64Var(&cfg.minADVUSD, "min-adv-usd", 1000000, "minimum ADV")
	flag.Float64Var(&cfg.maxADVParticipation, "max-adv-participation-ratio", 0.01, "maximum ADV participation")
	flag.Int64Var(&cfg.maxReconAgeSeconds, "max-reconciliation-age-s", 3600, "max broker reconciliation age")
	flag.Float64Var(&cfg.secSleepSeconds, "sleep-s", 0.15, "SEC pull sleep seconds")
	flag.Parse()
	cfg.role = strings.TrimPrefix(strings.TrimSpace(cfg.role), "sec-")
	if cfg.role == "" || !validRole(cfg.role) {
		log.Fatalf("unknown or missing daemon role %q", cfg.role)
	}
	return cfg
}
func (d *daemon) runOnce(ctx context.Context) error {
	if err := d.heartbeat("running:" + d.cfg.role); err != nil {
		return err
	}
	switch d.cfg.role {
	case "watchd":
		return d.watchd(ctx)
	case "pulld":
		return d.pulld(ctx)
	case "xbrld":
		return d.xbrld(ctx)
	case "pland":
		return d.pland(ctx)
	case "gated":
		return d.gated(ctx)
	case "staged":
		return d.staged(ctx)
	case "submitd":
		return d.submitd(ctx)
	case "recond":
		return d.recond(ctx)
	case "notifyd":
		return d.notifyd(ctx)
	default:
		return fmt.Errorf("unreachable role %q", d.cfg.role)
	}
}
func (d *daemon) watchd(ctx context.Context) error {
	rows, err := d.db.Query(`SELECT cik FROM securities WHERE investable=1 ORDER BY security_id LIMIT ?`, d.cfg.workLimit)
	if err != nil {
		return err
	}
	var ciks []string
	for rows.Next() {
		var cik string
		if err := rows.Scan(&cik); err != nil {
			rows.Close()
			return err
		}
		ciks = append(ciks, cik)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ciks) == 0 {
		return errNoWork
	}
	for _, cik := range ciks {
		args := []string{"watch", "--db", d.cfg.dbPath, "--cik", cik, "--limit", strconv.Itoa(d.cfg.watchLimit), "--forms", d.cfg.forms}
		if d.cfg.userAgent != "" {
			args = append(args, "--user-agent", d.cfg.userAgent)
		}
		out, err := d.runSec(ctx, args...)
		if err != nil {
			d.daemonEvent("daemon_transition_failed", map[string]any{"daemon": d.cfg.role, "transition": "watch", "cik": cik, "error": err.Error(), "stderr": truncate(out.stderr, 2000)})
			return err
		}
		queued, err := d.enqueuePullAccessions(cik)
		if err != nil {
			return err
		}
		d.daemonEvent("daemon_watch_cycle", map[string]any{"daemon": d.cfg.role, "cik": cik, "pull_accessions_enqueued": queued, "stdout_sha256": sha256Hex([]byte(out.stdout))})
	}
	return nil
}
func (d *daemon) pulld(ctx context.Context) error {
	item, err := d.claim("pull_accession")
	if err != nil {
		return err
	}
	args := []string{"pull", "--db", d.cfg.dbPath, "--accession", item.subjectID, "--raw-root", d.cfg.rawRoot, "--limit", "1", "--sleep-s", fmtFloat(d.cfg.secSleepSeconds)}
	if d.cfg.userAgent != "" {
		args = append(args, "--user-agent", d.cfg.userAgent)
	}
	out, err := d.runSec(ctx, args...)
	if err != nil {
		d.fail(item, err, out.stderr)
		return err
	}
	return d.succeed(item, []enqueue{{typ: "parse_accession", subject: item.subjectID, priority: item.priority}}, map[string]any{"stdout_sha256": sha256Hex([]byte(out.stdout))})
}
func (d *daemon) xbrld(ctx context.Context) error {
	item, err := d.claim("parse_accession")
	if err != nil {
		return err
	}
	out, err := d.runSec(ctx, "xbrl", "--db", d.cfg.dbPath, "--accession", item.subjectID, "--raw-root", d.cfg.rawRoot, "--strict", "--sleep-s", "0")
	if err != nil {
		d.fail(item, err, out.stderr)
		return err
	}
	return d.succeed(item, []enqueue{{typ: "run_model", subject: item.subjectID, priority: item.priority}}, map[string]any{"stdout_sha256": sha256Hex([]byte(out.stdout))})
}
func (d *daemon) pland(ctx context.Context) error {
	item, err := d.claim("run_model")
	if err != nil {
		return err
	}
	mode := d.mode()
	if !atLeastShadow(mode) {
		return d.deferWork(item, 5*time.Minute, "trading_mode="+mode+" does not permit pland")
	}
	if d.cfg.coreLib == "" {
		err := errors.New("--core-lib is required for pland")
		d.fail(item, err, "")
		return err
	}
	commands := []string{d.cfg.modelCommand}
	if d.cfg.modelCommand == "both" {
		commands = []string{"plan", "value"}
	}
	var runID string
	for _, command := range commands {
		out, err := d.runSec(ctx, append([]string{command}, d.modelArgs()...)...)
		if err != nil {
			d.fail(item, err, out.stderr)
			return err
		}
		if id := runIDFromEvent(out.stdout); id != "" {
			runID = id
		}
	}
	if runID == "" {
		err := errors.New("model command produced no run_id event")
		d.fail(item, err, "")
		return err
	}
	return d.succeed(item, []enqueue{{typ: "risk_check", subject: runID, priority: item.priority}}, map[string]any{"run_id": runID, "model_command": d.cfg.modelCommand})
}
func (d *daemon) gated(ctx context.Context) error {
	item, err := d.claim("risk_check")
	if err != nil {
		return err
	}
	mode := d.mode()
	if !atLeastShadow(mode) {
		return d.deferWork(item, 5*time.Minute, "trading_mode="+mode+" does not permit gated")
	}
	if d.cfg.coreLib == "" {
		err := errors.New("--core-lib is required for gated")
		d.fail(item, err, "")
		return err
	}
	args := []string{"gate", "--db", d.cfg.dbPath, "--core-lib", d.cfg.coreLib, "--run-id", item.subjectID,
		"--portfolio-value-usd", fmtFloat(d.cfg.portfolioValueUSD), "--cash-usd", fmtFloat(d.cfg.cashUSD),
		"--max-name-weight-ratio", fmtFloat(d.cfg.maxNameWeightRatio), "--max-order-notional-usd", fmtFloat(d.cfg.maxOrderNotionalUSD),
		"--min-adv-usd", fmtFloat(d.cfg.minADVUSD), "--max-adv-participation-ratio", fmtFloat(d.cfg.maxADVParticipation),
		"--max-reconciliation-age-s", strconv.FormatInt(d.cfg.maxReconAgeSeconds, 10)}
	out, err := d.runSec(ctx, args...)
	if err != nil {
		d.fail(item, err, out.stderr)
		return err
	}
	return d.succeed(item, []enqueue{{typ: "stage_orders", subject: item.subjectID, priority: item.priority}}, map[string]any{"stdout_sha256": sha256Hex([]byte(out.stdout))})
}
func (d *daemon) staged(ctx context.Context) error {
	item, err := d.claim("stage_orders")
	if err != nil {
		return err
	}
	mode := d.mode()
	if !stageAllowed(mode) {
		return d.deferWork(item, 5*time.Minute, "trading_mode="+mode+" does not permit staged")
	}
	out, err := d.runSec(ctx, "stage", "--db", d.cfg.dbPath, "--run-id", item.subjectID)
	if err != nil {
		d.fail(item, err, out.stderr)
		return err
	}
	next := []enqueue{}
	if submitAllowed(mode) {
		next = append(next, enqueue{typ: "submit_orders", subject: item.subjectID, priority: item.priority})
	}
	return d.succeed(item, next, map[string]any{"stdout_sha256": sha256Hex([]byte(out.stdout)), "mode": mode})
}
func (d *daemon) submitd(ctx context.Context) error {
	item, err := d.claim("submit_orders")
	if err != nil {
		return err
	}
	mode := d.mode()
	if !submitAllowed(mode) {
		return d.deferWork(item, 5*time.Minute, "trading_mode="+mode+" does not permit submitd")
	}
	out, err := d.runSec(ctx, "send", "--db", d.cfg.dbPath, "--adapter", d.cfg.adapter, "--max-reconciliation-age-s", strconv.FormatInt(d.cfg.maxReconAgeSeconds, 10))
	if err != nil {
		d.fail(item, err, out.stderr)
		return err
	}
	return d.succeed(item, nil, map[string]any{"stdout_sha256": sha256Hex([]byte(out.stdout)), "adapter": d.cfg.adapter})
}
func (d *daemon) recond(ctx context.Context) error {
	if d.cfg.portfolioValueUSD <= 0 {
		return errNoWork
	}
	out, err := d.runSec(ctx, "recon", "--db", d.cfg.dbPath, "--portfolio-value-usd", fmtFloat(d.cfg.portfolioValueUSD), "--cash-usd", fmtFloat(d.cfg.cashUSD), "--reconciled", "1")
	if err != nil {
		d.daemonEvent("daemon_transition_failed", map[string]any{"daemon": d.cfg.role, "transition": "recon", "error": err.Error(), "stderr": truncate(out.stderr, 2000)})
		return err
	}
	d.daemonEvent("daemon_recon_cycle", map[string]any{"daemon": d.cfg.role, "portfolio_value_usd": d.cfg.portfolioValueUSD, "cash_usd": d.cfg.cashUSD})
	return nil
}
func (d *daemon) notifyd(ctx context.Context) error {
	last, err := d.cursorInt64("notifyd")
	if err != nil {
		return err
	}
	rows, err := d.db.Query(`SELECT id, event_type, occurred_at, payload_sha256 FROM events WHERE id > ? ORDER BY id LIMIT ?`, last, d.cfg.workLimit)
	if err != nil {
		return err
	}
	type eventNotice struct {
		id          int64
		eventType   string
		occurredAt  string
		payloadHash string
	}
	var notices []eventNotice
	for rows.Next() {
		var notice eventNotice
		if err := rows.Scan(&notice.id, &notice.eventType, &notice.occurredAt, &notice.payloadHash); err != nil {
			rows.Close()
			return err
		}
		notices = append(notices, notice)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(notices) == 0 {
		return errNoWork
	}
	for _, notice := range notices {
		message := fmt.Sprintf("%s %s %s", notice.occurredAt, notice.eventType, notice.payloadHash)
		if d.cfg.ntfyTopic != "" {
			out, err := d.runSec(ctx, "ping", "--method", "ntfy", "--title", "sec-fa "+notice.eventType, "--message", message, "--ntfy-topic", d.cfg.ntfyTopic)
			if err != nil {
				d.daemonEvent("daemon_transition_failed", map[string]any{"daemon": d.cfg.role, "transition": "notify", "event_row_id": notice.id, "error": err.Error(), "stderr": truncate(out.stderr, 2000)})
				return err
			}
		} else {
			log.Printf("notifyd: %s", message)
		}
		if err := d.setCursor("notifyd", strconv.FormatInt(notice.id, 10)); err != nil {
			return err
		}
	}
	return nil
}
func (d *daemon) modelArgs() []string {
	return []string{"--db", d.cfg.dbPath, "--core-lib", d.cfg.coreLib,
		"--portfolio-value-usd", fmtFloat(d.cfg.portfolioValueUSD), "--cash-usd", fmtFloat(d.cfg.cashUSD),
		"--max-name-weight-ratio", fmtFloat(d.cfg.maxNameWeightRatio), "--target-gross-exposure-ratio", fmtFloat(d.cfg.targetGrossExposure),
		"--max-order-notional-usd", fmtFloat(d.cfg.maxOrderNotionalUSD), "--min-adv-usd", fmtFloat(d.cfg.minADVUSD),
		"--max-adv-participation-ratio", fmtFloat(d.cfg.maxADVParticipation), "--max-reconciliation-age-s", strconv.FormatInt(d.cfg.maxReconAgeSeconds, 10)}
}
func (d *daemon) runSec(ctx context.Context, args ...string) (commandOutput, error) {
	cmd := exec.CommandContext(ctx, d.cfg.secBin, args...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := commandOutput{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		return out, fmt.Errorf("%s %s failed: %w", d.cfg.secBin, strings.Join(args, " "), err)
	}
	return out, nil
}
func ensureDaemonSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS daemon_heartbeats (
    daemon_name TEXT PRIMARY KEY,
    pid INTEGER NOT NULL,
    host TEXT NOT NULL,
    started_at TEXT NOT NULL,
    heartbeat_at TEXT NOT NULL,
    state TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS work_items (
    work_id TEXT PRIMARY KEY,
    work_type TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    subject_hash TEXT,
    status TEXT NOT NULL CHECK (status IN ('queued', 'leased', 'succeeded', 'failed', 'dead')),
    priority INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT,
    lease_until TEXT,
    available_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(work_type, subject_id, subject_hash)
);
CREATE INDEX IF NOT EXISTS idx_work_items_claim
    ON work_items(work_type, status, available_at, priority, lease_until);
CREATE TABLE IF NOT EXISTS daemon_cursors (
    daemon_name TEXT PRIMARY KEY,
    cursor_value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
`)
	return err
}
func (d *daemon) heartbeat(state string) error {
	now := utcNow()
	_, err := d.db.Exec(`INSERT INTO daemon_heartbeats(daemon_name, pid, host, started_at, heartbeat_at, state)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(daemon_name) DO UPDATE SET pid=excluded.pid, host=excluded.host, heartbeat_at=excluded.heartbeat_at, state=excluded.state`,
		d.name, d.pid, d.host, now, now, state)
	return err
}
func (d *daemon) enqueuePullAccessions(cik string) (int, error) {
	rows, err := d.db.Query(`SELECT accession_number FROM filings WHERE cik=? AND raw_index_uri IS NULL ORDER BY filing_date DESC LIMIT ?`, cik, d.cfg.workLimit)
	if err != nil {
		return 0, err
	}
	var accessions []string
	for rows.Next() {
		var accession string
		if err := rows.Scan(&accession); err != nil {
			rows.Close()
			return 0, err
		}
		accessions = append(accessions, accession)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	count := 0
	for _, accession := range accessions {
		if err := enqueueTx(tx, "pull_accession", accession, "-", 0, time.Now().UTC()); err != nil {
			return 0, err
		}
		count++
	}
	if count > 0 {
		if err := appendEventTx(tx, "daemon_work_enqueued", map[string]any{"daemon": d.cfg.role, "work_type": "pull_accession", "cik": cik, "count": count}); err != nil {
			return 0, err
		}
	}
	return count, tx.Commit()
}
func (d *daemon) claim(workType string) (workItem, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return workItem{}, err
	}
	defer tx.Rollback()
	now := utcNow()
	var item workItem
	err = tx.QueryRow(`SELECT work_id, work_type, subject_id, priority, attempts
FROM work_items
WHERE work_type=? AND available_at <= ?
  AND (status='queued' OR (status='leased' AND (lease_until IS NULL OR lease_until < ?)))
ORDER BY priority DESC, created_at ASC
LIMIT 1`, workType, now, now).Scan(&item.id, &item.typeName, &item.subjectID, &item.priority, &item.attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return workItem{}, errNoWork
	}
	if err != nil {
		return workItem{}, err
	}
	leaseUntil := time.Now().UTC().Add(d.cfg.lease).Format("2006-01-02T15:04:05.000Z")
	if _, err := tx.Exec(`UPDATE work_items SET status='leased', attempts=attempts+1, lease_owner=?, lease_until=?, updated_at=? WHERE work_id=?`, d.name, leaseUntil, now, item.id); err != nil {
		return workItem{}, err
	}
	if err := appendEventTx(tx, "daemon_work_leased", map[string]any{"daemon": d.cfg.role, "work_id": item.id, "work_type": item.typeName, "subject_id": item.subjectID, "attempts": item.attempts + 1}); err != nil {
		return workItem{}, err
	}
	return item, tx.Commit()
}

type enqueue struct {
	typ      string
	subject  string
	priority int
}

func (d *daemon) succeed(item workItem, next []enqueue, extra map[string]any) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := utcNow()
	if _, err := tx.Exec(`UPDATE work_items SET status='succeeded', lease_owner=NULL, lease_until=NULL, updated_at=? WHERE work_id=?`, now, item.id); err != nil {
		return err
	}
	for _, e := range next {
		if err := enqueueTx(tx, e.typ, e.subject, "-", e.priority, time.Now().UTC()); err != nil {
			return err
		}
	}
	payload := map[string]any{"daemon": d.cfg.role, "work_id": item.id, "work_type": item.typeName, "subject_id": item.subjectID, "enqueued_count": len(next)}
	for k, v := range extra {
		payload[k] = v
	}
	if err := appendEventTx(tx, "daemon_work_succeeded", payload); err != nil {
		return err
	}
	return tx.Commit()
}
func (d *daemon) fail(item workItem, cause error, stderr string) {
	tx, err := d.db.Begin()
	if err != nil {
		log.Printf("fail begin: %v", err)
		return
	}
	defer tx.Rollback()
	status := "failed"
	if item.attempts+1 >= d.cfg.maxAttempts {
		status = "dead"
	}
	now := utcNow()
	_, _ = tx.Exec(`UPDATE work_items SET status=?, lease_owner=NULL, lease_until=NULL, updated_at=? WHERE work_id=?`, status, now, item.id)
	_ = appendEventTx(tx, "daemon_work_failed", map[string]any{"daemon": d.cfg.role, "work_id": item.id, "work_type": item.typeName, "subject_id": item.subjectID, "status": status, "error": cause.Error(), "stderr": truncate(stderr, 2000)})
	_ = tx.Commit()
}
func (d *daemon) deferWork(item workItem, delay time.Duration, reason string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := utcNow()
	available := time.Now().UTC().Add(delay).Format("2006-01-02T15:04:05.000Z")
	if _, err := tx.Exec(`UPDATE work_items SET status='queued', lease_owner=NULL, lease_until=NULL, available_at=?, updated_at=? WHERE work_id=?`, available, now, item.id); err != nil {
		return err
	}
	if err := appendEventTx(tx, "daemon_work_deferred", map[string]any{"daemon": d.cfg.role, "work_id": item.id, "work_type": item.typeName, "subject_id": item.subjectID, "available_at": available, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit()
}
func enqueueTx(tx *sql.Tx, workType, subjectID, subjectHash string, priority int, availableAt time.Time) error {
	now := utcNow()
	workID := stableID("work", map[string]any{"work_type": workType, "subject_id": subjectID, "subject_hash": subjectHash})
	_, err := tx.Exec(`INSERT INTO work_items(work_id, work_type, subject_id, subject_hash, status, priority, attempts, available_at, created_at, updated_at)
VALUES (?, ?, ?, ?, 'queued', ?, 0, ?, ?, ?)
ON CONFLICT(work_type, subject_id, subject_hash) DO UPDATE SET
  status=CASE WHEN work_items.status IN ('succeeded','dead') THEN work_items.status ELSE 'queued' END,
  priority=max(work_items.priority, excluded.priority),
  updated_at=excluded.updated_at`, workID, workType, subjectID, subjectHash, priority, availableAt.UTC().Format("2006-01-02T15:04:05.000Z"), now, now)
	return err
}
func (d *daemon) daemonEvent(eventType string, payload map[string]any) {
	tx, err := d.db.Begin()
	if err != nil {
		log.Printf("event begin: %v", err)
		return
	}
	defer tx.Rollback()
	if err := appendEventTx(tx, eventType, payload); err != nil {
		log.Printf("event append: %v", err)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("event commit: %v", err)
	}
}
func appendEventTx(tx *sql.Tx, eventType string, payload map[string]any) error {
	payloadJSON := canonicalJSON(payload)
	_, err := tx.Exec(`INSERT INTO events(event_id, event_type, event_version, occurred_at, payload_json, payload_sha256)
VALUES (?, ?, 1, ?, ?, ?)`, uuidV4(), eventType, utcNow(), payloadJSON, sha256Hex([]byte(payloadJSON)))
	return err
}
func (d *daemon) mode() string {
	var mode string
	if err := d.db.QueryRow(`SELECT value FROM settings WHERE key='trading_mode'`).Scan(&mode); err != nil {
		return "observe"
	}
	return mode
}
func (d *daemon) cursorInt64(name string) (int64, error) {
	var raw string
	err := d.db.QueryRow(`SELECT cursor_value FROM daemon_cursors WHERE daemon_name=?`, name).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	v, _ := strconv.ParseInt(raw, 10, 64)
	return v, nil
}
func (d *daemon) setCursor(name, value string) error {
	_, err := d.db.Exec(`INSERT INTO daemon_cursors(daemon_name, cursor_value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(daemon_name) DO UPDATE SET cursor_value=excluded.cursor_value, updated_at=excluded.updated_at`, name, value, utcNow())
	return err
}
func runIDFromEvent(stdout string) string {
	dec := json.NewDecoder(strings.NewReader(stdout))
	for {
		var event struct {
			Payload map[string]any `json:"payload"`
		}
		if err := dec.Decode(&event); err != nil {
			break
		}
		if runID, ok := event.Payload["run_id"].(string); ok && runID != "" {
			return runID
		}
	}
	return ""
}
func validRole(role string) bool {
	switch role {
	case "watchd", "pulld", "xbrld", "pland", "gated", "staged", "submitd", "recond", "notifyd":
		return true
	default:
		return false
	}
}
func roleFromProgram(argv0 string) string {
	switch filepath.Base(argv0) {
	case "sec-watchd":
		return "watchd"
	case "sec-pulld":
		return "pulld"
	case "sec-xbrld":
		return "xbrld"
	case "sec-pland":
		return "pland"
	case "sec-gated":
		return "gated"
	case "sec-staged":
		return "staged"
	case "sec-submitd":
		return "submitd"
	case "sec-recond":
		return "recond"
	case "sec-notifyd":
		return "notifyd"
	default:
		return ""
	}
}
func atLeastShadow(mode string) bool {
	switch mode {
	case "shadow", "stage", "paper", "live_limited", "live":
		return true
	default:
		return false
	}
}
func stageAllowed(mode string) bool {
	switch mode {
	case "stage", "paper", "live_limited", "live":
		return true
	default:
		return false
	}
}
func submitAllowed(mode string) bool {
	switch mode {
	case "paper", "live_limited", "live":
		return true
	default:
		return false
	}
}
func discoverSecBin() string {
	for _, candidate := range []string{"./build/sec", "./sec", "sec"} {
		if candidate == "sec" {
			return candidate
		}
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
	}
	return "sec"
}
func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
func fmtFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
func utcNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
func stableID(prefix string, payload any) string {
	return prefix + "-" + sha256Hex([]byte(canonicalJSON(payload)))
}
func canonicalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
func sha256Hex(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func uuidV4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
