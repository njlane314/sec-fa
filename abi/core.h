#ifndef CORE_H
#define CORE_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define FA_ABI_VERSION 2u
#define FA_SYMBOL_BYTES 16u
#define FA_REASON_BYTES 128u
#define FA_DIAGNOSTIC_BYTES 1024u
#define FA_MAX_SCENARIOS 3u

typedef enum fa_status_code {
    FA_OK = 0,
    FA_ERR_NULL_ARGUMENT = 1,
    FA_ERR_BAD_ABI_VERSION = 2,
    FA_ERR_INVALID_INPUT = 3,
    FA_ERR_RESOURCE_LIMIT = 4,
    FA_ERR_INVARIANT_FAILED = 5,
    FA_ERR_ALLOCATION_FAILED = 6
} fa_status_code;

typedef enum fa_metric_id {
    FA_METRIC_REVENUE = 1,
    FA_METRIC_NET_INCOME = 2,
    FA_METRIC_EPS_DILUTED = 3,
    FA_METRIC_DILUTED_SHARES = 4,
    FA_METRIC_OPERATING_CASH_FLOW = 5,
    FA_METRIC_CAPEX = 6,
    FA_METRIC_CASH = 7,
    FA_METRIC_DEBT = 8
} fa_metric_id;

typedef enum fa_metric_kind {
    FA_METRIC_KIND_UNKNOWN = 0,
    FA_METRIC_KIND_FLOW = 1,
    FA_METRIC_KIND_INSTANT = 2,
    FA_METRIC_KIND_PER_SHARE_FLOW = 3,
    FA_METRIC_KIND_RATIO = 4,
    FA_METRIC_KIND_DERIVED = 5
} fa_metric_kind;

typedef enum fa_period_semantics {
    FA_PERIOD_UNKNOWN = 0,
    FA_PERIOD_FISCAL_QUARTER = 1,
    FA_PERIOD_FISCAL_YTD = 2,
    FA_PERIOD_FISCAL_YEAR = 3,
    FA_PERIOD_TTM = 4,
    FA_PERIOD_INSTANT = 5,
    FA_PERIOD_STUB = 6,
    FA_PERIOD_TRANSITION = 7,
    FA_PERIOD_IRREGULAR = 8
} fa_period_semantics;

typedef enum fa_observation_status {
    FA_OBSERVATION_UNKNOWN = 0,
    FA_OBSERVATION_SELECTED = 1,
    FA_OBSERVATION_DERIVED = 2,
    FA_OBSERVATION_REJECTED = 3,
    FA_OBSERVATION_SUPERSEDED = 4
} fa_observation_status;

typedef enum fa_basis_id {
    FA_BASIS_UNKNOWN = 0,
    FA_BASIS_REVENUE_CUSTOMER_CONTRACT_EXCLUDING_TAX = 101,
    FA_BASIS_REVENUE_CUSTOMER_CONTRACT_INCLUDING_TAX = 102,
    FA_BASIS_REVENUE_SALES_NET = 103,
    FA_BASIS_REVENUE_GENERIC = 104,
    FA_BASIS_NET_INCOME_STANDARD = 201,
    FA_BASIS_EPS_DILUTED_STANDARD = 301,
    FA_BASIS_DILUTED_SHARES_STANDARD = 401,
    FA_BASIS_OPERATING_CASH_FLOW_STANDARD = 501,
    FA_BASIS_CAPEX_STANDARD = 601,
    FA_BASIS_CASH_STANDARD = 701,
    FA_BASIS_DEBT_STANDARD = 801
} fa_basis_id;

typedef enum fa_order_side {
    FA_SIDE_NONE = 0,
    FA_SIDE_BUY = 1,
    FA_SIDE_SELL = 2
} fa_order_side;

typedef enum fa_decision_state {
    FA_DECISION_REJECTED = 0,
    FA_DECISION_APPROVED = 1
} fa_decision_state;

typedef enum fa_quality_flag {
    FA_QUALITY_NONE = 0u,
    FA_QUALITY_EXTENSION_TAG = 1u << 0,
    FA_QUALITY_UNIT_CONVERTED = 1u << 1,
    FA_QUALITY_AMENDED_FILING = 1u << 2,
    FA_QUALITY_RESTATEMENT = 1u << 3,
    FA_QUALITY_FISCAL_CHANGE = 1u << 4,
    FA_QUALITY_MISSING_COMPARABLE = 1u << 5,
    FA_QUALITY_STALE = 1u << 6,
    FA_QUALITY_LOW_CONFIDENCE = 1u << 7,
    FA_QUALITY_PERIOD_YTD = 1u << 8,
    FA_QUALITY_PERIOD_FISCAL_YEAR = 1u << 9,
    FA_QUALITY_PERIOD_STUB = 1u << 10,
    FA_QUALITY_PERIOD_IRREGULAR = 1u << 11,
    FA_QUALITY_DURATION_MISMATCH = 1u << 12,
    FA_QUALITY_DURATION_NORMALIZED = 1u << 13,
    FA_QUALITY_DERIVED = 1u << 14,
    FA_QUALITY_BASIS_TRANSITION = 1u << 15,
    FA_QUALITY_DIMENSIONAL_FACT = 1u << 16,
    FA_QUALITY_AMBIGUOUS_CANDIDATES = 1u << 17,
    FA_QUALITY_LOW_PRECISION = 1u << 18
} fa_quality_flag;

typedef enum fa_statement_quality_flag {
    FA_STMT_QUALITY_NONE = 0u,
    FA_STMT_MISSING_REVENUE = 1u << 0,
    FA_STMT_MISSING_SHARES = 1u << 1,
    FA_STMT_MISSING_CASH = 1u << 2,
    FA_STMT_MISSING_DEBT = 1u << 3,
    FA_STMT_NEGATIVE_FCF = 1u << 4,
    FA_STMT_NON_COMPARABLE_PERIOD = 1u << 5,
    FA_STMT_AMENDED_OR_RESTATED = 1u << 6,
    FA_STMT_LOW_CONFIDENCE = 1u << 7
} fa_statement_quality_flag;

typedef struct fa_canonical_fact_v1 {
    uint32_t abi_version;
    uint64_t security_id;
    int32_t metric_id;
    double value;
    int64_t period_start_day;
    int64_t period_end_day;
    int64_t available_at_epoch_s;
    uint32_t quality_flags;
    uint32_t metric_kind;
    uint32_t period_semantics;
    uint32_t basis_id;
    uint32_t observation_status;
    uint32_t duration_days;
    uint64_t dimensions_hash;
} fa_canonical_fact_v1;

typedef struct fa_security_v1 {
    uint32_t abi_version;
    uint64_t security_id;
    char symbol[FA_SYMBOL_BYTES];
    uint8_t investable;
    double price_usd;
    double adv_usd;
} fa_security_v1;

typedef struct fa_position_v1 {
    uint32_t abi_version;
    uint64_t security_id;
    double quantity_shares;
    double market_value_usd;
    double weight_ratio;
} fa_position_v1;

typedef struct fa_model_config_v1 {
    uint32_t abi_version;
    double target_gross_exposure_ratio;
    double max_name_weight_ratio;
    double min_expected_return_proxy;
    double min_abs_order_notional_usd;
    double max_forecast_abs_growth_ratio;
    int64_t max_fact_age_s;
} fa_model_config_v1;

typedef struct fa_risk_limits_v1 {
    uint32_t abi_version;
    double portfolio_value_usd;
    double cash_usd;
    double max_name_weight_ratio;
    double max_order_notional_usd;
    double min_adv_usd;
    double max_adv_participation_ratio;
    double min_abs_order_notional_usd;
    uint8_t broker_reconciled;
    int64_t reconciliation_checked_at_epoch_s;
    int64_t now_epoch_s;
    int64_t max_reconciliation_age_s;
} fa_risk_limits_v1;

typedef struct fa_forecast_v1 {
    uint32_t abi_version;
    uint64_t security_id;
    double revenue_growth_ratio;
    double earnings_growth_ratio;
    double expected_return_proxy;
    double confidence_ratio;
    uint32_t quality_flags;
    char reason[FA_REASON_BYTES];
} fa_forecast_v1;

typedef struct fa_target_weight_v1 {
    uint32_t abi_version;
    uint64_t security_id;
    double current_weight_ratio;
    double target_weight_ratio;
    double delta_weight_ratio;
    char reason[FA_REASON_BYTES];
} fa_target_weight_v1;

typedef struct fa_order_intent_v1 {
    uint32_t abi_version;
    uint64_t security_id;
    int32_t side;
    double notional_usd;
    double current_weight_ratio;
    double target_weight_ratio;
    char reason[FA_REASON_BYTES];
} fa_order_intent_v1;

typedef struct fa_statement_snapshot_v1 {
    uint32_t abi_version;
    uint64_t security_id;
    int64_t period_start_day;
    int64_t period_end_day;
    int64_t available_at_epoch_s;
    uint8_t is_ttm;
    double revenue_usd;
    double net_income_usd;
    double diluted_eps_usd;
    double diluted_shares;
    double operating_cash_flow_usd;
    double capex_usd;
    double free_cash_flow_usd;
    double cash_usd;
    double debt_usd;
    double net_debt_usd;
    uint32_t quality_flags;
} fa_statement_snapshot_v1;

typedef struct fa_valuation_scenario_v1 {
    uint32_t abi_version;
    double revenue_cagr_5y;
    double terminal_revenue_growth;
    double target_fcf_margin;
    double discount_rate;
    double terminal_fcf_multiple;
    double probability_weight;
} fa_valuation_scenario_v1;

typedef struct fa_valuation_v1 {
    uint32_t abi_version;
    uint64_t security_id;
    double current_price_usd;
    double intrinsic_value_per_share_usd;
    double expected_return_ratio;
    double market_cap_usd;
    double enterprise_value_usd;
    double fcf_yield_ratio;
    double net_debt_usd;
    double confidence_ratio;
    uint32_t quality_flags;
    char reason[FA_REASON_BYTES];
} fa_valuation_v1;

typedef struct fa_model_output_v1 {
    uint32_t abi_version;
    size_t forecast_count;
    fa_forecast_v1* forecasts;
    size_t target_weight_count;
    fa_target_weight_v1* target_weights;
    size_t order_intent_count;
    fa_order_intent_v1* order_intents;
    char diagnostics[FA_DIAGNOSTIC_BYTES];
} fa_model_output_v1;

typedef struct fa_model_output_v2 {
    uint32_t abi_version;
    size_t valuation_count;
    fa_valuation_v1* valuations;
    size_t target_weight_count;
    fa_target_weight_v1* target_weights;
    size_t order_intent_count;
    fa_order_intent_v1* order_intents;
    char diagnostics[FA_DIAGNOSTIC_BYTES];
} fa_model_output_v2;

typedef struct fa_risk_decision_v1 {
    uint32_t abi_version;
    uint64_t security_id;
    int32_t side;
    int32_t decision;
    double notional_usd;
    char reason[FA_REASON_BYTES];
} fa_risk_decision_v1;

typedef struct fa_risk_output_v1 {
    uint32_t abi_version;
    size_t decision_count;
    fa_risk_decision_v1* decisions;
    char diagnostics[FA_DIAGNOSTIC_BYTES];
} fa_risk_output_v1;

const char* fa_status_name(fa_status_code code);

fa_status_code fa_model_run_v1(
    const fa_canonical_fact_v1* facts,
    size_t fact_count,
    const fa_security_v1* securities,
    size_t security_count,
    const fa_position_v1* positions,
    size_t position_count,
    const fa_model_config_v1* config,
    const fa_risk_limits_v1* risk_limits,
    fa_model_output_v1* output);

void fa_model_output_free_v1(fa_model_output_v1* output);

fa_status_code fa_model_run_v2(
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
    fa_model_output_v2* output);

void fa_model_output_free_v2(fa_model_output_v2* output);

fa_status_code fa_risk_check_v1(
    const fa_order_intent_v1* order_intents,
    size_t order_intent_count,
    const fa_security_v1* securities,
    size_t security_count,
    const fa_risk_limits_v1* risk_limits,
    fa_risk_output_v1* output);

void fa_risk_output_free_v1(fa_risk_output_v1* output);

#ifdef __cplusplus
}
#endif

#endif
