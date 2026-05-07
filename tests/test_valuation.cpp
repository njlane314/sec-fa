#include "core.h"

#include <cmath>
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

}  // namespace

int main() {
    fa_security_v1 securities[1] = {
        security(1, "AAA", 10.0, 50000000.0),
    };

    fa_statement_snapshot_v1 statements[2] = {
        statement(1, 19365),
        statement(1, 19000),
    };

    fa_position_v1 positions[1] = {};
    positions[0].abi_version = FA_ABI_VERSION;
    positions[0].security_id = 1;
    positions[0].quantity_shares = 0.0;
    positions[0].market_value_usd = 0.0;
    positions[0].weight_ratio = 0.0;

    fa_valuation_scenario_v1 scenarios[3] = {
        scenario(0.00, 0.00, 0.08, 0.12, 10.0, 0.25),
        scenario(0.04, 0.02, 0.12, 0.10, 16.0, 0.50),
        scenario(0.08, 0.03, 0.16, 0.09, 20.0, 0.25),
    };

    fa_model_config_v1 config{};
    config.abi_version = FA_ABI_VERSION;
    config.target_gross_exposure_ratio = 0.50;
    config.max_name_weight_ratio = 0.10;
    config.min_expected_return_proxy = 0.05;
    config.min_abs_order_notional_usd = 100.0;
    config.max_forecast_abs_growth_ratio = 2.0;
    config.max_fact_age_s = 0;

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
    return 0;
}
