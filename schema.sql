PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    event_version INTEGER NOT NULL,
    occurred_at TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    payload_sha256 TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS daemon_heartbeats (
    daemon_name TEXT PRIMARY KEY,
    pid INTEGER NOT NULL,
    host TEXT NOT NULL,
    started_at TEXT NOT NULL,
    heartbeat_at TEXT NOT NULL,
    state TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS work_items (
    work_id TEXT PRIMARY KEY,
    work_type TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    subject_hash TEXT,
    status TEXT NOT NULL CHECK (status IN (
        'queued', 'leased', 'succeeded', 'failed', 'dead'
    )),
    priority INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT,
    lease_until TEXT,
    available_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(work_type, subject_id, subject_hash)
);

CREATE INDEX IF NOT EXISTS idx_work_items_claim
    ON work_items(work_type, status, available_at, priority, lease_until);

CREATE TABLE IF NOT EXISTS daemon_cursors (
    daemon_name TEXT PRIMARY KEY,
    cursor_value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS securities (
    security_id INTEGER PRIMARY KEY,
    cik TEXT NOT NULL UNIQUE,
    symbol TEXT NOT NULL,
    investable INTEGER NOT NULL DEFAULT 0,
    price_usd REAL NOT NULL DEFAULT 0,
    adv_usd REAL NOT NULL DEFAULT 0,
    peer_group TEXT NOT NULL DEFAULT '',
    sector TEXT NOT NULL DEFAULT '',
    industry TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS source_documents (
    doc_id TEXT PRIMARY KEY,
    cik TEXT NOT NULL,
    accession_number TEXT,
    form TEXT,
    filed_at TEXT,
    source_url TEXT NOT NULL,
    local_path TEXT NOT NULL,
    sha256 TEXT NOT NULL UNIQUE,
    document_kind TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS filings (
    accession_number TEXT PRIMARY KEY,
    cik TEXT NOT NULL,
    form TEXT NOT NULL,
    filing_date TEXT,
    accepted_at TEXT,
    primary_document TEXT,
    source_url TEXT,
    raw_index_uri TEXT,
    raw_primary_uri TEXT,
    raw_sha256 TEXT,
    ingested_at TEXT NOT NULL
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
    preferred INTEGER NOT NULL DEFAULT 0,
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
    allowed_units_json TEXT NOT NULL,
    dimension_policy TEXT NOT NULL,
    allowed_forms_json TEXT,
    notes TEXT NOT NULL,
    UNIQUE(metric_id, taxonomy, concept_qname, basis_id)
);

CREATE TABLE IF NOT EXISTS dimension_signatures (
    dimensions_hash TEXT PRIMARY KEY,
    dimensions_json TEXT NOT NULL,
    has_dimensions INTEGER NOT NULL,
    scope_class TEXT NOT NULL,
    axis_count INTEGER NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS reporting_periods (
    period_id TEXT PRIMARY KEY,
    cik TEXT NOT NULL,
    accession_number TEXT,
    raw_start_date TEXT,
    raw_end_date TEXT,
    raw_instant_date TEXT,
    start_date_inclusive TEXT,
    end_date_exclusive TEXT,
    duration_days INTEGER NOT NULL DEFAULT 0,
    period_kind TEXT NOT NULL,
    period_semantics TEXT NOT NULL,
    fiscal_year INTEGER,
    fiscal_period TEXT,
    fiscal_period_ordinal INTEGER,
    period_length_class TEXT NOT NULL,
    source TEXT NOT NULL,
    created_at TEXT NOT NULL,
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
    raw_start_date TEXT,
    raw_end_date TEXT,
    raw_instant_date TEXT,
    start_date_inclusive TEXT,
    end_date_exclusive TEXT,
    duration_days INTEGER NOT NULL DEFAULT 0,
    dimensions_hash TEXT NOT NULL REFERENCES dimension_signatures(dimensions_hash),
    dimensions_json TEXT NOT NULL,
    segment_json TEXT,
    scenario_json TEXT,
    raw_context_sha256 TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS xbrl_units (
    unit_id TEXT PRIMARY KEY,
    cik TEXT NOT NULL,
    accession_number TEXT,
    raw_unit_id TEXT NOT NULL,
    unit_signature TEXT NOT NULL,
    numerator_json TEXT,
    denominator_json TEXT,
    raw_unit_sha256 TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS xbrl_facts (
    fact_id TEXT PRIMARY KEY,
    source_doc_id TEXT REFERENCES source_documents(doc_id),
    security_id INTEGER NOT NULL,
    cik TEXT NOT NULL,
    accession_number TEXT,
    taxonomy TEXT NOT NULL,
    concept_qname TEXT NOT NULL,
    concept_local_name TEXT NOT NULL,
    context_id TEXT NOT NULL REFERENCES xbrl_contexts(context_id),
    unit_id TEXT REFERENCES xbrl_units(unit_id),
    value_decimal REAL,
    value_text TEXT,
    decimals_attr TEXT,
    precision_attr TEXT,
    is_nil INTEGER NOT NULL DEFAULT 0,
    raw_fact_hash TEXT NOT NULL UNIQUE,
    raw_context_id TEXT NOT NULL,
    form TEXT,
    filed_at TEXT,
    accepted_at TEXT,
    available_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_xbrl_facts_lookup
    ON xbrl_facts(security_id, taxonomy, concept_qname, available_at);

CREATE TABLE IF NOT EXISTS canonical_observations (
    observation_id TEXT PRIMARY KEY,
    observation_hash TEXT NOT NULL UNIQUE,
    resolver_version TEXT NOT NULL,
    security_id INTEGER NOT NULL,
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
    value_decimal REAL NOT NULL,
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
    quality_flags_json TEXT NOT NULL DEFAULT '[]',
    quality_score REAL,
    accepted_at TEXT,
    available_at TEXT NOT NULL,
    created_at TEXT NOT NULL
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
    candidate_fact_ids_json TEXT NOT NULL,
    explanation TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS basis_compatibility_rules (
    rule_id TEXT PRIMARY KEY,
    metric_id INTEGER NOT NULL REFERENCES metric_definitions(metric_id),
    left_basis_id INTEGER NOT NULL REFERENCES measurement_bases(basis_id),
    right_basis_id INTEGER NOT NULL REFERENCES measurement_bases(basis_id),
    operation TEXT NOT NULL,
    compatible INTEGER NOT NULL,
    required_flag TEXT,
    notes TEXT NOT NULL
);

CREATE VIEW IF NOT EXISTS canonical_facts AS
SELECT
    observation_id AS fact_id,
    security_id,
    metric_id,
    value_decimal AS value,
    (SELECT raw_start_date FROM reporting_periods WHERE reporting_periods.period_id = canonical_observations.period_id) AS period_start_date,
    (SELECT raw_end_date FROM reporting_periods WHERE reporting_periods.period_id = canonical_observations.period_id) AS period_end_date,
    available_at,
    source_accession AS accession_number,
    taxonomy,
    concept_qname AS tag,
    unit_signature AS unit,
    quality_flags,
    observation_hash AS fact_hash,
    NULL AS source_event_id,
    created_at AS inserted_at
FROM canonical_observations
WHERE observation_status IN ('selected', 'derived');

CREATE TABLE IF NOT EXISTS universe_snapshots (
    snapshot_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    rule_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS universe_members (
    snapshot_id TEXT NOT NULL,
    security_id INTEGER NOT NULL,
    PRIMARY KEY(snapshot_id, security_id)
);

CREATE TABLE IF NOT EXISTS positions (
    security_id INTEGER PRIMARY KEY,
    quantity_shares REAL NOT NULL,
    market_value_usd REAL NOT NULL,
    weight_ratio REAL NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS broker_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    reconciled INTEGER NOT NULL,
    checked_at TEXT NOT NULL,
    portfolio_value_usd REAL NOT NULL,
    cash_usd REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS model_runs (
    run_id TEXT PRIMARY KEY,
    occurred_at TEXT NOT NULL,
    config_json TEXT NOT NULL,
    diagnostics TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS forecasts (
    run_id TEXT NOT NULL,
    security_id INTEGER NOT NULL,
    revenue_growth_ratio REAL NOT NULL,
    earnings_growth_ratio REAL NOT NULL,
    expected_return_proxy REAL NOT NULL,
    confidence_ratio REAL NOT NULL,
    quality_flags INTEGER NOT NULL,
    reason TEXT NOT NULL,
    PRIMARY KEY(run_id, security_id)
);

CREATE TABLE IF NOT EXISTS statement_snapshots (
    snapshot_id TEXT PRIMARY KEY,
    security_id INTEGER NOT NULL,
    period_start_date TEXT,
    period_end_date TEXT NOT NULL,
    available_at TEXT NOT NULL,
    is_ttm INTEGER NOT NULL,
    revenue_usd REAL NOT NULL,
    net_income_usd REAL NOT NULL,
    diluted_eps_usd REAL NOT NULL,
    diluted_shares REAL NOT NULL,
    operating_cash_flow_usd REAL NOT NULL,
    capex_usd REAL NOT NULL,
    free_cash_flow_usd REAL NOT NULL,
    cash_usd REAL NOT NULL,
    debt_usd REAL NOT NULL,
    net_debt_usd REAL NOT NULL,
    quality_flags INTEGER NOT NULL,
    source_hash TEXT NOT NULL,
    inserted_at TEXT NOT NULL,
    UNIQUE(security_id, period_end_date, available_at, is_ttm, source_hash)
);

CREATE INDEX IF NOT EXISTS idx_statement_snapshots_lookup
    ON statement_snapshots(security_id, period_end_date, available_at);

CREATE TABLE IF NOT EXISTS feature_snapshots (
    feature_snapshot_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    universe_snapshot_id TEXT REFERENCES universe_snapshots(snapshot_id),
    as_of_time TEXT NOT NULL,
    feature_version TEXT NOT NULL,
    input_statement_hash TEXT NOT NULL,
    security_count INTEGER NOT NULL,
    rule_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_feature_snapshots_lookup
    ON feature_snapshots(name, as_of_time, created_at);

CREATE TABLE IF NOT EXISTS feature_rows (
    feature_snapshot_id TEXT NOT NULL REFERENCES feature_snapshots(feature_snapshot_id),
    security_id INTEGER NOT NULL REFERENCES securities(security_id),
    symbol TEXT NOT NULL,
    peer_group TEXT NOT NULL,
    sector TEXT NOT NULL,
    industry TEXT NOT NULL,
    period_end_date TEXT NOT NULL,
    available_at TEXT NOT NULL,
    revenue_ttm_usd REAL NOT NULL,
    revenue_growth_yoy_ratio REAL NOT NULL,
    net_margin_ratio REAL NOT NULL,
    operating_cash_flow_ttm_usd REAL NOT NULL,
    capex_ttm_usd REAL NOT NULL,
    free_cash_flow_ttm_usd REAL NOT NULL,
    fcf_margin_ratio REAL NOT NULL,
    cash_usd REAL NOT NULL,
    debt_usd REAL NOT NULL,
    net_debt_usd REAL NOT NULL,
    net_debt_to_revenue_ratio REAL NOT NULL,
    diluted_shares REAL NOT NULL,
    statement_quality_flags INTEGER NOT NULL,
    feature_quality_flags INTEGER NOT NULL,
    source_statement_snapshot_id TEXT NOT NULL REFERENCES statement_snapshots(snapshot_id),
    previous_statement_snapshot_id TEXT REFERENCES statement_snapshots(snapshot_id),
    row_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(feature_snapshot_id, security_id),
    UNIQUE(feature_snapshot_id, row_hash)
);

CREATE INDEX IF NOT EXISTS idx_feature_rows_security
    ON feature_rows(security_id, period_end_date, available_at);

CREATE INDEX IF NOT EXISTS idx_feature_rows_peer
    ON feature_rows(peer_group, sector, industry);

CREATE TABLE IF NOT EXISTS model_assumptions (
    assumption_set_id TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    assumption_json TEXT NOT NULL,
    assumption_sha256 TEXT NOT NULL,
    operator_label TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS valuations (
    run_id TEXT NOT NULL,
    security_id INTEGER NOT NULL,
    current_price_usd REAL NOT NULL,
    intrinsic_value_per_share_usd REAL NOT NULL,
    expected_return_ratio REAL NOT NULL,
    market_cap_usd REAL NOT NULL,
    enterprise_value_usd REAL NOT NULL,
    fcf_yield_ratio REAL NOT NULL,
    net_debt_usd REAL NOT NULL,
    confidence_ratio REAL NOT NULL,
    quality_flags INTEGER NOT NULL,
    reason TEXT NOT NULL,
    PRIMARY KEY(run_id, security_id)
);

CREATE TABLE IF NOT EXISTS forecast_outcomes (
    forecast_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    security_id INTEGER NOT NULL,
    forecast_made_at TEXT NOT NULL,
    horizon_days INTEGER NOT NULL,
    expected_return_ratio REAL NOT NULL,
    confidence_ratio REAL NOT NULL,
    price_at_forecast_usd REAL NOT NULL,
    price_at_horizon_usd REAL,
    realized_return_ratio REAL,
    outcome_recorded_at TEXT,
    brier_like_error REAL,
    absolute_error REAL
);

CREATE TABLE IF NOT EXISTS autonomy_controls (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL,
    max_daily_turnover_ratio REAL NOT NULL,
    max_model_drawdown_ratio REAL NOT NULL,
    min_online_hit_rate REAL NOT NULL,
    min_reconciliation_freshness_s INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS target_weights (
    run_id TEXT NOT NULL,
    security_id INTEGER NOT NULL,
    current_weight_ratio REAL NOT NULL,
    target_weight_ratio REAL NOT NULL,
    delta_weight_ratio REAL NOT NULL,
    reason TEXT NOT NULL,
    PRIMARY KEY(run_id, security_id)
);

CREATE TABLE IF NOT EXISTS order_intents (
    intent_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    security_id INTEGER NOT NULL,
    side TEXT NOT NULL CHECK (side IN ('buy', 'sell', 'none')),
    notional_usd REAL NOT NULL,
    current_weight_ratio REAL NOT NULL,
    target_weight_ratio REAL NOT NULL,
    reason TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_order_intents_run ON order_intents(run_id);

CREATE TABLE IF NOT EXISTS risk_decisions (
    decision_id TEXT PRIMARY KEY,
    intent_id TEXT NOT NULL,
    security_id INTEGER NOT NULL,
    approved INTEGER NOT NULL,
    reason TEXT NOT NULL,
    notional_usd REAL NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_risk_decisions_intent ON risk_decisions(intent_id);

CREATE TABLE IF NOT EXISTS staged_orders (
    staged_order_id TEXT PRIMARY KEY,
    decision_id TEXT NOT NULL,
    intent_id TEXT NOT NULL,
    security_id INTEGER NOT NULL,
    side TEXT NOT NULL,
    notional_usd REAL NOT NULL,
    staged_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS broker_events (
    broker_event_id TEXT PRIMARY KEY,
    staged_order_id TEXT,
    event_type TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    occurred_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS product_types (
    product_type TEXT PRIMARY KEY,
    schema_name TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    hazard_class TEXT NOT NULL CHECK (hazard_class IN ('A','B','C')),
    description TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS data_products (
    product_id TEXT PRIMARY KEY,
    product_type TEXT NOT NULL REFERENCES product_types(product_type),
    schema_name TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    cell_id TEXT NOT NULL,
    family_id TEXT,
    content_sha256 TEXT NOT NULL,
    storage_kind TEXT NOT NULL,
    storage_ref TEXT NOT NULL,
    created_by_algorithm TEXT NOT NULL,
    algorithm_version TEXT NOT NULL,
    config_sha256 TEXT NOT NULL,
    code_sha256 TEXT,
    valid_time_start TEXT,
    valid_time_end TEXT,
    source_time TEXT,
    accepted_at TEXT,
    ingested_at TEXT,
    available_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    hazard_class TEXT NOT NULL CHECK (hazard_class IN ('A','B','C')),
    UNIQUE(product_type, cell_id, content_sha256, created_by_algorithm, config_sha256)
);

CREATE INDEX IF NOT EXISTS idx_data_products_type_cell
    ON data_products(product_type, cell_id, available_at);

CREATE INDEX IF NOT EXISTS idx_data_products_available
    ON data_products(available_at);

CREATE TABLE IF NOT EXISTS data_product_lineage (
    child_product_id TEXT NOT NULL REFERENCES data_products(product_id),
    parent_product_id TEXT NOT NULL REFERENCES data_products(product_id),
    role TEXT NOT NULL,
    PRIMARY KEY(child_product_id, parent_product_id, role)
);

CREATE TABLE IF NOT EXISTS algorithm_registry (
    algorithm_id TEXT PRIMARY KEY,
    algorithm_name TEXT NOT NULL,
    algorithm_version TEXT NOT NULL,
    hazard_class TEXT NOT NULL CHECK (hazard_class IN ('A','B','C')),
    purity TEXT NOT NULL CHECK (purity IN (
        'pure',
        'deterministic_io',
        'external_read',
        'external_write'
    )),
    deterministic INTEGER NOT NULL CHECK (deterministic IN (0,1)),
    executable_kind TEXT NOT NULL CHECK (executable_kind IN (
        'builtin',
        'rust_operation',
        'command',
        'c_abi',
        'noop_test'
    )),
    executable_ref TEXT NOT NULL,
    input_contract_json TEXT NOT NULL,
    output_contract_json TEXT NOT NULL,
    resource_contract_json TEXT NOT NULL,
    forbidden_resource_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(algorithm_name, algorithm_version)
);

-- Run tables are operational index state over append-only data_products,
-- data_product_lineage, workflow_edges, and graph_invariant_violations.
CREATE TABLE IF NOT EXISTS workflow_runs (
    workflow_run_id TEXT PRIMARY KEY,
    workflow_name TEXT NOT NULL,
    workflow_spec_sha256 TEXT NOT NULL,
    driver_json TEXT NOT NULL,
    mode TEXT NOT NULL,
    decision_as_of TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN (
        'created',
        'running',
        'succeeded',
        'failed',
        'rejected'
    )),
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    diagnostics TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS workflow_run_specs (
    workflow_run_id TEXT PRIMARY KEY REFERENCES workflow_runs(workflow_run_id),
    workflow_spec_sha256 TEXT NOT NULL,
    workflow_spec_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workflow_nodes (
    node_run_id TEXT PRIMARY KEY,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(workflow_run_id),
    node_id TEXT NOT NULL,
    algorithm_id TEXT NOT NULL REFERENCES algorithm_registry(algorithm_id),
    cell_id TEXT NOT NULL,
    config_sha256 TEXT NOT NULL,
    input_product_ids_json TEXT NOT NULL,
    output_product_ids_json TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL CHECK (status IN (
        'created',
        'ready',
        'running',
        'succeeded',
        'failed',
        'rejected'
    )),
    started_at TEXT,
    finished_at TEXT,
    diagnostics TEXT NOT NULL DEFAULT '',
    UNIQUE(workflow_run_id, node_id, cell_id)
);

CREATE TABLE IF NOT EXISTS workflow_edges (
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(workflow_run_id),
    from_node_id TEXT NOT NULL,
    to_node_id TEXT NOT NULL,
    product_role TEXT NOT NULL,
    PRIMARY KEY(workflow_run_id, from_node_id, to_node_id, product_role)
);

CREATE TABLE IF NOT EXISTS resource_declarations (
    resource_id TEXT PRIMARY KEY,
    resource_kind TEXT NOT NULL CHECK (resource_kind IN (
        'cpu',
        'sqlite',
        'filesystem',
        'sec_http',
        'market_data_http',
        'broker',
        'notification',
        'clock'
    )),
    max_concurrent INTEGER NOT NULL DEFAULT 1,
    min_interval_ms INTEGER NOT NULL DEFAULT 0,
    allowed_modes_json TEXT NOT NULL DEFAULT '[]',
    hazard_class TEXT NOT NULL CHECK (hazard_class IN ('A','B','C')),
    description TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS graph_invariant_violations (
    violation_id TEXT PRIMARY KEY,
    workflow_run_id TEXT REFERENCES workflow_runs(workflow_run_id),
    invariant_id TEXT NOT NULL,
    subject_json TEXT NOT NULL,
    severity TEXT NOT NULL CHECK (severity IN ('error','warning')),
    explanation TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT OR IGNORE INTO settings(key, value, updated_at) VALUES ('trading_mode', 'observe', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

INSERT OR IGNORE INTO autonomy_controls(
    id,
    enabled,
    max_daily_turnover_ratio,
    max_model_drawdown_ratio,
    min_online_hit_rate,
    min_reconciliation_freshness_s,
    updated_at
) VALUES (
    1,
    0,
    0.10,
    0.08,
    0.52,
    3600,
    strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
);

INSERT OR IGNORE INTO metric_definitions(metric_id, metric_name, metric_kind, normal_unit, additivity_class, default_dimension_policy, description) VALUES
    (1, 'revenue', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Top-line revenue.'),
    (2, 'net_income', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Net income or loss.'),
    (3, 'eps_diluted', 'per_share_flow', 'USD/shares', 'non_additive', 'consolidated_total_only', 'Diluted earnings per share.'),
    (4, 'diluted_shares', 'flow', 'shares', 'non_additive', 'consolidated_total_only', 'Weighted-average diluted shares.'),
    (5, 'operating_cash_flow', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Operating cash flow.'),
    (6, 'capex', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Capital expenditure cash outflow.'),
    (7, 'cash', 'instant', 'USD', 'point_in_time', 'consolidated_total_only', 'Cash and cash equivalents.'),
    (8, 'debt', 'instant', 'USD', 'point_in_time', 'consolidated_total_only', 'Debt obligations.'),
    (9, 'gross_profit', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Gross profit.'),
    (10, 'operating_income', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Operating income or loss.'),
    (11, 'research_and_development', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Research and development expense.'),
    (12, 'stock_based_compensation', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Stock-based compensation expense.'),
    (13, 'interest_expense', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Interest expense.'),
    (14, 'buybacks', 'flow', 'USD', 'additive_over_time', 'consolidated_total_only', 'Share repurchases.');

INSERT OR IGNORE INTO measurement_bases(basis_id, metric_id, basis_name, accounting_scope, tax_treatment, preferred, notes) VALUES
    (101, 1, 'customer_contract_revenue_excluding_assessed_tax', 'consolidated', 'excluding_assessed_tax', 1, 'Preferred ASC 606 revenue basis.'),
    (102, 1, 'customer_contract_revenue_including_assessed_tax', 'consolidated', 'including_assessed_tax', 0, 'Fallback revenue basis.'),
    (103, 1, 'sales_revenue_net', 'consolidated', 'unknown', 0, 'Legacy sales revenue basis.'),
    (104, 1, 'generic_revenues', 'consolidated', 'unknown', 0, 'Generic revenues fallback.'),
    (201, 2, 'net_income_loss', 'consolidated', 'not_applicable', 1, 'Standard net income/loss basis.'),
    (301, 3, 'eps_diluted', 'consolidated', 'not_applicable', 1, 'Standard diluted EPS basis.'),
    (401, 4, 'weighted_average_diluted_shares', 'consolidated', 'not_applicable', 1, 'Weighted average diluted shares.'),
    (501, 5, 'net_cash_provided_by_used_in_operating_activities', 'consolidated', 'not_applicable', 1, 'Operating cash flow basis.'),
    (601, 6, 'payments_to_acquire_pp_e', 'consolidated', 'not_applicable', 1, 'Capital expenditure basis.'),
    (701, 7, 'cash_and_cash_equivalents', 'consolidated', 'not_applicable', 1, 'Cash basis.'),
    (801, 8, 'long_term_debt', 'consolidated', 'not_applicable', 1, 'Debt basis.'),
    (901, 9, 'gross_profit', 'consolidated', 'not_applicable', 1, 'Gross profit basis.'),
    (1001, 10, 'operating_income_loss', 'consolidated', 'not_applicable', 1, 'Operating income/loss basis.'),
    (1101, 11, 'research_and_development_expense', 'consolidated', 'not_applicable', 1, 'Research and development expense basis.'),
    (1201, 12, 'share_based_compensation', 'consolidated', 'not_applicable', 1, 'Stock-based compensation basis.'),
    (1202, 12, 'allocated_share_based_compensation_expense', 'consolidated', 'not_applicable', 0, 'Allocated stock-based compensation fallback.'),
    (1301, 13, 'interest_expense_nonoperating', 'consolidated', 'not_applicable', 1, 'Nonoperating interest expense basis.'),
    (1302, 13, 'interest_expense', 'consolidated', 'not_applicable', 0, 'Generic interest expense fallback.'),
    (1303, 13, 'interest_expense_debt', 'consolidated', 'not_applicable', 0, 'Debt interest expense fallback.'),
    (1401, 14, 'payments_for_repurchase_of_common_stock', 'consolidated', 'not_applicable', 1, 'Cash paid to repurchase common stock.'),
    (1402, 14, 'stock_repurchased_and_retired_value', 'consolidated', 'not_applicable', 0, 'Stock repurchased and retired value fallback.'),
    (1403, 14, 'stock_repurchased_value', 'consolidated', 'not_applicable', 0, 'Stock repurchased value fallback.');
