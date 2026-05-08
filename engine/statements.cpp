#include "internal.h"

#include <cstdio>
#include <cstdlib>

namespace {

using namespace kernel;

constexpr size_t kMaxAnchorsPerSecurity = 12u;
constexpr size_t kMaxStatementsPerSecurity = 12u;
constexpr uint32_t kStatementLowConfidenceObservationFlags =
    FA_QUALITY_LOW_CONFIDENCE | FA_QUALITY_MISSING_COMPARABLE |
    FA_QUALITY_DURATION_MISMATCH | FA_QUALITY_BASIS_TRANSITION |
    FA_QUALITY_PERIOD_STUB | FA_QUALITY_PERIOD_IRREGULAR;

struct MetricPoint {
    bool ok;
    double value;
    int64_t start_day;
    int64_t end_day;
    int64_t available_at;
    uint32_t quality_flags;
};

bool fact_is_fresh_enough(const fa_canonical_fact_v1& fact,
                          int64_t now_epoch_s,
                          int64_t max_fact_age_s) {
    if (max_fact_age_s <= 0 || now_epoch_s <= 0 || fact.available_at_epoch_s <= 0) {
        return true;
    }
    return (now_epoch_s - fact.available_at_epoch_s) <= max_fact_age_s;
}

bool basic_fact_usable(const fa_canonical_fact_v1& fact,
                       uint64_t security_id,
                       int32_t metric_id,
                       int64_t anchor_day,
                       int64_t now_epoch_s,
                       int64_t max_fact_age_s) {
    if (fact.security_id != security_id || fact.metric_id != metric_id) {
        return false;
    }
    if (!finite_non_nan(fact.value) || fact.period_end_day <= 0 ||
        fact.period_end_day > anchor_day || fact.available_at_epoch_s <= 0) {
        return false;
    }
    if (!observation_status_is_usable(fact.observation_status)) {
        return false;
    }
    if ((fact.quality_flags & FA_QUALITY_AMBIGUOUS_CANDIDATES) != 0u) {
        return false;
    }
    return fact_is_fresh_enough(fact, now_epoch_s, max_fact_age_s);
}

bool flow_kind_ok(uint32_t metric_kind) {
    return metric_kind == FA_METRIC_KIND_FLOW ||
           metric_kind == FA_METRIC_KIND_PER_SHARE_FLOW;
}

bool quarter_flow_fact_usable(const fa_canonical_fact_v1& fact,
                              uint64_t security_id,
                              int32_t metric_id,
                              int64_t anchor_day,
                              int64_t now_epoch_s,
                              int64_t max_fact_age_s) {
    return basic_fact_usable(fact, security_id, metric_id, anchor_day, now_epoch_s,
                             max_fact_age_s) &&
           flow_kind_ok(fact.metric_kind) &&
           fact.period_semantics == FA_PERIOD_FISCAL_QUARTER &&
           duration_is_quarter_like(fact.duration_days);
}

bool annual_flow_fact_usable(const fa_canonical_fact_v1& fact,
                             uint64_t security_id,
                             int32_t metric_id,
                             int64_t anchor_day,
                             int64_t now_epoch_s,
                             int64_t max_fact_age_s) {
    return basic_fact_usable(fact, security_id, metric_id, anchor_day, now_epoch_s,
                             max_fact_age_s) &&
           flow_kind_ok(fact.metric_kind) &&
           fact.period_semantics == FA_PERIOD_FISCAL_YEAR &&
           fact.duration_days >= 330u && fact.duration_days <= 380u;
}

bool ytd_flow_fact_usable(const fa_canonical_fact_v1& fact,
                          uint64_t security_id,
                          int32_t metric_id,
                          int64_t anchor_day,
                          int64_t now_epoch_s,
                          int64_t max_fact_age_s) {
    return basic_fact_usable(fact, security_id, metric_id, anchor_day, now_epoch_s,
                             max_fact_age_s) &&
           flow_kind_ok(fact.metric_kind) &&
           fact.period_semantics == FA_PERIOD_FISCAL_YTD &&
           fact.duration_days >= 130u && fact.duration_days <= 300u;
}

bool revenue_anchor_fact_usable(const fa_canonical_fact_v1& fact,
                                uint64_t security_id,
                                int64_t now_epoch_s,
                                int64_t max_fact_age_s) {
    return quarter_flow_fact_usable(fact, security_id, FA_METRIC_REVENUE,
                                    fact.period_end_day, now_epoch_s,
                                    max_fact_age_s) ||
           annual_flow_fact_usable(fact, security_id, FA_METRIC_REVENUE,
                                   fact.period_end_day, now_epoch_s,
                                   max_fact_age_s);
}

void insert_anchor_desc(int64_t* anchors, size_t* count, int64_t anchor_day) {
    if (anchors == nullptr || count == nullptr || anchor_day <= 0) {
        return;
    }
    for (size_t i = 0u; i < *count; ++i) {
        if (anchors[i] == anchor_day) {
            return;
        }
    }
    size_t pos = 0u;
    while (pos < *count && anchors[pos] > anchor_day) {
        ++pos;
    }
    if (pos >= kMaxAnchorsPerSecurity) {
        return;
    }
    const size_t new_count =
        *count < kMaxAnchorsPerSecurity ? *count + 1u : kMaxAnchorsPerSecurity;
    size_t i = new_count;
    while (i > pos + 1u) {
        anchors[i - 1u] = anchors[i - 2u];
        --i;
    }
    anchors[pos] = anchor_day;
    *count = new_count;
}

void insert_quarter_desc(const fa_canonical_fact_v1& fact,
                         fa_canonical_fact_v1* quarters,
                         size_t* count) {
    if (quarters == nullptr || count == nullptr) {
        return;
    }
    for (size_t i = 0u; i < *count; ++i) {
        if (quarters[i].period_end_day == fact.period_end_day) {
            if (fact.available_at_epoch_s > quarters[i].available_at_epoch_s) {
                quarters[i] = fact;
            }
            return;
        }
    }

    size_t pos = 0u;
    while (pos < *count && quarters[pos].period_end_day > fact.period_end_day) {
        ++pos;
    }
    if (pos >= 4u) {
        return;
    }
    const size_t new_count = *count < 4u ? *count + 1u : 4u;
    size_t i = new_count;
    while (i > pos + 1u) {
        quarters[i - 1u] = quarters[i - 2u];
        --i;
    }
    quarters[pos] = fact;
    *count = new_count;
}

uint32_t statement_quality_from_observations(uint32_t flags) {
    uint32_t out = FA_STMT_QUALITY_NONE;
    if ((flags & (FA_QUALITY_AMENDED_FILING | FA_QUALITY_RESTATEMENT)) != 0u) {
        out |= FA_STMT_AMENDED_OR_RESTATED;
    }
    if ((flags & FA_QUALITY_DERIVED) != 0u) {
        out |= FA_STMT_DERIVED_TTM;
    }
    if ((flags & kStatementLowConfidenceObservationFlags) != 0u) {
        out |= FA_STMT_LOW_CONFIDENCE;
    }
    if ((flags & (FA_QUALITY_DURATION_MISMATCH | FA_QUALITY_BASIS_TRANSITION |
                  FA_QUALITY_PERIOD_STUB | FA_QUALITY_PERIOD_IRREGULAR)) != 0u) {
        out |= FA_STMT_NON_COMPARABLE_PERIOD;
    }
    return out;
}

bool close_days(int64_t lhs, int64_t rhs, int64_t tolerance_days) {
    const int64_t diff = lhs > rhs ? lhs - rhs : rhs - lhs;
    return diff <= tolerance_days;
}

bool flow_identity_compatible(const fa_canonical_fact_v1& lhs,
                              const fa_canonical_fact_v1& rhs) {
    return lhs.metric_id == rhs.metric_id &&
           lhs.metric_kind == rhs.metric_kind &&
           lhs.basis_id == rhs.basis_id &&
           lhs.dimensions_hash == rhs.dimensions_hash;
}

MetricPoint point_from_fact(const fa_canonical_fact_v1& fact) {
    MetricPoint out{};
    out.ok = true;
    out.value = fact.value;
    out.start_day = fact.period_start_day > 0 ? fact.period_start_day : fact.period_end_day;
    out.end_day = fact.period_end_day;
    out.available_at = fact.available_at_epoch_s;
    out.quality_flags = fact.quality_flags;
    return out;
}

bool choose_exact_annual_flow(const fa_canonical_fact_v1* facts,
                              size_t fact_count,
                              uint64_t security_id,
                              int32_t metric_id,
                              int64_t anchor_day,
                              int64_t now_epoch_s,
                              int64_t max_fact_age_s,
                              MetricPoint* out) {
    bool found = false;
    fa_canonical_fact_v1 selected{};
    for (size_t i = 0u; i < fact_count; ++i) {
        const fa_canonical_fact_v1& fact = facts[i];
        if (fact.period_end_day != anchor_day) {
            continue;
        }
        if (!annual_flow_fact_usable(fact, security_id, metric_id, anchor_day,
                                     now_epoch_s, max_fact_age_s)) {
            continue;
        }
        if (!found || fact.available_at_epoch_s > selected.available_at_epoch_s) {
            selected = fact;
            found = true;
        }
    }
    if (found && out != nullptr) {
        *out = point_from_fact(selected);
    }
    return found;
}

bool choose_ytd_bridge_ttm_flow(const fa_canonical_fact_v1* facts,
                                size_t fact_count,
                                uint64_t security_id,
                                int32_t metric_id,
                                int64_t anchor_day,
                                int64_t now_epoch_s,
                                int64_t max_fact_age_s,
                                MetricPoint* out) {
    bool found = false;
    MetricPoint selected{};

    for (size_t i = 0u; i < fact_count; ++i) {
        const fa_canonical_fact_v1& current = facts[i];
        if (current.period_end_day != anchor_day) {
            continue;
        }
        const bool current_is_ytd =
            ytd_flow_fact_usable(current, security_id, metric_id, anchor_day,
                                 now_epoch_s, max_fact_age_s);
        const bool current_is_q1 =
            quarter_flow_fact_usable(current, security_id, metric_id, anchor_day,
                                     now_epoch_s, max_fact_age_s);
        if (!current_is_ytd && !current_is_q1) {
            continue;
        }

        for (size_t j = 0u; j < fact_count; ++j) {
            const fa_canonical_fact_v1& annual = facts[j];
            if (!annual_flow_fact_usable(annual, security_id, metric_id,
                                         anchor_day, now_epoch_s, max_fact_age_s)) {
                continue;
            }
            if (!flow_identity_compatible(current, annual)) {
                continue;
            }
            if (annual.period_end_day >= current.period_end_day ||
                !close_days(annual.period_end_day + 1, current.period_start_day, 7)) {
                continue;
            }

            bool prior_found = false;
            fa_canonical_fact_v1 prior{};
            for (size_t k = 0u; k < fact_count; ++k) {
                const fa_canonical_fact_v1& candidate = facts[k];
                if (!basic_fact_usable(candidate, security_id, metric_id,
                                       annual.period_end_day, now_epoch_s,
                                       max_fact_age_s)) {
                    continue;
                }
                if (!flow_kind_ok(candidate.metric_kind) ||
                    candidate.period_semantics != current.period_semantics ||
                    !flow_identity_compatible(candidate, current)) {
                    continue;
                }
                if (!close_days(candidate.period_start_day, annual.period_start_day, 7) ||
                    !close_days(candidate.duration_days, current.duration_days, 7)) {
                    continue;
                }
                if (candidate.period_end_day >= annual.period_end_day) {
                    continue;
                }
                if (!prior_found ||
                    candidate.available_at_epoch_s > prior.available_at_epoch_s) {
                    prior = candidate;
                    prior_found = true;
                }
            }
            if (!prior_found) {
                continue;
            }

            MetricPoint candidate{};
            candidate.ok = true;
            candidate.value = annual.value + current.value - prior.value;
            candidate.start_day = annual.period_start_day > 0 ? annual.period_start_day
                                                               : annual.period_end_day;
            candidate.end_day = anchor_day;
            candidate.available_at = annual.available_at_epoch_s;
            if (current.available_at_epoch_s > candidate.available_at) {
                candidate.available_at = current.available_at_epoch_s;
            }
            if (prior.available_at_epoch_s > candidate.available_at) {
                candidate.available_at = prior.available_at_epoch_s;
            }
            candidate.quality_flags =
                annual.quality_flags | current.quality_flags | prior.quality_flags |
                FA_QUALITY_DERIVED;

            if (!found || candidate.available_at > selected.available_at) {
                selected = candidate;
                found = true;
            }
        }
    }

    if (found && out != nullptr) {
        *out = selected;
    }
    return found;
}

bool choose_quarter_ttm_flow(const fa_canonical_fact_v1* facts,
                             size_t fact_count,
                             uint64_t security_id,
                             int32_t metric_id,
                             int64_t anchor_day,
                             int64_t now_epoch_s,
                             int64_t max_fact_age_s,
                             MetricPoint* out) {
    fa_canonical_fact_v1 quarters[4]{};
    size_t quarter_count = 0u;

    for (size_t i = 0u; i < fact_count; ++i) {
        const fa_canonical_fact_v1& fact = facts[i];
        if (!quarter_flow_fact_usable(fact, security_id, metric_id, anchor_day,
                                      now_epoch_s, max_fact_age_s)) {
            continue;
        }
        insert_quarter_desc(fact, quarters, &quarter_count);
    }

    if (quarter_count < 4u || quarters[0].period_end_day != anchor_day) {
        return false;
    }
    for (size_t i = 1u; i < 4u; ++i) {
        if (!periods_are_comparable(quarters[0], quarters[i])) {
            return false;
        }
    }

    MetricPoint result{};
    result.ok = true;
    result.value = 0.0;
    result.start_day = quarters[0].period_start_day;
    result.end_day = anchor_day;
    result.available_at = 0;
    result.quality_flags = FA_QUALITY_NONE;
    for (size_t i = 0u; i < 4u; ++i) {
        result.value += quarters[i].value;
        if (quarters[i].period_start_day > 0 &&
            (result.start_day <= 0 || quarters[i].period_start_day < result.start_day)) {
            result.start_day = quarters[i].period_start_day;
        }
        if (quarters[i].available_at_epoch_s > result.available_at) {
            result.available_at = quarters[i].available_at_epoch_s;
        }
        result.quality_flags |= quarters[i].quality_flags;
    }

    if (out != nullptr) {
        *out = result;
    }
    return true;
}

MetricPoint latest_flow_ttm(const fa_canonical_fact_v1* facts,
                            size_t fact_count,
                            uint64_t security_id,
                            int32_t metric_id,
                            int64_t anchor_day,
                            int64_t now_epoch_s,
                            int64_t max_fact_age_s) {
    MetricPoint out{};
    if (choose_exact_annual_flow(facts, fact_count, security_id, metric_id,
                                 anchor_day, now_epoch_s, max_fact_age_s, &out)) {
        return out;
    }
    if (choose_quarter_ttm_flow(facts, fact_count, security_id, metric_id,
                                anchor_day, now_epoch_s, max_fact_age_s, &out)) {
        return out;
    }
    if (choose_ytd_bridge_ttm_flow(facts, fact_count, security_id, metric_id,
                                   anchor_day, now_epoch_s, max_fact_age_s, &out)) {
        return out;
    }
    return {};
}

bool point_fact_usable(const fa_canonical_fact_v1& fact,
                       uint64_t security_id,
                       int32_t metric_id,
                       int64_t anchor_day,
                       int64_t now_epoch_s,
                       int64_t max_fact_age_s) {
    if (!basic_fact_usable(fact, security_id, metric_id, anchor_day, now_epoch_s,
                           max_fact_age_s)) {
        return false;
    }
    if (metric_id == FA_METRIC_CASH || metric_id == FA_METRIC_DEBT) {
        return fact.metric_kind == FA_METRIC_KIND_INSTANT &&
               fact.period_semantics == FA_PERIOD_INSTANT;
    }
    if (metric_id == FA_METRIC_DILUTED_SHARES) {
        return flow_kind_ok(fact.metric_kind) &&
               (fact.period_semantics == FA_PERIOD_FISCAL_QUARTER ||
                fact.period_semantics == FA_PERIOD_FISCAL_YTD ||
                fact.period_semantics == FA_PERIOD_FISCAL_YEAR);
    }
    return false;
}

MetricPoint latest_point(const fa_canonical_fact_v1* facts,
                         size_t fact_count,
                         uint64_t security_id,
                         int32_t metric_id,
                         int64_t anchor_day,
                         int64_t now_epoch_s,
                         int64_t max_fact_age_s) {
    bool found = false;
    fa_canonical_fact_v1 selected{};
    for (size_t i = 0u; i < fact_count; ++i) {
        const fa_canonical_fact_v1& fact = facts[i];
        if (!point_fact_usable(fact, security_id, metric_id, anchor_day,
                               now_epoch_s, max_fact_age_s)) {
            continue;
        }
        const bool newer =
            !found || fact.period_end_day > selected.period_end_day ||
            (fact.period_end_day == selected.period_end_day &&
             fact.available_at_epoch_s > selected.available_at_epoch_s);
        if (newer) {
            selected = fact;
            found = true;
        }
    }
    return found ? point_from_fact(selected) : MetricPoint{};
}

int64_t max_available_at(const MetricPoint& a,
                         const MetricPoint& b,
                         const MetricPoint& c,
                         const MetricPoint& d,
                         const MetricPoint& e,
                         const MetricPoint& f,
                         const MetricPoint& g,
                         const MetricPoint& h) {
    int64_t out = 0;
    const MetricPoint points[8] = {a, b, c, d, e, f, g, h};
    for (size_t i = 0u; i < 8u; ++i) {
        if (points[i].ok && points[i].available_at > out) {
            out = points[i].available_at;
        }
    }
    return out;
}

uint32_t aggregate_observation_quality(const MetricPoint& a,
                                       const MetricPoint& b,
                                       const MetricPoint& c,
                                       const MetricPoint& d,
                                       const MetricPoint& e,
                                       const MetricPoint& f,
                                       const MetricPoint& g,
                                       const MetricPoint& h) {
    uint32_t out = FA_QUALITY_NONE;
    const MetricPoint points[8] = {a, b, c, d, e, f, g, h};
    for (size_t i = 0u; i < 8u; ++i) {
        if (points[i].ok) {
            out |= points[i].quality_flags;
        }
    }
    return out;
}

bool build_statement_for_anchor(const fa_canonical_fact_v1* facts,
                                size_t fact_count,
                                uint64_t security_id,
                                int64_t anchor_day,
                                int64_t now_epoch_s,
                                int64_t max_fact_age_s,
                                fa_statement_snapshot_v1* out) {
    if (out == nullptr) {
        return false;
    }

    const MetricPoint revenue =
        latest_flow_ttm(facts, fact_count, security_id, FA_METRIC_REVENUE,
                        anchor_day, now_epoch_s, max_fact_age_s);
    const MetricPoint net_income =
        latest_flow_ttm(facts, fact_count, security_id, FA_METRIC_NET_INCOME,
                        anchor_day, now_epoch_s, max_fact_age_s);
    const MetricPoint eps =
        latest_flow_ttm(facts, fact_count, security_id, FA_METRIC_EPS_DILUTED,
                        anchor_day, now_epoch_s, max_fact_age_s);
    const MetricPoint ocf =
        latest_flow_ttm(facts, fact_count, security_id,
                        FA_METRIC_OPERATING_CASH_FLOW, anchor_day, now_epoch_s,
                        max_fact_age_s);
    const MetricPoint capex =
        latest_flow_ttm(facts, fact_count, security_id, FA_METRIC_CAPEX,
                        anchor_day, now_epoch_s, max_fact_age_s);
    const MetricPoint shares =
        latest_point(facts, fact_count, security_id, FA_METRIC_DILUTED_SHARES,
                     anchor_day, now_epoch_s, max_fact_age_s);
    const MetricPoint cash =
        latest_point(facts, fact_count, security_id, FA_METRIC_CASH,
                     anchor_day, now_epoch_s, max_fact_age_s);
    const MetricPoint debt =
        latest_point(facts, fact_count, security_id, FA_METRIC_DEBT,
                     anchor_day, now_epoch_s, max_fact_age_s);

    if (!revenue.ok && !net_income.ok && !ocf.ok) {
        return false;
    }

    uint32_t quality = statement_quality_from_observations(
        aggregate_observation_quality(revenue, net_income, eps, ocf, capex,
                                      shares, cash, debt));
    if (!revenue.ok || revenue.value <= 0.0) {
        quality |= FA_STMT_MISSING_REVENUE | FA_STMT_LOW_CONFIDENCE;
    }
    if (!shares.ok || shares.value <= 0.0) {
        quality |= FA_STMT_MISSING_SHARES | FA_STMT_LOW_CONFIDENCE;
    }
    if (!ocf.ok) {
        quality |= FA_STMT_MISSING_OPERATING_CASH_FLOW | FA_STMT_LOW_CONFIDENCE;
    }
    if (!capex.ok) {
        quality |= FA_STMT_MISSING_CAPEX | FA_STMT_LOW_CONFIDENCE;
    }
    if (!cash.ok) {
        quality |= FA_STMT_MISSING_CASH;
    }
    if (!debt.ok) {
        quality |= FA_STMT_MISSING_DEBT;
    }

    const double fcf = (ocf.ok && capex.ok) ? ocf.value - capex.value : 0.0;
    if (ocf.ok && capex.ok && fcf < 0.0) {
        quality |= FA_STMT_NEGATIVE_FCF;
    }

    *out = {};
    out->abi_version = FA_ABI_VERSION;
    out->security_id = security_id;
    out->period_start_day = revenue.ok && revenue.start_day > 0 ? revenue.start_day : anchor_day;
    out->period_end_day = anchor_day;
    out->available_at_epoch_s =
        max_available_at(revenue, net_income, eps, ocf, capex, shares, cash, debt);
    out->is_ttm = 1u;
    out->revenue_usd = revenue.ok ? revenue.value : 0.0;
    out->net_income_usd = net_income.ok ? net_income.value : 0.0;
    out->diluted_eps_usd = eps.ok ? eps.value : 0.0;
    out->diluted_shares = shares.ok ? shares.value : 0.0;
    out->operating_cash_flow_usd = ocf.ok ? ocf.value : 0.0;
    out->capex_usd = capex.ok ? capex.value : 0.0;
    out->free_cash_flow_usd = fcf;
    out->cash_usd = cash.ok ? cash.value : 0.0;
    out->debt_usd = debt.ok ? debt.value : 0.0;
    out->net_debt_usd = out->debt_usd - out->cash_usd;
    out->quality_flags = quality;
    return out->available_at_epoch_s > 0;
}

bool validate_builder_inputs(const fa_canonical_fact_v1* facts,
                             size_t fact_count,
                             const fa_security_v1* securities,
                             size_t security_count,
                             const fa_model_config_v1* config,
                             const fa_risk_limits_v1* risk_limits,
                             fa_statement_build_output_v1* output) {
    if (output == nullptr || config == nullptr || risk_limits == nullptr) {
        return false;
    }
    if ((fact_count > 0u && facts == nullptr) ||
        (security_count > 0u && securities == nullptr)) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "null input array");
        return false;
    }
    if (fact_count > kMaxFacts || security_count > kMaxSecurities ||
        security_count > (kMaxStatements / kMaxStatementsPerSecurity)) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "resource limit exceeded");
        return false;
    }
    if (!config_has_valid_abi(*config) || !limits_have_valid_abi(*risk_limits)) {
        set_diag(output->diagnostics, sizeof(output->diagnostics), "ABI version mismatch");
        return false;
    }
    for (size_t i = 0u; i < fact_count; ++i) {
        if (!fact_has_valid_abi(facts[i])) {
            set_diag(output->diagnostics, sizeof(output->diagnostics), "fact ABI version mismatch");
            return false;
        }
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
    return true;
}

}  // namespace

extern "C" fa_status_code fa_build_statement_snapshots_v1(
    const fa_canonical_fact_v1* facts,
    size_t fact_count,
    const fa_security_v1* securities,
    size_t security_count,
    const fa_model_config_v1* config,
    const fa_risk_limits_v1* risk_limits,
    fa_statement_build_output_v1* output) {

    if (output == nullptr) {
        return FA_ERR_NULL_ARGUMENT;
    }
    output->abi_version = FA_ABI_VERSION;
    output->statement_count = 0u;
    output->statements = nullptr;
    output->diagnostics[0] = '\0';

    if (config == nullptr || risk_limits == nullptr) {
        kernel::set_diag(output->diagnostics, sizeof(output->diagnostics),
                         "null config or risk_limits");
        return FA_ERR_NULL_ARGUMENT;
    }
    if (!validate_builder_inputs(facts, fact_count, securities, security_count,
                                 config, risk_limits, output)) {
        return FA_ERR_INVALID_INPUT;
    }

    const size_t capacity = security_count * kMaxStatementsPerSecurity;
    output->statements = static_cast<fa_statement_snapshot_v1*>(
        std::calloc(capacity, sizeof(fa_statement_snapshot_v1)));
    if (capacity > 0u && output->statements == nullptr) {
        kernel::set_diag(output->diagnostics, sizeof(output->diagnostics),
                         "allocation failed");
        return FA_ERR_ALLOCATION_FAILED;
    }

    size_t statement_count = 0u;
    size_t investable_count = 0u;
    const int64_t now_epoch_s = risk_limits->now_epoch_s;
    const int64_t max_fact_age_s = config->max_fact_age_s;

    for (size_t i = 0u; i < security_count; ++i) {
        const fa_security_v1& security = securities[i];
        if (security.investable == 0u) {
            continue;
        }
        ++investable_count;

        int64_t anchors[kMaxAnchorsPerSecurity]{};
        size_t anchor_count = 0u;
        for (size_t j = 0u; j < fact_count; ++j) {
            if (revenue_anchor_fact_usable(facts[j], security.security_id,
                                           now_epoch_s, max_fact_age_s)) {
                insert_anchor_desc(anchors, &anchor_count, facts[j].period_end_day);
            }
        }

        size_t built_for_security = 0u;
        for (size_t j = 0u; j < anchor_count &&
                           built_for_security < kMaxStatementsPerSecurity; ++j) {
            fa_statement_snapshot_v1 statement{};
            if (!build_statement_for_anchor(facts, fact_count, security.security_id,
                                            anchors[j], now_epoch_s, max_fact_age_s,
                                            &statement)) {
                continue;
            }
            output->statements[statement_count] = statement;
            ++statement_count;
            ++built_for_security;
        }
    }

    output->statement_count = statement_count;
    (void)std::snprintf(output->diagnostics, sizeof(output->diagnostics),
                        "statements_ok securities=%zu investable=%zu facts=%zu statements=%zu",
                        security_count, investable_count, fact_count, statement_count);
    return FA_OK;
}
