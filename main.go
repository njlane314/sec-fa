package main

/*
#cgo CFLAGS: -I${SRCDIR}/include
#cgo linux LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>
#include "core.h"

typedef const char* (*fa_status_name_fn)(fa_status_code);
typedef fa_status_code (*fa_model_run_v1_fn)(
    const fa_canonical_fact_v1*, size_t,
    const fa_security_v1*, size_t,
    const fa_position_v1*, size_t,
    const fa_model_config_v1*,
    const fa_risk_limits_v1*,
    fa_model_output_v1*);
typedef void (*fa_model_output_free_v1_fn)(fa_model_output_v1*);
typedef fa_status_code (*fa_model_run_v2_fn)(
    const fa_statement_snapshot_v1*, size_t,
    const fa_security_v1*, size_t,
    const fa_position_v1*, size_t,
    const fa_valuation_scenario_v1*, size_t,
    const fa_model_config_v1*,
    const fa_risk_limits_v1*,
    fa_model_output_v2*);
typedef void (*fa_model_output_free_v2_fn)(fa_model_output_v2*);
typedef fa_status_code (*fa_risk_check_v1_fn)(
    const fa_order_intent_v1*, size_t,
    const fa_security_v1*, size_t,
    const fa_risk_limits_v1*,
    fa_risk_output_v1*);
typedef void (*fa_risk_output_free_v1_fn)(fa_risk_output_v1*);

static void* sec_dlopen(const char* path) { return dlopen(path, RTLD_NOW); }
static void* sec_dlsym(void* handle, const char* name) { return dlsym(handle, name); }
static const char* sec_dlerror(void) { return dlerror(); }
static const char* sec_status_name(void* fn, fa_status_code code) {
    return ((fa_status_name_fn)fn)(code);
}
static fa_status_code sec_model_run_v1(
    void* fn,
    const fa_canonical_fact_v1* facts, size_t fact_count,
    const fa_security_v1* securities, size_t security_count,
    const fa_position_v1* positions, size_t position_count,
    const fa_model_config_v1* config,
    const fa_risk_limits_v1* risk_limits,
    fa_model_output_v1* output) {
    return ((fa_model_run_v1_fn)fn)(
        facts, fact_count, securities, security_count, positions, position_count,
        config, risk_limits, output);
}
static void sec_model_output_free_v1(void* fn, fa_model_output_v1* output) {
    ((fa_model_output_free_v1_fn)fn)(output);
}
static fa_status_code sec_model_run_v2(
    void* fn,
    const fa_statement_snapshot_v1* statements, size_t statement_count,
    const fa_security_v1* securities, size_t security_count,
    const fa_position_v1* positions, size_t position_count,
    const fa_valuation_scenario_v1* scenarios, size_t scenario_count,
    const fa_model_config_v1* config,
    const fa_risk_limits_v1* risk_limits,
    fa_model_output_v2* output) {
    return ((fa_model_run_v2_fn)fn)(
        statements, statement_count, securities, security_count, positions, position_count,
        scenarios, scenario_count, config, risk_limits, output);
}
static void sec_model_output_free_v2(void* fn, fa_model_output_v2* output) {
    ((fa_model_output_free_v2_fn)fn)(output);
}
static fa_status_code sec_risk_check_v1(
    void* fn,
    const fa_order_intent_v1* intents, size_t intent_count,
    const fa_security_v1* securities, size_t security_count,
    const fa_risk_limits_v1* risk_limits,
    fa_risk_output_v1* output) {
    return ((fa_risk_check_v1_fn)fn)(
        intents, intent_count, securities, security_count, risk_limits, output);
}
static void sec_risk_output_free_v1(void* fn, fa_risk_output_v1* output) {
    ((fa_risk_output_free_v1_fn)fn)(output);
}
*/
import "C"

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

const (
	abiVersion      = 2
	reasonBytes     = 128
	diagnosticBytes = 1024

	metricRevenue           = 1
	metricNetIncome         = 2
	metricEPSDiluted        = 3
	metricDilutedShares     = 4
	metricOperatingCashFlow = 5
	metricCapex             = 6
	metricCash              = 7
	metricDebt              = 8

	basisRevenueExcludingTax = 101
	basisRevenueIncludingTax = 102
	basisRevenueSalesNet     = 103
	basisRevenueGeneric      = 104
	basisNetIncome           = 201
	basisEPSDiluted          = 301
	basisDilutedShares       = 401
	basisOperatingCashFlow   = 501
	basisCapex               = 601
	basisCash                = 701
	basisDebt                = 801

	qualityDimensionalFact      = 1 << 16
	stmtMissingRevenue          = 1 << 0
	stmtMissingShares           = 1 << 1
	stmtMissingCash             = 1 << 2
	stmtMissingDebt             = 1 << 3
	stmtNegativeFCF             = 1 << 4
	stmtLowConfidence           = 1 << 7
	resolverVersion             = "canonical_resolver_v3"
	totalDimensionsJSON         = "{}"
	defaultUserAgent            = "sec-fa operator@example.invalid"
	secArchiveBaseURL           = "https://www.sec.gov/Archives/edgar/data/%d/%s"
	defaultScenarioAssumptionID = "default"
)

var (
	totalDimensionsHash = sha256Hex([]byte(totalDimensionsJSON))

	metricNames = map[int]string{
		metricRevenue:           "revenue",
		metricNetIncome:         "net_income",
		metricEPSDiluted:        "eps_diluted",
		metricDilutedShares:     "diluted_shares",
		metricOperatingCashFlow: "operating_cash_flow",
		metricCapex:             "capex",
		metricCash:              "cash",
		metricDebt:              "debt",
	}

	metricKinds = map[int]string{
		metricRevenue:           "flow",
		metricNetIncome:         "flow",
		metricEPSDiluted:        "per_share_flow",
		metricDilutedShares:     "flow",
		metricOperatingCashFlow: "flow",
		metricCapex:             "flow",
		metricCash:              "instant",
		metricDebt:              "instant",
	}

	metricKindCodes = map[string]uint32{
		"flow":           1,
		"instant":        2,
		"per_share_flow": 3,
		"ratio":          4,
		"derived":        5,
	}

	periodCodes = map[string]uint32{
		"fiscal_quarter":        1,
		"fiscal_ytd":            2,
		"fiscal_year":           3,
		"trailing_twelve_month": 4,
		"instant":               5,
		"stub":                  6,
		"transition":            7,
		"irregular":             8,
	}

	basisByMetric = map[int]int{
		metricRevenue:           basisRevenueExcludingTax,
		metricNetIncome:         basisNetIncome,
		metricEPSDiluted:        basisEPSDiluted,
		metricDilutedShares:     basisDilutedShares,
		metricOperatingCashFlow: basisOperatingCashFlow,
		metricCapex:             basisCapex,
		metricCash:              basisCash,
		metricDebt:              basisDebt,
	}

	conceptCandidates = map[string]canonicalConcept{
		"us-gaap:RevenueFromContractWithCustomerExcludingAssessedTax":           {metricRevenue, basisRevenueExcludingTax, 1, "USD"},
		"us-gaap:RevenueFromContractWithCustomerIncludingAssessedTax":           {metricRevenue, basisRevenueIncludingTax, 2, "USD"},
		"us-gaap:SalesRevenueNet":                                               {metricRevenue, basisRevenueSalesNet, 3, "USD"},
		"us-gaap:Revenues":                                                      {metricRevenue, basisRevenueGeneric, 4, "USD"},
		"us-gaap:NetIncomeLoss":                                                 {metricNetIncome, basisNetIncome, 1, "USD"},
		"us-gaap:ProfitLoss":                                                    {metricNetIncome, basisNetIncome, 2, "USD"},
		"us-gaap:EarningsPerShareDiluted":                                       {metricEPSDiluted, basisEPSDiluted, 1, "USD/shares"},
		"us-gaap:WeightedAverageNumberOfDilutedSharesOutstanding":               {metricDilutedShares, basisDilutedShares, 1, "shares"},
		"us-gaap:NetCashProvidedByUsedInOperatingActivities":                    {metricOperatingCashFlow, basisOperatingCashFlow, 1, "USD"},
		"us-gaap:PaymentsToAcquirePropertyPlantAndEquipment":                    {metricCapex, basisCapex, 1, "USD"},
		"us-gaap:CashAndCashEquivalentsAtCarryingValue":                         {metricCash, basisCash, 1, "USD"},
		"us-gaap:CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents": {metricCash, basisCash, 2, "USD"},
		"us-gaap:LongTermDebtAndFinanceLeaseObligationsCurrent":                 {metricDebt, basisDebt, 1, "USD"},
		"us-gaap:LongTermDebtCurrent":                                           {metricDebt, basisDebt, 2, "USD"},
		"us-gaap:LongTermDebtNoncurrent":                                        {metricDebt, basisDebt, 3, "USD"},
	}
)

type canonicalConcept struct {
	metricID int
	basisID  int
	priority int
	unit     string
}

type appError struct {
	msg  string
	code int
}

func (e appError) Error() string { return e.msg }

func fail(code int, format string, args ...any) error {
	return appError{msg: fmt.Sprintf(format, args...), code: code}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		var ae appError
		if errors.As(err, &ae) {
			if ae.code == 0 {
				os.Exit(0)
			}
			fmt.Fprintln(os.Stderr, "error:", ae.msg)
			os.Exit(ae.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printHelp()
		return nil
	}
	cmd := aliasCommand(args[0])
	rest := args[1:]
	switch cmd {
	case "init-db":
		return cmdInitDB(rest)
	case "security-upsert":
		return cmdSecurityUpsert(rest)
	case "position-upsert":
		return cmdPositionUpsert(rest)
	case "sec-watch":
		return cmdSecWatch(rest)
	case "sec-fetch":
		return cmdSecFetch(rest)
	case "xbrl-parse":
		return cmdXBRLParse(rest)
	case "broker-reconcile":
		return cmdBrokerReconcile(rest)
	case "model-run":
		return cmdModelRun(rest)
	case "model-run-v2":
		return cmdModelRunV2(rest)
	case "risk-check":
		return cmdRiskCheck(rest)
	case "order-stage":
		return cmdOrderStage(rest)
	case "broker-submit":
		return cmdBrokerSubmit(rest)
	case "set-mode":
		return cmdSetMode(rest)
	case "trading-halt":
		return cmdTradingHalt(rest)
	case "status":
		return cmdStatus(rest)
	case "report":
		return cmdReport(rest)
	case "notify":
		return cmdNotify(rest)
	case "universe-build":
		return cmdUniverseBuild(rest)
	case "ci-seed":
		return cmdCISeed(rest)
	case "facts-companyfacts":
		return fail(2, "%s is reserved for a future companyfacts fallback; use watch, pull, and xbrl for the Go ingestion path", cmd)
	default:
		return fail(2, "unknown command: %s", args[0])
	}
}

func aliasCommand(cmd string) string {
	switch cmd {
	case "init":
		return "init-db"
	case "sym":
		return "security-upsert"
	case "pos":
		return "position-upsert"
	case "watch":
		return "sec-watch"
	case "pull":
		return "sec-fetch"
	case "comp":
		return "facts-companyfacts"
	case "xbrl":
		return "xbrl-parse"
	case "univ":
		return "universe-build"
	case "recon":
		return "broker-reconcile"
	case "plan":
		return "model-run"
	case "value":
		return "model-run-v2"
	case "gate":
		return "risk-check"
	case "stage":
		return "order-stage"
	case "send":
		return "broker-submit"
	case "mode":
		return "set-mode"
	case "halt":
		return "trading-halt"
	case "stat":
		return "status"
	case "ping":
		return "notify"
	default:
		return cmd
	}
}

func printHelp() {
	fmt.Println("usage: sec <command> [options]")
	fmt.Println()
	fmt.Println("commands: init sym pos watch pull comp xbrl univ recon plan value gate stage send mode halt stat report ping")
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return appError{msg: "help requested", code: 0}
		}
		return err
	}
	return nil
}

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
	if _, err := db.Exec(schemaSQL); err != nil {
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

func cmdSecWatch(args []string) error {
	fs := newFlagSet("sec-watch")
	dbPath := fs.String("db", "", "")
	cik := fs.String("cik", "", "")
	userAgent := fs.String("user-agent", defaultUserAgent, "")
	limit := fs.Int("limit", 40, "")
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
	body, err := httpGet(fmt.Sprintf("https://data.sec.gov/submissions/CIK%s.json", cik10), *userAgent)
	if err != nil {
		return err
	}
	var payload struct {
		Filings struct {
			Recent map[string][]any `json:"recent"`
		} `json:"filings"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	get := func(key string, i int) string {
		values := payload.Filings.Recent[key]
		if i >= len(values) || values[i] == nil {
			return ""
		}
		return fmt.Sprint(values[i])
	}
	accessions := payload.Filings.Recent["accessionNumber"]
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	emitted := 0
	for i := 0; i < len(accessions) && emitted < *limit; i++ {
		accession := get("accessionNumber", i)
		form := get("form", i)
		if accession == "" || (form != "10-K" && form != "10-Q" && form != "10-K/A" && form != "10-Q/A") {
			continue
		}
		primary := get("primaryDocument", i)
		sourceURL := archiveURL(cik10, accession, primary)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO filings(accession_number, cik, form, filing_date, accepted_at, primary_document, source_url, ingested_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, accession, cik10, form, get("filingDate", i), get("acceptanceDateTime", i), primary, sourceURL, utcNow()); err != nil {
			return err
		}
		event, err := appendEvent(tx, "filing_seen", map[string]any{"cik": cik10, "accession_number": accession, "form": form, "filing_date": get("filingDate", i), "accepted_at": get("acceptanceDateTime", i), "primary_document": primary, "source_url": sourceURL})
		if err != nil {
			return err
		}
		if err := jsonLine(event); err != nil {
			return err
		}
		emitted++
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "filing_seen emitted=%d\n", emitted)
	return nil
}

func cmdSecFetch(args []string) error {
	fs := newFlagSet("sec-fetch")
	dbPath := fs.String("db", "", "")
	rawRoot := fs.String("raw-root", "raw", "")
	userAgent := fs.String("user-agent", defaultUserAgent, "")
	accessionFilter := fs.String("accession", "", "")
	limit := fs.Int("limit", 20, "")
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
	query := `SELECT accession_number, cik, primary_document FROM filings WHERE raw_index_uri IS NULL OR raw_primary_uri IS NULL`
	var qargs []any
	if *accessionFilter != "" {
		query = `SELECT accession_number, cik, primary_document FROM filings WHERE accession_number=?`
		qargs = append(qargs, *accessionFilter)
	}
	query += ` ORDER BY filing_date DESC LIMIT ?`
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
	fetched := 0
	for rows.Next() {
		var accession, cik string
		var primary sql.NullString
		if err := rows.Scan(&accession, &cik, &primary); err != nil {
			return err
		}
		cik10 := normalizeCIK(cik)
		accNoDash := strings.ReplaceAll(accession, "-", "")
		dir := filepath.Join(*rawRoot, cik10, accNoDash)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		indexURL := archiveURL(cik10, accession, "")
		indexData, err := httpGet(indexURL, *userAgent)
		if err != nil {
			return err
		}
		indexPath := filepath.Join(dir, "index.json")
		if err := os.WriteFile(indexPath, indexData, 0o644); err != nil {
			return err
		}
		primaryPath := ""
		combined := sha256Hex(indexData)
		if primary.Valid && primary.String != "" {
			primaryURL := archiveURL(cik10, accession, primary.String)
			primaryData, err := httpGet(primaryURL, *userAgent)
			if err != nil {
				return err
			}
			primaryPath = filepath.Join(dir, primary.String)
			if err := os.WriteFile(primaryPath, primaryData, 0o644); err != nil {
				return err
			}
			combined = sha256Hex(append(indexData, primaryData...))
		}
		if _, err := tx.Exec(`UPDATE filings SET raw_index_uri=?, raw_primary_uri=?, raw_sha256=?, ingested_at=? WHERE accession_number=?`, indexPath, primaryPath, combined, utcNow(), accession); err != nil {
			return err
		}
		event, err := appendEvent(tx, "filing_fetched", map[string]any{"accession_number": accession, "cik": cik10, "raw_index_uri": indexPath, "raw_primary_uri": primaryPath, "raw_sha256": combined})
		if err != nil {
			return err
		}
		if err := jsonLine(event); err != nil {
			return err
		}
		fetched++
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "filing_fetched emitted=%d\n", fetched)
	return nil
}

func httpGet(url, userAgent string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fail(2, "GET %s failed: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
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

func jsonLine(value any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func utcNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func normalizeCIK(cik string) string {
	digits := make([]byte, 0, len(cik))
	for i := 0; i < len(cik); i++ {
		if cik[i] >= '0' && cik[i] <= '9' {
			digits = append(digits, cik[i])
		}
	}
	if len(digits) > 10 {
		digits = digits[len(digits)-10:]
	}
	return fmt.Sprintf("%010s", string(digits))
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mustCanonicalJSON(value any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		panic(err)
	}
	return strings.TrimSpace(buf.String())
}

func stableID(prefix string, payload any) string {
	return prefix + "-" + sha256Hex([]byte(mustCanonicalJSON(payload)))
}

func uuidV4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type xmlNode struct {
	Name     xml.Name
	Attr     []xml.Attr
	Text     string
	Children []*xmlNode
}

func parseXMLTree(data []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []*xmlNode
	var root *xmlNode
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{Name: t.Name, Attr: append([]xml.Attr(nil), t.Attr...)}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("empty XML document")
	}
	return root, nil
}

func attr(n *xmlNode, local string) string {
	for _, a := range n.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func child(n *xmlNode, local string) *xmlNode {
	for _, c := range n.Children {
		if c.Name.Local == local {
			return c
		}
	}
	return nil
}

func descendants(n *xmlNode, local string, out *[]*xmlNode) {
	for _, c := range n.Children {
		if local == "" || c.Name.Local == local {
			*out = append(*out, c)
		}
		descendants(c, local, out)
	}
}

func firstDescendant(n *xmlNode, local string) *xmlNode {
	var out []*xmlNode
	descendants(n, local, &out)
	if len(out) == 0 {
		return nil
	}
	return out[0]
}

func text(n *xmlNode) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*xmlNode)
	walk = func(cur *xmlNode) {
		b.WriteString(cur.Text)
		for _, c := range cur.Children {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

type periodInfo struct {
	RawStartDate        sql.NullString
	RawEndDate          sql.NullString
	RawInstantDate      sql.NullString
	StartDateInclusive  sql.NullString
	EndDateExclusive    sql.NullString
	DurationDays        int
	PeriodKind          string
	PeriodSemantics     string
	FiscalYear          sql.NullInt64
	FiscalPeriod        sql.NullString
	FiscalPeriodOrdinal sql.NullInt64
	PeriodLengthClass   string
}

type contextInfo struct {
	ID               string
	EntityIdentifier string
	Period           periodInfo
	DimensionsHash   string
	DimensionsJSON   string
	SegmentJSON      sql.NullString
	ScenarioJSON     sql.NullString
	ScopeClass       string
	AxisCount        int
}

type unitInfo struct {
	ID              string
	Signature       string
	NumeratorJSON   sql.NullString
	DenominatorJSON sql.NullString
}

type rawFact struct {
	SourceDocID      string
	SecurityID       int64
	CIK              string
	Accession        string
	Taxonomy         string
	ConceptQName     string
	ConceptLocalName string
	Context          contextInfo
	ContextID        string
	RawContextID     string
	Unit             *unitInfo
	UnitID           sql.NullString
	ValueDecimal     sql.NullFloat64
	ValueText        sql.NullString
	DecimalsAttr     sql.NullString
	PrecisionAttr    sql.NullString
	IsNil            int
	Form             string
	FiledAt          string
	AcceptedAt       string
	AvailableAt      string
	DimensionsHash   string
	DimensionalScope string
	DimensionsJSON   string
}

func cmdXBRLParse(args []string) error {
	fs := newFlagSet("xbrl-parse")
	dbPath := fs.String("db", "", "")
	accession := fs.String("accession", "", "")
	cik := fs.String("cik", "", "")
	symbol := fs.String("symbol", "", "")
	form := fs.String("form", "10-Q", "")
	filedAt := fs.String("filed-at", "", "")
	acceptedAt := fs.String("accepted-at", "", "")
	rawRoot := fs.String("raw-root", "raw", "")
	packageRoot := fs.String("package-root", "", "")
	userAgent := fs.String("user-agent", defaultUserAgent, "")
	sleepS := fs.Float64("sleep-s", 0, "")
	strict := fs.Bool("strict", false, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	_ = symbol
	_ = userAgent
	_ = sleepS
	_ = strict
	if *dbPath == "" || *accession == "" || *cik == "" {
		return fail(2, "--db, --accession, and --cik are required")
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
	if *acceptedAt == "" {
		*acceptedAt = utcNow()
	}
	if *filedAt == "" {
		*filedAt = strings.Split(*acceptedAt, "T")[0]
	}
	root := *packageRoot
	if root == "" {
		root = filepath.Join(*rawRoot, cik10, strings.ReplaceAll(*accession, "-", ""))
	}
	files, err := loadPackageFiles(root, cik10, *accession, *rawRoot)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO filings(accession_number, cik, form, filing_date, accepted_at, source_url, ingested_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, *accession, cik10, *form, *filedAt, *acceptedAt, archiveURL(cik10, *accession, ""), utcNow()); err != nil {
		return err
	}

	indexDocID := ""
	documentsStored := 0
	rawInserted := 0
	selectedInserted := 0
	var parseErrors []string
	contexts := map[string]contextInfo{}
	units := map[string]unitInfo{}

	for _, f := range files {
		docID := sourceDocumentID(cik10, f.URL, f.Hash)
		if f.Name == "index.json" {
			indexDocID = docID
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO source_documents(doc_id, cik, accession_number, form, filed_at, source_url, local_path, sha256, document_kind, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			docID, cik10, *accession, *form, *filedAt, f.URL, f.Path, f.Hash, f.Kind, utcNow()); err != nil {
			return err
		}
		documentsStored++
		if f.Kind != "inline_xbrl" && f.Kind != "xbrl_instance" {
			continue
		}
		root, err := parseXMLTree(f.Data)
		if err != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("%s: %v", f.Name, err))
			continue
		}
		docContexts := parseContexts(root)
		for id, ctx := range docContexts {
			contexts[id] = ctx
		}
		docUnits := parseUnits(root)
		for id, unit := range docUnits {
			units[id] = unit
		}
		for _, ctx := range docContexts {
			if err := insertContext(tx, cik10, *accession, ctx); err != nil {
				return err
			}
		}
		for _, unit := range docUnits {
			if err := insertUnit(tx, cik10, *accession, unit); err != nil {
				return err
			}
		}
		var facts []rawFact
		if f.Kind == "inline_xbrl" {
			facts = parseInlineFacts(root, docID, securityID, cik10, *accession, *form, *filedAt, *acceptedAt, docContexts, docUnits)
		} else {
			facts = parseClassicFacts(root, docID, securityID, cik10, *accession, *form, *filedAt, *acceptedAt, docContexts, docUnits)
		}
		for _, fact := range facts {
			inserted, err := insertRawFact(tx, fact)
			if err != nil {
				return err
			}
			if inserted {
				rawInserted++
			}
			ok, err := insertCanonicalObservation(tx, fact)
			if err != nil {
				return err
			}
			if ok {
				selectedInserted++
			}
		}
		fmt.Fprintf(os.Stderr, "parsed %s: kind=%s facts=%d\n", f.Name, f.Kind, len(facts))
	}
	event, err := appendEvent(tx, "xbrl_package_parsed", map[string]any{
		"accession_number": *accession, "cik": cik10, "security_id": securityID, "form": *form,
		"index_doc_id": indexDocID, "documents_stored": documentsStored, "raw_facts_inserted": rawInserted,
		"canonical_candidates": selectedInserted, "canonical_selected_inserted": selectedInserted,
		"derived_quarter_observations_inserted": 0, "canonical_exceptions_inserted": 0,
		"resolver_version": resolverVersion, "parse_errors": parseErrors,
	})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "xbrl packages processed documents=%d raw_inserted=%d selected_inserted=%d derived_inserted=0 exceptions_inserted=0 parse_errors=%d\n",
		documentsStored-1, rawInserted, selectedInserted, len(parseErrors))
	_ = contexts
	_ = units
	return jsonLine(event)
}

type packageFile struct {
	Name string
	Path string
	URL  string
	Hash string
	Kind string
	Data []byte
}

func loadPackageFiles(packageRoot, cik, accession, rawRoot string) ([]packageFile, error) {
	var out []packageFile
	if packageRoot == "" {
		return nil, fail(2, "xbrl requires a fetched raw package under --raw-root or an explicit --package-root")
	}
	indexPath := filepath.Join(packageRoot, "index.json")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	out = append(out, packageFile{
		Name: "index.json", Path: indexPath, URL: archiveURL(cik, accession, ""), Hash: sha256Hex(indexData), Kind: "sec_archive_index", Data: indexData,
	})
	var idx struct {
		Directory struct {
			Item []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"item"`
		} `json:"directory"`
	}
	if err := json.Unmarshal(indexData, &idx); err != nil {
		return nil, err
	}
	for _, item := range idx.Directory.Item {
		name := item.Name
		if !isXBRLRelevantName(name) {
			continue
		}
		path := filepath.Join(packageRoot, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		kind := "xbrl_instance"
		if strings.HasSuffix(strings.ToLower(name), ".htm") || strings.HasSuffix(strings.ToLower(name), ".html") {
			kind = "inline_xbrl"
		}
		out = append(out, packageFile{
			Name: name, Path: path, URL: archiveURL(cik, accession, name), Hash: sha256Hex(data), Kind: kind, Data: data,
		})
	}
	_ = rawRoot
	return out, nil
}

func isXBRLRelevantName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".xml") || strings.HasSuffix(lower, ".htm") || strings.HasSuffix(lower, ".html")
}

func archiveURL(cik10, accession, doc string) string {
	cikInt, _ := strconv.Atoi(cik10)
	accNoDash := strings.ReplaceAll(accession, "-", "")
	base := fmt.Sprintf(secArchiveBaseURL, cikInt, accNoDash)
	if doc == "" {
		return base + "/index.json"
	}
	return base + "/" + doc
}

func sourceDocumentID(cik, url, hash string) string {
	return stableID("doc", map[string]any{"cik": cik, "url": url, "sha256": hash})
}

func parseContexts(root *xmlNode) map[string]contextInfo {
	var nodes []*xmlNode
	descendants(root, "context", &nodes)
	out := map[string]contextInfo{}
	for _, n := range nodes {
		id := attr(n, "id")
		if id == "" {
			continue
		}
		entity := firstDescendant(n, "identifier")
		period := child(n, "period")
		info := contextInfo{ID: id, EntityIdentifier: text(entity), DimensionsHash: totalDimensionsHash, DimensionsJSON: totalDimensionsJSON, ScopeClass: "consolidated_total"}
		if period != nil {
			info.Period = parsePeriod(period)
		}
		var members []*xmlNode
		descendants(n, "explicitMember", &members)
		if len(members) > 0 {
			dims := make([]map[string]string, 0, len(members))
			for _, m := range members {
				dims = append(dims, map[string]string{"dimension": attr(m, "dimension"), "member": text(m)})
			}
			sort.Slice(dims, func(i, j int) bool { return dims[i]["dimension"] < dims[j]["dimension"] })
			j := mustCanonicalJSON(dims)
			info.DimensionsJSON = j
			info.DimensionsHash = sha256Hex([]byte(j))
			info.AxisCount = len(dims)
			info.ScopeClass = "segment"
			for _, d := range dims {
				if strings.Contains(strings.ToLower(d["dimension"]), "product") {
					info.ScopeClass = "product"
				}
			}
		}
		out[id] = info
	}
	return out
}

func parsePeriod(period *xmlNode) periodInfo {
	start := text(child(period, "startDate"))
	end := text(child(period, "endDate"))
	instant := text(child(period, "instant"))
	var p periodInfo
	if instant != "" {
		p.RawInstantDate = sql.NullString{String: instant, Valid: true}
		p.StartDateInclusive = sql.NullString{String: instant, Valid: true}
		p.EndDateExclusive = sql.NullString{String: instant, Valid: true}
		p.PeriodKind = "instant"
		p.PeriodSemantics = "instant"
		p.PeriodLengthClass = "instant"
		if y, err := strconv.Atoi(instant[:4]); err == nil {
			p.FiscalYear = sql.NullInt64{Int64: int64(y), Valid: true}
		}
		return p
	}
	p.RawStartDate = sql.NullString{String: start, Valid: start != ""}
	p.RawEndDate = sql.NullString{String: end, Valid: end != ""}
	p.StartDateInclusive = p.RawStartDate
	if t, err := parseDate(end); err == nil {
		p.EndDateExclusive = sql.NullString{String: t.AddDate(0, 0, 1).Format("2006-01-02"), Valid: true}
	}
	p.PeriodKind = "duration"
	p.DurationDays = durationDays(start, end)
	p.PeriodLengthClass = periodLengthClass(p.DurationDays)
	p.PeriodSemantics = "irregular"
	if p.DurationDays >= 70 && p.DurationDays <= 110 {
		p.PeriodSemantics = "fiscal_quarter"
	}
	if p.DurationDays >= 330 {
		p.PeriodSemantics = "fiscal_year"
	}
	if len(end) >= 4 {
		if y, err := strconv.Atoi(end[:4]); err == nil {
			p.FiscalYear = sql.NullInt64{Int64: int64(y), Valid: true}
		}
	}
	if p.PeriodSemantics == "fiscal_quarter" {
		q := quarterFromEnd(end)
		p.FiscalPeriod = sql.NullString{String: fmt.Sprintf("Q%d", q), Valid: true}
		p.FiscalPeriodOrdinal = sql.NullInt64{Int64: int64(q), Valid: true}
	}
	return p
}

func quarterFromEnd(date string) int {
	if len(date) < 7 {
		return 0
	}
	month, _ := strconv.Atoi(date[5:7])
	return ((month - 1) / 3) + 1
}

func periodLengthClass(days int) string {
	switch {
	case days == 0:
		return "instant"
	case days >= 70 && days <= 110:
		return "13w"
	case days >= 330 && days <= 380:
		return "year"
	default:
		return "irregular"
	}
}

func parseUnits(root *xmlNode) map[string]unitInfo {
	var nodes []*xmlNode
	descendants(root, "unit", &nodes)
	out := map[string]unitInfo{}
	for _, n := range nodes {
		id := attr(n, "id")
		if id == "" {
			continue
		}
		var measures []*xmlNode
		descendants(n, "measure", &measures)
		sig := ""
		for _, m := range measures {
			sig = normalizeMeasure(text(m))
			break
		}
		if sig == "" {
			sig = "unknown"
		}
		out[id] = unitInfo{ID: id, Signature: sig}
	}
	return out
}

func normalizeMeasure(value string) string {
	switch strings.TrimSpace(value) {
	case "iso4217:USD", "USD":
		return "USD"
	case "xbrli:shares", "shares":
		return "shares"
	default:
		if strings.Contains(value, "USD") {
			return "USD"
		}
		return strings.TrimSpace(value)
	}
}

func parseInlineFacts(root *xmlNode, docID string, securityID int64, cik, accession, form, filedAt, acceptedAt string, contexts map[string]contextInfo, units map[string]unitInfo) []rawFact {
	var nodes []*xmlNode
	descendants(root, "nonFraction", &nodes)
	descendants(root, "nonNumeric", &nodes)
	out := make([]rawFact, 0, len(nodes))
	for _, n := range nodes {
		name := attr(n, "name")
		if name == "" {
			continue
		}
		tax, local := splitQName(name)
		ctxID := attr(n, "contextRef")
		ctx, ok := contexts[ctxID]
		if !ok {
			continue
		}
		var unit *unitInfo
		unitRef := attr(n, "unitRef")
		if u, ok := units[unitRef]; ok {
			unit = &u
		}
		valueText := text(n)
		var dec sql.NullFloat64
		var txt sql.NullString
		if n.Name.Local == "nonFraction" {
			if v, ok := parseScaledNumber(valueText, attr(n, "scale")); ok {
				dec = sql.NullFloat64{Float64: v, Valid: true}
			}
		} else {
			txt = sql.NullString{String: valueText, Valid: true}
		}
		out = append(out, buildRawFact(docID, securityID, cik, accession, tax, local, ctx, unit, dec, txt, attr(n, "decimals"), attr(n, "precision"), form, filedAt, acceptedAt))
	}
	return out
}

func parseClassicFacts(root *xmlNode, docID string, securityID int64, cik, accession, form, filedAt, acceptedAt string, contexts map[string]contextInfo, units map[string]unitInfo) []rawFact {
	var nodes []*xmlNode
	descendants(root, "", &nodes)
	var out []rawFact
	for _, n := range nodes {
		ctxID := attr(n, "contextRef")
		if ctxID == "" {
			continue
		}
		tax := taxonomyForSpace(n.Name.Space)
		if tax == "" {
			continue
		}
		ctx, ok := contexts[ctxID]
		if !ok {
			continue
		}
		var unit *unitInfo
		if u, ok := units[attr(n, "unitRef")]; ok {
			unit = &u
		}
		valueText := text(n)
		var dec sql.NullFloat64
		var txt sql.NullString
		if v, ok := parseScaledNumber(valueText, "0"); ok {
			dec = sql.NullFloat64{Float64: v, Valid: true}
		} else {
			txt = sql.NullString{String: valueText, Valid: true}
		}
		out = append(out, buildRawFact(docID, securityID, cik, accession, tax, n.Name.Local, ctx, unit, dec, txt, attr(n, "decimals"), attr(n, "precision"), form, filedAt, acceptedAt))
	}
	return out
}

func splitQName(q string) (string, string) {
	parts := strings.SplitN(q, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", q
}

func taxonomyForSpace(space string) string {
	switch {
	case strings.Contains(space, "us-gaap"):
		return "us-gaap"
	case strings.Contains(space, "dei"):
		return "dei"
	default:
		return ""
	}
}

func parseScaledNumber(text, scale string) (float64, bool) {
	clean := strings.ReplaceAll(strings.TrimSpace(text), ",", "")
	if clean == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return 0, false
	}
	if scale != "" {
		s, err := strconv.Atoi(scale)
		if err == nil {
			v *= math.Pow10(s)
		}
	}
	return v, true
}

func buildRawFact(docID string, securityID int64, cik, accession, taxonomy, local string, ctx contextInfo, unit *unitInfo, dec sql.NullFloat64, txt sql.NullString, decimals, precision, form, filedAt, acceptedAt string) rawFact {
	unitID := sql.NullString{}
	if unit != nil {
		unitID = sql.NullString{String: unitIDFor(cik, accession, *unit), Valid: true}
	}
	scope := ctx.ScopeClass
	if scope == "" {
		scope = "consolidated_total"
	}
	return rawFact{
		SourceDocID: docID, SecurityID: securityID, CIK: cik, Accession: accession,
		Taxonomy: taxonomy, ConceptQName: taxonomy + ":" + local, ConceptLocalName: local,
		Context: ctx, ContextID: contextIDFor(cik, accession, ctx), RawContextID: ctx.ID,
		Unit: unit, UnitID: unitID, ValueDecimal: dec, ValueText: txt,
		DecimalsAttr:  sql.NullString{String: decimals, Valid: decimals != ""},
		PrecisionAttr: sql.NullString{String: precision, Valid: precision != ""},
		Form:          form, FiledAt: filedAt, AcceptedAt: acceptedAt, AvailableAt: acceptedAt,
		DimensionsHash: ctx.DimensionsHash, DimensionalScope: scope, DimensionsJSON: ctx.DimensionsJSON,
	}
}

func contextIDFor(cik, accession string, ctx contextInfo) string {
	p := ctx.Period
	return stableID("ctx", map[string]any{
		"cik": cik, "accession_number": accession, "raw_context_id": ctx.ID,
		"raw_start_date": nullableString(p.RawStartDate), "raw_end_date": nullableString(p.RawEndDate),
		"raw_instant_date": nullableString(p.RawInstantDate), "fiscal_year": nullableInt(p.FiscalYear),
		"fiscal_period": nullableString(p.FiscalPeriod), "dimensions_hash": ctx.DimensionsHash,
	})
}

func unitIDFor(cik, accession string, unit unitInfo) string {
	return stableID("unit", map[string]any{"cik": cik, "accession_number": accession, "raw_unit_id": unit.ID, "unit_signature": unit.Signature})
}

func periodIDFor(cik, accession string, p periodInfo) string {
	return stableID("period", map[string]any{
		"cik": cik, "accession_number": accession, "raw_start_date": nullableString(p.RawStartDate),
		"raw_end_date": nullableString(p.RawEndDate), "raw_instant_date": nullableString(p.RawInstantDate),
		"period_semantics": p.PeriodSemantics, "fiscal_year": nullableInt(p.FiscalYear), "fiscal_period": nullableString(p.FiscalPeriod),
	})
}

func insertContext(tx execer, cik, accession string, ctx contextInfo) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO dimension_signatures(dimensions_hash, dimensions_json, has_dimensions, scope_class, axis_count, created_at)
VALUES (?, ?, ?, ?, ?, ?)`, ctx.DimensionsHash, ctx.DimensionsJSON, boolInt(ctx.AxisCount > 0), ctx.ScopeClass, ctx.AxisCount, utcNow()); err != nil {
		return err
	}
	pid := periodIDFor(cik, accession, ctx.Period)
	p := ctx.Period
	if _, err := tx.Exec(`INSERT OR IGNORE INTO reporting_periods(
period_id, cik, accession_number, raw_start_date, raw_end_date, raw_instant_date,
start_date_inclusive, end_date_exclusive, duration_days, period_kind, period_semantics,
fiscal_year, fiscal_period, fiscal_period_ordinal, period_length_class, source, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'xbrl_context', ?)`,
		pid, cik, accession, nullableString(p.RawStartDate), nullableString(p.RawEndDate), nullableString(p.RawInstantDate),
		nullableString(p.StartDateInclusive), nullableString(p.EndDateExclusive), p.DurationDays, p.PeriodKind, p.PeriodSemantics,
		nullableInt(p.FiscalYear), nullableString(p.FiscalPeriod), nullableInt(p.FiscalPeriodOrdinal), p.PeriodLengthClass, utcNow()); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT OR IGNORE INTO xbrl_contexts(
context_id, cik, accession_number, raw_context_id, entity_identifier, period_id, period_kind,
raw_start_date, raw_end_date, raw_instant_date, start_date_inclusive, end_date_exclusive,
duration_days, dimensions_hash, dimensions_json, segment_json, scenario_json, raw_context_sha256)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		contextIDFor(cik, accession, ctx), cik, accession, ctx.ID, ctx.EntityIdentifier, pid, p.PeriodKind,
		nullableString(p.RawStartDate), nullableString(p.RawEndDate), nullableString(p.RawInstantDate),
		nullableString(p.StartDateInclusive), nullableString(p.EndDateExclusive), p.DurationDays,
		ctx.DimensionsHash, ctx.DimensionsJSON, nullableString(ctx.SegmentJSON), nullableString(ctx.ScenarioJSON),
		sha256Hex([]byte(ctx.ID+ctx.DimensionsJSON)))
	return err
}

func insertUnit(tx execer, cik, accession string, unit unitInfo) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO xbrl_units(unit_id, cik, accession_number, raw_unit_id, unit_signature, numerator_json, denominator_json, raw_unit_sha256)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, unitIDFor(cik, accession, unit), cik, accession, unit.ID, unit.Signature,
		nullableString(unit.NumeratorJSON), nullableString(unit.DenominatorJSON), sha256Hex([]byte(unit.ID+unit.Signature)))
	return err
}

func insertRawFact(tx execer, fact rawFact) (bool, error) {
	rawHash := rawFactHash(fact)
	factID := stableID("fact", map[string]any{
		"security_id": fact.SecurityID, "taxonomy": fact.Taxonomy, "concept": fact.ConceptQName,
		"unit": nullableString(fact.UnitID), "value_decimal": nullableFloat(fact.ValueDecimal),
		"value_text": nullableString(fact.ValueText), "accession_number": fact.Accession,
		"period_id":  periodIDFor(fact.CIK, fact.Accession, fact.Context.Period),
		"context_id": fact.ContextID, "raw_context_id": fact.RawContextID, "dimensions_hash": fact.DimensionsHash, "form": fact.Form,
	})
	res, err := tx.Exec(`INSERT OR IGNORE INTO xbrl_facts(
fact_id, source_doc_id, security_id, cik, accession_number, taxonomy, concept_qname, concept_local_name,
context_id, unit_id, value_decimal, value_text, decimals_attr, precision_attr, is_nil, raw_fact_hash,
raw_context_id, form, filed_at, accepted_at, available_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		factID, fact.SourceDocID, fact.SecurityID, fact.CIK, fact.Accession, fact.Taxonomy, fact.ConceptQName, fact.ConceptLocalName,
		fact.ContextID, nullableString(fact.UnitID), nullableFloat(fact.ValueDecimal), nullableString(fact.ValueText),
		nullableString(fact.DecimalsAttr), nullableString(fact.PrecisionAttr), fact.IsNil, rawHash,
		fact.RawContextID, fact.Form, fact.FiledAt, fact.AcceptedAt, fact.AvailableAt, utcNow())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func rawFactHash(f rawFact) string {
	return sha256Hex([]byte(mustCanonicalJSON(map[string]any{
		"security_id": f.SecurityID, "concept_qname": f.ConceptQName, "context_id": f.ContextID,
		"unit_id": nullableString(f.UnitID), "value_decimal": nullableFloat(f.ValueDecimal),
		"value_text": nullableString(f.ValueText), "accession_number": f.Accession,
	})))
}

func insertCanonicalObservation(tx execer, fact rawFact) (bool, error) {
	candidate, ok := conceptCandidates[fact.ConceptQName]
	if !ok || !fact.ValueDecimal.Valid || fact.DimensionalScope != "consolidated_total" {
		return false, nil
	}
	unit := ""
	if fact.Unit != nil {
		unit = fact.Unit.Signature
	}
	if unit == "" {
		unit = candidate.unit
	}
	pid := periodIDFor(fact.CIK, fact.Accession, fact.Context.Period)
	factID := stableID("fact", map[string]any{
		"security_id": fact.SecurityID, "taxonomy": fact.Taxonomy, "concept": fact.ConceptQName,
		"unit": nullableString(fact.UnitID), "value_decimal": nullableFloat(fact.ValueDecimal),
		"value_text": nullableString(fact.ValueText), "accession_number": fact.Accession,
		"period_id": pid, "context_id": fact.ContextID, "raw_context_id": fact.RawContextID,
		"dimensions_hash": fact.DimensionsHash, "form": fact.Form,
	})
	obsID := stableID("obs", map[string]any{
		"resolver_version": resolverVersion, "security_id": fact.SecurityID, "metric_id": candidate.metricID,
		"basis_id": candidate.basisID, "period_id": pid, "dimensions_hash": fact.DimensionsHash,
		"source_fact_id": factID, "observation_status": "selected",
	})
	obsHash := strings.TrimPrefix(obsID, "obs-")
	flags := 0
	if fact.DimensionsHash != totalDimensionsHash {
		flags |= qualityDimensionalFact
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO canonical_observations(
observation_id, observation_hash, resolver_version, security_id, cik, metric_id, metric_name, metric_kind,
basis_id, period_id, period_semantics, duration_days, fiscal_year, fiscal_period, value_decimal,
unit_signature, dimensions_hash, dimensional_scope, observation_status, source_fact_id, source_accession,
taxonomy, concept_qname, selection_reason, quality_flags, quality_flags_json, quality_score,
accepted_at, available_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'selected', ?, ?, ?, ?, ?, ?, '[]', 1.0, ?, ?, ?)`,
		obsID, obsHash, resolverVersion, fact.SecurityID, fact.CIK, candidate.metricID, metricNames[candidate.metricID], metricKinds[candidate.metricID],
		candidate.basisID, pid, fact.Context.Period.PeriodSemantics, fact.Context.Period.DurationDays,
		nullableInt(fact.Context.Period.FiscalYear), nullableString(fact.Context.Period.FiscalPeriod), fact.ValueDecimal.Float64,
		unit, fact.DimensionsHash, fact.DimensionalScope, factID, fact.Accession, fact.Taxonomy, fact.ConceptQName,
		fmt.Sprintf("priority=%d total-company xbrl fact", candidate.priority), flags, fact.AcceptedAt, fact.AvailableAt, utcNow())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func nullableString(v any) any {
	switch t := v.(type) {
	case sql.NullString:
		if t.Valid {
			return t.String
		}
		return nil
	case string:
		if t == "" {
			return nil
		}
		return t
	default:
		return v
	}
}

func nullableInt(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func nullableFloat(v sql.NullFloat64) any {
	if v.Valid {
		return v.Float64
	}
	return nil
}

func parseDate(value string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", value, time.UTC)
}

func durationDays(start, end string) int {
	s, err1 := parseDate(start)
	e, err2 := parseDate(end)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(e.Sub(s).Hours()/24) + 1
}

type coreLib struct {
	handle           unsafe.Pointer
	statusName       unsafe.Pointer
	modelRunV1       unsafe.Pointer
	modelFreeV1      unsafe.Pointer
	modelRunV2       unsafe.Pointer
	modelFreeV2      unsafe.Pointer
	riskCheckV1      unsafe.Pointer
	riskOutputFreeV1 unsafe.Pointer
}

func loadCore(path string) (*coreLib, error) {
	if path == "" {
		return nil, fail(2, "--core-lib is required")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fail(2, "core library not found: %s; run `make build` first", path)
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	handle := C.sec_dlopen(cpath)
	if handle == nil {
		return nil, fail(2, "failed to load core library: %s", cString(C.sec_dlerror()))
	}
	load := func(name string) (unsafe.Pointer, error) {
		cname := C.CString(name)
		defer C.free(unsafe.Pointer(cname))
		ptr := C.sec_dlsym(handle, cname)
		if ptr == nil {
			return nil, fail(2, "missing core symbol %s: %s", name, cString(C.sec_dlerror()))
		}
		return ptr, nil
	}
	var err error
	lib := &coreLib{handle: handle}
	if lib.statusName, err = load("fa_status_name"); err != nil {
		return nil, err
	}
	if lib.modelRunV1, err = load("fa_model_run_v1"); err != nil {
		return nil, err
	}
	if lib.modelFreeV1, err = load("fa_model_output_free_v1"); err != nil {
		return nil, err
	}
	if lib.modelRunV2, err = load("fa_model_run_v2"); err != nil {
		return nil, err
	}
	if lib.modelFreeV2, err = load("fa_model_output_free_v2"); err != nil {
		return nil, err
	}
	if lib.riskCheckV1, err = load("fa_risk_check_v1"); err != nil {
		return nil, err
	}
	if lib.riskOutputFreeV1, err = load("fa_risk_output_free_v1"); err != nil {
		return nil, err
	}
	return lib, nil
}

func (c *coreLib) status(code C.fa_status_code) string {
	raw := C.sec_status_name(c.statusName, code)
	if raw == nil {
		return fmt.Sprintf("status_%d", int(code))
	}
	return C.GoString(raw)
}

func cString(raw *C.char) string {
	if raw == nil {
		return ""
	}
	return C.GoString(raw)
}

func cCharArrayString(ptr unsafe.Pointer, n int) string {
	b := C.GoBytes(ptr, C.int(n))
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func setCCharArray(dst *C.char, n int, value string) {
	bytes := []byte(value)
	if len(bytes) > n-1 {
		bytes = bytes[:n-1]
	}
	for i := 0; i < n; i++ {
		*(*C.char)(unsafe.Add(unsafe.Pointer(dst), i)) = 0
	}
	for i, b := range bytes {
		*(*C.char)(unsafe.Add(unsafe.Pointer(dst), i)) = C.char(b)
	}
}

func cmdModelRun(args []string) error {
	fs := modelFlagSet("model-run")
	if err := parseFlags(fs.fs, args); err != nil {
		return err
	}
	db, err := openDB(fs.db)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return err
	}
	core, err := loadCore(fs.coreLib)
	if err != nil {
		return err
	}
	facts, err := loadFacts(db)
	if err != nil {
		return err
	}
	securities, _, err := loadSecurities(db)
	if err != nil {
		return err
	}
	positions, err := loadPositions(db)
	if err != nil {
		return err
	}
	config := C.fa_model_config_v1{
		abi_version:                   abiVersion,
		target_gross_exposure_ratio:   C.double(fs.targetGrossExposure),
		max_name_weight_ratio:         C.double(fs.maxNameWeight),
		min_expected_return_proxy:     C.double(fs.minExpectedReturn),
		min_abs_order_notional_usd:    C.double(fs.minAbsOrderNotional),
		max_forecast_abs_growth_ratio: C.double(fs.maxForecastAbsGrowth),
		max_fact_age_s:                C.int64_t(int64(fs.maxFactAgeDays * 86400)),
	}
	limits, err := makeRiskLimits(db, fs.riskLimitArgs)
	if err != nil {
		return err
	}
	var out C.fa_model_output_v1
	status := C.sec_model_run_v1(core.modelRunV1,
		factPtr(facts), C.size_t(len(facts)),
		securityPtr(securities), C.size_t(len(securities)),
		positionPtr(positions), C.size_t(len(positions)),
		&config, &limits, &out)
	diagnostics := cCharArrayString(unsafe.Pointer(&out.diagnostics[0]), diagnosticBytes)
	if status != C.FA_OK {
		C.sec_model_output_free_v1(core.modelFreeV1, &out)
		return fail(5, "fa_model_run_v1 failed: %s: %s", core.status(status), diagnostics)
	}
	defer C.sec_model_output_free_v1(core.modelFreeV1, &out)
	runID := uuidV4()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	configJSON := mustCanonicalJSON(map[string]any{
		"target_gross_exposure_ratio": fs.targetGrossExposure, "max_name_weight_ratio": fs.maxNameWeight,
		"min_expected_return_proxy": fs.minExpectedReturn, "min_abs_order_notional_usd": fs.minAbsOrderNotional,
		"max_forecast_abs_growth_ratio": fs.maxForecastAbsGrowth, "max_fact_age_days": fs.maxFactAgeDays, "core_lib": fs.coreLib,
	})
	if _, err := tx.Exec(`INSERT INTO model_runs(run_id, occurred_at, config_json, diagnostics) VALUES (?, ?, ?, ?)`, runID, utcNow(), configJSON, diagnostics); err != nil {
		return err
	}
	forecasts := unsafe.Slice(out.forecasts, int(out.forecast_count))
	for _, f := range forecasts {
		_, err := tx.Exec(`INSERT INTO forecasts(run_id, security_id, revenue_growth_ratio, earnings_growth_ratio, expected_return_proxy, confidence_ratio, quality_flags, reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, runID, uint64(f.security_id), float64(f.revenue_growth_ratio), float64(f.earnings_growth_ratio), float64(f.expected_return_proxy), float64(f.confidence_ratio), uint32(f.quality_flags), cCharArrayString(unsafe.Pointer(&f.reason[0]), reasonBytes))
		if err != nil {
			return err
		}
	}
	targets := unsafe.Slice(out.target_weights, int(out.target_weight_count))
	if err := persistTargets(tx, runID, targets); err != nil {
		return err
	}
	intents := unsafe.Slice(out.order_intents, int(out.order_intent_count))
	if err := persistIntents(tx, runID, intents); err != nil {
		return err
	}
	event, err := appendEvent(tx, "model_run_completed", map[string]any{
		"run_id": runID, "forecast_count": int(out.forecast_count), "target_weight_count": int(out.target_weight_count),
		"order_intent_count": int(out.order_intent_count), "diagnostics": diagnostics,
	})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return jsonLine(event)
}

type modelFlags struct {
	fs                   *flag.FlagSet
	db                   string
	coreLib              string
	targetGrossExposure  float64
	maxNameWeight        float64
	minExpectedReturn    float64
	minAbsOrderNotional  float64
	maxForecastAbsGrowth float64
	maxFactAgeDays       float64
	maxStatementAgeDays  float64
	forecastHorizonDays  int
	operatorLabel        string
	assumptionJSON       string
	assumptionFile       string
	assumptionSetID      string
	riskLimitArgs
}

type riskLimitArgs struct {
	portfolioValue          float64
	cash                    float64
	maxOrderNotional        float64
	minADV                  float64
	maxADVParticipation     float64
	maxReconciliationAgeSec int64
}

func modelFlagSet(name string) *modelFlags {
	fs := newFlagSet(name)
	m := &modelFlags{fs: fs}
	fs.StringVar(&m.db, "db", "", "")
	fs.StringVar(&m.coreLib, "core-lib", "", "")
	fs.Float64Var(&m.portfolioValue, "portfolio-value-usd", 0, "")
	fs.Float64Var(&m.cash, "cash-usd", 0, "")
	fs.Float64Var(&m.maxNameWeight, "max-name-weight-ratio", 0.05, "")
	fs.Float64Var(&m.targetGrossExposure, "target-gross-exposure-ratio", 0.50, "")
	fs.Float64Var(&m.minExpectedReturn, "min-expected-return-proxy", 0.05, "")
	fs.Float64Var(&m.minAbsOrderNotional, "min-abs-order-notional-usd", 100, "")
	fs.Float64Var(&m.maxForecastAbsGrowth, "max-forecast-abs-growth-ratio", 2.0, "")
	fs.Float64Var(&m.maxFactAgeDays, "max-fact-age-days", 540, "")
	fs.Float64Var(&m.maxStatementAgeDays, "max-statement-age-days", 540, "")
	fs.Float64Var(&m.maxOrderNotional, "max-order-notional-usd", 10000, "")
	fs.Float64Var(&m.minADV, "min-adv-usd", 1000000, "")
	fs.Float64Var(&m.maxADVParticipation, "max-adv-participation-ratio", 0.01, "")
	fs.Int64Var(&m.maxReconciliationAgeSec, "max-reconciliation-age-s", 3600, "")
	fs.IntVar(&m.forecastHorizonDays, "forecast-horizon-days", 90, "")
	fs.StringVar(&m.operatorLabel, "operator-label", "default", "")
	fs.StringVar(&m.assumptionJSON, "assumption-json", "", "")
	fs.StringVar(&m.assumptionFile, "assumption-file", "", "")
	fs.StringVar(&m.assumptionSetID, "assumption-set-id", "", "")
	return m
}

func loadSecurities(db *sql.DB) ([]C.fa_security_v1, []securityRow, error) {
	rows, err := db.Query(`SELECT security_id, symbol, investable, price_usd, adv_usd FROM securities ORDER BY security_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []C.fa_security_v1
	var raw []securityRow
	for rows.Next() {
		var r securityRow
		if err := rows.Scan(&r.id, &r.symbol, &r.investable, &r.price, &r.adv); err != nil {
			return nil, nil, err
		}
		var sec C.fa_security_v1
		sec.abi_version = abiVersion
		sec.security_id = C.uint64_t(r.id)
		setCCharArray(&sec.symbol[0], 16, r.symbol)
		sec.investable = C.uint8_t(r.investable)
		sec.price_usd = C.double(r.price)
		sec.adv_usd = C.double(r.adv)
		out = append(out, sec)
		raw = append(raw, r)
	}
	return out, raw, rows.Err()
}

type securityRow struct {
	id         uint64
	symbol     string
	investable int
	price      float64
	adv        float64
}

func loadPositions(db *sql.DB) ([]C.fa_position_v1, error) {
	rows, err := db.Query(`SELECT security_id, quantity_shares, market_value_usd, weight_ratio FROM positions ORDER BY security_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []C.fa_position_v1
	for rows.Next() {
		var p C.fa_position_v1
		p.abi_version = abiVersion
		if err := rows.Scan(&p.security_id, &p.quantity_shares, &p.market_value_usd, &p.weight_ratio); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func loadFacts(db *sql.DB) ([]C.fa_canonical_fact_v1, error) {
	rows, err := db.Query(`SELECT co.security_id, co.metric_id, co.value_decimal, rp.raw_start_date, rp.raw_end_date, rp.raw_instant_date,
co.available_at, co.quality_flags, co.metric_kind, co.period_semantics, co.basis_id, co.observation_status, co.duration_days, co.dimensions_hash
FROM canonical_observations co
JOIN reporting_periods rp ON rp.period_id = co.period_id
WHERE co.observation_status IN ('selected', 'derived') AND co.dimensional_scope = 'consolidated_total'
ORDER BY co.security_id, co.metric_id, rp.raw_end_date, co.available_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []C.fa_canonical_fact_v1
	for rows.Next() {
		var securityID uint64
		var metricID int32
		var value float64
		var start, end, instant sql.NullString
		var available string
		var quality uint32
		var metricKind, periodSemantics, status, dimHash string
		var basis, duration uint32
		if err := rows.Scan(&securityID, &metricID, &value, &start, &end, &instant, &available, &quality, &metricKind, &periodSemantics, &basis, &status, &duration, &dimHash); err != nil {
			return nil, err
		}
		startDate := nullableString(start)
		endDate := nullableString(end)
		if startDate == nil {
			startDate = nullableString(instant)
		}
		if endDate == nil {
			endDate = nullableString(instant)
		}
		f := C.fa_canonical_fact_v1{
			abi_version:          abiVersion,
			security_id:          C.uint64_t(securityID),
			metric_id:            C.int32_t(metricID),
			value:                C.double(value),
			period_start_day:     C.int64_t(epochDay(fmt.Sprint(startDate))),
			period_end_day:       C.int64_t(epochDay(fmt.Sprint(endDate))),
			available_at_epoch_s: C.int64_t(epochSecond(available)),
			quality_flags:        C.uint32_t(quality),
			metric_kind:          C.uint32_t(metricKindCodes[metricKind]),
			period_semantics:     C.uint32_t(periodCodes[periodSemantics]),
			basis_id:             C.uint32_t(basis),
			observation_status:   C.uint32_t(map[string]uint32{"selected": 1, "derived": 2}[status]),
			duration_days:        C.uint32_t(duration),
			dimensions_hash:      C.uint64_t(hash64(dimHash)),
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func makeRiskLimits(db *sql.DB, args riskLimitArgs) (C.fa_risk_limits_v1, error) {
	var reconciled int
	var checkedAt string
	var portfolio, cash float64
	err := db.QueryRow(`SELECT reconciled, checked_at, portfolio_value_usd, cash_usd FROM broker_state WHERE id=1`).Scan(&reconciled, &checkedAt, &portfolio, &cash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return C.fa_risk_limits_v1{}, err
	}
	if args.portfolioValue > 0 {
		portfolio = args.portfolioValue
	}
	if args.cash > 0 {
		cash = args.cash
	}
	limits := C.fa_risk_limits_v1{
		abi_version:                       abiVersion,
		portfolio_value_usd:               C.double(portfolio),
		cash_usd:                          C.double(cash),
		max_name_weight_ratio:             0.05,
		max_order_notional_usd:            C.double(args.maxOrderNotional),
		min_adv_usd:                       C.double(args.minADV),
		max_adv_participation_ratio:       C.double(args.maxADVParticipation),
		min_abs_order_notional_usd:        100,
		broker_reconciled:                 C.uint8_t(reconciled),
		reconciliation_checked_at_epoch_s: C.int64_t(epochSecond(checkedAt)),
		now_epoch_s:                       C.int64_t(time.Now().UTC().Unix()),
		max_reconciliation_age_s:          C.int64_t(args.maxReconciliationAgeSec),
	}
	return limits, nil
}

func factPtr(v []C.fa_canonical_fact_v1) *C.fa_canonical_fact_v1 {
	if len(v) == 0 {
		return nil
	}
	return &v[0]
}
func securityPtr(v []C.fa_security_v1) *C.fa_security_v1 {
	if len(v) == 0 {
		return nil
	}
	return &v[0]
}
func positionPtr(v []C.fa_position_v1) *C.fa_position_v1 {
	if len(v) == 0 {
		return nil
	}
	return &v[0]
}
func statementPtr(v []C.fa_statement_snapshot_v1) *C.fa_statement_snapshot_v1 {
	if len(v) == 0 {
		return nil
	}
	return &v[0]
}
func scenarioPtr(v []C.fa_valuation_scenario_v1) *C.fa_valuation_scenario_v1 {
	if len(v) == 0 {
		return nil
	}
	return &v[0]
}
func intentPtr(v []C.fa_order_intent_v1) *C.fa_order_intent_v1 {
	if len(v) == 0 {
		return nil
	}
	return &v[0]
}

func epochDay(date string) int64 {
	t, err := parseDate(date)
	if err != nil {
		return 0
	}
	return t.Unix() / 86400
}

func epochSecond(value string) int64 {
	if value == "" {
		return 0
	}
	layouts := []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z", "2006-01-02"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func hash64(hexString string) uint64 {
	if len(hexString) < 16 {
		return 0
	}
	v, _ := strconv.ParseUint(hexString[:16], 16, 64)
	return v
}

func persistTargets(tx execer, runID string, targets []C.fa_target_weight_v1) error {
	for _, t := range targets {
		_, err := tx.Exec(`INSERT INTO target_weights(run_id, security_id, current_weight_ratio, target_weight_ratio, delta_weight_ratio, reason)
VALUES (?, ?, ?, ?, ?, ?)`, runID, uint64(t.security_id), float64(t.current_weight_ratio), float64(t.target_weight_ratio), float64(t.delta_weight_ratio), cCharArrayString(unsafe.Pointer(&t.reason[0]), reasonBytes))
		if err != nil {
			return err
		}
	}
	return nil
}

func persistIntents(tx execer, runID string, intents []C.fa_order_intent_v1) error {
	for _, oi := range intents {
		_, err := tx.Exec(`INSERT INTO order_intents(intent_id, run_id, security_id, side, notional_usd, current_weight_ratio, target_weight_ratio, reason, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, uuidV4(), runID, uint64(oi.security_id), sideText(int32(oi.side)), float64(oi.notional_usd), float64(oi.current_weight_ratio), float64(oi.target_weight_ratio), cCharArrayString(unsafe.Pointer(&oi.reason[0]), reasonBytes), utcNow())
		if err != nil {
			return err
		}
	}
	return nil
}

func sideText(side int32) string {
	switch side {
	case 1:
		return "buy"
	case 2:
		return "sell"
	default:
		return "none"
	}
}

func sideCode(side string) int32 {
	switch side {
	case "buy":
		return 1
	case "sell":
		return 2
	default:
		return 0
	}
}

type statementSnapshot struct {
	id                  string
	securityID          uint64
	periodStartDate     sql.NullString
	periodEndDate       string
	availableAt         string
	revenue, netIncome  float64
	eps, shares         float64
	ocf, capex, fcf     float64
	cash, debt, netDebt float64
	qualityFlags        uint32
	sourceHash          string
}

func cmdModelRunV2(args []string) error {
	fs := modelFlagSet("model-run-v2")
	if err := parseFlags(fs.fs, args); err != nil {
		return err
	}
	db, err := openDB(fs.db)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return err
	}
	core, err := loadCore(fs.coreLib)
	if err != nil {
		return err
	}
	assumptionID, scenarios, assumptionHash, err := ensureAssumptions(db, fs.operatorLabel)
	if err != nil {
		return err
	}
	statements, err := buildLatestStatements(db)
	if err != nil {
		return err
	}
	cStatements := make([]C.fa_statement_snapshot_v1, 0, len(statements))
	for _, s := range statements {
		startDate := ""
		if s.periodStartDate.Valid {
			startDate = s.periodStartDate.String
		}
		cStatements = append(cStatements, C.fa_statement_snapshot_v1{
			abi_version:             abiVersion,
			security_id:             C.uint64_t(s.securityID),
			period_start_day:        C.int64_t(epochDay(startDate)),
			period_end_day:          C.int64_t(epochDay(s.periodEndDate)),
			available_at_epoch_s:    C.int64_t(epochSecond(s.availableAt)),
			is_ttm:                  1,
			revenue_usd:             C.double(s.revenue),
			net_income_usd:          C.double(s.netIncome),
			diluted_eps_usd:         C.double(s.eps),
			diluted_shares:          C.double(s.shares),
			operating_cash_flow_usd: C.double(s.ocf),
			capex_usd:               C.double(s.capex),
			free_cash_flow_usd:      C.double(s.fcf),
			cash_usd:                C.double(s.cash),
			debt_usd:                C.double(s.debt),
			net_debt_usd:            C.double(s.netDebt),
			quality_flags:           C.uint32_t(s.qualityFlags),
		})
	}
	securities, _, err := loadSecurities(db)
	if err != nil {
		return err
	}
	positions, err := loadPositions(db)
	if err != nil {
		return err
	}
	config := C.fa_model_config_v1{
		abi_version:                   abiVersion,
		target_gross_exposure_ratio:   C.double(fs.targetGrossExposure),
		max_name_weight_ratio:         C.double(fs.maxNameWeight),
		min_expected_return_proxy:     C.double(fs.minExpectedReturn),
		min_abs_order_notional_usd:    C.double(fs.minAbsOrderNotional),
		max_forecast_abs_growth_ratio: C.double(fs.maxForecastAbsGrowth),
		max_fact_age_s:                C.int64_t(int64(fs.maxStatementAgeDays * 86400)),
	}
	limits, err := makeRiskLimits(db, fs.riskLimitArgs)
	if err != nil {
		return err
	}
	var out C.fa_model_output_v2
	status := C.sec_model_run_v2(core.modelRunV2,
		statementPtr(cStatements), C.size_t(len(cStatements)),
		securityPtr(securities), C.size_t(len(securities)),
		positionPtr(positions), C.size_t(len(positions)),
		scenarioPtr(scenarios), C.size_t(len(scenarios)),
		&config, &limits, &out)
	diagnostics := cCharArrayString(unsafe.Pointer(&out.diagnostics[0]), diagnosticBytes)
	if status != C.FA_OK {
		C.sec_model_output_free_v2(core.modelFreeV2, &out)
		return fail(5, "fa_model_run_v2 failed: %s: %s", core.status(status), diagnostics)
	}
	defer C.sec_model_output_free_v2(core.modelFreeV2, &out)
	runID := uuidV4()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	insertedStatements := 0
	for _, s := range statements {
		res, err := tx.Exec(`INSERT OR IGNORE INTO statement_snapshots(
snapshot_id, security_id, period_start_date, period_end_date, available_at, is_ttm,
revenue_usd, net_income_usd, diluted_eps_usd, diluted_shares, operating_cash_flow_usd,
capex_usd, free_cash_flow_usd, cash_usd, debt_usd, net_debt_usd, quality_flags, source_hash, inserted_at)
VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.id, s.securityID, nullableString(s.periodStartDate), s.periodEndDate, s.availableAt,
			s.revenue, s.netIncome, s.eps, s.shares, s.ocf, s.capex, s.fcf, s.cash, s.debt, s.netDebt, s.qualityFlags, s.sourceHash, utcNow())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		insertedStatements += int(n)
	}
	configJSON := mustCanonicalJSON(map[string]any{
		"model_version": "valuation_v2", "target_gross_exposure_ratio": fs.targetGrossExposure,
		"max_name_weight_ratio": fs.maxNameWeight, "assumption_set_id": assumptionID,
		"assumption_sha256": assumptionHash, "core_lib": fs.coreLib,
	})
	if _, err := tx.Exec(`INSERT INTO model_runs(run_id, occurred_at, config_json, diagnostics) VALUES (?, ?, ?, ?)`, runID, utcNow(), configJSON, diagnostics); err != nil {
		return err
	}
	vals := unsafe.Slice(out.valuations, int(out.valuation_count))
	for _, v := range vals {
		_, err := tx.Exec(`INSERT INTO valuations(run_id, security_id, current_price_usd, intrinsic_value_per_share_usd,
expected_return_ratio, market_cap_usd, enterprise_value_usd, fcf_yield_ratio, net_debt_usd, confidence_ratio, quality_flags, reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			runID, uint64(v.security_id), float64(v.current_price_usd), float64(v.intrinsic_value_per_share_usd),
			float64(v.expected_return_ratio), float64(v.market_cap_usd), float64(v.enterprise_value_usd),
			float64(v.fcf_yield_ratio), float64(v.net_debt_usd), float64(v.confidence_ratio), uint32(v.quality_flags),
			cCharArrayString(unsafe.Pointer(&v.reason[0]), reasonBytes))
		if err != nil {
			return err
		}
		_, _ = tx.Exec(`INSERT INTO forecast_outcomes(forecast_id, run_id, security_id, forecast_made_at, horizon_days, expected_return_ratio, confidence_ratio, price_at_forecast_usd)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, uuidV4(), runID, uint64(v.security_id), utcNow(), fs.forecastHorizonDays, float64(v.expected_return_ratio), float64(v.confidence_ratio), float64(v.current_price_usd))
	}
	if err := persistTargets(tx, runID, unsafe.Slice(out.target_weights, int(out.target_weight_count))); err != nil {
		return err
	}
	if err := persistIntents(tx, runID, unsafe.Slice(out.order_intents, int(out.order_intent_count))); err != nil {
		return err
	}
	event, err := appendEvent(tx, "model_run_v2_completed", map[string]any{
		"run_id": runID, "statement_count": len(statements), "statement_snapshots_inserted": insertedStatements,
		"valuation_count": int(out.valuation_count), "target_weight_count": int(out.target_weight_count),
		"order_intent_count": int(out.order_intent_count), "forecast_outcome_count": int(out.valuation_count),
		"assumption_set_id": assumptionID, "model_input_sha256": sha256Hex([]byte(configJSON)),
		"autonomy_approved": false, "autonomy_reason": "trading_mode=observe blocks autonomous staging",
		"diagnostics": diagnostics,
	})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return jsonLine(event)
}

func ensureAssumptions(db *sql.DB, operator string) (string, []C.fa_valuation_scenario_v1, string, error) {
	assumptions := map[string]any{
		"scenario_count": 3,
		"scenarios": []map[string]any{
			{"name": "bear", "probability_weight": 0.25, "revenue_cagr_5y": -0.02, "terminal_revenue_growth": 0.00, "target_fcf_margin": 0.06, "discount_rate": 0.12, "terminal_fcf_multiple": 10.0},
			{"name": "base", "probability_weight": 0.50, "revenue_cagr_5y": 0.04, "terminal_revenue_growth": 0.02, "target_fcf_margin": 0.12, "discount_rate": 0.10, "terminal_fcf_multiple": 16.0},
			{"name": "bull", "probability_weight": 0.25, "revenue_cagr_5y": 0.10, "terminal_revenue_growth": 0.03, "target_fcf_margin": 0.18, "discount_rate": 0.09, "terminal_fcf_multiple": 22.0},
		},
	}
	jsonText := mustCanonicalJSON(assumptions)
	hash := sha256Hex([]byte(jsonText))
	id := uuidV4()
	if _, err := db.Exec(`INSERT INTO model_assumptions(assumption_set_id, created_at, assumption_json, assumption_sha256, operator_label)
VALUES (?, ?, ?, ?, ?)`, id, utcNow(), jsonText, hash, operator); err != nil {
		return "", nil, "", err
	}
	scenarios := []C.fa_valuation_scenario_v1{
		{abi_version: abiVersion, revenue_cagr_5y: -0.02, terminal_revenue_growth: 0, target_fcf_margin: 0.06, discount_rate: 0.12, terminal_fcf_multiple: 10, probability_weight: 0.25},
		{abi_version: abiVersion, revenue_cagr_5y: 0.04, terminal_revenue_growth: 0.02, target_fcf_margin: 0.12, discount_rate: 0.10, terminal_fcf_multiple: 16, probability_weight: 0.50},
		{abi_version: abiVersion, revenue_cagr_5y: 0.10, terminal_revenue_growth: 0.03, target_fcf_margin: 0.18, discount_rate: 0.09, terminal_fcf_multiple: 22, probability_weight: 0.25},
	}
	_ = defaultScenarioAssumptionID
	return id, scenarios, hash, nil
}

func buildLatestStatements(db *sql.DB) ([]statementSnapshot, error) {
	rows, err := db.Query(`SELECT security_id FROM securities WHERE investable=1 ORDER BY security_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []statementSnapshot
	for rows.Next() {
		var securityID uint64
		if err := rows.Scan(&securityID); err != nil {
			return nil, err
		}
		ends, err := candidateEndDates(db, securityID)
		if err != nil {
			return nil, err
		}
		count := 0
		for _, end := range ends {
			s, ok, err := buildStatement(db, securityID, end)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, s)
				count++
				if count >= 2 {
					break
				}
			}
		}
	}
	return out, rows.Err()
}

func candidateEndDates(db *sql.DB, securityID uint64) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT rp.raw_end_date
FROM canonical_observations co JOIN reporting_periods rp ON rp.period_id=co.period_id
WHERE co.security_id=? AND co.metric_id=? AND co.observation_status IN ('selected','derived')
AND co.dimensional_scope='consolidated_total' AND co.period_semantics IN ('fiscal_quarter','fiscal_year')
ORDER BY rp.raw_end_date DESC LIMIT 8`, securityID, metricRevenue)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var end string
		if err := rows.Scan(&end); err != nil {
			return nil, err
		}
		out = append(out, end)
	}
	return out, rows.Err()
}

func buildStatement(db *sql.DB, securityID uint64, anchor string) (statementSnapshot, bool, error) {
	revenue, _ := latestFlowTTM(db, securityID, metricRevenue, anchor)
	netIncome, _ := latestFlowTTM(db, securityID, metricNetIncome, anchor)
	eps, _ := latestFlowTTM(db, securityID, metricEPSDiluted, anchor)
	ocf, _ := latestFlowTTM(db, securityID, metricOperatingCashFlow, anchor)
	capex, _ := latestFlowTTM(db, securityID, metricCapex, anchor)
	shares, _ := latestPoint(db, securityID, metricDilutedShares, anchor, "'fiscal_quarter','fiscal_year','fiscal_ytd'")
	cash, _ := latestPoint(db, securityID, metricCash, anchor, "'instant'")
	debt, _ := latestPoint(db, securityID, metricDebt, anchor, "'instant'")
	if !revenue.ok && !netIncome.ok && !ocf.ok {
		return statementSnapshot{}, false, nil
	}
	quality := uint32(0)
	if !revenue.ok || revenue.value <= 0 {
		quality |= stmtMissingRevenue | stmtLowConfidence
	}
	if !shares.ok || shares.value <= 0 {
		quality |= stmtMissingShares | stmtLowConfidence
	}
	if !cash.ok {
		quality |= stmtMissingCash
	}
	if !debt.ok {
		quality |= stmtMissingDebt
	}
	fcf := ocf.value - capex.value
	if fcf < 0 {
		quality |= stmtNegativeFCF
	}
	sourceHash := sha256Hex([]byte(mustCanonicalJSON([]string{revenue.source, netIncome.source, eps.source, shares.source, ocf.source, capex.source, cash.source, debt.source})))
	start := revenue.start
	if start == "" {
		start = anchor
	}
	s := statementSnapshot{
		securityID: securityID, periodStartDate: sql.NullString{String: start, Valid: start != ""},
		periodEndDate: anchor, availableAt: maxString(revenue.available, netIncome.available, eps.available, shares.available, ocf.available, capex.available, cash.available, debt.available),
		revenue: revenue.value, netIncome: netIncome.value, eps: eps.value, shares: shares.value,
		ocf: ocf.value, capex: capex.value, fcf: fcf, cash: cash.value, debt: debt.value, netDebt: debt.value - cash.value,
		qualityFlags: quality, sourceHash: sourceHash,
	}
	s.id = stableID("stmt", map[string]any{"security_id": securityID, "period_end_date": s.periodEndDate, "available_at": s.availableAt, "source_hash": s.sourceHash})
	return s, true, nil
}

type metricPoint struct {
	ok        bool
	value     float64
	start     string
	end       string
	available string
	source    string
}

func latestFlowTTM(db *sql.DB, securityID uint64, metricID int, anchor string) (metricPoint, error) {
	rows, err := db.Query(`SELECT co.observation_id, co.value_decimal, co.available_at, rp.raw_start_date, rp.raw_end_date, co.period_semantics, co.duration_days
FROM canonical_observations co JOIN reporting_periods rp ON rp.period_id=co.period_id
WHERE co.security_id=? AND co.metric_id=? AND co.observation_status IN ('selected','derived') AND co.dimensional_scope='consolidated_total'
AND co.period_semantics IN ('fiscal_quarter','fiscal_year') AND rp.raw_end_date <= ?
ORDER BY rp.raw_end_date DESC, co.available_at DESC`, securityID, metricID, anchor)
	if err != nil {
		return metricPoint{}, err
	}
	defer rows.Close()
	var quarters []metricPoint
	for rows.Next() {
		var p metricPoint
		var sem string
		var dur int
		if err := rows.Scan(&p.source, &p.value, &p.available, &p.start, &p.end, &sem, &dur); err != nil {
			return metricPoint{}, err
		}
		p.ok = true
		if sem == "fiscal_quarter" && dur >= 70 && dur <= 110 {
			quarters = append(quarters, p)
		}
		if sem == "fiscal_year" && dur >= 330 {
			return p, nil
		}
	}
	if len(quarters) >= 4 {
		sum := 0.0
		start := quarters[0].start
		available := ""
		var sources []string
		for _, q := range quarters[:4] {
			sum += q.value
			if q.start < start {
				start = q.start
			}
			if q.available > available {
				available = q.available
			}
			sources = append(sources, q.source)
		}
		return metricPoint{ok: true, value: sum, start: start, end: quarters[0].end, available: available, source: strings.Join(sources, ",")}, nil
	}
	return metricPoint{}, nil
}

func latestPoint(db *sql.DB, securityID uint64, metricID int, anchor, semantics string) (metricPoint, error) {
	row := db.QueryRow(fmt.Sprintf(`SELECT co.observation_id, co.value_decimal, co.available_at, COALESCE(rp.raw_start_date, rp.raw_instant_date), COALESCE(rp.raw_end_date, rp.raw_instant_date)
FROM canonical_observations co JOIN reporting_periods rp ON rp.period_id=co.period_id
WHERE co.security_id=? AND co.metric_id=? AND co.observation_status IN ('selected','derived') AND co.dimensional_scope='consolidated_total'
AND co.period_semantics IN (%s) AND COALESCE(rp.raw_end_date, rp.raw_instant_date) <= ?
ORDER BY COALESCE(rp.raw_end_date, rp.raw_instant_date) DESC, co.available_at DESC LIMIT 1`, semantics), securityID, metricID, anchor)
	var p metricPoint
	err := row.Scan(&p.source, &p.value, &p.available, &p.start, &p.end)
	if errors.Is(err, sql.ErrNoRows) {
		return metricPoint{}, nil
	}
	if err != nil {
		return metricPoint{}, err
	}
	p.ok = true
	return p, nil
}

func maxString(values ...string) string {
	out := ""
	for _, v := range values {
		if v > out {
			out = v
		}
	}
	if out == "" {
		return utcNow()
	}
	return out
}

func cmdRiskCheck(args []string) error {
	fs := newFlagSet("risk-check")
	dbPath := fs.String("db", "", "")
	corePath := fs.String("core-lib", "", "")
	runID := fs.String("run-id", "", "")
	rl := riskLimitArgs{}
	fs.Float64Var(&rl.portfolioValue, "portfolio-value-usd", 0, "")
	fs.Float64Var(&rl.cash, "cash-usd", 0, "")
	maxNameWeight := fs.Float64("max-name-weight-ratio", 0.05, "")
	fs.Float64Var(&rl.maxOrderNotional, "max-order-notional-usd", 10000, "")
	fs.Float64Var(&rl.minADV, "min-adv-usd", 1000000, "")
	fs.Float64Var(&rl.maxADVParticipation, "max-adv-participation-ratio", 0.01, "")
	fs.Int64Var(&rl.maxReconciliationAgeSec, "max-reconciliation-age-s", 3600, "")
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
	core, err := loadCore(*corePath)
	if err != nil {
		return err
	}
	intents, ids, err := loadPendingIntents(db, *runID)
	if err != nil {
		return err
	}
	securities, _, err := loadSecurities(db)
	if err != nil {
		return err
	}
	limits, err := makeRiskLimits(db, rl)
	if err != nil {
		return err
	}
	limits.max_name_weight_ratio = C.double(*maxNameWeight)
	var out C.fa_risk_output_v1
	status := C.sec_risk_check_v1(core.riskCheckV1, intentPtr(intents), C.size_t(len(intents)), securityPtr(securities), C.size_t(len(securities)), &limits, &out)
	diagnostics := cCharArrayString(unsafe.Pointer(&out.diagnostics[0]), diagnosticBytes)
	if status != C.FA_OK {
		C.sec_risk_output_free_v1(core.riskOutputFreeV1, &out)
		return fail(5, "fa_risk_check_v1 failed: %s: %s", core.status(status), diagnostics)
	}
	defer C.sec_risk_output_free_v1(core.riskOutputFreeV1, &out)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	decisions := unsafe.Slice(out.decisions, int(out.decision_count))
	approved := 0
	for i, d := range decisions {
		isApproved := int32(d.decision) == 1
		if isApproved {
			approved++
		}
		intentID := ""
		if i < len(ids) {
			intentID = ids[i]
		}
		_, err := tx.Exec(`INSERT INTO risk_decisions(decision_id, intent_id, security_id, approved, reason, notional_usd, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, uuidV4(), intentID, uint64(d.security_id), boolInt(isApproved), cCharArrayString(unsafe.Pointer(&d.reason[0]), reasonBytes), float64(d.notional_usd), utcNow())
		if err != nil {
			return err
		}
	}
	event, err := appendEvent(tx, "risk_check_completed", map[string]any{
		"intent_count": len(decisions), "approved_count": approved, "rejected_count": len(decisions) - approved, "diagnostics": diagnostics,
	})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return jsonLine(event)
}

func loadPendingIntents(db *sql.DB, runID string) ([]C.fa_order_intent_v1, []string, error) {
	query := `SELECT oi.intent_id, oi.security_id, oi.side, oi.notional_usd, oi.current_weight_ratio, oi.target_weight_ratio, oi.reason
FROM order_intents oi LEFT JOIN risk_decisions rd ON rd.intent_id=oi.intent_id
WHERE rd.intent_id IS NULL`
	var args []any
	if runID != "" {
		query += " AND oi.run_id=?"
		args = append(args, runID)
	}
	query += " ORDER BY oi.created_at"
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []C.fa_order_intent_v1
	var ids []string
	for rows.Next() {
		var id, side, reason string
		var securityID uint64
		var notional, current, target float64
		if err := rows.Scan(&id, &securityID, &side, &notional, &current, &target, &reason); err != nil {
			return nil, nil, err
		}
		var oi C.fa_order_intent_v1
		oi.abi_version = abiVersion
		oi.security_id = C.uint64_t(securityID)
		oi.side = C.int32_t(sideCode(side))
		oi.notional_usd = C.double(notional)
		oi.current_weight_ratio = C.double(current)
		oi.target_weight_ratio = C.double(target)
		setCCharArray(&oi.reason[0], reasonBytes, reason)
		out = append(out, oi)
		ids = append(ids, id)
	}
	return out, ids, rows.Err()
}

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
