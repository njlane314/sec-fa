CREATE TABLE IF NOT EXISTS events (
    id BIGSERIAL PRIMARY KEY,
    event_id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    event_version INTEGER NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    payload_json JSONB NOT NULL,
    payload_sha256 TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS securities (
    security_id BIGINT PRIMARY KEY,
    cik TEXT NOT NULL UNIQUE,
    symbol TEXT NOT NULL,
    investable BOOLEAN NOT NULL DEFAULT FALSE,
    price_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    adv_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS source_documents (
    doc_id TEXT PRIMARY KEY,
    cik TEXT NOT NULL,
    accession_number TEXT,
    form TEXT,
    filed_at TIMESTAMPTZ,
    source_url TEXT NOT NULL,
    local_path TEXT NOT NULL,
    sha256 TEXT NOT NULL UNIQUE,
    document_kind TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS filings (
    accession_number TEXT PRIMARY KEY,
    cik TEXT NOT NULL,
    form TEXT NOT NULL,
    filing_date DATE,
    accepted_at TIMESTAMPTZ,
    primary_document TEXT,
    source_url TEXT,
    raw_index_uri TEXT,
    raw_primary_uri TEXT,
    raw_sha256 TEXT,
    ingested_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_filings_cik_form_date ON filings(cik, form, filing_date);

CREATE TABLE IF NOT EXISTS metric_definitions (
    metric_id INTEGER PRIMARY KEY,
    metric_name TEXT NOT NULL UNIQUE,
    metric_kind TEXT NOT NULL,
    normal_unit TEXT NOT NULL,
    additivity_class TEXT NOT NULL,
    default_dimension_policy TEXT NOT NULL,
    description TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS measurement_bases (
    basis_id INTEGER PRIMARY KEY,
    metric_id INTEGER NOT NULL REFERENCES metric_definitions(metric_id),
    basis_name TEXT NOT NULL,
    accounting_scope TEXT NOT NULL,
    tax_treatment TEXT NOT NULL,
    preferred BOOLEAN NOT NULL DEFAULT FALSE,
    notes TEXT NOT NULL,
    UNIQUE(metric_id, basis_name)
);

CREATE TABLE IF NOT EXISTS metric_concept_candidates (
    candidate_id TEXT PRIMARY KEY,
    metric_id INTEGER NOT NULL REFERENCES metric_definitions(metric_id),
    basis_id INTEGER NOT NULL REFERENCES measurement_bases(basis_id),
    taxonomy TEXT NOT NULL,
    concept_qname TEXT NOT NULL,
    priority INTEGER NOT NULL,
    allowed_units_json JSONB NOT NULL,
    dimension_policy TEXT NOT NULL,
    allowed_forms_json JSONB,
    notes TEXT NOT NULL,
    UNIQUE(metric_id, taxonomy, concept_qname, basis_id)
);

CREATE TABLE IF NOT EXISTS dimension_signatures (
    dimensions_hash TEXT PRIMARY KEY,
    dimensions_json JSONB NOT NULL,
    has_dimensions BOOLEAN NOT NULL,
    scope_class TEXT NOT NULL,
    axis_count INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS reporting_periods (
    period_id TEXT PRIMARY KEY,
    cik TEXT NOT NULL,
    accession_number TEXT,
    raw_start_date DATE,
    raw_end_date DATE,
    raw_instant_date DATE,
    start_date_inclusive DATE,
    end_date_exclusive DATE,
    duration_days INTEGER NOT NULL DEFAULT 0,
    period_kind TEXT NOT NULL,
    period_semantics TEXT NOT NULL,
    fiscal_year INTEGER,
    fiscal_period TEXT,
    fiscal_period_ordinal INTEGER,
    period_length_class TEXT NOT NULL,
    source TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    CHECK (
        (period_kind = 'instant' AND raw_instant_date IS NOT NULL)
        OR
        (period_kind = 'duration' AND raw_start_date IS NOT NULL AND raw_end_date IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_reporting_periods_lookup
    ON reporting_periods(cik, period_semantics, fiscal_year, fiscal_period, raw_end_date);

CREATE TABLE IF NOT EXISTS xbrl_contexts (
    context_id TEXT PRIMARY KEY,
    cik TEXT NOT NULL,
    accession_number TEXT,
    raw_context_id TEXT NOT NULL,
    entity_identifier TEXT NOT NULL,
    period_id TEXT NOT NULL REFERENCES reporting_periods(period_id),
    period_kind TEXT NOT NULL,
    raw_start_date DATE,
    raw_end_date DATE,
    raw_instant_date DATE,
    start_date_inclusive DATE,
    end_date_exclusive DATE,
    duration_days INTEGER NOT NULL DEFAULT 0,
    dimensions_hash TEXT NOT NULL REFERENCES dimension_signatures(dimensions_hash),
    dimensions_json JSONB NOT NULL,
    segment_json JSONB,
    scenario_json JSONB,
    raw_context_sha256 TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS xbrl_units (
    unit_id TEXT PRIMARY KEY,
    cik TEXT NOT NULL,
    accession_number TEXT,
    raw_unit_id TEXT NOT NULL,
    unit_signature TEXT NOT NULL,
    numerator_json JSONB,
    denominator_json JSONB,
    raw_unit_sha256 TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS xbrl_facts (
    fact_id TEXT PRIMARY KEY,
    source_doc_id TEXT REFERENCES source_documents(doc_id),
    security_id BIGINT NOT NULL,
    cik TEXT NOT NULL,
    accession_number TEXT,
    taxonomy TEXT NOT NULL,
    concept_qname TEXT NOT NULL,
    concept_local_name TEXT NOT NULL,
    context_id TEXT NOT NULL REFERENCES xbrl_contexts(context_id),
    unit_id TEXT REFERENCES xbrl_units(unit_id),
    value_decimal NUMERIC,
    value_text TEXT,
    decimals_attr TEXT,
    precision_attr TEXT,
    is_nil BOOLEAN NOT NULL DEFAULT FALSE,
    raw_fact_hash TEXT NOT NULL UNIQUE,
    raw_context_id TEXT NOT NULL,
    form TEXT,
    filed_at TIMESTAMPTZ,
    accepted_at TIMESTAMPTZ,
    available_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_xbrl_facts_lookup
    ON xbrl_facts(security_id, taxonomy, concept_qname, available_at);

CREATE TABLE IF NOT EXISTS canonical_observations (
    observation_id TEXT PRIMARY KEY,
    observation_hash TEXT NOT NULL UNIQUE,
    resolver_version TEXT NOT NULL,
    security_id BIGINT NOT NULL,
    cik TEXT NOT NULL,
    metric_id INTEGER NOT NULL REFERENCES metric_definitions(metric_id),
    metric_name TEXT NOT NULL,
    metric_kind TEXT NOT NULL,
    basis_id INTEGER NOT NULL REFERENCES measurement_bases(basis_id),
    period_id TEXT NOT NULL REFERENCES reporting_periods(period_id),
    period_semantics TEXT NOT NULL,
    duration_days INTEGER NOT NULL DEFAULT 0,
    fiscal_year INTEGER,
    fiscal_period TEXT,
    value_decimal NUMERIC NOT NULL,
    unit_signature TEXT NOT NULL,
    dimensions_hash TEXT NOT NULL REFERENCES dimension_signatures(dimensions_hash),
    dimensional_scope TEXT NOT NULL,
    observation_status TEXT NOT NULL CHECK (observation_status IN ('selected', 'derived', 'rejected', 'superseded')),
    source_fact_id TEXT REFERENCES xbrl_facts(fact_id),
    source_accession TEXT,
    taxonomy TEXT,
    concept_qname TEXT,
    selection_reason TEXT NOT NULL,
    quality_flags INTEGER NOT NULL DEFAULT 0,
    quality_flags_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    quality_score DOUBLE PRECISION,
    accepted_at TIMESTAMPTZ,
    available_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_canonical_observations_lookup
    ON canonical_observations(security_id, metric_id, period_semantics, fiscal_year, fiscal_period, available_at);

CREATE TABLE IF NOT EXISTS observation_lineage (
    child_observation_id TEXT NOT NULL REFERENCES canonical_observations(observation_id),
    parent_observation_id TEXT NOT NULL REFERENCES canonical_observations(observation_id),
    operation TEXT NOT NULL,
    operand_role TEXT NOT NULL,
    rule_version TEXT NOT NULL,
    PRIMARY KEY(child_observation_id, parent_observation_id, operand_role)
);

CREATE TABLE IF NOT EXISTS canonical_observation_exceptions (
    exception_id TEXT PRIMARY KEY,
    resolver_version TEXT NOT NULL,
    cik TEXT NOT NULL,
    accession_number TEXT,
    metric_id INTEGER NOT NULL,
    period_id TEXT,
    exception_type TEXT NOT NULL,
    candidate_fact_ids_json JSONB NOT NULL,
    explanation TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS basis_compatibility_rules (
    rule_id TEXT PRIMARY KEY,
    metric_id INTEGER NOT NULL REFERENCES metric_definitions(metric_id),
    left_basis_id INTEGER NOT NULL REFERENCES measurement_bases(basis_id),
    right_basis_id INTEGER NOT NULL REFERENCES measurement_bases(basis_id),
    operation TEXT NOT NULL,
    compatible BOOLEAN NOT NULL,
    required_flag TEXT,
    notes TEXT NOT NULL
);

CREATE OR REPLACE VIEW canonical_facts AS
SELECT
    observation_id AS fact_id,
    security_id,
    metric_id,
    value_decimal::DOUBLE PRECISION AS value,
    (SELECT raw_start_date FROM reporting_periods WHERE reporting_periods.period_id = canonical_observations.period_id) AS period_start_date,
    (SELECT raw_end_date FROM reporting_periods WHERE reporting_periods.period_id = canonical_observations.period_id) AS period_end_date,
    available_at,
    source_accession AS accession_number,
    taxonomy,
    concept_qname AS tag,
    unit_signature AS unit,
    quality_flags,
    observation_hash AS fact_hash,
    NULL::TEXT AS source_event_id,
    created_at AS inserted_at
FROM canonical_observations
WHERE observation_status IN ('selected', 'derived');

CREATE TABLE IF NOT EXISTS universe_snapshots (
    snapshot_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    rule_json JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS universe_members (
    snapshot_id TEXT NOT NULL,
    security_id BIGINT NOT NULL,
    PRIMARY KEY(snapshot_id, security_id)
);

CREATE TABLE IF NOT EXISTS positions (
    security_id BIGINT PRIMARY KEY,
    quantity_shares DOUBLE PRECISION NOT NULL,
    market_value_usd DOUBLE PRECISION NOT NULL,
    weight_ratio DOUBLE PRECISION NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS broker_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    reconciled BOOLEAN NOT NULL,
    checked_at TIMESTAMPTZ NOT NULL,
    portfolio_value_usd DOUBLE PRECISION NOT NULL,
    cash_usd DOUBLE PRECISION NOT NULL
);

CREATE TABLE IF NOT EXISTS model_runs (
    run_id TEXT PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL,
    config_json JSONB NOT NULL,
    diagnostics TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS forecasts (
    run_id TEXT NOT NULL,
    security_id BIGINT NOT NULL,
    revenue_growth_ratio DOUBLE PRECISION NOT NULL,
    earnings_growth_ratio DOUBLE PRECISION NOT NULL,
    expected_return_proxy DOUBLE PRECISION NOT NULL,
    confidence_ratio DOUBLE PRECISION NOT NULL,
    quality_flags INTEGER NOT NULL,
    reason TEXT NOT NULL,
    PRIMARY KEY(run_id, security_id)
);

CREATE TABLE IF NOT EXISTS statement_snapshots (
    snapshot_id TEXT PRIMARY KEY,
    security_id BIGINT NOT NULL,
    period_start_date DATE,
    period_end_date DATE NOT NULL,
    available_at TIMESTAMPTZ NOT NULL,
    is_ttm BOOLEAN NOT NULL,
    revenue_usd DOUBLE PRECISION NOT NULL,
    net_income_usd DOUBLE PRECISION NOT NULL,
    diluted_eps_usd DOUBLE PRECISION NOT NULL,
    diluted_shares DOUBLE PRECISION NOT NULL,
    operating_cash_flow_usd DOUBLE PRECISION NOT NULL,
    capex_usd DOUBLE PRECISION NOT NULL,
    free_cash_flow_usd DOUBLE PRECISION NOT NULL,
    cash_usd DOUBLE PRECISION NOT NULL,
    debt_usd DOUBLE PRECISION NOT NULL,
    net_debt_usd DOUBLE PRECISION NOT NULL,
    quality_flags INTEGER NOT NULL,
    source_hash TEXT NOT NULL,
    inserted_at TIMESTAMPTZ NOT NULL,
    UNIQUE(security_id, period_end_date, available_at, is_ttm, source_hash)
);

CREATE INDEX IF NOT EXISTS idx_statement_snapshots_lookup
    ON statement_snapshots(security_id, period_end_date, available_at);

CREATE TABLE IF NOT EXISTS model_assumptions (
    assumption_set_id TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL,
    assumption_json JSONB NOT NULL,
    assumption_sha256 TEXT NOT NULL,
    operator_label TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS valuations (
    run_id TEXT NOT NULL,
    security_id BIGINT NOT NULL,
    current_price_usd DOUBLE PRECISION NOT NULL,
    intrinsic_value_per_share_usd DOUBLE PRECISION NOT NULL,
    expected_return_ratio DOUBLE PRECISION NOT NULL,
    market_cap_usd DOUBLE PRECISION NOT NULL,
    enterprise_value_usd DOUBLE PRECISION NOT NULL,
    fcf_yield_ratio DOUBLE PRECISION NOT NULL,
    net_debt_usd DOUBLE PRECISION NOT NULL,
    confidence_ratio DOUBLE PRECISION NOT NULL,
    quality_flags INTEGER NOT NULL,
    reason TEXT NOT NULL,
    PRIMARY KEY(run_id, security_id)
);

CREATE TABLE IF NOT EXISTS forecast_outcomes (
    forecast_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    security_id BIGINT NOT NULL,
    forecast_made_at TIMESTAMPTZ NOT NULL,
    horizon_days INTEGER NOT NULL,
    expected_return_ratio DOUBLE PRECISION NOT NULL,
    confidence_ratio DOUBLE PRECISION NOT NULL,
    price_at_forecast_usd DOUBLE PRECISION NOT NULL,
    price_at_horizon_usd DOUBLE PRECISION,
    realized_return_ratio DOUBLE PRECISION,
    outcome_recorded_at TIMESTAMPTZ,
    brier_like_error DOUBLE PRECISION,
    absolute_error DOUBLE PRECISION
);

CREATE TABLE IF NOT EXISTS autonomy_controls (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled BOOLEAN NOT NULL,
    max_daily_turnover_ratio DOUBLE PRECISION NOT NULL,
    max_model_drawdown_ratio DOUBLE PRECISION NOT NULL,
    min_online_hit_rate DOUBLE PRECISION NOT NULL,
    min_reconciliation_freshness_s INTEGER NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS target_weights (
    run_id TEXT NOT NULL,
    security_id BIGINT NOT NULL,
    current_weight_ratio DOUBLE PRECISION NOT NULL,
    target_weight_ratio DOUBLE PRECISION NOT NULL,
    delta_weight_ratio DOUBLE PRECISION NOT NULL,
    reason TEXT NOT NULL,
    PRIMARY KEY(run_id, security_id)
);

CREATE TABLE IF NOT EXISTS order_intents (
    intent_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    security_id BIGINT NOT NULL,
    side TEXT NOT NULL CHECK (side IN ('buy', 'sell', 'none')),
    notional_usd DOUBLE PRECISION NOT NULL,
    current_weight_ratio DOUBLE PRECISION NOT NULL,
    target_weight_ratio DOUBLE PRECISION NOT NULL,
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS risk_decisions (
    decision_id TEXT PRIMARY KEY,
    intent_id TEXT NOT NULL,
    security_id BIGINT NOT NULL,
    approved BOOLEAN NOT NULL,
    reason TEXT NOT NULL,
    notional_usd DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS staged_orders (
    staged_order_id TEXT PRIMARY KEY,
    decision_id TEXT NOT NULL,
    intent_id TEXT NOT NULL,
    security_id BIGINT NOT NULL,
    side TEXT NOT NULL,
    notional_usd DOUBLE PRECISION NOT NULL,
    staged_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS broker_events (
    broker_event_id TEXT PRIMARY KEY,
    staged_order_id TEXT,
    event_type TEXT NOT NULL,
    payload_json JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

INSERT INTO settings(key, value, updated_at)
VALUES ('trading_mode', 'observe', now())
ON CONFLICT (key) DO NOTHING;

INSERT INTO autonomy_controls(
    id,
    enabled,
    max_daily_turnover_ratio,
    max_model_drawdown_ratio,
    min_online_hit_rate,
    min_reconciliation_freshness_s,
    updated_at
)
VALUES (
    1,
    FALSE,
    0.10,
    0.08,
    0.52,
    3600,
    now()
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO metric_definitions(metric_id, metric_name, metric_kind, normal_unit, additivity_class, default_dimension_policy, description) VALUES
    (1, 'revenue', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Top-line revenue.'),
    (2, 'net_income', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Net income or loss.'),
    (3, 'eps_diluted', 'per_share_flow', 'USD/shares', 'non_additive', 'consolidated_total_only', 'Diluted earnings per share.'),
    (4, 'diluted_shares', 'flow', 'shares', 'non_additive', 'consolidated_total_only', 'Weighted-average diluted shares.'),
    (5, 'operating_cash_flow', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Operating cash flow.'),
    (6, 'capex', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Capital expenditure cash outflow.'),
    (7, 'cash', 'instant', 'USD', 'point_in_time', 'consolidated_total_only', 'Cash and cash equivalents.'),
    (8, 'debt', 'instant', 'USD', 'point_in_time', 'consolidated_total_only', 'Debt obligations.')
ON CONFLICT (metric_id) DO NOTHING;

INSERT INTO measurement_bases(basis_id, metric_id, basis_name, accounting_scope, tax_treatment, preferred, notes) VALUES
    (101, 1, 'customer_contract_revenue_excluding_assessed_tax', 'consolidated', 'excluding_assessed_tax', TRUE, 'Preferred ASC 606 revenue basis.'),
    (102, 1, 'customer_contract_revenue_including_assessed_tax', 'consolidated', 'including_assessed_tax', FALSE, 'Fallback revenue basis.'),
    (103, 1, 'sales_revenue_net', 'consolidated', 'unknown', FALSE, 'Legacy sales revenue basis.'),
    (104, 1, 'generic_revenues', 'consolidated', 'unknown', FALSE, 'Generic revenues fallback.'),
    (201, 2, 'net_income_loss', 'consolidated', 'not_applicable', TRUE, 'Standard net income/loss basis.'),
    (301, 3, 'eps_diluted', 'consolidated', 'not_applicable', TRUE, 'Standard diluted EPS basis.'),
    (401, 4, 'weighted_average_diluted_shares', 'consolidated', 'not_applicable', TRUE, 'Weighted average diluted shares.'),
    (501, 5, 'net_cash_provided_by_used_in_operating_activities', 'consolidated', 'not_applicable', TRUE, 'Operating cash flow basis.'),
    (601, 6, 'payments_to_acquire_pp_e', 'consolidated', 'not_applicable', TRUE, 'Capital expenditure basis.'),
    (701, 7, 'cash_and_cash_equivalents', 'consolidated', 'not_applicable', TRUE, 'Cash basis.'),
    (801, 8, 'long_term_debt', 'consolidated', 'not_applicable', TRUE, 'Debt basis.')
ON CONFLICT (basis_id) DO NOTHING;
