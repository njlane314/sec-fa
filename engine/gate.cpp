#include "internal.h"

#include <cstdio>
#include <cstdlib>

extern "C" fa_status_code fa_check_risk_limits_v1(
    const fa_order_intent_v1* order_intents,
    size_t order_intent_count,
    const fa_security_v1* securities,
    size_t security_count,
    const fa_risk_limits_v1* risk_limits,
    fa_gate_output_v1* output) {

    if (output == nullptr) {
        return FA_ERR_NULL_ARGUMENT;
    }
    output->abi_version = FA_ABI_VERSION;
    output->decision_count = 0u;
    output->decisions = nullptr;
    output->diagnostics[0] = '\0';

    if (risk_limits == nullptr) {
        kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "null risk_limits");
        return FA_ERR_NULL_ARGUMENT;
    }
    if (!kernel::limits_have_valid_abi(*risk_limits)) {
        kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "risk limit ABI version mismatch");
        return FA_ERR_BAD_ABI_VERSION;
    }
    if ((order_intent_count > 0u && order_intents == nullptr) || (security_count > 0u && securities == nullptr)) {
        kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "null input array");
        return FA_ERR_NULL_ARGUMENT;
    }
    if (order_intent_count > kernel::kMaxIntents || security_count > kernel::kMaxSecurities) {
        kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "resource limit exceeded");
        return FA_ERR_RESOURCE_LIMIT;
    }
    for (size_t i = 0u; i < security_count; ++i) {
        if (!kernel::security_has_valid_abi(securities[i])) {
            kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "security ABI version mismatch");
            return FA_ERR_BAD_ABI_VERSION;
        }
    }

    output->decisions = static_cast<fa_risk_decision_v1*>(
        std::calloc(order_intent_count, sizeof(fa_risk_decision_v1)));
    if (order_intent_count > 0u && output->decisions == nullptr) {
        kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "allocation failed");
        return FA_ERR_ALLOCATION_FAILED;
    }

    double aggregate_buy_notional = 0.0;
    for (size_t i = 0u; i < order_intent_count; ++i) {
        if (!kernel::order_intent_has_valid_abi(order_intents[i])) {
            fa_gate_output_free_v1(output);
            kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "order intent ABI version mismatch");
            return FA_ERR_BAD_ABI_VERSION;
        }
        if (order_intents[i].side == FA_SIDE_BUY &&
            kernel::finite_non_nan(order_intents[i].notional_usd) &&
            order_intents[i].notional_usd > 0.0) {
            aggregate_buy_notional += order_intents[i].notional_usd;
        }
    }

    const bool reconciliation_fresh =
        risk_limits->broker_reconciled != 0u &&
        risk_limits->now_epoch_s > 0 &&
        risk_limits->reconciliation_checked_at_epoch_s > 0 &&
        risk_limits->max_reconciliation_age_s > 0 &&
        (risk_limits->now_epoch_s - risk_limits->reconciliation_checked_at_epoch_s) <=
            risk_limits->max_reconciliation_age_s;

    for (size_t i = 0u; i < order_intent_count; ++i) {
        const fa_order_intent_v1& intent = order_intents[i];
        fa_risk_decision_v1& decision = output->decisions[i];
        decision.abi_version = FA_ABI_VERSION;
        decision.security_id = intent.security_id;
        decision.side = intent.side;
        decision.decision = FA_DECISION_REJECTED;
        decision.notional_usd = intent.notional_usd;
        kernel::copy_reason(decision.reason, sizeof(decision.reason), "rejected by default");

        const fa_security_v1* security = kernel::find_security(securities, security_count, intent.security_id);
        if (!reconciliation_fresh) {
            kernel::copy_reason(decision.reason, sizeof(decision.reason), "broker reconciliation is missing or stale");
            continue;
        }
        if (security == nullptr) {
            kernel::copy_reason(decision.reason, sizeof(decision.reason), "security not found");
            continue;
        }
        if (security->investable == 0u) {
            kernel::copy_reason(decision.reason, sizeof(decision.reason), "security not investable");
            continue;
        }
        if (intent.side != FA_SIDE_BUY && intent.side != FA_SIDE_SELL) {
            kernel::copy_reason(decision.reason, sizeof(decision.reason), "invalid order side");
            continue;
        }
        if (!kernel::finite_non_nan(intent.notional_usd) ||
            intent.notional_usd < risk_limits->min_abs_order_notional_usd) {
            kernel::copy_reason(decision.reason, sizeof(decision.reason), "notional below minimum or invalid");
            continue;
        }
        if (risk_limits->max_order_notional_usd > 0.0 &&
            intent.notional_usd > risk_limits->max_order_notional_usd) {
            kernel::copy_reason(decision.reason, sizeof(decision.reason), "order notional exceeds hard limit");
            continue;
        }
        if (!kernel::finite_non_nan(intent.target_weight_ratio) ||
            kernel::abs_double(intent.target_weight_ratio) > risk_limits->max_name_weight_ratio) {
            kernel::copy_reason(decision.reason, sizeof(decision.reason), "target weight exceeds name limit");
            continue;
        }
        if (!kernel::finite_non_nan(security->adv_usd) || security->adv_usd < risk_limits->min_adv_usd) {
            kernel::copy_reason(decision.reason, sizeof(decision.reason), "liquidity below minimum ADV");
            continue;
        }
        if (risk_limits->max_adv_participation_ratio > 0.0 &&
            intent.notional_usd > security->adv_usd * risk_limits->max_adv_participation_ratio) {
            kernel::copy_reason(decision.reason, sizeof(decision.reason), "order exceeds ADV participation limit");
            continue;
        }
        if (intent.side == FA_SIDE_BUY && aggregate_buy_notional > risk_limits->cash_usd) {
            kernel::copy_reason(decision.reason, sizeof(decision.reason), "aggregate buy notional exceeds cash");
            continue;
        }

        decision.decision = FA_DECISION_APPROVED;
        kernel::copy_reason(decision.reason, sizeof(decision.reason), "approved by deterministic hard risk gate");
    }

    output->decision_count = order_intent_count;
    (void)std::snprintf(output->diagnostics, sizeof(output->diagnostics),
                        "gate_ok intents=%zu reconciliation_fresh=%d aggregate_buy_notional=%.2f",
                        order_intent_count, reconciliation_fresh ? 1 : 0, aggregate_buy_notional);
    return FA_OK;
}
