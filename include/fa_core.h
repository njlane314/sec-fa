#ifndef FA_CORE_H
#define FA_CORE_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define FA_ABI_VERSION 1u
#define FA_SYMBOL_BYTES 16u
#define FA_REASON_BYTES 128u
#define FA_DIAGNOSTIC_BYTES 1024u

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
    FA_QUALITY_LOW_CONFIDENCE = 1u << 7
} fa_quality_flag;

typedef struct fa_canonical_fact_v1 {
    uint32_t abi_version;
    uint64_t security_id;
    int32_t metric_id;
    double value;
    int64_t period_start_day;
    int64_t period_end_day;
    int64_t available_at_epoch_s;
    uint32_t quality_flags;
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
