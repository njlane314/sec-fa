#include "internal.h"

#include <cstdio>
#include <cstdlib>

namespace {

using namespace kernel;

bool statement_usable_for_valuation(const fa_statement_snapshot_v1& statement) {
    if (!statement_has_valid_abi(statement)) {
        return false;
    }
    if (statement.security_id == 0u || statement.period_end_day <= 0 ||
        statement.available_at_epoch_s <= 0) {
        return false;
    }
    if (!finite_non_nan(statement.revenue_usd) || statement.revenue_usd <= 0.0) {
        return false;
    }
    if (!finite_non_nan(statement.diluted_shares) || statement.diluted_shares <= 0.0) {
        return false;
    }
    return true;
}

bool choose_latest_two_ttm_statements(const fa_statement_snapshot_v1* statements,
                                      size_t statement_count,
                                      uint64_t security_id,
                                      int64_t now_epoch_s,
                                      int64_t max_statement_age_s,
                                      StatementPair* out) {
    if (out == nullptr) {
        return false;
    }
    out->has_latest = false;
    out->has_previous = false;
    out->latest = {};
    out->previous = {};

    if (statements == nullptr) {
        return true;
    }

    for (size_t i = 0u; i < statement_count; ++i) {
        const fa_statement_snapshot_v1& statement = statements[i];
        if (statement.security_id != security_id || statement.is_ttm == 0u) {
            continue;
        }
        if (!statement_has_valid_abi(statement)) {
            return false;
        }
        if (!statement_usable_for_valuation(statement)) {
            continue;
        }
        if (max_statement_age_s > 0 && now_epoch_s > 0 &&
            (now_epoch_s - statement.available_at_epoch_s) > max_statement_age_s) {
            continue;
        }

        const bool newer_than_latest =
            (!out->has_latest) ||
            (statement.period_end_day > out->latest.period_end_day) ||
            (statement.period_end_day == out->latest.period_end_day &&
             statement.available_at_epoch_s > out->latest.available_at_epoch_s);

        if (newer_than_latest) {
            if (out->has_latest && out->latest.period_end_day != statement.period_end_day) {
                out->has_previous = true;
                out->previous = out->latest;
            }
            out->has_latest = true;
            out->latest = statement;
            continue;
        }

        if (statement.period_end_day != out->latest.period_end_day) {
            const bool newer_than_previous =
                (!out->has_previous) ||
                (statement.period_end_day > out->previous.period_end_day) ||
                (statement.period_end_day == out->previous.period_end_day &&
                 statement.available_at_epoch_s > out->previous.available_at_epoch_s);
            if (newer_than_previous) {
                out->has_previous = true;
                out->previous = statement;
            }
        }
    }

    return true;
}

double pow_int(double base, int exponent) {
    double result = 1.0;
    for (int i = 0; i < exponent; ++i) {
        result *= base;
    }
    return result;
}

bool valid_scenario(const fa_valuation_scenario_v1& scenario) {
    if (scenario.abi_version != FA_ABI_VERSION) {
        return false;
    }
    if (!finite_non_nan(scenario.revenue_cagr_5y) ||
        !finite_non_nan(scenario.terminal_revenue_growth) ||
        !finite_non_nan(scenario.target_fcf_margin) ||
        !finite_non_nan(scenario.discount_rate) ||
        !finite_non_nan(scenario.terminal_fcf_multiple) ||
        !finite_non_nan(scenario.probability_weight)) {
        return false;
    }
    if (scenario.revenue_cagr_5y < -0.80 || scenario.revenue_cagr_5y > 1.00) {
        return false;
    }
    if (scenario.terminal_revenue_growth < -0.20 || scenario.terminal_revenue_growth > 0.08) {
        return false;
    }
    if (scenario.target_fcf_margin < -0.50 || scenario.target_fcf_margin > 0.60) {
        return false;
    }
    if (scenario.discount_rate < 0.03 || scenario.discount_rate > 0.40) {
        return false;
    }
    if (scenario.terminal_fcf_multiple < 0.0 || scenario.terminal_fcf_multiple > 60.0) {
        return false;
    }
    if (scenario.probability_weight < 0.0 || scenario.probability_weight > 1.0) {
        return false;
    }
    return true;
}

double dcf_equity_value_usd(const fa_statement_snapshot_v1& latest,
                            const fa_valuation_scenario_v1& scenario) {
    double present_value_fcf = 0.0;
    double revenue = latest.revenue_usd;

    for (int year = 1; year <= 5; ++year) {
        revenue *= (1.0 + scenario.revenue_cagr_5y);
        const double fcf = revenue * scenario.target_fcf_margin;
        const double discount = pow_int(1.0 + scenario.discount_rate, year);
        present_value_fcf += fcf / discount;
    }

    const double year_5_fcf = revenue * scenario.target_fcf_margin;
    const double terminal_fcf = year_5_fcf * (1.0 + scenario.terminal_revenue_growth);
    const double terminal_value = terminal_fcf * scenario.terminal_fcf_multiple;
    const double discounted_terminal_value =
        terminal_value / pow_int(1.0 + scenario.discount_rate, 5);
    const double enterprise_value = present_value_fcf + discounted_terminal_value;

    return enterprise_value - latest.net_debt_usd;
}

double confidence_from_statement_quality(uint32_t quality_flags, bool has_previous) {
    double confidence = 0.90;
    if (!has_previous) {
        confidence *= 0.70;
    }
    if ((quality_flags & FA_STMT_MISSING_CASH) != 0u) {
        confidence *= 0.85;
    }
    if ((quality_flags & FA_STMT_MISSING_DEBT) != 0u) {
        confidence *= 0.85;
    }
    if ((quality_flags & FA_STMT_MISSING_OPERATING_CASH_FLOW) != 0u) {
        confidence *= 0.55;
    }
    if ((quality_flags & FA_STMT_MISSING_CAPEX) != 0u) {
        confidence *= 0.70;
    }
    if ((quality_flags & FA_STMT_NEGATIVE_FCF) != 0u) {
        confidence *= 0.75;
    }
    if ((quality_flags & FA_STMT_NON_COMPARABLE_PERIOD) != 0u) {
        confidence *= 0.60;
    }
    if ((quality_flags & FA_STMT_AMENDED_OR_RESTATED) != 0u) {
        confidence *= 0.65;
    }
    if ((quality_flags & FA_STMT_LOW_CONFIDENCE) != 0u) {
        confidence *= 0.50;
    }
    return clamp_double(confidence, 0.0, 1.0);
}

bool value_security(const fa_security_v1& security,
                    const StatementPair& pair,
                    const fa_valuation_scenario_v1* scenarios,
                    size_t scenario_count,
                    fa_valuation_v1* valuation) {
    if (valuation == nullptr) {
        return false;
    }

    *valuation = {};
    valuation->abi_version = FA_ABI_VERSION;
    valuation->security_id = security.security_id;
    valuation->current_price_usd = security.price_usd;
    valuation->quality_flags = FA_STMT_LOW_CONFIDENCE;
    copy_reason(valuation->reason, sizeof(valuation->reason), "not valued");

    if (security.investable == 0u) {
        copy_reason(valuation->reason, sizeof(valuation->reason), "not investable");
        return true;
    }
    if (!pair.has_latest || !statement_usable_for_valuation(pair.latest)) {
        copy_reason(valuation->reason, sizeof(valuation->reason), "missing usable statement");
        return true;
    }
    if (!finite_non_nan(security.price_usd) || security.price_usd <= 0.0) {
        copy_reason(valuation->reason, sizeof(valuation->reason), "missing market price");
        return true;
    }

    const fa_statement_snapshot_v1& latest = pair.latest;
    double scenario_weight_sum = 0.0;
    double weighted_equity_value = 0.0;

    for (size_t i = 0u; i < scenario_count; ++i) {
        if (!valid_scenario(scenarios[i])) {
            return false;
        }
        const double equity_value = dcf_equity_value_usd(latest, scenarios[i]);
        if (!finite_non_nan(equity_value)) {
            return false;
        }
        weighted_equity_value += equity_value * scenarios[i].probability_weight;
        scenario_weight_sum += scenarios[i].probability_weight;
    }

    if (scenario_weight_sum <= kTiny) {
        copy_reason(valuation->reason, sizeof(valuation->reason), "zero scenario weight");
        return true;
    }

    const double normalized_equity_value = weighted_equity_value / scenario_weight_sum;
    const double intrinsic_per_share = normalized_equity_value / latest.diluted_shares;
    const double market_cap = security.price_usd * latest.diluted_shares;

    valuation->intrinsic_value_per_share_usd = intrinsic_per_share;
    valuation->expected_return_ratio = (intrinsic_per_share - security.price_usd) / security.price_usd;
    valuation->market_cap_usd = market_cap;
    valuation->net_debt_usd = latest.net_debt_usd;
    valuation->enterprise_value_usd = market_cap + latest.net_debt_usd;
    valuation->fcf_yield_ratio = market_cap > kTiny ? latest.free_cash_flow_usd / market_cap : 0.0;
    valuation->quality_flags = latest.quality_flags;
    valuation->confidence_ratio = confidence_from_statement_quality(latest.quality_flags, pair.has_previous);

    if (valuation->expected_return_ratio > 0.15 && valuation->confidence_ratio >= 0.50) {
        copy_reason(valuation->reason, sizeof(valuation->reason),
                    "positive valuation spread with sufficient confidence");
    } else {
        copy_reason(valuation->reason, sizeof(valuation->reason),
                    "valuation spread or confidence below threshold");
    }

    return true;
}

double valuation_score(const fa_valuation_v1& valuation) {
    if (!finite_non_nan(valuation.expected_return_ratio) ||
        !finite_non_nan(valuation.confidence_ratio)) {
        return 0.0;
    }
    if (valuation.expected_return_ratio <= 0.0 || valuation.confidence_ratio < 0.50) {
        return 0.0;
    }

    double penalty = 1.0;
    if ((valuation.quality_flags & FA_STMT_NEGATIVE_FCF) != 0u) {
        penalty *= 0.50;
    }
    if ((valuation.quality_flags & FA_STMT_AMENDED_OR_RESTATED) != 0u) {
        penalty *= 0.50;
    }
    if ((valuation.quality_flags & FA_STMT_LOW_CONFIDENCE) != 0u) {
        penalty *= 0.25;
    }
    if (valuation.net_debt_usd > 0.0 && valuation.market_cap_usd > kTiny) {
        const double net_debt_to_market_cap =
            clamp_double(valuation.net_debt_usd / valuation.market_cap_usd, 0.0, 3.0);
        penalty *= 1.0 / (1.0 + net_debt_to_market_cap);
    }

    const double raw = valuation.expected_return_ratio * valuation.confidence_ratio * penalty;
    return clamp_double(raw, 0.0, 2.0);
}

double liquidity_adjusted_valuation_score(const fa_valuation_v1& valuation,
                                          const fa_security_v1& security,
                                          const fa_risk_limits_v1& limits) {
    if (!finite_non_nan(security.adv_usd) || security.adv_usd < limits.min_adv_usd) {
        return 0.0;
    }
    return valuation_score(valuation);
}

void build_targets_from_valuations(const fa_valuation_v1* valuations,
                                   size_t valuation_count,
                                   const fa_security_v1* securities,
                                   const fa_position_v1* positions,
                                   size_t position_count,
                                   const fa_model_config_v1& config,
                                   const fa_risk_limits_v1& limits,
                                   fa_value_output_v1* output) {
    double score_sum = 0.0;
    for (size_t i = 0u; i < valuation_count; ++i) {
        score_sum += liquidity_adjusted_valuation_score(valuations[i], securities[i], limits);
    }

    size_t intent_count = 0u;
    for (size_t i = 0u; i < valuation_count; ++i) {
        const fa_valuation_v1& valuation = valuations[i];
        const fa_security_v1& security = securities[i];
        fa_target_weight_v1& target = output->target_weights[i];
        target.abi_version = FA_ABI_VERSION;
        target.security_id = valuation.security_id;
        target.current_weight_ratio =
            current_weight_for_security(positions, position_count, valuation.security_id);
        target.target_weight_ratio = 0.0;
        target.delta_weight_ratio = -target.current_weight_ratio;
        copy_reason(target.reason, sizeof(target.reason), "zero target");

        const double score = liquidity_adjusted_valuation_score(valuation, security, limits);
        if (security.investable != 0u && score_sum > kTiny && score > 0.0) {
            const double raw_weight =
                config.target_gross_exposure_ratio * score / score_sum;
            target.target_weight_ratio =
                clamp_double(raw_weight, 0.0, config.max_name_weight_ratio);
            target.delta_weight_ratio =
                target.target_weight_ratio - target.current_weight_ratio;
            copy_reason(target.reason, sizeof(target.reason),
                        "valuation-weighted long-only target");
        }

        const double notional =
            abs_double(target.delta_weight_ratio * limits.portfolio_value_usd);
        if (notional >= config.min_abs_order_notional_usd) {
            fa_order_intent_v1& intent = output->order_intents[intent_count];
            intent.abi_version = FA_ABI_VERSION;
            intent.security_id = target.security_id;
            intent.side = target.delta_weight_ratio > 0.0 ? FA_SIDE_BUY : FA_SIDE_SELL;
            intent.notional_usd = notional;
            intent.current_weight_ratio = target.current_weight_ratio;
            intent.target_weight_ratio = target.target_weight_ratio;
            copy_reason(intent.reason, sizeof(intent.reason), target.reason);
            ++intent_count;
        }
    }

    output->target_weight_count = valuation_count;
    output->order_intent_count = intent_count;
}

bool validate_value_v1_inputs(const fa_statement_snapshot_v1* statements,
                                  size_t statement_count,
                                  const fa_security_v1* securities,
                                  size_t security_count,
                                  const fa_position_v1* positions,
                                  size_t position_count,
                                  const fa_valuation_scenario_v1* scenarios,
                                  size_t scenario_count,
                                  const fa_model_config_v1* config,
                                  const fa_risk_limits_v1* risk_limits,
                                  fa_value_output_v1* output) {
    if (output == nullptr || config == nullptr || risk_limits == nullptr) {
        return false;
    }
    if ((statement_count > 0u && statements == nullptr) ||
        (security_count > 0u && securities == nullptr) ||
        (position_count > 0u && positions == nullptr) ||
        (scenario_count > 0u && scenarios == nullptr)) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "null input array");
        return false;
    }
    if (statement_count > kMaxStatements || security_count > kMaxSecurities ||
        position_count > kMaxSecurities || scenario_count > static_cast<size_t>(FA_MAX_SCENARIOS)) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "resource limit exceeded");
        return false;
    }
    if (scenario_count == 0u) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "at least one valuation scenario is required");
        return false;
    }
    if (!config_has_valid_abi(*config) || !limits_have_valid_abi(*risk_limits)) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "ABI version mismatch");
        return false;
    }
    if (!finite_non_nan(risk_limits->portfolio_value_usd) ||
        risk_limits->portfolio_value_usd <= 0.0) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "portfolio_value_usd must be positive");
        return false;
    }
    if (!finite_non_nan(config->target_gross_exposure_ratio) ||
        config->target_gross_exposure_ratio < 0.0 ||
        config->target_gross_exposure_ratio > 1.0) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "target_gross_exposure_ratio out of range");
        return false;
    }
    if (!finite_non_nan(config->max_name_weight_ratio) ||
        config->max_name_weight_ratio <= 0.0 ||
        config->max_name_weight_ratio > 1.0) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "max_name_weight_ratio out of range");
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
        if (!finite_non_nan(positions[i].weight_ratio) ||
            abs_double(positions[i].weight_ratio) > 10.0) {
            set_diag(output->diagnostics, sizeof(output->diagnostics), "position weight out of range");
            return false;
        }
    }
    for (size_t i = 0u; i < statement_count; ++i) {
        if (!statement_has_valid_abi(statements[i])) {
            set_diag(output->diagnostics, sizeof(output->diagnostics), "statement ABI version mismatch");
            return false;
        }
    }
    for (size_t i = 0u; i < scenario_count; ++i) {
        if (!valid_scenario(scenarios[i])) {
            set_diag(output->diagnostics, sizeof(output->diagnostics), "invalid valuation scenario");
            return false;
        }
    }
    return true;
}

}  // namespace

extern "C" fa_status_code fa_build_valuation_plan_v1(
    const fa_statement_snapshot_v1* statements,
    size_t statement_count,
    const fa_security_v1* securities,
    size_t security_count,
    const fa_position_v1* positions,
    size_t position_count,
    const fa_valuation_scenario_v1* scenarios,
    size_t scenario_count,
    const fa_model_config_v1* config,
    const fa_risk_limits_v1* risk_limits,
    fa_value_output_v1* output) {

    if (output == nullptr) {
        return FA_ERR_NULL_ARGUMENT;
    }
    output->abi_version = FA_ABI_VERSION;
    output->valuation_count = 0u;
    output->valuations = nullptr;
    output->target_weight_count = 0u;
    output->target_weights = nullptr;
    output->order_intent_count = 0u;
    output->order_intents = nullptr;
    output->diagnostics[0] = '\0';

    if (config == nullptr || risk_limits == nullptr) {
        kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "null config or risk_limits");
        return FA_ERR_NULL_ARGUMENT;
    }
    if (!validate_value_v1_inputs(statements, statement_count, securities, security_count,
                                      positions, position_count, scenarios, scenario_count,
                                      config, risk_limits, output)) {
        return FA_ERR_INVALID_INPUT;
    }

    output->valuations = static_cast<fa_valuation_v1*>(
        std::calloc(security_count, sizeof(fa_valuation_v1)));
    output->target_weights = static_cast<fa_target_weight_v1*>(
        std::calloc(security_count, sizeof(fa_target_weight_v1)));
    output->order_intents = static_cast<fa_order_intent_v1*>(
        std::calloc(security_count, sizeof(fa_order_intent_v1)));
    if (security_count > 0u &&
        (output->valuations == nullptr || output->target_weights == nullptr ||
         output->order_intents == nullptr)) {
        fa_value_output_free_v1(output);
        kernel::set_diag(output->diagnostics, sizeof(output->diagnostics), "allocation failed");
        return FA_ERR_ALLOCATION_FAILED;
    }

    size_t investable_count = 0u;
    size_t valued_count = 0u;
    for (size_t i = 0u; i < security_count; ++i) {
        const fa_security_v1& security = securities[i];
        if (security.investable != 0u) {
            ++investable_count;
        }

        StatementPair pair{};
        const bool scan_ok = choose_latest_two_ttm_statements(
            statements, statement_count, security.security_id, risk_limits->now_epoch_s,
            config->max_fact_age_s, &pair);
        if (!scan_ok) {
            fa_value_output_free_v1(output);
            kernel::set_diag(output->diagnostics, sizeof(output->diagnostics),
                             "statement ABI version mismatch");
            return FA_ERR_BAD_ABI_VERSION;
        }

        if (!value_security(security, pair, scenarios, scenario_count, &output->valuations[i])) {
            fa_value_output_free_v1(output);
            kernel::set_diag(output->diagnostics, sizeof(output->diagnostics),
                             "valuation calculation failed");
            return FA_ERR_INVALID_INPUT;
        }
        if (output->valuations[i].confidence_ratio > 0.0 &&
            kernel::finite_non_nan(output->valuations[i].intrinsic_value_per_share_usd)) {
            ++valued_count;
        }
    }
    output->valuation_count = security_count;

    build_targets_from_valuations(output->valuations, output->valuation_count, securities,
                                  positions, position_count, *config, *risk_limits, output);

    (void)std::snprintf(output->diagnostics, sizeof(output->diagnostics),
                        "value_ok securities=%zu investable=%zu statements=%zu valuations=%zu intents=%zu",
                        security_count, investable_count, statement_count, valued_count,
                        output->order_intent_count);
    return FA_OK;
}
