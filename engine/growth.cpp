#include "internal.h"

#include <cstdio>
#include <cstdlib>

namespace {

using namespace kernel;

bool choose_latest_two_metric(const fa_canonical_fact_v1* facts,
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
    result->latest_duration_days = 0u;
    result->latest_metric_kind = FA_METRIC_KIND_UNKNOWN;
    result->latest_period_semantics = FA_PERIOD_UNKNOWN;
    result->latest_basis_id = FA_BASIS_UNKNOWN;
    result->latest_dimensions_hash = 0u;
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
        if (!fact_is_model_comparable(fact, metric_id)) {
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
                const fa_canonical_fact_v1 old_latest{FA_ABI_VERSION,
                                                       security_id,
                                                       metric_id,
                                                       result->latest_value,
                                                       0,
                                                       result->latest_end_day,
                                                       result->latest_available_at,
                                                       result->quality_flags,
                                                       result->latest_metric_kind,
                                                       result->latest_period_semantics,
                                                       result->latest_basis_id,
                                                       FA_OBSERVATION_SELECTED,
                                                       result->latest_duration_days,
                                                       result->latest_dimensions_hash};
                if (periods_are_comparable(fact, old_latest)) {
                    result->has_previous = true;
                    result->previous_value = result->latest_value;
                    result->previous_end_day = result->latest_end_day;
                } else {
                    result->has_previous = false;
                    result->previous_value = 0.0;
                    result->previous_end_day = 0;
                }
            }
            result->has_latest = true;
            result->latest_value = fact.value;
            result->latest_end_day = fact.period_end_day;
            result->latest_available_at = fact.available_at_epoch_s;
            result->latest_duration_days = fact.duration_days;
            result->latest_metric_kind = fact.metric_kind;
            result->latest_period_semantics = fact.period_semantics;
            result->latest_basis_id = fact.basis_id;
            result->latest_dimensions_hash = fact.dimensions_hash;
            result->quality_flags |= fact.quality_flags;
        } else if (fact.period_end_day != result->latest_end_day) {
            const fa_canonical_fact_v1 latest{FA_ABI_VERSION,
                                              security_id,
                                              metric_id,
                                              result->latest_value,
                                              0,
                                              result->latest_end_day,
                                              result->latest_available_at,
                                              result->quality_flags,
                                              result->latest_metric_kind,
                                              result->latest_period_semantics,
                                              result->latest_basis_id,
                                              FA_OBSERVATION_SELECTED,
                                              result->latest_duration_days,
                                              result->latest_dimensions_hash};
            if (!result->has_latest || !periods_are_comparable(fact, latest)) {
                continue;
            }
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

double growth_from_pair(const MetricPair& pair, double max_abs_growth, bool* usable) {
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

bool validate_plan_v1_inputs(const fa_canonical_fact_v1* facts,
                                  size_t fact_count,
                                  const fa_security_v1* securities,
                                  size_t security_count,
                                  const fa_position_v1* positions,
                                  size_t position_count,
                                  const fa_model_config_v1* config,
                                  const fa_risk_limits_v1* risk_limits,
                                  fa_plan_output_v1* output) {
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

extern "C" fa_status_code fa_build_portfolio_plan_v1(
    const fa_canonical_fact_v1* facts,
    size_t fact_count,
    const fa_security_v1* securities,
    size_t security_count,
    const fa_position_v1* positions,
    size_t position_count,
    const fa_model_config_v1* config,
    const fa_risk_limits_v1* risk_limits,
    fa_plan_output_v1* output) {

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
        kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "null config or risk_limits");
        return FA_ERR_NULL_ARGUMENT;
    }
    if (!validate_plan_v1_inputs(facts, fact_count, securities, security_count, positions, position_count,
                                      config, risk_limits, output)) {
        return FA_ERR_INVALID_INPUT;
    }

    output->forecasts = static_cast<fa_forecast_v1*>(std::calloc(security_count, sizeof(fa_forecast_v1)));
    output->target_weights = static_cast<fa_target_weight_v1*>(std::calloc(security_count, sizeof(fa_target_weight_v1)));
    output->order_intents = static_cast<fa_order_intent_v1*>(std::calloc(security_count, sizeof(fa_order_intent_v1)));
    if ((security_count > 0u) &&
        (output->forecasts == nullptr || output->target_weights == nullptr || output->order_intents == nullptr)) {
        fa_plan_output_free_v1(output);
        kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "allocation failed");
        return FA_ERR_ALLOCATION_FAILED;
    }

    double score_sum = 0.0;
    size_t investable_count = 0u;

    for (size_t i = 0u; i < security_count; ++i) {
        const fa_security_v1& security = securities[i];
        fa_forecast_v1& forecast = output->forecasts[i];
        forecast.abi_version = FA_ABI_VERSION;
        forecast.security_id = security.security_id;
        forecast.revenue_growth_ratio = 0.0;
        forecast.earnings_growth_ratio = 0.0;
        forecast.expected_return_proxy = 0.0;
        forecast.confidence_ratio = 0.0;
        forecast.quality_flags = FA_QUALITY_NONE;
        kernel::copy_reason(forecast.reason, sizeof(forecast.reason), "not investable or insufficient facts");

        if (security.investable == 0u) {
            continue;
        }
        ++investable_count;

        MetricPair revenue{};
        MetricPair earnings{};
        const bool revenue_scan_ok = choose_latest_two_metric(
            facts, fact_count, security.security_id, FA_METRIC_REVENUE,
            risk_limits->now_epoch_s, config->max_fact_age_s, &revenue);
        const bool earnings_scan_ok = choose_latest_two_metric(
            facts, fact_count, security.security_id, FA_METRIC_NET_INCOME,
            risk_limits->now_epoch_s, config->max_fact_age_s, &earnings);
        if (!revenue_scan_ok || !earnings_scan_ok) {
            fa_plan_output_free_v1(output);
            kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "fact ABI version mismatch");
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

        forecast.expected_return_proxy =
            kernel::clamp_double(expected, -config->max_forecast_abs_growth_ratio, config->max_forecast_abs_growth_ratio);
        forecast.confidence_ratio = kernel::clamp_double(confidence, 0.0, 1.0);

        if (forecast.expected_return_proxy > config->min_expected_return_proxy && forecast.confidence_ratio >= 0.20) {
            score_sum += forecast.expected_return_proxy * forecast.confidence_ratio;
            kernel::copy_reason(forecast.reason, sizeof(forecast.reason), "positive deterministic baseline score");
        } else {
            kernel::copy_reason(forecast.reason, sizeof(forecast.reason), "below model threshold");
        }
    }

    output->forecast_count = security_count;

    size_t intent_count = 0u;
    for (size_t i = 0u; i < security_count; ++i) {
        const fa_security_v1& security = securities[i];
        const fa_forecast_v1& forecast = output->forecasts[i];
        fa_target_weight_v1& target = output->target_weights[i];
        target.abi_version = FA_ABI_VERSION;
        target.security_id = security.security_id;
        target.current_weight_ratio =
            kernel::current_weight_for_security(positions, position_count, security.security_id);
        target.target_weight_ratio = 0.0;
        target.delta_weight_ratio = -target.current_weight_ratio;
        kernel::copy_reason(target.reason, sizeof(target.reason), "zero target");

        if (security.investable != 0u && score_sum > kTiny &&
            forecast.expected_return_proxy > config->min_expected_return_proxy && forecast.confidence_ratio >= 0.20) {
            const double raw_score = forecast.expected_return_proxy * forecast.confidence_ratio;
            const double raw_weight = config->target_gross_exposure_ratio * raw_score / score_sum;
            target.target_weight_ratio = kernel::clamp_double(raw_weight, 0.0, config->max_name_weight_ratio);
            target.delta_weight_ratio = target.target_weight_ratio - target.current_weight_ratio;
            kernel::copy_reason(target.reason, sizeof(target.reason), "score-weighted long-only target");
        }

        const double notional = kernel::abs_double(target.delta_weight_ratio * risk_limits->portfolio_value_usd);
        if (notional >= config->min_abs_order_notional_usd) {
            fa_order_intent_v1& intent = output->order_intents[intent_count];
            intent.abi_version = FA_ABI_VERSION;
            intent.security_id = target.security_id;
            intent.side = target.delta_weight_ratio > 0.0 ? FA_SIDE_BUY : FA_SIDE_SELL;
            intent.notional_usd = notional;
            intent.current_weight_ratio = target.current_weight_ratio;
            intent.target_weight_ratio = target.target_weight_ratio;
            kernel::copy_reason(intent.reason, sizeof(intent.reason), target.reason);
            ++intent_count;
        }
    }

    output->target_weight_count = security_count;
    output->order_intent_count = intent_count;
    (void)std::snprintf(output->diagnostics, sizeof(output->diagnostics),
                        "plan_ok securities=%zu investable=%zu facts=%zu intents=%zu",
                        security_count, investable_count, fact_count, intent_count);
    return FA_OK;
}

extern "C" fa_status_code fa_plan_v1(
    const fa_canonical_fact_v1* facts,
    size_t fact_count,
    const fa_security_v1* securities,
    size_t security_count,
    const fa_position_v1* positions,
    size_t position_count,
    const fa_model_config_v1* config,
    const fa_risk_limits_v1* risk_limits,
    fa_plan_output_v1* output) {
    return fa_build_portfolio_plan_v1(facts, fact_count, securities, security_count,
                                      positions, position_count, config, risk_limits,
                                      output);
}
