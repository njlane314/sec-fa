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

CREATE TABLE IF NOT EXISTS canonical_facts (
    fact_id BIGSERIAL PRIMARY KEY,
    security_id BIGINT NOT NULL,
    metric_id INTEGER NOT NULL,
    value DOUBLE PRECISION NOT NULL,
    period_start_date DATE,
    period_end_date DATE NOT NULL,
    available_at TIMESTAMPTZ NOT NULL,
    accession_number TEXT,
    taxonomy TEXT,
    tag TEXT,
    unit TEXT,
    quality_flags INTEGER NOT NULL DEFAULT 0,
    fact_hash TEXT NOT NULL UNIQUE,
    source_event_id TEXT,
    inserted_at TIMESTAMPTZ NOT NULL,
    UNIQUE(security_id, metric_id, period_end_date, available_at, accession_number, taxonomy, tag, unit)
);

CREATE INDEX IF NOT EXISTS idx_canonical_facts_lookup ON canonical_facts(security_id, metric_id, period_end_date, available_at);


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
