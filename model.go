package main

/*
#cgo CFLAGS: -I${SRCDIR}/include
#include "core.h"

typedef fa_status_code (*fa_model_run_v1_fn)(
    const fa_canonical_fact_v1*, size_t,
    const fa_security_v1*, size_t,
    const fa_position_v1*, size_t,
    const fa_model_config_v1*,
    const fa_risk_limits_v1*,
    fa_model_output_v1*);
typedef void (*fa_model_output_free_v1_fn)(fa_model_output_v1*);

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
*/
import "C"

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"time"
	"unsafe"
)

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
