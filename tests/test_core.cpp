#include "core.h"

#include <cstdlib>
#include <cmath>
#include <cstring>
#include <cstdio>

namespace {

void require(bool condition) { if (!condition) { std::abort(); } }

double absd(double v) { return v < 0.0 ? -v : v; }

fa_canonical_fact_v1 fact(uint64_t security_id, int32_t metric_id, double value, int64_t end_day) {
    fa_canonical_fact_v1 f{};
    f.abi_version = FA_ABI_VERSION;
    f.security_id = security_id;
    f.metric_id = metric_id;
    f.value = value;
    f.period_start_day = end_day - 90;
    f.period_end_day = end_day;
    f.available_at_epoch_s = 1700000000 + end_day;
    f.quality_flags = FA_QUALITY_NONE;
    f.metric_kind = FA_METRIC_KIND_FLOW;
    f.period_semantics = FA_PERIOD_FISCAL_QUARTER;
    f.basis_id = metric_id == FA_METRIC_REVENUE ? FA_BASIS_REVENUE_CUSTOMER_CONTRACT_EXCLUDING_TAX : FA_BASIS_NET_INCOME_STANDARD;
    f.observation_status = FA_OBSERVATION_SELECTED;
    f.duration_days = 91u;
    f.dimensions_hash = 0x44136fa355b3678au;
    return f;
}

fa_security_v1 security(uint64_t id, const char* symbol, double adv) {
    fa_security_v1 s{};
    s.abi_version = FA_ABI_VERSION;
    s.security_id = id;
    (void)std::snprintf(s.symbol, sizeof(s.symbol), "%s", symbol);
    s.investable = 1;
    s.price_usd = 100.0;
    s.adv_usd = adv;
    return s;
}

}  // namespace

int main() {
    fa_canonical_fact_v1 facts[8] = {
        fact(1, FA_METRIC_REVENUE, 100.0, 1000),
        fact(1, FA_METRIC_REVENUE, 125.0, 1100),
        fact(1, FA_METRIC_NET_INCOME, 10.0, 1000),
        fact(1, FA_METRIC_NET_INCOME, 13.0, 1100),
        fact(2, FA_METRIC_REVENUE, 100.0, 1000),
        fact(2, FA_METRIC_REVENUE, 90.0, 1100),
        fact(2, FA_METRIC_NET_INCOME, 10.0, 1000),
        fact(2, FA_METRIC_NET_INCOME, 9.0, 1100),
    };
    fa_security_v1 securities[2] = { security(1, "AAA", 10000000.0), security(2, "BBB", 10000000.0) };
    fa_position_v1 positions[1] = {};
    positions[0].abi_version = FA_ABI_VERSION;
    positions[0].security_id = 1;
    positions[0].quantity_shares = 0.0;
    positions[0].market_value_usd = 0.0;
    positions[0].weight_ratio = 0.0;

    fa_model_config_v1 config{};
    config.abi_version = FA_ABI_VERSION;
    config.target_gross_exposure_ratio = 0.50;
    config.max_name_weight_ratio = 0.10;
    config.min_expected_return_proxy = 0.01;
    config.min_abs_order_notional_usd = 100.0;
    config.max_forecast_abs_growth_ratio = 2.0;
    config.max_fact_age_s = 0;

    fa_risk_limits_v1 limits{};
    limits.abi_version = FA_ABI_VERSION;
    limits.portfolio_value_usd = 100000.0;
    limits.cash_usd = 50000.0;
    limits.max_name_weight_ratio = 0.10;
    limits.max_order_notional_usd = 20000.0;
    limits.min_adv_usd = 100000.0;
    limits.max_adv_participation_ratio = 0.02;
    limits.min_abs_order_notional_usd = 100.0;
    limits.broker_reconciled = 1;
    limits.reconciliation_checked_at_epoch_s = 1800000000;
    limits.now_epoch_s = 1800000100;
    limits.max_reconciliation_age_s = 3600;

    fa_model_output_v1 output{};
    const fa_status_code status = fa_model_run_v1(facts, 8, securities, 2, positions, 1, &config, &limits, &output);
    require(status == FA_OK);
    require(output.forecast_count == 2);
    require(output.target_weight_count == 2);
    require(output.order_intent_count == 1);
    require(output.order_intents[0].security_id == 1);
    require(output.order_intents[0].side == FA_SIDE_BUY);
    require(absd(output.order_intents[0].notional_usd - 10000.0) < 1.0);

    fa_risk_output_v1 risk{};
    const fa_status_code risk_status = fa_risk_check_v1(output.order_intents, output.order_intent_count,
                                                        securities, 2, &limits, &risk);
    require(risk_status == FA_OK);
    require(risk.decision_count == 1);
    require(risk.decisions[0].decision == FA_DECISION_APPROVED);

    limits.reconciliation_checked_at_epoch_s = 1700000000;
    fa_risk_output_v1 stale{};
    const fa_status_code stale_status = fa_risk_check_v1(output.order_intents, output.order_intent_count,
                                                         securities, 2, &limits, &stale);
    require(stale_status == FA_OK);
    require(stale.decision_count == 1);
    require(stale.decisions[0].decision == FA_DECISION_REJECTED);

    fa_risk_output_free_v1(&stale);
    fa_risk_output_free_v1(&risk);
    fa_model_output_free_v1(&output);
    return 0;
}
