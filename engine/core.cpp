#include "internal.h"

#include <cmath>
#include <cstdio>
#include <cstdlib>

namespace kernel {

bool finite_non_nan(double value) {
    return std::isfinite(value) != 0;
}

double abs_double(double value) {
    return value < 0.0 ? -value : value;
}

double clamp_double(double value, double low, double high) {
    if (value < low) {
        return low;
    }
    if (value > high) {
        return high;
    }
    return value;
}

void copy_reason(char* dst, size_t dst_size, const char* src) {
    if (dst == nullptr || dst_size == 0u) {
        return;
    }
    if (src == nullptr) {
        dst[0] = '\0';
        return;
    }
    (void)std::snprintf(dst, dst_size, "%s", src);
}

void set_diag(char* dst, size_t dst_size, const char* msg) {
    copy_reason(dst, dst_size, msg);
}

bool fact_has_valid_abi(const fa_canonical_fact_v1& fact) {
    return fact.abi_version == FA_ABI_VERSION;
}

bool security_has_valid_abi(const fa_security_v1& security) {
    return security.abi_version == FA_ABI_VERSION;
}

bool position_has_valid_abi(const fa_position_v1& position) {
    return position.abi_version == FA_ABI_VERSION;
}

bool order_intent_has_valid_abi(const fa_order_intent_v1& intent) {
    return intent.abi_version == FA_ABI_VERSION;
}

bool statement_has_valid_abi(const fa_statement_snapshot_v1& statement) {
    return statement.abi_version == FA_ABI_VERSION;
}

bool config_has_valid_abi(const fa_model_config_v1& config) {
    return config.abi_version == FA_ABI_VERSION;
}

bool limits_have_valid_abi(const fa_risk_limits_v1& limits) {
    return limits.abi_version == FA_ABI_VERSION;
}

bool observation_status_is_usable(uint32_t status) {
    return status == FA_OBSERVATION_SELECTED || status == FA_OBSERVATION_DERIVED;
}

bool metric_requires_duration(int32_t metric_id) {
    return metric_id == FA_METRIC_REVENUE || metric_id == FA_METRIC_NET_INCOME ||
           metric_id == FA_METRIC_EPS_DILUTED || metric_id == FA_METRIC_OPERATING_CASH_FLOW ||
           metric_id == FA_METRIC_CAPEX;
}

bool duration_is_quarter_like(uint32_t duration_days) {
    return duration_days >= 70u && duration_days <= 110u;
}

bool fact_is_model_comparable(const fa_canonical_fact_v1& fact, int32_t metric_id) {
    if (!observation_status_is_usable(fact.observation_status)) {
        return false;
    }
    if ((fact.quality_flags & FA_QUALITY_AMBIGUOUS_CANDIDATES) != 0u) {
        return false;
    }
    if (metric_requires_duration(metric_id)) {
        const bool kind_ok = fact.metric_kind == FA_METRIC_KIND_FLOW ||
                             fact.metric_kind == FA_METRIC_KIND_PER_SHARE_FLOW;
        return kind_ok && fact.period_semantics == FA_PERIOD_FISCAL_QUARTER &&
               duration_is_quarter_like(fact.duration_days);
    }
    return fact.metric_kind == FA_METRIC_KIND_INSTANT && fact.period_semantics == FA_PERIOD_INSTANT;
}

bool periods_are_comparable(const fa_canonical_fact_v1& lhs,
                            const fa_canonical_fact_v1& rhs) {
    if (lhs.metric_kind != rhs.metric_kind) {
        return false;
    }
    if (lhs.period_semantics != rhs.period_semantics) {
        return false;
    }
    if (lhs.basis_id != rhs.basis_id) {
        return false;
    }
    if (lhs.dimensions_hash != rhs.dimensions_hash) {
        return false;
    }
    const uint32_t lhs_duration = lhs.duration_days;
    const uint32_t rhs_duration = rhs.duration_days;
    if (lhs_duration == 0u || rhs_duration == 0u) {
        return false;
    }
    const uint32_t diff =
        lhs_duration > rhs_duration ? lhs_duration - rhs_duration : rhs_duration - lhs_duration;
    return diff <= 7u;
}

double current_weight_for_security(const fa_position_v1* positions,
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

const fa_security_v1* find_security(const fa_security_v1* securities,
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

}  // namespace kernel

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

extern "C" void fa_plan_output_free_v1(fa_plan_output_v1* output) {
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

extern "C" void fa_value_output_free_v1(fa_value_output_v1* output) {
    if (output == nullptr) {
        return;
    }
    std::free(output->valuations);
    std::free(output->target_weights);
    std::free(output->order_intents);
    output->valuations = nullptr;
    output->target_weights = nullptr;
    output->order_intents = nullptr;
    output->valuation_count = 0u;
    output->target_weight_count = 0u;
    output->order_intent_count = 0u;
    output->diagnostics[0] = '\0';
}

extern "C" void fa_gate_output_free_v1(fa_gate_output_v1* output) {
    if (output == nullptr) {
        return;
    }
    std::free(output->decisions);
    output->decisions = nullptr;
    output->decision_count = 0u;
    output->diagnostics[0] = '\0';
}
