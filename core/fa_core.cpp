#include "fa_core.h"

#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>

namespace {

constexpr double kTiny = 1.0e-12;
constexpr size_t kMaxSecurities = 20000u;
constexpr size_t kMaxFacts = 5000000u;
constexpr size_t kMaxIntents = 20000u;

struct MetricPair {
    bool has_latest;
    bool has_previous;
    double latest_value;
    double previous_value;
    int64_t latest_end_day;
    int64_t previous_end_day;
    int64_t latest_available_at;
    uint32_t quality_flags;
};

static bool finite_non_nan(double value) {
    return std::isfinite(value) != 0;
}

static double abs_double(double value) {
    return value < 0.0 ? -value : value;
}

static double clamp_double(double value, double low, double high) {
    if (value < low) {
        return low;
    }
    if (value > high) {
        return high;
    }
    return value;
}

static void copy_reason(char* dst, size_t dst_size, const char* src) {
    if (dst == nullptr || dst_size == 0u) {
        return;
    }
    if (src == nullptr) {
        dst[0] = '\0';
        return;
    }
    (void)std::snprintf(dst, dst_size, "%s", src);
}

static void set_diag(char* dst, size_t dst_size, const char* msg) {
    copy_reason(dst, dst_size, msg);
}

static bool fact_has_valid_abi(const fa_canonical_fact_v1& fact) {
    return fact.abi_version == FA_ABI_VERSION;
}

static bool security_has_valid_abi(const fa_security_v1& sec) {
    return sec.abi_version == FA_ABI_VERSION;
}

static bool position_has_valid_abi(const fa_position_v1& pos) {
    return pos.abi_version == FA_ABI_VERSION;
}

static bool order_intent_has_valid_abi(const fa_order_intent_v1& intent) {
    return intent.abi_version == FA_ABI_VERSION;
}

static bool config_has_valid_abi(const fa_model_config_v1& config) {
    return config.abi_version == FA_ABI_VERSION;
}

static bool limits_have_valid_abi(const fa_risk_limits_v1& limits) {
    return limits.abi_version == FA_ABI_VERSION;
}

static double current_weight_for_security(const fa_position_v1* positions,
                                          size_t position_count,
                                          uint64_t security_id) {
    if (positions == nullptr) {
        return 0.0;
    }
    for (size_t i = 0u; i < position_count; ++i) {
        if (positions[i].security_id == security_id) {
            return positions[i].weight_ratio;
        }
    }
    return 0.0;
}

static const fa_security_v1* find_security(const fa_security_v1* securities,
                                           size_t security_count,
                                           uint64_t security_id) {
    if (securities == nullptr) {
        return nullptr;
    }
    for (size_t i = 0u; i < security_count; ++i) {
        if (securities[i].security_id == security_id) {
            return &securities[i];
        }
    }
    return nullptr;
}

static bool choose_latest_two_metric(const fa_canonical_fact_v1* facts,
                                     size_t fact_count,
                                     uint64_t security_id,
                                     int32_t metric_id,
                                     int64_t now_epoch_s,
                                     int64_t max_fact_age_s,
                                     MetricPair* result) {
    if (result == nullptr) {
        return false;
    }
    result->has_latest = false;
    result->has_previous = false;
    result->latest_value = 0.0;
    result->previous_value = 0.0;
    result->latest_end_day = 0;
    result->previous_end_day = 0;
    result->latest_available_at = 0;
    result->quality_flags = FA_QUALITY_NONE;

    if (facts == nullptr) {
        return false;
    }

    for (size_t i = 0u; i < fact_count; ++i) {
        const fa_canonical_fact_v1& fact = facts[i];
        if (fact.security_id != security_id || fact.metric_id != metric_id) {
            continue;
        }
        if (!fact_has_valid_abi(fact)) {
            return false;
        }
        if (!finite_non_nan(fact.value) || fact.period_end_day <= 0 || fact.available_at_epoch_s <= 0) {
            continue;
        }
        if (max_fact_age_s > 0 && now_epoch_s > 0 && (now_epoch_s - fact.available_at_epoch_s) > max_fact_age_s) {
            continue;
        }

        const bool newer_than_latest =
            (!result->has_latest) ||
            (fact.period_end_day > result->latest_end_day) ||
            (fact.period_end_day == result->latest_end_day && fact.available_at_epoch_s > result->latest_available_at);

        if (newer_than_latest) {
            if (result->has_latest && fact.period_end_day != result->latest_end_day) {
                result->has_previous = true;
                result->previous_value = result->latest_value;
                result->previous_end_day = result->latest_end_day;
            }
            result->has_latest = true;
            result->latest_value = fact.value;
            result->latest_end_day = fact.period_end_day;
            result->latest_available_at = fact.available_at_epoch_s;
            result->quality_flags |= fact.quality_flags;
        } else if (fact.period_end_day != result->latest_end_day) {
            const bool newer_than_previous =
                (!result->has_previous) || (fact.period_end_day > result->previous_end_day);
            if (newer_than_previous) {
                result->has_previous = true;
                result->previous_value = fact.value;
                result->previous_end_day = fact.period_end_day;
                result->quality_flags |= fact.quality_flags;
            }
        }
    }

    return true;
}

static double growth_from_pair(const MetricPair& pair, double max_abs_growth, bool* usable) {
    if (usable != nullptr) {
        *usable = false;
    }
    if (!pair.has_latest || !pair.has_previous) {
        return 0.0;
    }
    if (!finite_non_nan(pair.latest_value) || !finite_non_nan(pair.previous_value)) {
        return 0.0;
    }
    const double denom = abs_double(pair.previous_value);
    if (denom <= kTiny) {
        return 0.0;
    }
    const double raw_growth = (pair.latest_value - pair.previous_value) / denom;
    if (!finite_non_nan(raw_growth)) {
        return 0.0;
    }
    if (usable != nullptr) {
        *usable = true;
    }
    return clamp_double(raw_growth, -max_abs_growth, max_abs_growth);
}

static bool validate_common_inputs(const fa_canonical_fact_v1* facts,
                                   size_t fact_count,
                                   const fa_security_v1* securities,
                                   size_t security_count,
                                   const fa_position_v1* positions,
                                   size_t position_count,
                                   const fa_model_config_v1* config,
                                   const fa_risk_limits_v1* risk_limits,
                                   fa_model_output_v1* output) {
    if (output == nullptr || config == nullptr || risk_limits == nullptr) {
        return false;
    }
    if ((fact_count > 0u && facts == nullptr) || (security_count > 0u && securities == nullptr) ||
        (position_count > 0u && positions == nullptr)) {
        return false;
    }
    if (fact_count > kMaxFacts || security_count > kMaxSecurities || position_count > kMaxSecurities) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "resource limit exceeded");
        return false;
    }
    if (!config_has_valid_abi(*config) || !limits_have_valid_abi(*risk_limits)) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "ABI version mismatch");
        return false;
    }
    if (!finite_non_nan(risk_limits->portfolio_value_usd) || risk_limits->portfolio_value_usd <= 0.0) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "portfolio_value_usd must be positive");
        return false;
    }
    if (!finite_non_nan(config->target_gross_exposure_ratio) || config->target_gross_exposure_ratio < 0.0 ||
        config->target_gross_exposure_ratio > 1.0) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "target_gross_exposure_ratio out of range");
        return false;
    }
    if (!finite_non_nan(config->max_name_weight_ratio) || config->max_name_weight_ratio <= 0.0 ||
        config->max_name_weight_ratio > 1.0) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "max_name_weight_ratio out of range");
        return false;
    }
    if (!finite_non_nan(config->max_forecast_abs_growth_ratio) || config->max_forecast_abs_growth_ratio <= 0.0 ||
        config->max_forecast_abs_growth_ratio > 10.0) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "max_forecast_abs_growth_ratio out of range");
        return false;
    }

    for (size_t i = 0u; i < security_count; ++i) {
        if (!security_has_valid_abi(securities[i])) {
            set_diag(output->diagnostics, sizeof(output->diagnostics), "security ABI version mismatch");
            return false;
        }
        if (securities[i].security_id == 0u) {
            set_diag(output->diagnostics, sizeof(output->diagnostics), "security_id must be nonzero");
            return false;
        }
    }
    for (size_t i = 0u; i < position_count; ++i) {
        if (!position_has_valid_abi(positions[i])) {
            set_diag(output->diagnostics, sizeof(output->diagnostics), "position ABI version mismatch");
            return false;
        }
        if (!finite_non_nan(positions[i].weight_ratio) || abs_double(positions[i].weight_ratio) > 10.0) {
            set_diag(output->diagnostics, sizeof(output->diagnostics), "position weight out of range");
            return false;
        }
    }
    return true;
}

}  // namespace

extern "C" const char* fa_status_name(fa_status_code code) {
    switch (code) {
        case FA_OK: return "FA_OK";
        case FA_ERR_NULL_ARGUMENT: return "FA_ERR_NULL_ARGUMENT";
        case FA_ERR_BAD_ABI_VERSION: return "FA_ERR_BAD_ABI_VERSION";
        case FA_ERR_INVALID_INPUT: return "FA_ERR_INVALID_INPUT";
        case FA_ERR_RESOURCE_LIMIT: return "FA_ERR_RESOURCE_LIMIT";
        case FA_ERR_INVARIANT_FAILED: return "FA_ERR_INVARIANT_FAILED";
        case FA_ERR_ALLOCATION_FAILED: return "FA_ERR_ALLOCATION_FAILED";
        default: return "FA_ERR_UNKNOWN";
    }
}

extern "C" void fa_model_output_free_v1(fa_model_output_v1* output) {
    if (output == nullptr) {
        return;
    }
    std::free(output->forecasts);
    std::free(output->target_weights);
    std::free(output->order_intents);
    output->forecasts = nullptr;
    output->target_weights = nullptr;
    output->order_intents = nullptr;
    output->forecast_count = 0u;
    output->target_weight_count = 0u;
    output->order_intent_count = 0u;
    output->diagnostics[0] = '\0';
}

extern "C" void fa_risk_output_free_v1(fa_risk_output_v1* output) {
    if (output == nullptr) {
        return;
    }
    std::free(output->decisions);
    output->decisions = nullptr;
    output->decision_count = 0u;
    output->diagnostics[0] = '\0';
}

extern "C" fa_status_code fa_model_run_v1(
    const fa_canonical_fact_v1* facts,
    size_t fact_count,
    const fa_security_v1* securities,
    size_t security_count,
    const fa_position_v1* positions,
    size_t position_count,
    const fa_model_config_v1* config,
    const fa_risk_limits_v1* risk_limits,
    fa_model_output_v1* output) {

    if (output == nullptr) {
        return FA_ERR_NULL_ARGUMENT;
    }
    output->abi_version = FA_ABI_VERSION;
    output->forecast_count = 0u;
    output->forecasts = nullptr;
    output->target_weight_count = 0u;
    output->target_weights = nullptr;
    output->order_intent_count = 0u;
    output->order_intents = nullptr;
    output->diagnostics[0] = '\0';

    if (config == nullptr || risk_limits == nullptr) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "null config or risk_limits");
        return FA_ERR_NULL_ARGUMENT;
    }
    if (!validate_common_inputs(facts, fact_count, securities, security_count, positions, position_count,
                                config, risk_limits, output)) {
        return FA_ERR_INVALID_INPUT;
    }

    output->forecasts = static_cast<fa_forecast_v1*>(std::calloc(security_count, sizeof(fa_forecast_v1)));
    output->target_weights = static_cast<fa_target_weight_v1*>(std::calloc(security_count, sizeof(fa_target_weight_v1)));
    output->order_intents = static_cast<fa_order_intent_v1*>(std::calloc(security_count, sizeof(fa_order_intent_v1)));
    if ((security_count > 0u) && (output->forecasts == nullptr || output->target_weights == nullptr || output->order_intents == nullptr)) {
        fa_model_output_free_v1(output);
        set_diag(output->diagnostics, sizeof(output->diagnostics), "allocation failed");
        return FA_ERR_ALLOCATION_FAILED;
    }

    double score_sum = 0.0;
    size_t investable_count = 0u;

    for (size_t i = 0u; i < security_count; ++i) {
        const fa_security_v1& sec = securities[i];
        fa_forecast_v1& forecast = output->forecasts[i];
        forecast.abi_version = FA_ABI_VERSION;
        forecast.security_id = sec.security_id;
        forecast.revenue_growth_ratio = 0.0;
        forecast.earnings_growth_ratio = 0.0;
        forecast.expected_return_proxy = 0.0;
        forecast.confidence_ratio = 0.0;
        forecast.quality_flags = FA_QUALITY_NONE;
        copy_reason(forecast.reason, sizeof(forecast.reason), "not investable or insufficient facts");

        if (sec.investable == 0u) {
            continue;
        }
        ++investable_count;

        MetricPair revenue{};
        MetricPair earnings{};
        const bool revenue_scan_ok = choose_latest_two_metric(facts, fact_count, sec.security_id, FA_METRIC_REVENUE,
                                                              risk_limits->now_epoch_s, config->max_fact_age_s, &revenue);
        const bool earnings_scan_ok = choose_latest_two_metric(facts, fact_count, sec.security_id, FA_METRIC_NET_INCOME,
                                                               risk_limits->now_epoch_s, config->max_fact_age_s, &earnings);
        if (!revenue_scan_ok || !earnings_scan_ok) {
            fa_model_output_free_v1(output);
            set_diag(output->diagnostics, sizeof(output->diagnostics), "fact ABI version mismatch");
            return FA_ERR_BAD_ABI_VERSION;
        }

        bool revenue_usable = false;
        bool earnings_usable = false;
        const double revenue_growth = growth_from_pair(revenue, config->max_forecast_abs_growth_ratio, &revenue_usable);
        const double earnings_growth = growth_from_pair(earnings, config->max_forecast_abs_growth_ratio, &earnings_usable);
        forecast.revenue_growth_ratio = revenue_growth;
        forecast.earnings_growth_ratio = earnings_growth;
        forecast.quality_flags = revenue.quality_flags | earnings.quality_flags;

        if (!revenue_usable && !earnings_usable) {
            forecast.quality_flags |= FA_QUALITY_MISSING_COMPARABLE | FA_QUALITY_LOW_CONFIDENCE;
            continue;
        }

        double confidence = 0.0;
        double expected = 0.0;
        if (revenue_usable && earnings_usable) {
            expected = (0.70 * revenue_growth) + (0.30 * earnings_growth);
            confidence = 0.80;
        } else if (revenue_usable) {
            expected = 0.50 * revenue_growth;
            confidence = 0.45;
            forecast.quality_flags |= FA_QUALITY_MISSING_COMPARABLE;
        } else {
            expected = 0.25 * earnings_growth;
            confidence = 0.30;
            forecast.quality_flags |= FA_QUALITY_MISSING_COMPARABLE;
        }

        if (forecast.quality_flags != FA_QUALITY_NONE) {
            confidence *= 0.70;
        }

        forecast.expected_return_proxy = clamp_double(expected, -config->max_forecast_abs_growth_ratio, config->max_forecast_abs_growth_ratio);
        forecast.confidence_ratio = clamp_double(confidence, 0.0, 1.0);

        if (forecast.expected_return_proxy > config->min_expected_return_proxy && forecast.confidence_ratio >= 0.20) {
            score_sum += forecast.expected_return_proxy * forecast.confidence_ratio;
            copy_reason(forecast.reason, sizeof(forecast.reason), "positive deterministic baseline score");
        } else {
            copy_reason(forecast.reason, sizeof(forecast.reason), "below model threshold");
        }
    }

    output->forecast_count = security_count;

    size_t intent_count = 0u;
    for (size_t i = 0u; i < security_count; ++i) {
        const fa_security_v1& sec = securities[i];
        const fa_forecast_v1& forecast = output->forecasts[i];
        fa_target_weight_v1& target = output->target_weights[i];
        target.abi_version = FA_ABI_VERSION;
        target.security_id = sec.security_id;
        target.current_weight_ratio = current_weight_for_security(positions, position_count, sec.security_id);
        target.target_weight_ratio = 0.0;
        target.delta_weight_ratio = -target.current_weight_ratio;
        copy_reason(target.reason, sizeof(target.reason), "zero target");

        if (sec.investable != 0u && score_sum > kTiny &&
            forecast.expected_return_proxy > config->min_expected_return_proxy && forecast.confidence_ratio >= 0.20) {
            const double raw_score = forecast.expected_return_proxy * forecast.confidence_ratio;
            const double raw_weight = config->target_gross_exposure_ratio * raw_score / score_sum;
            target.target_weight_ratio = clamp_double(raw_weight, 0.0, config->max_name_weight_ratio);
            target.delta_weight_ratio = target.target_weight_ratio - target.current_weight_ratio;
            copy_reason(target.reason, sizeof(target.reason), "score-weighted long-only target");
        }

        const double notional = abs_double(target.delta_weight_ratio * risk_limits->portfolio_value_usd);
        if (notional >= config->min_abs_order_notional_usd) {
            fa_order_intent_v1& intent = output->order_intents[intent_count];
            intent.abi_version = FA_ABI_VERSION;
            intent.security_id = sec.security_id;
            intent.side = target.delta_weight_ratio > 0.0 ? FA_SIDE_BUY : FA_SIDE_SELL;
            intent.notional_usd = notional;
            intent.current_weight_ratio = target.current_weight_ratio;
            intent.target_weight_ratio = target.target_weight_ratio;
            copy_reason(intent.reason, sizeof(intent.reason), target.reason);
            ++intent_count;
        }
    }

    output->target_weight_count = security_count;
    output->order_intent_count = intent_count;
    (void)std::snprintf(output->diagnostics, sizeof(output->diagnostics),
                        "model_run_ok securities=%zu investable=%zu facts=%zu intents=%zu",
                        security_count, investable_count, fact_count, intent_count);
    return FA_OK;
}

extern "C" fa_status_code fa_risk_check_v1(
    const fa_order_intent_v1* order_intents,
    size_t order_intent_count,
    const fa_security_v1* securities,
    size_t security_count,
    const fa_risk_limits_v1* risk_limits,
    fa_risk_output_v1* output) {

    if (output == nullptr) {
        return FA_ERR_NULL_ARGUMENT;
    }
    output->abi_version = FA_ABI_VERSION;
    output->decision_count = 0u;
    output->decisions = nullptr;
    output->diagnostics[0] = '\0';

    if (risk_limits == nullptr) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "null risk_limits");
        return FA_ERR_NULL_ARGUMENT;
    }
    if (!limits_have_valid_abi(*risk_limits)) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "risk limit ABI version mismatch");
        return FA_ERR_BAD_ABI_VERSION;
    }
    if ((order_intent_count > 0u && order_intents == nullptr) || (security_count > 0u && securities == nullptr)) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "null input array");
        return FA_ERR_NULL_ARGUMENT;
    }
    if (order_intent_count > kMaxIntents || security_count > kMaxSecurities) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "resource limit exceeded");
        return FA_ERR_RESOURCE_LIMIT;
    }
    for (size_t i = 0u; i < security_count; ++i) {
        if (!security_has_valid_abi(securities[i])) {
            set_diag(output->diagnostics, sizeof(output->diagnostics), "security ABI version mismatch");
            return FA_ERR_BAD_ABI_VERSION;
        }
    }

    output->decisions = static_cast<fa_risk_decision_v1*>(std::calloc(order_intent_count, sizeof(fa_risk_decision_v1)));
    if (order_intent_count > 0u && output->decisions == nullptr) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "allocation failed");
        return FA_ERR_ALLOCATION_FAILED;
    }

    double aggregate_buy_notional = 0.0;
    for (size_t i = 0u; i < order_intent_count; ++i) {
        if (!order_intent_has_valid_abi(order_intents[i])) {
            fa_risk_output_free_v1(output);
            set_diag(output->diagnostics, sizeof(output->diagnostics), "order intent ABI version mismatch");
            return FA_ERR_BAD_ABI_VERSION;
        }
        if (order_intents[i].side == FA_SIDE_BUY && finite_non_nan(order_intents[i].notional_usd) && order_intents[i].notional_usd > 0.0) {
            aggregate_buy_notional += order_intents[i].notional_usd;
        }
    }

    const bool reconciliation_fresh =
        risk_limits->broker_reconciled != 0u &&
        risk_limits->now_epoch_s > 0 &&
        risk_limits->reconciliation_checked_at_epoch_s > 0 &&
        risk_limits->max_reconciliation_age_s > 0 &&
        (risk_limits->now_epoch_s - risk_limits->reconciliation_checked_at_epoch_s) <= risk_limits->max_reconciliation_age_s;

    for (size_t i = 0u; i < order_intent_count; ++i) {
        const fa_order_intent_v1& intent = order_intents[i];
        fa_risk_decision_v1& decision = output->decisions[i];
        decision.abi_version = FA_ABI_VERSION;
        decision.security_id = intent.security_id;
        decision.side = intent.side;
        decision.decision = FA_DECISION_REJECTED;
        decision.notional_usd = intent.notional_usd;
        copy_reason(decision.reason, sizeof(decision.reason), "rejected by default");

        const fa_security_v1* sec = find_security(securities, security_count, intent.security_id);
        if (!reconciliation_fresh) {
            copy_reason(decision.reason, sizeof(decision.reason), "broker reconciliation is missing or stale");
            continue;
        }
        if (sec == nullptr) {
            copy_reason(decision.reason, sizeof(decision.reason), "security not found");
            continue;
        }
        if (sec->investable == 0u) {
            copy_reason(decision.reason, sizeof(decision.reason), "security not investable");
            continue;
        }
        if (intent.side != FA_SIDE_BUY && intent.side != FA_SIDE_SELL) {
            copy_reason(decision.reason, sizeof(decision.reason), "invalid order side");
            continue;
        }
        if (!finite_non_nan(intent.notional_usd) || intent.notional_usd < risk_limits->min_abs_order_notional_usd) {
            copy_reason(decision.reason, sizeof(decision.reason), "notional below minimum or invalid");
            continue;
        }
        if (risk_limits->max_order_notional_usd > 0.0 && intent.notional_usd > risk_limits->max_order_notional_usd) {
            copy_reason(decision.reason, sizeof(decision.reason), "order notional exceeds hard limit");
            continue;
        }
        if (!finite_non_nan(intent.target_weight_ratio) || abs_double(intent.target_weight_ratio) > risk_limits->max_name_weight_ratio) {
            copy_reason(decision.reason, sizeof(decision.reason), "target weight exceeds name limit");
            continue;
        }
        if (!finite_non_nan(sec->adv_usd) || sec->adv_usd < risk_limits->min_adv_usd) {
            copy_reason(decision.reason, sizeof(decision.reason), "liquidity below minimum ADV");
            continue;
        }
        if (risk_limits->max_adv_participation_ratio > 0.0 && intent.notional_usd > sec->adv_usd * risk_limits->max_adv_participation_ratio) {
            copy_reason(decision.reason, sizeof(decision.reason), "order exceeds ADV participation limit");
            continue;
        }
        if (intent.side == FA_SIDE_BUY && aggregate_buy_notional > risk_limits->cash_usd) {
            copy_reason(decision.reason, sizeof(decision.reason), "aggregate buy notional exceeds cash");
            continue;
        }

        decision.decision = FA_DECISION_APPROVED;
        copy_reason(decision.reason, sizeof(decision.reason), "approved by deterministic hard risk gate");
    }

    output->decision_count = order_intent_count;
    (void)std::snprintf(output->diagnostics, sizeof(output->diagnostics),
                        "risk_check_ok intents=%zu reconciliation_fresh=%d aggregate_buy_notional=%.2f",
                        order_intent_count, reconciliation_fresh ? 1 : 0, aggregate_buy_notional);
    return FA_OK;
}
