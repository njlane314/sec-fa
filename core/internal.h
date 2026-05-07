#ifndef CORE_INTERNAL_H
#define CORE_INTERNAL_H

#include "core.h"

#include <stddef.h>
#include <stdint.h>

#if defined(__GNUC__) || defined(__clang__)
#define CORE_INTERNAL_SYMBOL __attribute__((visibility("hidden")))
#else
#define CORE_INTERNAL_SYMBOL
#endif

namespace kernel {

constexpr double kTiny = 1.0e-12;
constexpr size_t kMaxSecurities = 20000u;
constexpr size_t kMaxFacts = 5000000u;
constexpr size_t kMaxIntents = 20000u;
constexpr size_t kMaxStatements = 200000u;

struct MetricPair {
    bool has_latest;
    bool has_previous;
    double latest_value;
    double previous_value;
    int64_t latest_end_day;
    int64_t previous_end_day;
    int64_t latest_available_at;
    uint32_t latest_duration_days;
    uint32_t latest_metric_kind;
    uint32_t latest_period_semantics;
    uint32_t latest_basis_id;
    uint64_t latest_dimensions_hash;
    uint32_t quality_flags;
};

struct StatementPair {
    bool has_latest;
    bool has_previous;
    fa_statement_snapshot_v1 latest;
    fa_statement_snapshot_v1 previous;
};

CORE_INTERNAL_SYMBOL bool finite_non_nan(double value);
CORE_INTERNAL_SYMBOL double abs_double(double value);
CORE_INTERNAL_SYMBOL double clamp_double(double value, double low, double high);
CORE_INTERNAL_SYMBOL void copy_reason(char* dst, size_t dst_size, const char* src);
CORE_INTERNAL_SYMBOL void set_diag(char* dst, size_t dst_size, const char* msg);

CORE_INTERNAL_SYMBOL bool fact_has_valid_abi(const fa_canonical_fact_v1& fact);
CORE_INTERNAL_SYMBOL bool security_has_valid_abi(const fa_security_v1& security);
CORE_INTERNAL_SYMBOL bool position_has_valid_abi(const fa_position_v1& position);
CORE_INTERNAL_SYMBOL bool order_intent_has_valid_abi(const fa_order_intent_v1& intent);
CORE_INTERNAL_SYMBOL bool statement_has_valid_abi(const fa_statement_snapshot_v1& statement);
CORE_INTERNAL_SYMBOL bool config_has_valid_abi(const fa_model_config_v1& config);
CORE_INTERNAL_SYMBOL bool limits_have_valid_abi(const fa_risk_limits_v1& limits);

CORE_INTERNAL_SYMBOL bool observation_status_is_usable(uint32_t status);
CORE_INTERNAL_SYMBOL bool metric_requires_duration(int32_t metric_id);
CORE_INTERNAL_SYMBOL bool duration_is_quarter_like(uint32_t duration_days);
CORE_INTERNAL_SYMBOL bool fact_is_model_comparable(const fa_canonical_fact_v1& fact, int32_t metric_id);
CORE_INTERNAL_SYMBOL bool periods_are_comparable(const fa_canonical_fact_v1& lhs,
                                                const fa_canonical_fact_v1& rhs);
CORE_INTERNAL_SYMBOL double current_weight_for_security(const fa_position_v1* positions,
                                                       size_t position_count,
                                                       uint64_t security_id);
CORE_INTERNAL_SYMBOL const fa_security_v1* find_security(const fa_security_v1* securities,
                                                        size_t security_count,
                                                        uint64_t security_id);

}  // namespace kernel

#undef CORE_INTERNAL_SYMBOL

#endif
