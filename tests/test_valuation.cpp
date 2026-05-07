#include "core.h"

#include <cmath>
#include <cstring>
#include <cstdio>
#include <cstdlib>

namespace {

void require(bool condition) {
    if (!condition) {
        std::abort();
    }
}

double absd(double value) {
    return value < 0.0 ? -value : value;
}

bool contains(const char* text, const char* needle) {
    return std::strstr(text, needle) != nullptr;
}

fa_security_v1 security(uint64_t id, const char* symbol, double price, double adv) {
    fa_security_v1 sec{};
    sec.abi_version = FA_ABI_VERSION;
    sec.security_id = id;
    (void)std::snprintf(sec.symbol, sizeof(sec.symbol), "%s", symbol);
    sec.investable = 1;
    sec.price_usd = price;
    sec.adv_usd = adv;
    return sec;
}

fa_statement_snapshot_v1 statement(uint64_t id, int64_t period_end_day) {
    fa_statement_snapshot_v1 statement{};
    statement.abi_version = FA_ABI_VERSION;
    statement.security_id = id;
    statement.period_start_day = period_end_day - 365;
    statement.period_end_day = period_end_day;
    statement.available_at_epoch_s = 1700000000 + period_end_day;
    statement.is_ttm = 1;
    statement.revenue_usd = 1000000000.0;
    statement.net_income_usd = 100000000.0;
    statement.diluted_eps_usd = 1.00;
    statement.diluted_shares = 100000000.0;
    statement.operating_cash_flow_usd = 160000000.0;
    statement.capex_usd = 40000000.0;
    statement.free_cash_flow_usd = 120000000.0;
    statement.cash_usd = 300000000.0;
    statement.debt_usd = 100000000.0;
    statement.net_debt_usd = statement.debt_usd - statement.cash_usd;
    statement.quality_flags = FA_STMT_QUALITY_NONE;
    return statement;
}

fa_valuation_scenario_v1 scenario(double revenue_cagr,
                                  double terminal_growth,
                                  double fcf_margin,
                                  double discount_rate,
                                  double terminal_multiple,
                                  double weight) {
    fa_valuation_scenario_v1 scenario{};
    scenario.abi_version = FA_ABI_VERSION;
    scenario.revenue_cagr_5y = revenue_cagr;
    scenario.terminal_revenue_growth = terminal_growth;
    scenario.target_fcf_margin = fcf_margin;
    scenario.discount_rate = discount_rate;
    scenario.terminal_fcf_multiple = terminal_multiple;
    scenario.probability_weight = weight;
    return scenario;
}

fa_position_v1 position(uint64_t security_id, double weight_ratio) {
    fa_position_v1 pos{};
    pos.abi_version = FA_ABI_VERSION;
    pos.security_id = security_id;
    pos.quantity_shares = 0.0;
    pos.market_value_usd = 0.0;
    pos.weight_ratio = weight_ratio;
    return pos;
}

void standard_scenarios(fa_valuation_scenario_v1* scenarios) {
    scenarios[0] = scenario(0.00, 0.00, 0.08, 0.12, 10.0, 0.25);
    scenarios[1] = scenario(0.04, 0.02, 0.12, 0.10, 16.0, 0.50);
    scenarios[2] = scenario(0.08, 0.03, 0.16, 0.09, 20.0, 0.25);
}

fa_model_config_v1 model_config() {
    fa_model_config_v1 config{};
    config.abi_version = FA_ABI_VERSION;
    config.target_gross_exposure_ratio = 0.50;
    config.max_name_weight_ratio = 0.10;
    config.min_expected_return_proxy = 0.05;
    config.min_abs_order_notional_usd = 100.0;
    config.max_forecast_abs_growth_ratio = 2.0;
    config.max_fact_age_s = 0;
    return config;
}

fa_risk_limits_v1 fresh_limits() {
    fa_risk_limits_v1 limits{};
    limits.abi_version = FA_ABI_VERSION;
    limits.portfolio_value_usd = 100000.0;
    limits.cash_usd = 100000.0;
    limits.max_name_weight_ratio = 0.10;
    limits.max_order_notional_usd = 20000.0;
    limits.min_adv_usd = 100000.0;
    limits.max_adv_participation_ratio = 0.01;
    limits.min_abs_order_notional_usd = 100.0;
    limits.broker_reconciled = 1;
    limits.reconciliation_checked_at_epoch_s = 1800000000;
    limits.now_epoch_s = 1800000100;
    limits.max_reconciliation_age_s = 3600;
    return limits;
}

void test_value_happy_path() {
    fa_security_v1 securities[1] = {
        security(1, "AAA", 10.0, 50000000.0),
    };

    fa_statement_snapshot_v1 statements[2] = {
        statement(1, 19365),
        statement(1, 19000),
    };

    fa_position_v1 positions[1] = { position(1, 0.0) };

    fa_valuation_scenario_v1 scenarios[3]{};
    standard_scenarios(scenarios);

    fa_model_config_v1 config = model_config();
    fa_risk_limits_v1 limits = fresh_limits();

    fa_value_output_v1 output{};
    const fa_status_code status = fa_value_v1(
        statements, 2, securities, 1, positions, 1, scenarios, 3, &config, &limits, &output);

    require(status == FA_OK);
    require(output.valuation_count == 1);
    require(output.target_weight_count == 1);
    require(output.order_intent_count == 1);
    require(output.valuations[0].security_id == 1);
    require(output.valuations[0].intrinsic_value_per_share_usd > 0.0);
    require(output.valuations[0].expected_return_ratio > 0.0);
    require(output.valuations[0].confidence_ratio >= 0.50);
    require(output.target_weights[0].target_weight_ratio > 0.0);
    require(output.target_weights[0].target_weight_ratio <= limits.max_name_weight_ratio);
    require(output.order_intents[0].side == FA_SIDE_BUY);
    require(absd(output.order_intents[0].notional_usd - 10000.0) < 1.0);

    fa_value_output_free_v1(&output);
}

void test_value_rejects_missing_or_invalid_scenarios() {
    fa_security_v1 securities[1] = {
        security(1, "AAA", 10.0, 50000000.0),
    };
    fa_statement_snapshot_v1 statements[1] = {
        statement(1, 19365),
    };
    fa_model_config_v1 config = model_config();
    fa_risk_limits_v1 limits = fresh_limits();

    fa_value_output_v1 missing{};
    fa_status_code status =
        fa_value_v1(statements, 1u, securities, 1u, nullptr, 0u, nullptr, 0u,
                    &config, &limits, &missing);
    require(status == FA_ERR_INVALID_INPUT);
    require(contains(missing.diagnostics, "at least one valuation scenario is required"));
    fa_value_output_free_v1(&missing);

    fa_valuation_scenario_v1 invalid[1] = {
        scenario(0.04, 0.02, 0.12, 0.01, 16.0, 1.00),
    };
    fa_value_output_v1 bad{};
    status = fa_value_v1(statements, 1u, securities, 1u, nullptr, 0u, invalid, 1u,
                         &config, &limits, &bad);
    require(status == FA_ERR_INVALID_INPUT);
    require(contains(bad.diagnostics, "invalid valuation scenario"));
    fa_value_output_free_v1(&bad);
}

void test_value_filters_stale_statements() {
    fa_security_v1 securities[1] = {
        security(1, "AAA", 10.0, 50000000.0),
    };
    fa_statement_snapshot_v1 statements[2] = {
        statement(1, 19365),
        statement(1, 19000),
    };
    fa_valuation_scenario_v1 scenarios[3]{};
    standard_scenarios(scenarios);
    fa_model_config_v1 config = model_config();
    config.max_fact_age_s = 3600;
    fa_risk_limits_v1 limits = fresh_limits();

    fa_value_output_v1 output{};
    const fa_status_code status =
        fa_value_v1(statements, 2u, securities, 1u, nullptr, 0u, scenarios, 3u,
                    &config, &limits, &output);
    require(status == FA_OK);
    require(output.valuation_count == 1u);
    require(output.target_weight_count == 1u);
    require(output.order_intent_count == 0u);
    require(output.valuations[0].confidence_ratio == 0.0);
    require(contains(output.valuations[0].reason, "missing usable statement"));
    require(output.target_weights[0].target_weight_ratio == 0.0);
    fa_value_output_free_v1(&output);
}

void test_value_low_confidence_statement_does_not_create_intent() {
    fa_security_v1 securities[1] = {
        security(1, "AAA", 10.0, 50000000.0),
    };
    fa_statement_snapshot_v1 statements[2] = {
        statement(1, 19365),
        statement(1, 19000),
    };
    statements[0].quality_flags = FA_STMT_LOW_CONFIDENCE | FA_STMT_AMENDED_OR_RESTATED;

    fa_valuation_scenario_v1 scenarios[3]{};
    standard_scenarios(scenarios);
    fa_model_config_v1 config = model_config();
    fa_risk_limits_v1 limits = fresh_limits();

    fa_value_output_v1 output{};
    const fa_status_code status =
        fa_value_v1(statements, 2u, securities, 1u, nullptr, 0u, scenarios, 3u,
                    &config, &limits, &output);
    require(status == FA_OK);
    require(output.valuation_count == 1u);
    require(output.target_weight_count == 1u);
    require(output.order_intent_count == 0u);
    require(output.valuations[0].expected_return_ratio > 0.0);
    require(output.valuations[0].confidence_ratio < 0.50);
    require((output.valuations[0].quality_flags & FA_STMT_LOW_CONFIDENCE) != 0u);
    require(output.target_weights[0].target_weight_ratio == 0.0);
    fa_value_output_free_v1(&output);
}

}  // namespace

int main() {
    test_value_happy_path();
    test_value_rejects_missing_or_invalid_scenarios();
    test_value_filters_stale_statements();
    test_value_low_confidence_statement_does_not_create_intent();
    return 0;
}
