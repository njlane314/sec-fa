package main

/*
#cgo CFLAGS: -I${SRCDIR}/../abi
#include "core.h"

typedef fa_status_code (*fa_risk_check_v1_fn)(
    const fa_order_intent_v1*, size_t,
    const fa_security_v1*, size_t,
    const fa_risk_limits_v1*,
    fa_risk_output_v1*);
typedef void (*fa_risk_output_free_v1_fn)(fa_risk_output_v1*);

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
	"database/sql"
	"unsafe"
)

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
