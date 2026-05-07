#include "core.h"

#include <cstdlib>
#include <cmath>
#include <cstring>
#include <cstdio>

namespace {

void require(bool condition) { if (!condition) { std::abort(); } }

double absd(double v) { return v < 0.0 ? -v : v; }

bool contains(const char* text, const char* needle) {
    return std::strstr(text, needle) != nullptr;
}

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

fa_security_v1 security(uint64_t id, const char* symbol, double adv, uint8_t investable = 1u) {
    fa_security_v1 s{};
    s.abi_version = FA_ABI_VERSION;
    s.security_id = id;
    (void)std::snprintf(s.symbol, sizeof(s.symbol), "%s", symbol);
    s.investable = investable;
    s.price_usd = 100.0;
    s.adv_usd = adv;
    return s;
}

fa_position_v1 position(uint64_t security_id, double weight_ratio) {
    fa_position_v1 p{};
    p.abi_version = FA_ABI_VERSION;
    p.security_id = security_id;
    p.quantity_shares = 0.0;
    p.market_value_usd = 0.0;
    p.weight_ratio = weight_ratio;
    return p;
}

fa_order_intent_v1 intent(uint64_t security_id,
                          int32_t side,
                          double notional_usd,
                          double target_weight_ratio) {
    fa_order_intent_v1 i{};
    i.abi_version = FA_ABI_VERSION;
    i.security_id = security_id;
    i.side = side;
    i.notional_usd = notional_usd;
    i.current_weight_ratio = 0.0;
    i.target_weight_ratio = target_weight_ratio;
    return i;
}

fa_model_config_v1 model_config() {
    fa_model_config_v1 config{};
    config.abi_version = FA_ABI_VERSION;
    config.target_gross_exposure_ratio = 0.50;
    config.max_name_weight_ratio = 0.10;
    config.min_expected_return_proxy = 0.01;
    config.min_abs_order_notional_usd = 100.0;
    config.max_forecast_abs_growth_ratio = 2.0;
    config.max_fact_age_s = 0;
    return config;
}

fa_risk_limits_v1 fresh_limits() {
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
    return limits;
}

void expect_gate_decision(const fa_order_intent_v1& order_intent,
                          const fa_security_v1* securities,
                          size_t security_count,
                          const fa_risk_limits_v1& limits,
                          int32_t expected_decision,
                          const char* expected_reason) {
    fa_gate_output_v1 output{};
    const fa_status_code status =
        fa_check_risk_limits_v1(&order_intent, 1u, securities, security_count, &limits, &output);
    require(status == FA_OK);
    require(output.decision_count == 1u);
    require(output.decisions[0].decision == expected_decision);
    require(contains(output.decisions[0].reason, expected_reason));
    fa_gate_output_free_v1(&output);
}

void test_plan_and_gate_happy_path() {
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
    fa_security_v1 securities[2] = {
        security(1, "AAA", 10000000.0),
        security(2, "BBB", 10000000.0),
    };
    fa_position_v1 positions[1] = { position(1, 0.0) };
    fa_model_config_v1 config = model_config();
    fa_risk_limits_v1 limits = fresh_limits();

    fa_plan_output_v1 output{};
    const fa_status_code status = fa_build_portfolio_plan_v1(facts, 8, securities, 2, positions, 1, &config, &limits, &output);
    require(status == FA_OK);
    require(output.forecast_count == 2);
    require(output.target_weight_count == 2);
    require(output.order_intent_count == 1);
    require(output.order_intents[0].security_id == 1);
    require(output.order_intents[0].side == FA_SIDE_BUY);
    require(absd(output.order_intents[0].notional_usd - 10000.0) < 1.0);

    fa_gate_output_v1 risk{};
    const fa_status_code risk_status = fa_check_risk_limits_v1(output.order_intents, output.order_intent_count,
                                                        securities, 2, &limits, &risk);
    require(risk_status == FA_OK);
    require(risk.decision_count == 1);
    require(risk.decisions[0].decision == FA_DECISION_APPROVED);

    limits.reconciliation_checked_at_epoch_s = 1700000000;
    fa_gate_output_v1 stale{};
    const fa_status_code stale_status = fa_check_risk_limits_v1(output.order_intents, output.order_intent_count,
                                                         securities, 2, &limits, &stale);
    require(stale_status == FA_OK);
    require(stale.decision_count == 1);
    require(stale.decisions[0].decision == FA_DECISION_REJECTED);

    fa_gate_output_free_v1(&stale);
    fa_gate_output_free_v1(&risk);
    fa_plan_output_free_v1(&output);
}

void test_gate_rejects_hard_limits() {
    fa_security_v1 securities[4] = {
        security(1, "AAA", 10000000.0),
        security(2, "NOPE", 10000000.0, 0u),
        security(3, "LOW", 50000.0),
        security(4, "ADV", 10000000.0),
    };
    fa_risk_limits_v1 limits = fresh_limits();
    limits.cash_usd = 1000000.0;

    expect_gate_decision(intent(99, FA_SIDE_BUY, 1000.0, 0.01), securities, 4u, limits,
                         FA_DECISION_REJECTED, "security not found");
    expect_gate_decision(intent(2, FA_SIDE_BUY, 1000.0, 0.01), securities, 4u, limits,
                         FA_DECISION_REJECTED, "security not investable");
    expect_gate_decision(intent(1, FA_SIDE_NONE, 1000.0, 0.01), securities, 4u, limits,
                         FA_DECISION_REJECTED, "invalid order side");
    expect_gate_decision(intent(1, FA_SIDE_BUY, 99.0, 0.01), securities, 4u, limits,
                         FA_DECISION_REJECTED, "notional below minimum or invalid");

    fa_risk_limits_v1 capped_order = limits;
    capped_order.max_order_notional_usd = 1000.0;
    expect_gate_decision(intent(1, FA_SIDE_BUY, 1001.0, 0.01), securities, 4u, capped_order,
                         FA_DECISION_REJECTED, "order notional exceeds hard limit");

    expect_gate_decision(intent(1, FA_SIDE_BUY, 1000.0, 0.11), securities, 4u, limits,
                         FA_DECISION_REJECTED, "target weight exceeds name limit");
    expect_gate_decision(intent(3, FA_SIDE_BUY, 1000.0, 0.01), securities, 4u, limits,
                         FA_DECISION_REJECTED, "liquidity below minimum ADV");

    fa_risk_limits_v1 capped_adv = limits;
    capped_adv.max_adv_participation_ratio = 0.0001;
    expect_gate_decision(intent(4, FA_SIDE_BUY, 1001.0, 0.01), securities, 4u, capped_adv,
                         FA_DECISION_REJECTED, "order exceeds ADV participation limit");

    fa_risk_limits_v1 unreconciled = limits;
    unreconciled.broker_reconciled = 0u;
    expect_gate_decision(intent(1, FA_SIDE_BUY, 1000.0, 0.01), securities, 4u, unreconciled,
                         FA_DECISION_REJECTED, "broker reconciliation is missing or stale");
}

void test_gate_rejects_aggregate_buys_above_cash() {
    fa_security_v1 securities[2] = {
        security(1, "AAA", 10000000.0),
        security(2, "BBB", 10000000.0),
    };
    fa_order_intent_v1 orders[2] = {
        intent(1, FA_SIDE_BUY, 600.0, 0.006),
        intent(2, FA_SIDE_BUY, 600.0, 0.006),
    };

    fa_risk_limits_v1 limits = fresh_limits();
    limits.cash_usd = 1000.0;

    fa_gate_output_v1 rejected{};
    fa_status_code status = fa_check_risk_limits_v1(orders, 2u, securities, 2u, &limits, &rejected);
    require(status == FA_OK);
    require(rejected.decision_count == 2u);
    require(rejected.decisions[0].decision == FA_DECISION_REJECTED);
    require(rejected.decisions[1].decision == FA_DECISION_REJECTED);
    require(contains(rejected.decisions[0].reason, "aggregate buy notional exceeds cash"));
    require(contains(rejected.decisions[1].reason, "aggregate buy notional exceeds cash"));
    fa_gate_output_free_v1(&rejected);

    limits.cash_usd = 1200.0;
    fa_gate_output_v1 approved{};
    status = fa_check_risk_limits_v1(orders, 2u, securities, 2u, &limits, &approved);
    require(status == FA_OK);
    require(approved.decision_count == 2u);
    require(approved.decisions[0].decision == FA_DECISION_APPROVED);
    require(approved.decisions[1].decision == FA_DECISION_APPROVED);
    fa_gate_output_free_v1(&approved);
}

void test_plan_ignores_non_comparable_observations() {
    fa_canonical_fact_v1 facts[4] = {
        fact(1, FA_METRIC_REVENUE, 100.0, 1000),
        fact(1, FA_METRIC_REVENUE, 125.0, 1100),
        fact(1, FA_METRIC_NET_INCOME, 10.0, 1000),
        fact(1, FA_METRIC_NET_INCOME, 13.0, 1100),
    };
    facts[0].period_semantics = FA_PERIOD_FISCAL_YTD;
    facts[0].duration_days = 180u;
    facts[0].quality_flags = FA_QUALITY_PERIOD_YTD;
    facts[1].period_semantics = FA_PERIOD_FISCAL_YTD;
    facts[1].duration_days = 180u;
    facts[1].quality_flags = FA_QUALITY_PERIOD_YTD;
    facts[2].quality_flags = FA_QUALITY_AMBIGUOUS_CANDIDATES;
    facts[3].quality_flags = FA_QUALITY_AMBIGUOUS_CANDIDATES;

    fa_security_v1 securities[1] = { security(1, "AAA", 10000000.0) };
    fa_model_config_v1 config = model_config();
    fa_risk_limits_v1 limits = fresh_limits();

    fa_plan_output_v1 output{};
    const fa_status_code status =
        fa_build_portfolio_plan_v1(facts, 4u, securities, 1u, nullptr, 0u, &config, &limits, &output);
    require(status == FA_OK);
    require(output.forecast_count == 1u);
    require(output.target_weight_count == 1u);
    require(output.order_intent_count == 0u);
    require(output.forecasts[0].confidence_ratio == 0.0);
    require((output.forecasts[0].quality_flags & FA_QUALITY_MISSING_COMPARABLE) != 0u);
    require((output.forecasts[0].quality_flags & FA_QUALITY_LOW_CONFIDENCE) != 0u);
    require(output.target_weights[0].target_weight_ratio == 0.0);
    fa_plan_output_free_v1(&output);
}

void test_plan_rejects_relevant_fact_abi_mismatch() {
    fa_canonical_fact_v1 facts[1] = {
        fact(1, FA_METRIC_REVENUE, 100.0, 1000),
    };
    facts[0].abi_version = FA_ABI_VERSION + 1u;

    fa_security_v1 securities[1] = { security(1, "AAA", 10000000.0) };
    fa_model_config_v1 config = model_config();
    fa_risk_limits_v1 limits = fresh_limits();

    fa_plan_output_v1 output{};
    const fa_status_code status =
        fa_build_portfolio_plan_v1(facts, 1u, securities, 1u, nullptr, 0u, &config, &limits, &output);
    require(status == FA_ERR_BAD_ABI_VERSION);
    require(output.forecasts == nullptr);
    require(output.target_weights == nullptr);
    require(output.order_intents == nullptr);
    require(contains(output.diagnostics, "fact ABI version mismatch"));
    fa_plan_output_free_v1(&output);
}

}  // namespace

int main() {
    test_plan_and_gate_happy_path();
    test_gate_rejects_hard_limits();
    test_gate_rejects_aggregate_buys_above_cash();
    test_plan_ignores_non_comparable_observations();
    test_plan_rejects_relevant_fact_abi_mismatch();
    return 0;
}
