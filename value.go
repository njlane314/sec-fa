package main

/*
#cgo CFLAGS: -I${SRCDIR}/include
#include "core.h"

typedef fa_status_code (*fa_model_run_v2_fn)(
    const fa_statement_snapshot_v1*, size_t,
    const fa_security_v1*, size_t,
    const fa_position_v1*, size_t,
    const fa_valuation_scenario_v1*, size_t,
    const fa_model_config_v1*,
    const fa_risk_limits_v1*,
    fa_model_output_v2*);
typedef void (*fa_model_output_free_v2_fn)(fa_model_output_v2*);

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
*/
import "C"

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

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
