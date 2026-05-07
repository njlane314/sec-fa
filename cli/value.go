package main

/*
#cgo CFLAGS: -I${SRCDIR}/../abi
#include "core.h"

typedef fa_status_code (*fa_build_valuation_plan_v1_fn)(
    const fa_statement_snapshot_v1*, size_t,
    const fa_security_v1*, size_t,
    const fa_position_v1*, size_t,
    const fa_valuation_scenario_v1*, size_t,
    const fa_model_config_v1*,
    const fa_risk_limits_v1*,
    fa_value_output_v1*);
typedef void (*fa_value_output_free_v1_fn)(fa_value_output_v1*);
typedef fa_status_code (*fa_build_statement_snapshots_v1_fn)(
    const fa_canonical_fact_v1*, size_t,
    const fa_security_v1*, size_t,
    const fa_model_config_v1*,
    const fa_risk_limits_v1*,
    fa_statement_build_output_v1*);
typedef void (*fa_statement_build_output_free_v1_fn)(fa_statement_build_output_v1*);

static fa_status_code sec_build_valuation_plan_v1(
    void* fn,
    const fa_statement_snapshot_v1* statements, size_t statement_count,
    const fa_security_v1* securities, size_t security_count,
    const fa_position_v1* positions, size_t position_count,
    const fa_valuation_scenario_v1* scenarios, size_t scenario_count,
    const fa_model_config_v1* config,
    const fa_risk_limits_v1* risk_limits,
    fa_value_output_v1* output) {
    return ((fa_build_valuation_plan_v1_fn)fn)(
        statements, statement_count, securities, security_count, positions, position_count,
        scenarios, scenario_count, config, risk_limits, output);
}
static void sec_value_output_free_v1(void* fn, fa_value_output_v1* output) {
    ((fa_value_output_free_v1_fn)fn)(output);
}
static fa_status_code sec_build_statement_snapshots_v1(
    void* fn,
    const fa_canonical_fact_v1* facts, size_t fact_count,
    const fa_security_v1* securities, size_t security_count,
    const fa_model_config_v1* config,
    const fa_risk_limits_v1* risk_limits,
    fa_statement_build_output_v1* output) {
    return ((fa_build_statement_snapshots_v1_fn)fn)(
        facts, fact_count, securities, security_count, config, risk_limits, output);
}
static void sec_statement_build_output_free_v1(
    void* fn, fa_statement_build_output_v1* output) {
    ((fa_statement_build_output_free_v1_fn)fn)(output);
}
*/
import "C"

import (
	"database/sql"
	"unsafe"
)

func cmdValue(args []string) error {
	fs := modelFlagSet("value")
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
	facts, err := loadFacts(db)
	if err != nil {
		return err
	}
	var statementOut C.fa_statement_build_output_v1
	statementStatus := C.sec_build_statement_snapshots_v1(core.buildStmtsV1,
		factPtr(facts), C.size_t(len(facts)),
		securityPtr(securities), C.size_t(len(securities)),
		&config, &limits, &statementOut)
	statementDiagnostics := cCharArrayString(unsafe.Pointer(&statementOut.diagnostics[0]), diagnosticBytes)
	if statementStatus != C.FA_OK {
		C.sec_statement_build_output_free_v1(core.buildStmtsFreeV1, &statementOut)
		return fail(5, "fa_build_statement_snapshots_v1 failed: %s: %s", core.status(statementStatus), statementDiagnostics)
	}
	defer C.sec_statement_build_output_free_v1(core.buildStmtsFreeV1, &statementOut)
	statements := unsafe.Slice(statementOut.statements, int(statementOut.statement_count))
	var out C.fa_value_output_v1
	status := C.sec_build_valuation_plan_v1(core.buildValuationPlanV1,
		statementPtr(statements), C.size_t(len(statements)),
		securityPtr(securities), C.size_t(len(securities)),
		positionPtr(positions), C.size_t(len(positions)),
		scenarioPtr(scenarios), C.size_t(len(scenarios)),
		&config, &limits, &out)
	diagnostics := cCharArrayString(unsafe.Pointer(&out.diagnostics[0]), diagnosticBytes)
	if status != C.FA_OK {
		C.sec_value_output_free_v1(core.valueFreeV1, &out)
		return fail(5, "fa_build_valuation_plan_v1 failed: %s: %s", core.status(status), diagnostics)
	}
	defer C.sec_value_output_free_v1(core.valueFreeV1, &out)
	runID := uuidV4()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	insertedStatements := 0
	for _, s := range statements {
		startDate := dateFromEpochDay(int64(s.period_start_day))
		endDate := dateFromEpochDay(int64(s.period_end_day))
		availableAt := utcFromEpochSecond(int64(s.available_at_epoch_s))
		if endDate == "" || availableAt == "" {
			return fail(5, "C++ statement builder returned invalid statement date")
		}
		sourceHash := statementSnapshotSourceHash(s)
		res, err := tx.Exec(`INSERT OR IGNORE INTO statement_snapshots(
	snapshot_id, security_id, period_start_date, period_end_date, available_at, is_ttm,
	revenue_usd, net_income_usd, diluted_eps_usd, diluted_shares, operating_cash_flow_usd,
	capex_usd, free_cash_flow_usd, cash_usd, debt_usd, net_debt_usd, quality_flags, source_hash, inserted_at)
	VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			statementSnapshotID(s, sourceHash), uint64(s.security_id), nullableString(startDate), endDate, availableAt,
			float64(s.revenue_usd), float64(s.net_income_usd), float64(s.diluted_eps_usd), float64(s.diluted_shares),
			float64(s.operating_cash_flow_usd), float64(s.capex_usd), float64(s.free_cash_flow_usd),
			float64(s.cash_usd), float64(s.debt_usd), float64(s.net_debt_usd), uint32(s.quality_flags), sourceHash, utcNow())
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
	event, err := appendEvent(tx, "value_completed", map[string]any{
		"run_id": runID, "statement_count": len(statements), "statement_snapshots_inserted": insertedStatements,
		"valuation_count": int(out.valuation_count), "target_weight_count": int(out.target_weight_count),
		"order_intent_count": int(out.order_intent_count), "forecast_outcome_count": int(out.valuation_count),
		"assumption_set_id": assumptionID, "model_input_sha256": sha256Hex([]byte(configJSON)),
		"autonomy_approved": false, "autonomy_reason": "trading_mode=observe blocks autonomous staging",
		"statement_builder_diagnostics": statementDiagnostics, "diagnostics": diagnostics,
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

func statementSnapshotSourceHash(s C.fa_statement_snapshot_v1) string {
	return sha256Hex([]byte(mustCanonicalJSON(map[string]any{
		"security_id":               uint64(s.security_id),
		"period_start_day":          int64(s.period_start_day),
		"period_end_day":            int64(s.period_end_day),
		"available_at_epoch_s":      int64(s.available_at_epoch_s),
		"is_ttm":                    uint8(s.is_ttm),
		"revenue_usd":               float64(s.revenue_usd),
		"net_income_usd":            float64(s.net_income_usd),
		"diluted_eps_usd":           float64(s.diluted_eps_usd),
		"diluted_shares":            float64(s.diluted_shares),
		"operating_cash_flow_usd":   float64(s.operating_cash_flow_usd),
		"capex_usd":                 float64(s.capex_usd),
		"free_cash_flow_usd":        float64(s.free_cash_flow_usd),
		"cash_usd":                  float64(s.cash_usd),
		"debt_usd":                  float64(s.debt_usd),
		"net_debt_usd":              float64(s.net_debt_usd),
		"quality_flags":             uint32(s.quality_flags),
		"statement_builder_version": abiVersion,
	})))
}

func statementSnapshotID(s C.fa_statement_snapshot_v1, sourceHash string) string {
	return stableID("stmt", map[string]any{
		"security_id":     uint64(s.security_id),
		"period_end_date": dateFromEpochDay(int64(s.period_end_day)),
		"available_at":    utcFromEpochSecond(int64(s.available_at_epoch_s)),
		"source_hash":     sourceHash,
	})
}
