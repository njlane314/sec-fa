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

CREATE TABLE IF NOT EXISTS securities (
    security_id INTEGER PRIMARY KEY,
    cik TEXT NOT NULL UNIQUE,
    symbol TEXT NOT NULL,
    investable INTEGER NOT NULL DEFAULT 0,
    price_usd REAL NOT NULL DEFAULT 0,
    adv_usd REAL NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
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

CREATE TABLE IF NOT EXISTS canonical_facts (
    fact_id INTEGER PRIMARY KEY AUTOINCREMENT,
    security_id INTEGER NOT NULL,
    metric_id INTEGER NOT NULL,
    value REAL NOT NULL,
    period_start_date TEXT,
    period_end_date TEXT NOT NULL,
    available_at TEXT NOT NULL,
    accession_number TEXT,
    taxonomy TEXT,
    tag TEXT,
    unit TEXT,
    quality_flags INTEGER NOT NULL DEFAULT 0,
    fact_hash TEXT NOT NULL UNIQUE,
    source_event_id TEXT,
    inserted_at TEXT NOT NULL,
    UNIQUE(security_id, metric_id, period_end_date, available_at, accession_number, taxonomy, tag, unit)
);

CREATE INDEX IF NOT EXISTS idx_canonical_facts_lookup ON canonical_facts(security_id, metric_id, period_end_date, available_at);


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

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT OR IGNORE INTO settings(key, value, updated_at) VALUES ('trading_mode', 'observe', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
