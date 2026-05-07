mod fa_core;

use crate::fa_core::*;
use anyhow::{anyhow, bail, Context as _, Result};
use base64::Engine as _;
use clap::{Args, Parser, Subcommand};
use rusqlite::{params, Connection, OptionalExtension, Transaction};
use sec_filings::{parse_xbrl_package, ParseRequest};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::{BTreeMap, BTreeSet};
use std::env;
use std::fs;
use std::io::{self, Write};
use std::path::{Path, PathBuf};
use std::process;
use time::macros::format_description;
use time::{Date, Duration, OffsetDateTime};
use uuid::Uuid;

const DEFAULT_DB_PATH: &str = ".fa.db";
const DEFAULT_FUNDAMENTAL_FORMS: &str = "10-K,10-Q,10-K/A,10-Q/A";
const DEFAULT_WATCH_FORMS: &str = "10-K,10-Q,10-K/A,10-Q/A,8-K,8-K/A";
const DEFAULT_USER_AGENT: &str = "sec-fa operator@example.invalid";
const DEFAULT_DATABENTO_BASE_URL: &str = "https://hist.databento.com/v0";
const DEFAULT_DATABENTO_DATASET: &str = "EQUS.MINI";
const DEFAULT_DATABENTO_SCHEMA: &str = "ohlcv-1d";
const TOTAL_DIMENSIONS_JSON: &str = "{}";

#[derive(Debug)]
struct ExitError {
    code: i32,
    message: String,
}

impl std::fmt::Display for ExitError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.message)
    }
}
impl std::error::Error for ExitError {}

fn fail<T>(code: i32, message: impl Into<String>) -> Result<T> {
    Err(anyhow!(ExitError {
        code,
        message: message.into()
    }))
}

#[derive(Parser, Debug)]
#[command(
    name = "sec",
    version,
    about = "SEC filings to fundamental-analysis operations toolchain"
)]
struct Cli {
    #[command(subcommand)]
    command: Option<Command>,
}

#[derive(Subcommand, Debug)]
enum Command {
    Init {
        #[arg(long, default_value = DEFAULT_DB_PATH)]
        db: PathBuf,
    },
    Sym {
        #[arg(long)]
        db: PathBuf,
        #[arg(long)]
        cik: String,
        #[arg(long)]
        symbol: String,
        #[arg(long = "price-usd", default_value_t = 0.0)]
        price_usd: f64,
        #[arg(long = "adv-usd", default_value_t = 0.0)]
        adv_usd: f64,
        #[arg(long, default_value_t = 0)]
        investable: i64,
    },
    ImportSecurities {
        #[arg(long)]
        db: PathBuf,
        #[arg(long = "csv")]
        csv_path: PathBuf,
        #[arg(long = "default-investable", default_value_t = 1)]
        default_investable: i64,
    },
    IngestUniverse {
        #[arg(long)]
        db: PathBuf,
        #[arg(long = "csv")]
        csv_path: PathBuf,
        #[arg(long = "raw-root", default_value = "raw")]
        raw_root: PathBuf,
        #[arg(long, default_value = DEFAULT_FUNDAMENTAL_FORMS)]
        forms: String,
        #[arg(long = "user-agent", default_value = "")]
        user_agent: String,
        #[arg(long = "default-investable", default_value_t = 1)]
        default_investable: i64,
        #[arg(long = "limit-per-cik", default_value_t = 1)]
        limit_per_cik: usize,
        #[arg(long = "annual-limit-per-cik", default_value_t = 1)]
        annual_limit_per_cik: usize,
        #[arg(long = "quarterly-limit-per-cik", default_value_t = 4)]
        quarterly_limit_per_cik: usize,
        #[arg(long = "sleep-s", default_value_t = 0.2)]
        sleep_s: f64,
        #[arg(long = "pull-sleep-s", default_value_t = 0.15)]
        pull_sleep_s: f64,
        #[arg(long = "continue-on-error")]
        continue_on_error: bool,
        #[arg(long = "universe-name", default_value = "")]
        universe_name: String,
        #[arg(long = "min-adv-usd", default_value_t = 0.0)]
        min_adv_usd: f64,
        #[arg(long = "min-fact-count", default_value_t = 2)]
        min_fact_count: i64,
        #[arg(long = "server-url", env = "SEC_FA_SERVER_URL")]
        server_url: Option<String>,
    },
    Databento {
        #[arg(long, default_value = DEFAULT_DB_PATH)]
        db: PathBuf,
        #[arg(long)]
        out: Option<PathBuf>,
        #[arg(long)]
        symbols: String,
        #[arg(long, default_value = DEFAULT_DATABENTO_DATASET)]
        dataset: String,
        #[arg(long, default_value = DEFAULT_DATABENTO_SCHEMA)]
        schema: String,
        #[arg(long = "stype-in", default_value = "raw_symbol")]
        stype_in: String,
        #[arg(long = "base-url", default_value = DEFAULT_DATABENTO_BASE_URL)]
        base_url: String,
        #[arg(long)]
        start: Option<String>,
        #[arg(long)]
        end: Option<String>,
        #[arg(long = "lookback-days", default_value_t = 7)]
        lookback_days: i64,
        #[arg(long = "adv-window-days", default_value_t = 5)]
        adv_window_days: usize,
        #[arg(long, default_value = "US")]
        countries: String,
        #[arg(long = "security-types", default_value = "EQS")]
        security_types: String,
        #[arg(long = "default-investable", default_value_t = 1)]
        default_investable: i64,
        #[arg(long = "import-db")]
        import_db: bool,
        #[arg(long = "max-symbols", default_value_t = 100)]
        max_symbols: usize,
    },
    Pos {
        #[arg(long)]
        db: PathBuf,
        #[arg(long)]
        cik: String,
        #[arg(long = "quantity-shares", default_value_t = 0.0)]
        quantity_shares: f64,
        #[arg(long = "market-value-usd", default_value_t = 0.0)]
        market_value_usd: f64,
        #[arg(long = "weight-ratio", default_value_t = 0.0)]
        weight_ratio: f64,
    },
    Watch {
        #[arg(long, default_value = DEFAULT_DB_PATH)]
        db: PathBuf,
        #[arg(long)]
        cik: String,
        #[arg(long = "user-agent", default_value = "")]
        user_agent: String,
        #[arg(long, default_value_t = 40)]
        limit: usize,
        #[arg(long, default_value = DEFAULT_WATCH_FORMS)]
        forms: String,
        #[arg(long = "server-url", env = "SEC_FA_SERVER_URL")]
        server_url: Option<String>,
    },
    Pull {
        #[arg(long, default_value = DEFAULT_DB_PATH)]
        db: PathBuf,
        #[arg(long = "raw-root", default_value = "raw")]
        raw_root: PathBuf,
        #[arg(long = "user-agent", default_value = "")]
        user_agent: String,
        #[arg(long)]
        accession: Option<String>,
        #[arg(long)]
        cik: Option<String>,
        #[arg(long, default_value = "")]
        forms: String,
        #[arg(long, default_value_t = 20)]
        limit: usize,
        #[arg(long = "sleep-s", default_value_t = 0.15)]
        sleep_s: f64,
        #[arg(long = "server-url", env = "SEC_FA_SERVER_URL")]
        server_url: Option<String>,
    },
    Xbrl {
        #[arg(long, default_value = DEFAULT_DB_PATH)]
        db: PathBuf,
        #[arg(long)]
        accession: Option<String>,
        #[arg(long)]
        cik: Option<String>,
        #[arg(long)]
        symbol: Option<String>,
        #[arg(long)]
        form: Option<String>,
        #[arg(long = "filed-at")]
        filed_at: Option<String>,
        #[arg(long = "accepted-at")]
        accepted_at: Option<String>,
        #[arg(long = "raw-root", default_value = "raw")]
        raw_root: PathBuf,
        #[arg(long = "package-root")]
        package_root: Option<PathBuf>,
        #[arg(long = "user-agent", default_value = "")]
        user_agent: String,
        #[arg(long)]
        latest: bool,
        #[arg(long, default_value = "")]
        forms: String,
        #[arg(long = "sleep-s", default_value_t = 0.0)]
        sleep_s: f64,
        #[arg(long)]
        strict: bool,
    },
    Univ {
        #[arg(long)]
        db: PathBuf,
        #[arg(long, default_value = "default")]
        name: String,
        #[arg(long = "min-adv-usd", default_value_t = 0.0)]
        min_adv_usd: f64,
        #[arg(long = "min-fact-count", default_value_t = 0)]
        min_fact_count: i64,
    },
    Recon {
        #[arg(long)]
        db: PathBuf,
        #[arg(long = "portfolio-value-usd", default_value_t = 0.0)]
        portfolio_value_usd: f64,
        #[arg(long = "cash-usd", default_value_t = 0.0)]
        cash_usd: f64,
        #[arg(long, default_value_t = 0)]
        reconciled: i64,
    },
    Plan(ModelArgs),
    Value(ModelArgs),
    Gate(GateArgs),
    Stage {
        #[arg(long)]
        db: PathBuf,
        #[arg(long = "run-id")]
        run_id: Option<String>,
        #[arg(long, default_value_t = 100)]
        limit: i64,
        #[arg(long)]
        autonomous: bool,
    },
    Send {
        #[arg(long)]
        db: PathBuf,
        #[arg(long, default_value = "mock")]
        adapter: String,
        #[arg(long, default_value_t = 100)]
        limit: i64,
        #[arg(long = "max-reconciliation-age-s", default_value_t = 3600)]
        max_reconciliation_age_s: i64,
    },
    Mode {
        #[arg(long)]
        db: PathBuf,
        mode: String,
    },
    Halt {
        #[arg(long)]
        db: PathBuf,
        #[arg(long, default_value = "")]
        reason: String,
    },
    Stat {
        #[arg(long)]
        db: PathBuf,
    },
    Report {
        kind: Option<String>,
        #[arg(long)]
        db: PathBuf,
        #[arg(long, default_value = "today")]
        date: String,
    },
    Ping {
        #[arg(long, default_value = "")]
        method: String,
        #[arg(long, default_value = "sec-fa")]
        title: String,
        #[arg(long, default_value = "")]
        message: String,
        #[arg(long, default_value = "default")]
        priority: String,
        #[arg(long = "ntfy-topic", default_value = "")]
        ntfy_topic: String,
    },
}

#[derive(Args, Debug, Clone)]
struct ModelArgs {
    #[arg(long)]
    db: PathBuf,
    #[arg(long = "core-lib")]
    core_lib: String,
    #[arg(long = "portfolio-value-usd", default_value_t = 0.0)]
    portfolio_value_usd: f64,
    #[arg(long = "cash-usd", default_value_t = 0.0)]
    cash_usd: f64,
    #[arg(long = "max-name-weight-ratio", default_value_t = 0.05)]
    max_name_weight_ratio: f64,
    #[arg(long = "target-gross-exposure-ratio", default_value_t = 0.50)]
    target_gross_exposure_ratio: f64,
    #[arg(long = "min-expected-return-proxy", default_value_t = 0.05)]
    min_expected_return_proxy: f64,
    #[arg(long = "min-abs-order-notional-usd", default_value_t = 100.0)]
    min_abs_order_notional_usd: f64,
    #[arg(long = "max-forecast-abs-growth-ratio", default_value_t = 2.0)]
    max_forecast_abs_growth_ratio: f64,
    #[arg(long = "max-fact-age-days", default_value_t = 540.0)]
    max_fact_age_days: f64,
    #[arg(long = "max-statement-age-days", default_value_t = 540.0)]
    max_statement_age_days: f64,
    #[arg(long = "max-order-notional-usd", default_value_t = 10_000.0)]
    max_order_notional_usd: f64,
    #[arg(long = "min-adv-usd", default_value_t = 1_000_000.0)]
    min_adv_usd: f64,
    #[arg(long = "max-adv-participation-ratio", default_value_t = 0.01)]
    max_adv_participation_ratio: f64,
    #[arg(long = "max-reconciliation-age-s", default_value_t = 3600)]
    max_reconciliation_age_s: i64,
    #[arg(long = "forecast-horizon-days", default_value_t = 90)]
    forecast_horizon_days: i64,
    #[arg(long = "operator-label", default_value = "default")]
    operator_label: String,
    #[arg(long = "assumption-json", default_value = "")]
    assumption_json: String,
    #[arg(long = "assumption-file", default_value = "")]
    assumption_file: String,
    #[arg(long = "assumption-set-id", default_value = "")]
    assumption_set_id: String,
}

#[derive(Args, Debug, Clone)]
struct GateArgs {
    #[arg(long)]
    db: PathBuf,
    #[arg(long = "core-lib")]
    core_lib: String,
    #[arg(long = "run-id")]
    run_id: Option<String>,
    #[arg(long = "portfolio-value-usd", default_value_t = 0.0)]
    portfolio_value_usd: f64,
    #[arg(long = "cash-usd", default_value_t = 0.0)]
    cash_usd: f64,
    #[arg(long = "max-name-weight-ratio", default_value_t = 0.05)]
    max_name_weight_ratio: f64,
    #[arg(long = "max-order-notional-usd", default_value_t = 10_000.0)]
    max_order_notional_usd: f64,
    #[arg(long = "min-adv-usd", default_value_t = 1_000_000.0)]
    min_adv_usd: f64,
    #[arg(long = "max-adv-participation-ratio", default_value_t = 0.01)]
    max_adv_participation_ratio: f64,
    #[arg(long = "max-reconciliation-age-s", default_value_t = 3600)]
    max_reconciliation_age_s: i64,
}

fn main() {
    match run() {
        Ok(()) => {}
        Err(err) => {
            if let Some(e) = err.downcast_ref::<ExitError>() {
                if e.code != 0 {
                    eprintln!("error: {}", e.message);
                }
                process::exit(e.code);
            }
            eprintln!("error: {err:#}");
            process::exit(1);
        }
    }
}

fn run() -> Result<()> {
    let cli = Cli::parse();
    match cli.command {
        None => {
            print_help();
            Ok(())
        }
        Some(Command::Init { db }) => cmd_init(&db),
        Some(Command::Sym {
            db,
            cik,
            symbol,
            price_usd,
            adv_usd,
            investable,
        }) => cmd_sym(&db, &cik, &symbol, price_usd, adv_usd, investable != 0),
        Some(Command::ImportSecurities {
            db,
            csv_path,
            default_investable,
        }) => cmd_import_securities(&db, &csv_path, default_investable != 0),
        Some(Command::IngestUniverse {
            db,
            csv_path,
            raw_root,
            forms,
            user_agent,
            default_investable,
            limit_per_cik,
            annual_limit_per_cik,
            quarterly_limit_per_cik,
            sleep_s,
            pull_sleep_s,
            continue_on_error,
            universe_name,
            min_adv_usd,
            min_fact_count,
            server_url,
        }) => cmd_ingest_universe(
            &db,
            &csv_path,
            &raw_root,
            &forms,
            &user_agent,
            default_investable != 0,
            limit_per_cik,
            annual_limit_per_cik,
            quarterly_limit_per_cik,
            sleep_s,
            pull_sleep_s,
            continue_on_error,
            &universe_name,
            min_adv_usd,
            min_fact_count,
            server_url.as_deref(),
        ),
        Some(Command::Databento {
            db,
            out,
            symbols,
            dataset,
            schema,
            stype_in,
            base_url,
            start,
            end,
            lookback_days,
            adv_window_days,
            countries,
            security_types,
            default_investable,
            import_db,
            max_symbols,
        }) => cmd_databento(
            &db,
            out.as_deref(),
            &symbols,
            &dataset,
            &schema,
            &stype_in,
            &base_url,
            start.as_deref(),
            end.as_deref(),
            lookback_days,
            adv_window_days,
            &countries,
            &security_types,
            default_investable != 0,
            import_db,
            max_symbols,
        ),
        Some(Command::Pos {
            db,
            cik,
            quantity_shares,
            market_value_usd,
            weight_ratio,
        }) => cmd_pos(&db, &cik, quantity_shares, market_value_usd, weight_ratio),
        Some(Command::Watch {
            db,
            cik,
            user_agent,
            limit,
            forms,
            server_url,
        }) => cmd_watch(&db, &cik, &user_agent, limit, &forms, server_url.as_deref()),
        Some(Command::Pull {
            db,
            raw_root,
            user_agent,
            accession,
            cik,
            forms,
            limit,
            sleep_s,
            server_url,
        }) => cmd_pull(
            &db,
            &raw_root,
            &user_agent,
            accession.as_deref(),
            cik.as_deref(),
            &forms,
            limit,
            sleep_s,
            server_url.as_deref(),
        ),
        Some(Command::Xbrl {
            db,
            accession,
            cik,
            symbol: _,
            form,
            filed_at,
            accepted_at,
            raw_root,
            package_root,
            user_agent: _,
            latest,
            forms,
            sleep_s: _,
            strict: _,
        }) => cmd_xbrl(
            &db,
            accession,
            cik,
            form,
            filed_at,
            accepted_at,
            raw_root,
            package_root,
            latest,
            forms,
        ),
        Some(Command::Univ {
            db,
            name,
            min_adv_usd,
            min_fact_count,
        }) => cmd_univ(&db, &name, min_adv_usd, min_fact_count),
        Some(Command::Recon {
            db,
            portfolio_value_usd,
            cash_usd,
            reconciled,
        }) => cmd_recon(&db, portfolio_value_usd, cash_usd, reconciled != 0),
        Some(Command::Plan(args)) => cmd_plan(args),
        Some(Command::Value(args)) => cmd_value(args),
        Some(Command::Gate(args)) => cmd_gate(args),
        Some(Command::Stage {
            db,
            run_id,
            limit,
            autonomous: _,
        }) => cmd_stage(&db, run_id.as_deref(), limit),
        Some(Command::Send {
            db,
            adapter,
            limit,
            max_reconciliation_age_s,
        }) => cmd_send(&db, &adapter, limit, max_reconciliation_age_s),
        Some(Command::Mode { db, mode }) => cmd_mode(&db, &mode),
        Some(Command::Halt { db, reason }) => {
            set_mode(&db, "halted", "trading_halted", json!({"reason": reason}))
        }
        Some(Command::Stat { db }) => cmd_stat(&db),
        Some(Command::Report { kind: _, db, date }) => cmd_report(&db, &date),
        Some(Command::Ping {
            method,
            title,
            message,
            priority,
            ntfy_topic,
        }) => cmd_ping(&method, &title, &message, &priority, &ntfy_topic),
    }
}

fn print_help() {
    println!("usage: sec <command> [options]\n");
    println!("commands: init sym import-securities ingest-universe databento pos watch pull xbrl univ recon plan value gate stage send mode halt stat report ping");
}

fn open_db(path: &Path) -> Result<Connection> {
    let conn = Connection::open(path).with_context(|| format!("opening db {}", path.display()))?;
    conn.execute_batch("PRAGMA foreign_keys = ON;")?;
    Ok(conn)
}

fn ensure_db(conn: &Connection) -> Result<()> {
    let exists: Option<String> = conn
        .query_row(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='events'",
            [],
            |row| row.get(0),
        )
        .optional()?;
    if exists.is_none() {
        fail(
            1,
            "database is not initialized; run `init` or `init --db <path>` first",
        )
    } else {
        Ok(())
    }
}

fn cmd_init(db_path: &Path) -> Result<()> {
    if let Some(parent) = db_path.parent().filter(|p| !p.as_os_str().is_empty()) {
        fs::create_dir_all(parent)?;
    }
    let conn = open_db(db_path)?;
    let schema = load_schema_sql()?;
    conn.execute_batch(&schema)?;
    seed_metric_concept_candidates(&conn)?;
    insert_total_dimension_signature(&conn)?;
    json_line(&json!({"db": db_path, "status": "initialized"}))
}

fn cmd_sym(
    db_path: &Path,
    cik: &str,
    symbol: &str,
    price: f64,
    adv: f64,
    investable: bool,
) -> Result<()> {
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let cik10 = normalize_cik(cik);
    let security_id = cik10.parse::<i64>().unwrap_or(0);
    let now = utc_now();
    let tx = conn.transaction()?;
    tx.execute(
        "INSERT INTO securities(security_id, cik, symbol, investable, price_usd, adv_usd, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(security_id) DO UPDATE SET cik=excluded.cik, symbol=excluded.symbol, investable=excluded.investable, price_usd=excluded.price_usd, adv_usd=excluded.adv_usd, updated_at=excluded.updated_at",
        params![security_id, cik10, symbol.to_ascii_uppercase(), bool_int(investable), price, adv, now],
    )?;
    let event = append_event(
        &tx,
        "security_upserted",
        json!({"security_id": security_id, "cik": cik10, "symbol": symbol.to_ascii_uppercase(), "investable": investable, "price_usd": price, "adv_usd": adv}),
    )?;
    tx.commit()?;
    json_line(&event)
}

#[derive(Debug, Deserialize)]
struct SecurityCsvRow {
    cik: String,
    #[serde(alias = "ticker", alias = "symbol")]
    ticker: String,
    price_usd: f64,
    adv_usd: f64,
    #[serde(default)]
    investable: Option<i64>,
}

#[derive(Debug, Clone)]
struct SecuritySeed {
    cik: String,
    ticker: String,
    price_usd: f64,
    adv_usd: f64,
    investable: bool,
}

#[derive(Debug, Clone)]
struct SecuritySeedBatch {
    seeds: Vec<SecuritySeed>,
    input_rows: usize,
    duplicate_cik_rows_collapsed: usize,
}

fn cmd_import_securities(db_path: &Path, csv_path: &Path, default_investable: bool) -> Result<()> {
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let batch = read_security_seed_file(csv_path, default_investable)?;
    let event = import_security_seed_batch(&mut conn, &batch)?;
    json_line(&event)
}

fn read_security_seed_file(csv_path: &Path, default_investable: bool) -> Result<SecuritySeedBatch> {
    let mut rdr = csv::Reader::from_path(csv_path)?;
    let mut by_cik = BTreeMap::<String, SecuritySeed>::new();
    let mut rows = 0usize;
    for row in rdr.deserialize::<SecurityCsvRow>() {
        let row = row?;
        rows += 1;
        let cik = normalize_cik(&row.cik);
        let seed = SecuritySeed {
            cik: cik.clone(),
            ticker: row.ticker.to_ascii_uppercase(),
            price_usd: row.price_usd,
            adv_usd: row.adv_usd,
            investable: row.investable.map(|v| v != 0).unwrap_or(default_investable),
        };
        match by_cik.get(&cik) {
            Some(existing) if existing.adv_usd >= seed.adv_usd => {}
            _ => {
                by_cik.insert(cik, seed);
            }
        }
    }
    let seeds = by_cik.into_values().collect::<Vec<_>>();
    Ok(SecuritySeedBatch {
        duplicate_cik_rows_collapsed: rows.saturating_sub(seeds.len()),
        input_rows: rows,
        seeds,
    })
}

fn import_security_seed_batch(conn: &mut Connection, batch: &SecuritySeedBatch) -> Result<Value> {
    let tx = conn.transaction()?;
    for seed in &batch.seeds {
        let security_id = seed.cik.parse::<i64>().unwrap_or(0);
        tx.execute(
            "INSERT INTO securities(security_id, cik, symbol, investable, price_usd, adv_usd, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(security_id) DO UPDATE SET cik=excluded.cik, symbol=excluded.symbol, investable=excluded.investable, price_usd=excluded.price_usd, adv_usd=excluded.adv_usd, updated_at=excluded.updated_at",
            params![security_id, seed.cik, seed.ticker, bool_int(seed.investable), seed.price_usd, seed.adv_usd, utc_now()],
        )?;
    }
    let event = append_event(
        &tx,
        "security_master_imported",
        json!({
            "row_count": batch.input_rows,
            "security_count": batch.seeds.len(),
            "duplicate_cik_rows_collapsed": batch.duplicate_cik_rows_collapsed,
        }),
    )?;
    tx.commit()?;
    Ok(event)
}

#[derive(Debug, Clone)]
struct FilingIngestGroup {
    name: String,
    forms: String,
    limit: usize,
}

#[allow(clippy::too_many_arguments)]
fn cmd_ingest_universe(
    db_path: &Path,
    csv_path: &Path,
    raw_root: &Path,
    forms: &str,
    user_agent: &str,
    default_investable: bool,
    limit_per_cik: usize,
    annual_limit_per_cik: usize,
    quarterly_limit_per_cik: usize,
    sleep_s: f64,
    pull_sleep_s: f64,
    continue_on_error: bool,
    universe_name: &str,
    min_adv_usd: f64,
    min_fact_count: i64,
    server_url: Option<&str>,
) -> Result<()> {
    if forms.trim().is_empty() {
        return fail(2, "--forms must not be empty");
    }
    if sleep_s < 0.0 || pull_sleep_s < 0.0 {
        return fail(2, "sleep intervals must be non-negative");
    }
    let ingest_groups = filing_ingest_groups(
        forms,
        annual_limit_per_cik,
        quarterly_limit_per_cik,
        limit_per_cik,
    );
    if ingest_groups.is_empty() {
        return fail(
            2,
            "at least one filing limit must be positive for requested forms",
        );
    }

    let batch = read_security_seed_file(csv_path, default_investable)?;
    {
        let mut conn = open_db(db_path)?;
        ensure_db(&conn)?;
        let import_event = import_security_seed_batch(&mut conn, &batch)?;
        json_line(&import_event)?;
    }

    let mut batch_errors = Vec::<String>::new();
    let mut watched = 0usize;
    let mut fetched = 0usize;
    let mut parsed = 0usize;
    let mut watch_passes = 0usize;
    let mut pull_passes = 0usize;

    for (i, seed) in batch.seeds.iter().enumerate() {
        let mut issuer_ok = true;
        let mut issuer_fetched = true;
        let mut parsed_accessions = BTreeSet::<String>::new();
        for group in &ingest_groups {
            if let Err(err) = cmd_watch(
                db_path,
                &seed.cik,
                user_agent,
                group.limit,
                &group.forms,
                server_url,
            ) {
                issuer_ok = false;
                issuer_fetched = false;
                record_issuer_batch_error(
                    &mut batch_errors,
                    continue_on_error,
                    "watch",
                    &group.name,
                    seed,
                    err,
                )?;
                break;
            }
            watch_passes += 1;

            if let Err(err) = cmd_pull(
                db_path,
                raw_root,
                user_agent,
                None,
                Some(&seed.cik),
                &group.forms,
                group.limit,
                pull_sleep_s,
                server_url,
            ) {
                issuer_ok = false;
                issuer_fetched = false;
                record_issuer_batch_error(
                    &mut batch_errors,
                    continue_on_error,
                    "pull",
                    &group.name,
                    seed,
                    err,
                )?;
                break;
            }
            pull_passes += 1;

            match parse_fetched_filings_for_group(
                db_path,
                raw_root,
                seed,
                group,
                &mut parsed_accessions,
                continue_on_error,
                &mut batch_errors,
            ) {
                Ok(group_parsed) => parsed += group_parsed,
                Err(err) => return Err(err),
            }
        }
        if issuer_ok {
            watched += 1;
        }
        if issuer_fetched {
            fetched += 1;
        }
        if sleep_s > 0.0 && i + 1 < batch.seeds.len() {
            std::thread::sleep(std::time::Duration::from_secs_f64(sleep_s));
        }
    }

    let mut built_universe = false;
    if !universe_name.trim().is_empty() {
        if let Err(err) = cmd_univ(db_path, universe_name, min_adv_usd, min_fact_count) {
            if continue_on_error {
                let msg = format!("universe {universe_name}: {err}");
                eprintln!("batch_error {msg}");
                batch_errors.push(msg);
            } else {
                return fail(1, format!("universe {universe_name}: {err}"));
            }
        } else {
            built_universe = true;
        }
    }

    let status = if batch_errors.is_empty() {
        "completed"
    } else {
        "completed_with_errors"
    };
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let tx = conn.transaction()?;
    let event = append_event(
        &tx,
        "universe_ingest_completed",
        json!({
            "status": status,
            "csv": csv_path.to_string_lossy(),
            "raw_root": raw_root.to_string_lossy(),
            "forms": forms,
            "ingest_groups": ingest_group_payloads(&ingest_groups),
            "input_rows": batch.input_rows,
            "security_count": batch.seeds.len(),
            "duplicate_cik_rows_collapsed": batch.duplicate_cik_rows_collapsed,
            "limit_per_cik": limit_per_cik,
            "annual_limit_per_cik": annual_limit_per_cik,
            "quarterly_limit_per_cik": quarterly_limit_per_cik,
            "watched": watched,
            "pulled": fetched,
            "watch_passes": watch_passes,
            "pull_passes": pull_passes,
            "parsed": parsed,
            "universe_name": if universe_name.trim().is_empty() { Value::Null } else { json!(universe_name) },
            "universe_built": built_universe,
            "errors": batch_errors,
        }),
    )?;
    tx.commit()?;
    json_line(&event)
}

fn filing_ingest_groups(
    forms: &str,
    annual_limit: usize,
    quarterly_limit: usize,
    fallback_limit: usize,
) -> Vec<FilingIngestGroup> {
    let allowed = parse_form_set(forms);
    if allowed.is_empty() {
        return Vec::new();
    }
    if allowed.contains("*") || allowed.contains("ALL") {
        let limit = if fallback_limit == 0 {
            annual_limit + quarterly_limit
        } else {
            fallback_limit
        };
        if limit == 0 {
            return Vec::new();
        }
        return vec![FilingIngestGroup {
            name: "all".to_string(),
            forms: "*".to_string(),
            limit,
        }];
    }

    let annual_forms = selected_forms_csv(&allowed, &["10-K", "10-K/A"]);
    let quarterly_forms = selected_forms_csv(&allowed, &["10-Q", "10-Q/A"]);
    let covered = parse_form_set(&[annual_forms.as_str(), quarterly_forms.as_str()].join(","));
    let mut other_forms = allowed.difference(&covered).cloned().collect::<Vec<_>>();
    other_forms.sort();

    let mut groups = Vec::<FilingIngestGroup>::new();
    if !annual_forms.is_empty() && annual_limit > 0 {
        groups.push(FilingIngestGroup {
            name: "annual".to_string(),
            forms: annual_forms,
            limit: annual_limit,
        });
    }
    if !quarterly_forms.is_empty() && quarterly_limit > 0 {
        groups.push(FilingIngestGroup {
            name: "quarterly".to_string(),
            forms: quarterly_forms,
            limit: quarterly_limit,
        });
    }
    if !other_forms.is_empty() && fallback_limit > 0 {
        groups.push(FilingIngestGroup {
            name: "other".to_string(),
            forms: other_forms.join(","),
            limit: fallback_limit,
        });
    }
    groups
}

fn selected_forms_csv(allowed: &BTreeSet<String>, candidates: &[&str]) -> String {
    candidates
        .iter()
        .filter(|form| allowed.contains(**form))
        .copied()
        .collect::<Vec<_>>()
        .join(",")
}

fn ingest_group_payloads(groups: &[FilingIngestGroup]) -> Vec<Value> {
    groups
        .iter()
        .map(|group| json!({"name": group.name, "forms": group.forms, "limit": group.limit}))
        .collect()
}

fn record_issuer_batch_error(
    errors: &mut Vec<String>,
    continue_on_error: bool,
    action: &str,
    group_name: &str,
    seed: &SecuritySeed,
    err: anyhow::Error,
) -> Result<()> {
    let msg = format!(
        "{action} {group_name} {} {}: {err:#}",
        seed.cik, seed.ticker
    );
    if continue_on_error {
        eprintln!("batch_error {msg}");
        errors.push(msg);
        Ok(())
    } else {
        fail(1, msg)
    }
}

#[derive(Debug, Clone)]
struct FetchedFilingMetadata {
    accession: String,
    cik: String,
    form: String,
    filing_date: String,
    accepted_at: String,
    primary_document: Option<String>,
}

fn parse_fetched_filings_for_group(
    db_path: &Path,
    raw_root: &Path,
    seed: &SecuritySeed,
    group: &FilingIngestGroup,
    parsed_accessions: &mut BTreeSet<String>,
    continue_on_error: bool,
    errors: &mut Vec<String>,
) -> Result<usize> {
    let filings = fetched_filing_metadata_list(db_path, &seed.cik, &group.forms, group.limit)?;
    let mut parsed = 0usize;
    for filing in filings {
        if !parsed_accessions.insert(filing.accession.clone()) {
            continue;
        }
        let result = cmd_xbrl(
            db_path,
            Some(filing.accession.clone()),
            Some(filing.cik),
            Some(filing.form),
            Some(filing.filing_date),
            Some(filing.accepted_at),
            raw_root.to_path_buf(),
            None,
            false,
            group.forms.clone(),
        );
        match result {
            Ok(()) => parsed += 1,
            Err(err) if continue_on_error => {
                let primary = filing.primary_document.unwrap_or_default();
                let msg = format!("xbrl {} {} {}: {err:#}", group.name, seed.cik, primary);
                eprintln!("batch_error {msg}");
                errors.push(msg);
            }
            Err(err) => return fail(1, format!("xbrl {} {}: {err:#}", group.name, seed.cik)),
        }
    }
    Ok(parsed)
}

fn fetched_filing_metadata_list(
    db_path: &Path,
    cik: &str,
    forms: &str,
    limit: usize,
) -> Result<Vec<FetchedFilingMetadata>> {
    if limit == 0 {
        return Ok(Vec::new());
    }
    let conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let mut query = "SELECT accession_number, cik, form, filing_date, accepted_at, primary_document FROM filings WHERE raw_index_uri IS NOT NULL AND cik=?".to_string();
    let mut args = vec![normalize_cik(cik)];
    if let Some((clause, mut form_args)) = sql_form_filter_clause("form", forms) {
        query.push_str(&clause);
        args.append(&mut form_args);
    }
    query.push_str(" ORDER BY filing_date DESC, accepted_at DESC LIMIT ?");
    args.push(limit.to_string());
    let refs = args
        .iter()
        .map(|s| s as &dyn rusqlite::ToSql)
        .collect::<Vec<_>>();
    let mut stmt = conn.prepare(&query)?;
    let mut rows = stmt.query(refs.as_slice())?;
    let mut out = Vec::<FetchedFilingMetadata>::new();
    while let Some(row) = rows.next()? {
        out.push(FetchedFilingMetadata {
            accession: row.get(0)?,
            cik: row.get(1)?,
            form: row.get(2)?,
            filing_date: row.get(3)?,
            accepted_at: row.get(4)?,
            primary_document: row.get(5)?,
        });
    }
    Ok(out)
}

#[derive(Debug, Clone)]
struct DatabentoSecurityMasterRow {
    symbol: String,
    cik: String,
    issuer_name: String,
    shares_outstanding: f64,
}

#[derive(Debug, Clone)]
struct DatabentoBarRow {
    symbol: String,
    time: String,
    close: f64,
    volume: f64,
}

#[derive(Debug, Clone)]
struct DatabentoSecuritySeed {
    symbol: String,
    cik: String,
    issuer_name: String,
    price_usd: f64,
    adv_usd: f64,
    investable: bool,
    shares_outstanding: f64,
    market_cap_usd: f64,
    latest_bar_time: String,
    bar_count: usize,
}

#[allow(clippy::too_many_arguments)]
fn cmd_databento(
    db_path: &Path,
    out_path: Option<&Path>,
    symbols_raw: &str,
    dataset: &str,
    schema: &str,
    stype_in: &str,
    base_url: &str,
    start: Option<&str>,
    end: Option<&str>,
    lookback_days: i64,
    adv_window_days: usize,
    countries: &str,
    security_types: &str,
    default_investable: bool,
    import_db: bool,
    max_symbols: usize,
) -> Result<()> {
    if max_symbols == 0 {
        return fail(2, "--max-symbols must be positive");
    }
    if lookback_days <= 0 {
        return fail(2, "--lookback-days must be positive");
    }
    if adv_window_days == 0 {
        return fail(2, "--adv-window-days must be positive");
    }
    let symbols = parse_databento_symbols(symbols_raw, max_symbols)?;
    let (range_start, range_end) = databento_time_range(start, end, lookback_days);
    let api_key = env::var("DATABENTO_API_KEY")
        .unwrap_or_default()
        .trim()
        .to_string();
    if api_key.is_empty() {
        return fail(2, "DATABENTO_API_KEY is not set");
    }
    let base = base_url.trim_end_matches('/');
    let security_rows = fetch_databento_security_master(
        base,
        &api_key,
        &symbols,
        stype_in,
        countries,
        security_types,
    )?;
    let bar_rows = fetch_databento_ohlcv(
        base,
        &api_key,
        &symbols,
        dataset,
        schema,
        stype_in,
        &range_start,
        &range_end,
    )?;
    let seeds = build_databento_security_seeds(
        &symbols,
        &security_rows,
        &bar_rows,
        adv_window_days,
        default_investable,
    )?;

    if let Some(path) = out_path {
        write_databento_seed_csv(path, &seeds, dataset, schema)?;
    } else if !import_db {
        write_databento_seed_csv(Path::new("-"), &seeds, dataset, schema)?;
        return Ok(());
    }

    let mut import_event = Value::Null;
    if import_db {
        let batch = databento_security_seed_batch(&seeds);
        let mut conn = open_db(db_path)?;
        ensure_db(&conn)?;
        import_event = import_security_seed_batch(&mut conn, &batch)?;
    }
    json_line(&json!({
        "source": "databento",
        "dataset": dataset,
        "schema": schema,
        "symbols": symbols,
        "start": range_start,
        "end": range_end,
        "security_rows": security_rows.len(),
        "bar_rows": bar_rows.len(),
        "seed_rows": seeds.len(),
        "out": out_path.map(|p| p.to_string_lossy().to_string()),
        "imported": import_db,
        "import_event": import_event,
        "api_key_env": "DATABENTO_API_KEY",
        "adv_window_days": adv_window_days,
        "default_investable": default_investable,
    }))
}

fn parse_databento_symbols(raw: &str, max_symbols: usize) -> Result<Vec<String>> {
    if raw.trim().is_empty() {
        return fail(2, "--symbols is required");
    }
    let mut seen = BTreeSet::<String>::new();
    let mut symbols = Vec::<String>::new();
    for part in raw.split(',') {
        let symbol = part.trim().to_ascii_uppercase();
        if symbol.is_empty() {
            continue;
        }
        if symbol == "ALL_SYMBOLS" {
            return fail(
                2,
                "ALL_SYMBOLS is not allowed; pass an explicit limited symbol list",
            );
        }
        if seen.insert(symbol.clone()) {
            symbols.push(symbol);
        }
    }
    if symbols.is_empty() {
        return fail(2, "--symbols contains no valid symbols");
    }
    if symbols.len() > max_symbols {
        return fail(
            2,
            format!(
                "--symbols has {} symbols, over --max-symbols {max_symbols}",
                symbols.len()
            ),
        );
    }
    Ok(symbols)
}

fn databento_time_range(
    start: Option<&str>,
    end: Option<&str>,
    lookback_days: i64,
) -> (String, String) {
    let range_end = end
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(ToOwned::to_owned)
        .unwrap_or_else(|| OffsetDateTime::now_utc().date().to_string());
    let range_start = start
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(ToOwned::to_owned)
        .unwrap_or_else(|| {
            Date::parse(&range_end, format_description!("[year]-[month]-[day]"))
                .map(|d| (d - Duration::days(lookback_days)).to_string())
                .unwrap_or_else(|_| {
                    (OffsetDateTime::now_utc() - Duration::days(lookback_days))
                        .date()
                        .to_string()
                })
        });
    (range_start, range_end)
}

fn fetch_databento_security_master(
    base_url: &str,
    api_key: &str,
    symbols: &[String],
    stype_in: &str,
    countries: &str,
    security_types: &str,
) -> Result<Vec<DatabentoSecurityMasterRow>> {
    let symbols_joined = symbols.join(",");
    let mut form = vec![
        ("symbols", symbols_joined.as_str()),
        ("stype_in", stype_in),
        ("compression", "none"),
    ];
    if !countries.trim().is_empty() {
        form.push(("countries", countries));
    }
    if !security_types.trim().is_empty() {
        form.push(("security_types", security_types));
    }
    let body = databento_post_form(base_url, api_key, "security_master.get_last", &form)?;
    parse_databento_security_master_jsonl(&body)
}

#[allow(clippy::too_many_arguments)]
fn fetch_databento_ohlcv(
    base_url: &str,
    api_key: &str,
    symbols: &[String],
    dataset: &str,
    schema: &str,
    stype_in: &str,
    start: &str,
    end: &str,
) -> Result<Vec<DatabentoBarRow>> {
    let symbols_joined = symbols.join(",");
    let form = vec![
        ("dataset", dataset),
        ("symbols", symbols_joined.as_str()),
        ("schema", schema),
        ("start", start),
        ("end", end),
        ("stype_in", stype_in),
        ("stype_out", "raw_symbol"),
        ("encoding", "json"),
        ("compression", "none"),
        ("pretty_px", "true"),
        ("pretty_ts", "true"),
        ("map_symbols", "true"),
    ];
    let body = databento_post_form(base_url, api_key, "timeseries.get_range", &form)?;
    parse_databento_bars_jsonl(&body)
}

fn databento_post_form(
    base_url: &str,
    api_key: &str,
    method: &str,
    form: &[(&str, &str)],
) -> Result<String> {
    let endpoint = format!("{}/{}", base_url.trim_end_matches('/'), method);
    let auth = base64::engine::general_purpose::STANDARD.encode(format!("{api_key}:"));
    let response = ureq::post(&endpoint)
        .timeout(std::time::Duration::from_secs(60))
        .set("Authorization", &format!("Basic {auth}"))
        .set("Accept", "application/json")
        .send_form(form);
    match response {
        Ok(resp) => resp.into_string().map_err(Into::into),
        Err(ureq::Error::Status(code, resp)) => {
            let body = resp.into_string().unwrap_or_default();
            fail(
                2,
                format!("databento {method} returned HTTP {code}: {}", body.trim()),
            )
        }
        Err(err) => Err(err.into()),
    }
}

fn parse_databento_security_master_jsonl(body: &str) -> Result<Vec<DatabentoSecurityMasterRow>> {
    let mut out = Vec::<DatabentoSecurityMasterRow>::new();
    for line in body.lines().map(str::trim).filter(|line| !line.is_empty()) {
        let raw: Value = serde_json::from_str(line)?;
        let symbol =
            value_string(&raw, &["symbol", "raw_symbol", "instrument_id"]).to_ascii_uppercase();
        if symbol.is_empty() {
            continue;
        }
        out.push(DatabentoSecurityMasterRow {
            symbol,
            cik: normalize_cik(&value_string(&raw, &["cik", "issuer_cik"])),
            issuer_name: value_string(
                &raw,
                &["issuer_name", "name", "security_description", "description"],
            ),
            shares_outstanding: value_f64(&raw, &["shares_outstanding", "outstanding_shares"])
                .unwrap_or(0.0),
        });
    }
    Ok(out)
}

fn parse_databento_bars_jsonl(body: &str) -> Result<Vec<DatabentoBarRow>> {
    let mut out = Vec::<DatabentoBarRow>::new();
    for line in body.lines().map(str::trim).filter(|line| !line.is_empty()) {
        let raw: Value = serde_json::from_str(line)?;
        let symbol = value_string(&raw, &["symbol", "raw_symbol"]).to_ascii_uppercase();
        if symbol.is_empty() {
            continue;
        }
        out.push(DatabentoBarRow {
            symbol,
            time: value_string(&raw, &["ts_event", "ts_recv", "ts_out", "timestamp"]),
            close: value_f64(&raw, &["close", "close_price", "px_close"]).unwrap_or(0.0),
            volume: value_f64(&raw, &["volume", "size"]).unwrap_or(0.0),
        });
    }
    Ok(out)
}

fn build_databento_security_seeds(
    symbols: &[String],
    security_rows: &[DatabentoSecurityMasterRow],
    bar_rows: &[DatabentoBarRow],
    adv_window_days: usize,
    investable: bool,
) -> Result<Vec<DatabentoSecuritySeed>> {
    let mut security_by_symbol = BTreeMap::<String, DatabentoSecurityMasterRow>::new();
    for row in security_rows {
        security_by_symbol.insert(row.symbol.clone(), row.clone());
    }
    let mut bars_by_symbol = BTreeMap::<String, Vec<DatabentoBarRow>>::new();
    for row in bar_rows {
        if row.close > 0.0 && row.volume >= 0.0 {
            bars_by_symbol
                .entry(row.symbol.clone())
                .or_default()
                .push(row.clone());
        }
    }
    let mut out = Vec::<DatabentoSecuritySeed>::new();
    let mut missing = Vec::<String>::new();
    for symbol in symbols {
        let Some(security) = security_by_symbol.get(symbol) else {
            missing.push(format!("{symbol}:security_master"));
            continue;
        };
        if security.cik == "0000000000" {
            missing.push(format!("{symbol}:cik"));
            continue;
        }
        let Some(bars) = bars_by_symbol.get_mut(symbol) else {
            missing.push(format!("{symbol}:ohlcv"));
            continue;
        };
        bars.sort_by(|a, b| a.time.cmp(&b.time));
        let window_start = bars.len().saturating_sub(adv_window_days);
        let window = &bars[window_start..];
        let Some(latest) = bars.last() else {
            missing.push(format!("{symbol}:ohlcv"));
            continue;
        };
        let adv_usd =
            window.iter().map(|bar| bar.close * bar.volume).sum::<f64>() / window.len() as f64;
        let market_cap_usd = if security.shares_outstanding > 0.0 {
            latest.close * security.shares_outstanding
        } else {
            0.0
        };
        out.push(DatabentoSecuritySeed {
            symbol: symbol.clone(),
            cik: security.cik.clone(),
            issuer_name: security.issuer_name.clone(),
            price_usd: latest.close,
            adv_usd,
            investable,
            shares_outstanding: security.shares_outstanding,
            market_cap_usd,
            latest_bar_time: latest.time.clone(),
            bar_count: bars.len(),
        });
    }
    if !missing.is_empty() {
        return fail(
            2,
            format!(
                "databento response missing required rows: {}",
                missing.join(",")
            ),
        );
    }
    Ok(out)
}

fn write_databento_seed_csv(
    path: &Path,
    rows: &[DatabentoSecuritySeed],
    dataset: &str,
    schema: &str,
) -> Result<()> {
    let writer: Box<dyn Write> = if path == Path::new("-") {
        Box::new(io::stdout())
    } else {
        Box::new(fs::File::create(path)?)
    };
    let mut wtr = csv::Writer::from_writer(writer);
    wtr.write_record([
        "cik",
        "ticker",
        "price_usd",
        "adv_usd",
        "investable",
        "company",
        "shares_outstanding",
        "market_cap_usd",
        "databento_dataset",
        "databento_schema",
        "databento_bar_count",
        "databento_latest_ts",
    ])?;
    for row in rows {
        wtr.write_record([
            row.cik.as_str(),
            row.symbol.as_str(),
            &float_string(row.price_usd),
            &float_string(row.adv_usd),
            if row.investable { "1" } else { "0" },
            row.issuer_name.as_str(),
            &float_string(row.shares_outstanding),
            &float_string(row.market_cap_usd),
            dataset,
            schema,
            &row.bar_count.to_string(),
            row.latest_bar_time.as_str(),
        ])?;
    }
    wtr.flush()?;
    Ok(())
}

fn databento_security_seed_batch(rows: &[DatabentoSecuritySeed]) -> SecuritySeedBatch {
    SecuritySeedBatch {
        input_rows: rows.len(),
        duplicate_cik_rows_collapsed: 0,
        seeds: rows
            .iter()
            .map(|row| SecuritySeed {
                cik: row.cik.clone(),
                ticker: row.symbol.clone(),
                price_usd: row.price_usd,
                adv_usd: row.adv_usd,
                investable: row.investable,
            })
            .collect(),
    }
}

fn value_string(raw: &Value, keys: &[&str]) -> String {
    for key in keys {
        if let Some(value) = raw.get(*key) {
            if let Some(s) = value.as_str() {
                return s.trim().to_string();
            }
            if value.is_number() || value.is_boolean() {
                return value.to_string();
            }
        }
    }
    String::new()
}

fn value_f64(raw: &Value, keys: &[&str]) -> Option<f64> {
    for key in keys {
        if let Some(value) = raw.get(*key) {
            if let Some(v) = value.as_f64() {
                return Some(v);
            }
            if let Some(s) = value.as_str() {
                if let Ok(v) = s.trim().replace(',', "").parse::<f64>() {
                    return Some(v);
                }
            }
        }
    }
    None
}

fn float_string(value: f64) -> String {
    if value.is_finite() {
        let s = format!("{value:.6}");
        s.trim_end_matches('0').trim_end_matches('.').to_string()
    } else {
        "0".to_string()
    }
}

fn cmd_pos(db_path: &Path, cik: &str, quantity: f64, mv: f64, weight: f64) -> Result<()> {
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let security_id = normalize_cik(cik).parse::<i64>().unwrap_or(0);
    let tx = conn.transaction()?;
    tx.execute(
        "INSERT INTO positions(security_id, quantity_shares, market_value_usd, weight_ratio, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(security_id) DO UPDATE SET quantity_shares=excluded.quantity_shares, market_value_usd=excluded.market_value_usd, weight_ratio=excluded.weight_ratio, updated_at=excluded.updated_at",
        params![security_id, quantity, mv, weight, utc_now()],
    )?;
    let event = append_event(
        &tx,
        "position_upserted",
        json!({"security_id": security_id, "quantity_shares": quantity, "market_value_usd": mv, "weight_ratio": weight}),
    )?;
    tx.commit()?;
    json_line(&event)
}

fn cmd_watch(
    db_path: &Path,
    cik: &str,
    user_agent: &str,
    limit: usize,
    forms: &str,
    server_url: Option<&str>,
) -> Result<()> {
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let cik10 = normalize_cik(cik);
    let body = sec_get(
        &format!("https://data.sec.gov/submissions/CIK{cik10}.json"),
        user_agent,
        server_url,
    )?;
    let payload: Value = serde_json::from_slice(&body)?;
    let recent = payload
        .get("filings")
        .and_then(|v| v.get("recent"))
        .context("SEC submissions payload missing filings.recent")?;
    let accessions = recent
        .get("accessionNumber")
        .and_then(Value::as_array)
        .context("SEC submissions payload missing accessionNumber")?;
    let allowed = parse_form_set(forms);
    let tx = conn.transaction()?;
    let mut emitted = 0usize;
    for i in 0..accessions.len() {
        if emitted >= limit {
            break;
        }
        let accession = recent_string(recent, "accessionNumber", i);
        let form = recent_string(recent, "form", i);
        if accession.is_empty() || !form_allowed(&allowed, &form) {
            continue;
        }
        let primary = recent_string(recent, "primaryDocument", i);
        let source_url = archive_url(&cik10, &accession, Some(&primary));
        tx.execute(
            "INSERT OR IGNORE INTO filings(accession_number, cik, form, filing_date, accepted_at, primary_document, source_url, ingested_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
            params![accession, cik10, form, recent_string(recent, "filingDate", i), recent_string(recent, "acceptanceDateTime", i), primary, source_url, utc_now()],
        )?;
        let event = append_event(
            &tx,
            "filing_seen",
            json!({"cik": cik10.clone(), "accession_number": accession, "form": form, "filing_date": recent_string(recent, "filingDate", i), "accepted_at": recent_string(recent, "acceptanceDateTime", i), "primary_document": primary, "source_url": source_url}),
        )?;
        json_line(&event)?;
        emitted += 1;
    }
    tx.commit()?;
    eprintln!("filing_seen emitted={emitted}");
    Ok(())
}

fn cmd_pull(
    db_path: &Path,
    raw_root: &Path,
    user_agent: &str,
    accession_filter: Option<&str>,
    cik_filter: Option<&str>,
    forms: &str,
    limit: usize,
    sleep_s: f64,
    server_url: Option<&str>,
) -> Result<()> {
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let mut query = "SELECT accession_number, cik, primary_document FROM filings WHERE (raw_index_uri IS NULL OR (coalesce(primary_document, '') != '' AND raw_primary_uri IS NULL))".to_string();
    let mut args = Vec::<String>::new();
    if let Some(accession) = accession_filter.filter(|s| !s.is_empty()) {
        query =
            "SELECT accession_number, cik, primary_document FROM filings WHERE accession_number=?"
                .to_string();
        args.push(accession.to_string());
    } else if let Some(cik) = cik_filter.filter(|s| !s.is_empty()) {
        query.push_str(" AND cik=?");
        args.push(normalize_cik(cik));
    }
    if let Some((clause, mut form_args)) = sql_form_filter_clause("form", forms) {
        query.push_str(&clause);
        args.append(&mut form_args);
    }
    query.push_str(" ORDER BY filing_date DESC LIMIT ?");
    args.push(limit.to_string());
    let refs = args
        .iter()
        .map(|s| s as &dyn rusqlite::ToSql)
        .collect::<Vec<_>>();
    let pull_rows = {
        let mut stmt = conn.prepare(&query)?;
        let mut rows = stmt.query(refs.as_slice())?;
        let mut out = Vec::<(String, String, Option<String>)>::new();
        while let Some(row) = rows.next()? {
            out.push((row.get(0)?, row.get(1)?, row.get(2)?));
        }
        out
    };
    let tx = conn.transaction()?;
    let mut fetched = 0usize;
    for (accession, cik, primary) in pull_rows {
        let cik10 = normalize_cik(&cik);
        let dir = raw_root.join(&cik10).join(accession.replace('-', ""));
        fs::create_dir_all(&dir)?;
        let index_url = archive_url(&cik10, &accession, None);
        let index_data = sec_get(&index_url, user_agent, server_url)?;
        let index_path = dir.join("index.json");
        fs::write(&index_path, &index_data)?;
        let names = package_artifact_names(&index_data, primary.as_deref().unwrap_or_default())?;
        let mut hashes = vec![sha256_hex(&index_data)];
        let mut primary_path = String::new();
        for name in &names {
            let path = archive_local_path(&dir, name)?;
            if let Some(parent) = path.parent() {
                fs::create_dir_all(parent)?;
            }
            let data = sec_get(
                &archive_url(&cik10, &accession, Some(name)),
                user_agent,
                server_url,
            )?;
            fs::write(&path, &data)?;
            hashes.push(sha256_hex(&data));
            if primary.as_deref() == Some(name.as_str()) {
                primary_path = path.to_string_lossy().into_owned();
            }
            if sleep_s > 0.0 {
                std::thread::sleep(std::time::Duration::from_secs_f64(sleep_s));
            }
        }
        let combined = sha256_hex(hashes.join("\n").as_bytes());
        let index_path_text = index_path.to_string_lossy().into_owned();
        tx.execute("UPDATE filings SET raw_index_uri=?, raw_primary_uri=?, raw_sha256=?, ingested_at=? WHERE accession_number=?", params![index_path_text, primary_path, combined, utc_now(), accession])?;
        let event = append_event(
            &tx,
            "filing_fetched",
            json!({"accession_number": accession, "cik": cik10, "raw_index_uri": index_path_text, "raw_primary_uri": primary_path, "raw_sha256": combined, "package_file_count": names.len() + 1}),
        )?;
        json_line(&event)?;
        fetched += 1;
    }
    tx.commit()?;
    eprintln!("filing_fetched emitted={fetched}");
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn cmd_xbrl(
    db_path: &Path,
    accession: Option<String>,
    cik: Option<String>,
    form: Option<String>,
    filed_at: Option<String>,
    accepted_at: Option<String>,
    raw_root: PathBuf,
    package_root: Option<PathBuf>,
    latest: bool,
    forms: String,
) -> Result<()> {
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let summary = parse_xbrl_package(
        &mut conn,
        ParseRequest {
            accession,
            cik,
            form,
            filed_at,
            accepted_at,
            raw_root,
            package_root,
            latest,
            forms: (!forms.is_empty()).then_some(forms),
        },
    )?;
    eprintln!("xbrl packages processed documents={} raw_inserted={} selected_inserted={} derived_inserted=0 exceptions_inserted=0 parse_errors={}", summary.documents_stored.saturating_sub(1), summary.raw_facts_inserted, summary.canonical_selected_inserted, summary.parse_errors.len());
    json_line(&summary.event)
}

fn cmd_univ(db_path: &Path, name: &str, min_adv: f64, min_facts: i64) -> Result<()> {
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let members = {
        let mut stmt = conn.prepare("SELECT s.security_id FROM securities s WHERE s.investable=1 AND s.adv_usd >= ? AND (SELECT COUNT(*) FROM canonical_observations co WHERE co.security_id=s.security_id AND co.observation_status IN ('selected','derived')) >= ? ORDER BY s.security_id")?;
        let rows = stmt.query_map(params![min_adv, min_facts], |row| row.get::<_, i64>(0))?;
        rows.collect::<rusqlite::Result<Vec<_>>>()?
    };
    let snapshot_id = stable_id("universe", json!({"name": name, "created_at": utc_now()}));
    let tx = conn.transaction()?;
    let rule = json!({"min_adv_usd": min_adv, "min_fact_count": min_facts});
    tx.execute("INSERT INTO universe_snapshots(snapshot_id, name, created_at, rule_json) VALUES (?, ?, ?, ?)", params![snapshot_id, name, utc_now(), canonical_json(&rule)])?;
    for sid in &members {
        tx.execute(
            "INSERT OR IGNORE INTO universe_members(snapshot_id, security_id) VALUES (?, ?)",
            params![snapshot_id, sid],
        )?;
    }
    let event = append_event(
        &tx,
        "universe_snapshot_created",
        json!({"snapshot_id": snapshot_id, "name": name, "member_count": members.len(), "rule": rule}),
    )?;
    tx.commit()?;
    json_line(&event)
}

fn cmd_recon(db_path: &Path, portfolio_value: f64, cash: f64, reconciled: bool) -> Result<()> {
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let tx = conn.transaction()?;
    tx.execute("INSERT INTO broker_state(id, reconciled, checked_at, portfolio_value_usd, cash_usd) VALUES (1, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET reconciled=excluded.reconciled, checked_at=excluded.checked_at, portfolio_value_usd=excluded.portfolio_value_usd, cash_usd=excluded.cash_usd", params![bool_int(reconciled), utc_now(), portfolio_value, cash])?;
    let event = append_event(
        &tx,
        "broker_reconciliation_snapshot",
        json!({"reconciled": reconciled, "portfolio_value_usd": portfolio_value, "cash_usd": cash, "source": "manual_or_mock"}),
    )?;
    tx.commit()?;
    json_line(&event)
}

fn cmd_plan(args: ModelArgs) -> Result<()> {
    let mut conn = open_db(&args.db)?;
    ensure_db(&conn)?;
    let core = Core::load(&args.core_lib)?;
    let facts = load_facts(&conn)?;
    let securities = load_securities(&conn)?;
    let positions = load_positions(&conn)?;
    let config = FaModelConfigV1 {
        abi_version: ABI_VERSION,
        target_gross_exposure_ratio: args.target_gross_exposure_ratio,
        max_name_weight_ratio: args.max_name_weight_ratio,
        min_expected_return_proxy: args.min_expected_return_proxy,
        min_abs_order_notional_usd: args.min_abs_order_notional_usd,
        max_forecast_abs_growth_ratio: args.max_forecast_abs_growth_ratio,
        max_fact_age_s: (args.max_fact_age_days * 86_400.0) as i64,
    };
    let limits = make_risk_limits(&conn, risk_args_from_model(&args, args.max_fact_age_days))?;
    let mut out = unsafe { core.build_plan(&facts, &securities, &positions, &config, &limits)? };
    let diagnostics = diagnostic_string(&out.diagnostics);
    let run_id = Uuid::new_v4().to_string();
    let tx = conn.transaction()?;
    let config_json = json!({"target_gross_exposure_ratio": args.target_gross_exposure_ratio, "max_name_weight_ratio": args.max_name_weight_ratio, "min_expected_return_proxy": args.min_expected_return_proxy, "min_abs_order_notional_usd": args.min_abs_order_notional_usd, "max_forecast_abs_growth_ratio": args.max_forecast_abs_growth_ratio, "max_fact_age_days": args.max_fact_age_days, "core_lib": args.core_lib});
    tx.execute(
        "INSERT INTO model_runs(run_id, occurred_at, config_json, diagnostics) VALUES (?, ?, ?, ?)",
        params![run_id, utc_now(), canonical_json(&config_json), diagnostics],
    )?;
    unsafe {
        let (forecasts, targets, intents) = plan_slices(&out);
        for f in forecasts {
            tx.execute("INSERT INTO forecasts(run_id, security_id, revenue_growth_ratio, earnings_growth_ratio, expected_return_proxy, confidence_ratio, quality_flags, reason) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", params![run_id, f.security_id as i64, f.revenue_growth_ratio, f.earnings_growth_ratio, f.expected_return_proxy, f.confidence_ratio, f.quality_flags as i64, reason_string(&f.reason)])?;
        }
        persist_targets(&tx, &run_id, targets)?;
        persist_intents(&tx, &run_id, intents)?;
        let event = append_event(
            &tx,
            "plan_completed",
            json!({"run_id": run_id, "forecast_count": forecasts.len(), "target_weight_count": targets.len(), "order_intent_count": intents.len(), "diagnostics": diagnostics}),
        )?;
        tx.commit()?;
        core.free_plan(&mut out);
        json_line(&event)
    }
}

fn cmd_value(args: ModelArgs) -> Result<()> {
    let mut conn = open_db(&args.db)?;
    ensure_db(&conn)?;
    let core = Core::load(&args.core_lib)?;
    let (assumption_id, assumption_hash) = ensure_assumptions(&conn, &args.operator_label)?;
    let securities = load_securities(&conn)?;
    let positions = load_positions(&conn)?;
    let facts = load_facts(&conn)?;
    let config = FaModelConfigV1 {
        abi_version: ABI_VERSION,
        target_gross_exposure_ratio: args.target_gross_exposure_ratio,
        max_name_weight_ratio: args.max_name_weight_ratio,
        min_expected_return_proxy: args.min_expected_return_proxy,
        min_abs_order_notional_usd: args.min_abs_order_notional_usd,
        max_forecast_abs_growth_ratio: args.max_forecast_abs_growth_ratio,
        max_fact_age_s: (args.max_statement_age_days * 86_400.0) as i64,
    };
    let limits = make_risk_limits(
        &conn,
        risk_args_from_model(&args, args.max_statement_age_days),
    )?;
    let mut statement_out =
        unsafe { core.build_statements(&facts, &securities, &config, &limits)? };
    let statement_diagnostics = diagnostic_string(&statement_out.diagnostics);
    let statements = unsafe { statement_slice(&statement_out).to_vec() };
    let scenarios = valuation_scenarios();
    let mut out = unsafe {
        core.build_value(
            &statements,
            &securities,
            &positions,
            &scenarios,
            &config,
            &limits,
        )?
    };
    let diagnostics = diagnostic_string(&out.diagnostics);
    let run_id = Uuid::new_v4().to_string();
    let tx = conn.transaction()?;
    let mut inserted_statements = 0usize;
    for s in &statements {
        let start_date = date_from_epoch_day(s.period_start_day);
        let end_date = date_from_epoch_day(s.period_end_day);
        let available_at = utc_from_epoch_second(s.available_at_epoch_s);
        if end_date.is_empty() || available_at.is_empty() {
            bail!("C++ statement builder returned invalid statement date");
        }
        let source_hash = statement_snapshot_source_hash(s);
        let changed = tx.execute(
            "INSERT OR IGNORE INTO statement_snapshots(snapshot_id, security_id, period_start_date, period_end_date, available_at, is_ttm, revenue_usd, net_income_usd, diluted_eps_usd, diluted_shares, operating_cash_flow_usd, capex_usd, free_cash_flow_usd, cash_usd, debt_usd, net_debt_usd, quality_flags, source_hash, inserted_at) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            params![statement_snapshot_id(s, &source_hash), s.security_id as i64, optional_nonempty(start_date), end_date, available_at, s.revenue_usd, s.net_income_usd, s.diluted_eps_usd, s.diluted_shares, s.operating_cash_flow_usd, s.capex_usd, s.free_cash_flow_usd, s.cash_usd, s.debt_usd, s.net_debt_usd, s.quality_flags as i64, source_hash, utc_now()],
        )?;
        inserted_statements += changed;
    }
    let config_json = json!({"model_version": "valuation_v2", "target_gross_exposure_ratio": args.target_gross_exposure_ratio, "max_name_weight_ratio": args.max_name_weight_ratio, "assumption_set_id": assumption_id.clone(), "assumption_sha256": assumption_hash.clone(), "core_lib": args.core_lib});
    tx.execute(
        "INSERT INTO model_runs(run_id, occurred_at, config_json, diagnostics) VALUES (?, ?, ?, ?)",
        params![run_id, utc_now(), canonical_json(&config_json), diagnostics],
    )?;
    unsafe {
        let (valuations, targets, intents) = value_slices(&out);
        for v in valuations {
            tx.execute("INSERT INTO valuations(run_id, security_id, current_price_usd, intrinsic_value_per_share_usd, expected_return_ratio, market_cap_usd, enterprise_value_usd, fcf_yield_ratio, net_debt_usd, confidence_ratio, quality_flags, reason) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", params![run_id, v.security_id as i64, v.current_price_usd, v.intrinsic_value_per_share_usd, v.expected_return_ratio, v.market_cap_usd, v.enterprise_value_usd, v.fcf_yield_ratio, v.net_debt_usd, v.confidence_ratio, v.quality_flags as i64, reason_string(&v.reason)])?;
            tx.execute("INSERT INTO forecast_outcomes(forecast_id, run_id, security_id, forecast_made_at, horizon_days, expected_return_ratio, confidence_ratio, price_at_forecast_usd) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", params![Uuid::new_v4().to_string(), run_id, v.security_id as i64, utc_now(), args.forecast_horizon_days, v.expected_return_ratio, v.confidence_ratio, v.current_price_usd])?;
        }
        persist_targets(&tx, &run_id, targets)?;
        persist_intents(&tx, &run_id, intents)?;
        let event = append_event(
            &tx,
            "value_completed",
            json!({"run_id": run_id, "statement_count": statements.len(), "statement_snapshots_inserted": inserted_statements, "valuation_count": valuations.len(), "target_weight_count": targets.len(), "order_intent_count": intents.len(), "forecast_outcome_count": valuations.len(), "assumption_set_id": assumption_id, "model_input_sha256": sha256_hex(canonical_json(&config_json).as_bytes()), "autonomy_approved": false, "autonomy_reason": "trading_mode=observe blocks autonomous staging", "statement_builder_diagnostics": statement_diagnostics, "diagnostics": diagnostics}),
        )?;
        tx.commit()?;
        core.free_value(&mut out);
        core.free_statements(&mut statement_out);
        json_line(&event)
    }
}

fn cmd_gate(args: GateArgs) -> Result<()> {
    let mut conn = open_db(&args.db)?;
    ensure_db(&conn)?;
    let core = Core::load(&args.core_lib)?;
    let (intents, ids) = load_pending_intents(&conn, args.run_id.as_deref())?;
    let securities = load_securities(&conn)?;
    let limits = make_risk_limits(
        &conn,
        RiskArgs {
            portfolio_value: args.portfolio_value_usd,
            cash: args.cash_usd,
            max_name_weight: args.max_name_weight_ratio,
            max_order_notional: args.max_order_notional_usd,
            min_adv: args.min_adv_usd,
            max_adv_participation: args.max_adv_participation_ratio,
            max_reconciliation_age_s: args.max_reconciliation_age_s,
        },
    )?;
    let mut out = unsafe { core.check_risk(&intents, &securities, &limits)? };
    let diagnostics = diagnostic_string(&out.diagnostics);
    let tx = conn.transaction()?;
    unsafe {
        let decisions = gate_decisions(&out);
        let mut approved = 0usize;
        for (i, d) in decisions.iter().enumerate() {
            let is_approved = d.decision == 1;
            if is_approved {
                approved += 1;
            }
            let intent_id = ids.get(i).cloned().unwrap_or_default();
            tx.execute("INSERT INTO risk_decisions(decision_id, intent_id, security_id, approved, reason, notional_usd, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)", params![Uuid::new_v4().to_string(), intent_id, d.security_id as i64, bool_int(is_approved), reason_string(&d.reason), d.notional_usd, utc_now()])?;
        }
        let event = append_event(
            &tx,
            "gate_completed",
            json!({"intent_count": decisions.len(), "approved_count": approved, "rejected_count": decisions.len().saturating_sub(approved), "diagnostics": diagnostics}),
        )?;
        tx.commit()?;
        core.free_gate(&mut out);
        json_line(&event)
    }
}

fn cmd_stage(db_path: &Path, run_id: Option<&str>, limit: i64) -> Result<()> {
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let mode = setting(&conn, "trading_mode");
    if !matches!(mode.as_str(), "stage" | "paper" | "live_limited" | "live") {
        return fail(
            3,
            format!("cannot stage orders in mode={mode:?}; set mode explicitly"),
        );
    }
    let mut query = "SELECT rd.decision_id, rd.intent_id, rd.security_id, oi.side, rd.notional_usd FROM risk_decisions rd JOIN order_intents oi ON oi.intent_id=rd.intent_id LEFT JOIN staged_orders so ON so.decision_id=rd.decision_id WHERE rd.approved=1 AND so.decision_id IS NULL".to_string();
    let mut args = Vec::<String>::new();
    if let Some(run_id) = run_id.filter(|s| !s.is_empty()) {
        query.push_str(" AND oi.run_id=?");
        args.push(run_id.to_string());
    }
    query.push_str(" ORDER BY rd.created_at LIMIT ?");
    args.push(limit.to_string());
    let refs = args
        .iter()
        .map(|s| s as &dyn rusqlite::ToSql)
        .collect::<Vec<_>>();
    let stage_rows = {
        let mut stmt = conn.prepare(&query)?;
        let mut rows = stmt.query(refs.as_slice())?;
        let mut out = Vec::<(String, String, i64, String, f64)>::new();
        while let Some(row) = rows.next()? {
            out.push((
                row.get(0)?,
                row.get(1)?,
                row.get(2)?,
                row.get(3)?,
                row.get(4)?,
            ));
        }
        out
    };
    let tx = conn.transaction()?;
    let mut staged = 0usize;
    for (decision_id, intent_id, security_id, side, notional) in stage_rows {
        let staged_id = Uuid::new_v4().to_string();
        tx.execute("INSERT INTO staged_orders(staged_order_id, decision_id, intent_id, security_id, side, notional_usd, staged_at) VALUES (?, ?, ?, ?, ?, ?, ?)", params![staged_id, decision_id, intent_id, security_id, side, notional, utc_now()])?;
        let event = append_event(
            &tx,
            "order_staged",
            json!({"staged_order_id": staged_id, "decision_id": decision_id, "intent_id": intent_id, "security_id": security_id, "side": side, "notional_usd": notional}),
        )?;
        json_line(&event)?;
        staged += 1;
    }
    tx.commit()?;
    eprintln!("orders staged={staged}");
    Ok(())
}

fn cmd_send(
    db_path: &Path,
    adapter: &str,
    limit: i64,
    max_reconciliation_age_s: i64,
) -> Result<()> {
    if adapter != "mock" {
        return fail(3, "only --adapter mock is implemented");
    }
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let mode = setting(&conn, "trading_mode");
    if !matches!(mode.as_str(), "paper" | "live_limited" | "live") {
        return fail(3, format!("cannot send broker orders in mode={mode:?}"));
    }
    require_fresh_broker_reconciliation(&conn, max_reconciliation_age_s)?;
    let send_rows = {
        let mut stmt = conn.prepare("SELECT so.staged_order_id, so.security_id, so.side, so.notional_usd FROM staged_orders so LEFT JOIN broker_events be ON be.staged_order_id=so.staged_order_id AND be.event_type='mock_order_submitted' WHERE be.broker_event_id IS NULL ORDER BY so.staged_at LIMIT ?")?;
        let mut rows = stmt.query([limit])?;
        let mut out = Vec::<(String, i64, String, f64)>::new();
        while let Some(row) = rows.next()? {
            out.push((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?));
        }
        out
    };
    let tx = conn.transaction()?;
    let mut sent = 0usize;
    for (staged_id, security_id, side, notional) in send_rows {
        let event_id = Uuid::new_v4().to_string();
        let mut payload = json!({"adapter": "mock", "staged_order_id": staged_id, "security_id": security_id, "side": side, "notional_usd": notional, "mode": mode.clone()});
        tx.execute("INSERT INTO broker_events(broker_event_id, staged_order_id, event_type, payload_json, occurred_at) VALUES (?, ?, 'mock_order_submitted', ?, ?)", params![event_id, staged_id, canonical_json(&payload), utc_now()])?;
        payload["broker_event_id"] = json!(event_id);
        let event = append_event(&tx, "broker_order_submitted_mock", payload)?;
        json_line(&event)?;
        sent += 1;
    }
    tx.commit()?;
    eprintln!("mock broker submissions={sent}");
    Ok(())
}

fn cmd_mode(db_path: &Path, mode: &str) -> Result<()> {
    let allowed = [
        "observe",
        "shadow",
        "stage",
        "paper",
        "live_limited",
        "live",
        "halted",
    ]
    .into_iter()
    .collect::<BTreeSet<_>>();
    if !allowed.contains(mode) {
        return fail(3, format!("invalid mode {mode:?}"));
    }
    set_mode(db_path, mode, "trading_mode_set", json!({"mode": mode}))
}

fn set_mode(db_path: &Path, mode: &str, event_type: &str, payload: Value) -> Result<()> {
    let mut conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let tx = conn.transaction()?;
    tx.execute("INSERT INTO settings(key, value, updated_at) VALUES ('trading_mode', ?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at", params![mode, utc_now()])?;
    let event = append_event(&tx, event_type, payload)?;
    tx.commit()?;
    json_line(&event)
}

fn cmd_stat(db_path: &Path) -> Result<()> {
    let conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let tables = [
        "events",
        "securities",
        "filings",
        "source_documents",
        "xbrl_facts",
        "canonical_observations",
        "canonical_observation_exceptions",
        "universe_snapshots",
        "universe_members",
        "model_runs",
        "statement_snapshots",
        "model_assumptions",
        "valuations",
        "forecast_outcomes",
        "order_intents",
        "risk_decisions",
        "staged_orders",
        "broker_events",
    ];
    let mut counts = serde_json::Map::new();
    for table in tables {
        counts.insert(table.to_string(), json!(count_table(&conn, table)));
    }
    json_line(
        &json!({"trading_mode": setting(&conn, "trading_mode"), "broker_state": broker_state(&conn), "counts": counts}),
    )
}

fn cmd_report(db_path: &Path, date: &str) -> Result<()> {
    let conn = open_db(db_path)?;
    ensure_db(&conn)?;
    let date = if date == "today" {
        OffsetDateTime::now_utc().date().to_string()
    } else {
        date.to_string()
    };
    println!("FA DAILY REPORT - {date} UTC\n");
    println!("state:");
    println!("  trading_mode: {}", setting(&conn, "trading_mode"));
    println!("\ndata:");
    for table in [
        "securities",
        "filings",
        "source_documents",
        "xbrl_facts",
        "canonical_observations",
    ] {
        println!("  {table}: {}", count_table(&conn, table));
    }
    println!("\nmodel/execution:");
    for table in [
        "model_runs",
        "valuations",
        "order_intents",
        "risk_decisions",
        "staged_orders",
        "broker_events",
    ] {
        println!("  {table}: {}", count_table(&conn, table));
    }
    Ok(())
}

fn cmd_ping(
    method: &str,
    title: &str,
    message: &str,
    priority: &str,
    ntfy_topic: &str,
) -> Result<()> {
    if method == "ntfy" {
        let topic = if !ntfy_topic.is_empty() {
            ntfy_topic.to_string()
        } else {
            env::var("NTFY_TOPIC").unwrap_or_default()
        };
        if topic.is_empty() {
            return fail(1, "ntfy requires --ntfy-topic or NTFY_TOPIC");
        }
        let response = ureq::post(&format!("https://ntfy.sh/{topic}"))
            .set("Title", title)
            .set("Priority", priority)
            .send_string(message)?;
        let body = response.into_string().unwrap_or_default();
        return json_line(
            &json!({"method": method, "status": "sent", "response": body.chars().take(200).collect::<String>()}),
        );
    }
    fail(1, format!("unsupported notify method {method:?}"))
}

fn ensure_assumptions(conn: &Connection, operator: &str) -> Result<(String, String)> {
    let assumptions = json!({
        "scenario_count": 3,
        "scenarios": [
            {"name": "bear", "probability_weight": 0.25, "revenue_cagr_5y": -0.02, "terminal_revenue_growth": 0.00, "target_fcf_margin": 0.06, "discount_rate": 0.12, "terminal_fcf_multiple": 10.0},
            {"name": "base", "probability_weight": 0.50, "revenue_cagr_5y": 0.04, "terminal_revenue_growth": 0.02, "target_fcf_margin": 0.12, "discount_rate": 0.10, "terminal_fcf_multiple": 16.0},
            {"name": "bull", "probability_weight": 0.25, "revenue_cagr_5y": 0.10, "terminal_revenue_growth": 0.03, "target_fcf_margin": 0.18, "discount_rate": 0.09, "terminal_fcf_multiple": 22.0}
        ]
    });
    let json_text = canonical_json(&assumptions);
    let hash = sha256_hex(json_text.as_bytes());
    let id = Uuid::new_v4().to_string();
    conn.execute("INSERT INTO model_assumptions(assumption_set_id, created_at, assumption_json, assumption_sha256, operator_label) VALUES (?, ?, ?, ?, ?)", params![id, utc_now(), json_text, hash, operator])?;
    Ok((id, hash))
}

fn risk_args_from_model(args: &ModelArgs, _age_days: f64) -> RiskArgs {
    RiskArgs {
        portfolio_value: args.portfolio_value_usd,
        cash: args.cash_usd,
        max_name_weight: args.max_name_weight_ratio,
        max_order_notional: args.max_order_notional_usd,
        min_adv: args.min_adv_usd,
        max_adv_participation: args.max_adv_participation_ratio,
        max_reconciliation_age_s: args.max_reconciliation_age_s,
    }
}

fn append_event(tx: &Transaction<'_>, event_type: &str, payload: Value) -> Result<Value> {
    let event_id = Uuid::new_v4().to_string();
    let occurred_at = utc_now();
    let payload_json = canonical_json(&payload);
    let payload_sha256 = sha256_hex(payload_json.as_bytes());
    tx.execute("INSERT INTO events(event_id, event_type, event_version, occurred_at, payload_json, payload_sha256) VALUES (?, ?, 1, ?, ?, ?)", params![event_id, event_type, occurred_at, payload_json, payload_sha256])?;
    Ok(
        json!({"event_id": event_id, "event_type": event_type, "event_version": 1, "occurred_at": occurred_at, "payload": payload}),
    )
}

fn load_schema_sql() -> Result<String> {
    let mut candidates = Vec::new();
    if let Ok(root) = env::var("SEC_FA_ROOT") {
        if !root.is_empty() {
            candidates.push(PathBuf::from(root).join("schema.sql"));
        }
    }
    candidates.push(PathBuf::from("schema.sql"));
    candidates.push(PathBuf::from("..").join("schema.sql"));
    if let Ok(exe) = env::current_exe() {
        if let Some(dir) = exe.parent() {
            candidates.push(dir.join("..").join("schema.sql"));
        }
    }
    for path in candidates {
        if let Ok(data) = fs::read_to_string(&path) {
            return Ok(data);
        }
    }
    fail(1, "schema.sql not found")
}

fn seed_metric_concept_candidates(conn: &Connection) -> Result<()> {
    for (concept, metric_id, basis_id, priority, unit) in concept_candidates() {
        let (taxonomy, local) = concept.split_once(':').unwrap_or(("", concept));
        let payload = json!({"metric_id": metric_id, "basis_id": basis_id, "taxonomy": taxonomy, "concept": local});
        conn.execute("INSERT OR IGNORE INTO metric_concept_candidates(candidate_id, metric_id, basis_id, taxonomy, concept_qname, priority, allowed_units_json, dimension_policy, allowed_forms_json, notes) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", params![stable_id("mcc", payload), metric_id, basis_id, taxonomy, local, priority, canonical_json(&json!([unit])), "consolidated_total_only", canonical_json(&json!(["10-K", "10-Q", "10-K/A", "10-Q/A"])), "seeded by sec-fa constants"])?;
    }
    Ok(())
}

fn insert_total_dimension_signature(conn: &Connection) -> Result<()> {
    conn.execute("INSERT OR IGNORE INTO dimension_signatures(dimensions_hash, dimensions_json, has_dimensions, scope_class, axis_count, created_at) VALUES (?, ?, 0, 'consolidated_total', 0, ?)", params![sha256_hex(TOTAL_DIMENSIONS_JSON.as_bytes()), TOTAL_DIMENSIONS_JSON, utc_now()])?;
    Ok(())
}

fn concept_candidates() -> Vec<(&'static str, i64, i64, i64, &'static str)> {
    vec![
        (
            "us-gaap:RevenueFromContractWithCustomerExcludingAssessedTax",
            1,
            101,
            1,
            "USD",
        ),
        (
            "us-gaap:RevenueFromContractWithCustomerIncludingAssessedTax",
            1,
            102,
            2,
            "USD",
        ),
        ("us-gaap:SalesRevenueNet", 1, 103, 3, "USD"),
        ("us-gaap:Revenues", 1, 104, 4, "USD"),
        ("us-gaap:NetIncomeLoss", 2, 201, 1, "USD"),
        ("us-gaap:ProfitLoss", 2, 201, 2, "USD"),
        ("us-gaap:EarningsPerShareDiluted", 3, 301, 1, "USD/shares"),
        (
            "us-gaap:WeightedAverageNumberOfDilutedSharesOutstanding",
            4,
            401,
            1,
            "shares",
        ),
        (
            "us-gaap:NetCashProvidedByUsedInOperatingActivities",
            5,
            501,
            1,
            "USD",
        ),
        (
            "us-gaap:PaymentsToAcquirePropertyPlantAndEquipment",
            6,
            601,
            1,
            "USD",
        ),
        (
            "us-gaap:CashAndCashEquivalentsAtCarryingValue",
            7,
            701,
            1,
            "USD",
        ),
        (
            "us-gaap:CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents",
            7,
            701,
            2,
            "USD",
        ),
        (
            "us-gaap:LongTermDebtAndFinanceLeaseObligationsCurrent",
            8,
            801,
            1,
            "USD",
        ),
        ("us-gaap:LongTermDebtCurrent", 8, 801, 2, "USD"),
        ("us-gaap:LongTermDebtNoncurrent", 8, 801, 3, "USD"),
    ]
}

fn require_fresh_broker_reconciliation(conn: &Connection, max_age_s: i64) -> Result<()> {
    if max_age_s <= 0 {
        return fail(4, "broker reconciliation freshness limit must be positive");
    }
    let row = conn
        .query_row(
            "SELECT reconciled, checked_at FROM broker_state WHERE id=1",
            [],
            |row| Ok((row.get::<_, i64>(0)?, row.get::<_, String>(1)?)),
        )
        .optional()?;
    let Some((reconciled, checked_at)) = row else {
        return fail(4, "broker reconciliation is missing or stale");
    };
    let checked = epoch_second(&checked_at);
    let now = OffsetDateTime::now_utc().unix_timestamp();
    if reconciled == 0 || checked <= 0 || now - checked > max_age_s {
        return fail(4, "broker reconciliation is missing or stale");
    }
    Ok(())
}

fn setting(conn: &Connection, key: &str) -> String {
    conn.query_row("SELECT value FROM settings WHERE key=?", [key], |row| {
        row.get(0)
    })
    .unwrap_or_else(|_| "observe".to_string())
}

fn broker_state(conn: &Connection) -> Value {
    conn.query_row("SELECT reconciled, checked_at, portfolio_value_usd, cash_usd FROM broker_state WHERE id=1", [], |row| Ok(json!({"reconciled": row.get::<_, i64>(0)? != 0, "checked_at": row.get::<_, String>(1)?, "portfolio_value_usd": row.get::<_, f64>(2)?, "cash_usd": row.get::<_, f64>(3)?}))).unwrap_or(Value::Null)
}

fn count_table(conn: &Connection, table: &str) -> i64 {
    conn.query_row(&format!("SELECT COUNT(*) FROM {table}"), [], |row| {
        row.get(0)
    })
    .unwrap_or(0)
}

fn json_line<T: Serialize>(value: &T) -> Result<()> {
    println!("{}", serde_json::to_string(value)?);
    Ok(())
}

fn normalize_cik(cik: &str) -> String {
    let mut digits: String = cik.chars().filter(|c| c.is_ascii_digit()).collect();
    if digits.len() > 10 {
        digits = digits[digits.len() - 10..].to_string();
    }
    format!("{:0>10}", digits)
}

fn bool_int(value: bool) -> i64 {
    if value {
        1
    } else {
        0
    }
}
fn optional_nonempty(value: String) -> Option<String> {
    (!value.is_empty()).then_some(value)
}

fn parse_form_set(forms: &str) -> BTreeSet<String> {
    forms
        .split(',')
        .map(|s| s.trim().to_ascii_uppercase())
        .filter(|s| !s.is_empty())
        .collect()
}
fn form_allowed(forms: &BTreeSet<String>, form: &str) -> bool {
    forms.contains("*")
        || forms.contains("ALL")
        || forms.contains(&form.trim().to_ascii_uppercase())
}

fn sql_form_filter_clause(column: &str, forms: &str) -> Option<(String, Vec<String>)> {
    let mut values: Vec<String> = parse_form_set(forms)
        .into_iter()
        .filter(|s| s != "*" && s != "ALL")
        .collect();
    if values.is_empty() {
        return None;
    }
    values.sort();
    let placeholders = std::iter::repeat("?")
        .take(values.len())
        .collect::<Vec<_>>()
        .join(",");
    Some((format!(" AND upper({column}) IN ({placeholders})"), values))
}

fn effective_user_agent(user_agent: &str) -> String {
    if !user_agent.trim().is_empty() {
        user_agent.trim().to_string()
    } else if let Ok(env) = env::var("SEC_USER_AGENT") {
        if !env.trim().is_empty() {
            env.trim().to_string()
        } else {
            DEFAULT_USER_AGENT.to_string()
        }
    } else {
        DEFAULT_USER_AGENT.to_string()
    }
}

fn sec_get(url: &str, user_agent: &str, server_url: Option<&str>) -> Result<Vec<u8>> {
    if let Some(base) = server_url.filter(|s| !s.is_empty()) {
        let target = format!(
            "{}/fetch?url={}",
            base.trim_end_matches('/'),
            percent_encode(url)
        );
        return http_get(&target, &effective_user_agent(user_agent));
    }
    http_get(url, &effective_user_agent(user_agent))
}

fn http_get(url: &str, user_agent: &str) -> Result<Vec<u8>> {
    let resp = ureq::get(url).set("User-Agent", user_agent).call()?;
    let mut reader = resp.into_reader();
    let mut out = Vec::new();
    std::io::copy(&mut reader, &mut out)?;
    Ok(out)
}

fn percent_encode(value: &str) -> String {
    value
        .bytes()
        .flat_map(|b| match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => vec![b as char],
            _ => format!("%{b:02X}").chars().collect(),
        })
        .collect()
}

fn recent_string(recent: &Value, key: &str, i: usize) -> String {
    recent
        .get(key)
        .and_then(Value::as_array)
        .and_then(|a| a.get(i))
        .map(|v| {
            v.as_str()
                .map(ToOwned::to_owned)
                .unwrap_or_else(|| v.to_string().trim_matches('"').to_string())
        })
        .unwrap_or_default()
}

#[derive(Debug, Deserialize)]
struct ArchiveIndex {
    directory: ArchiveDirectory,
}
#[derive(Debug, Deserialize)]
struct ArchiveDirectory {
    item: Vec<ArchiveItem>,
}
#[derive(Debug, Deserialize)]
struct ArchiveItem {
    name: String,
}

fn package_artifact_names(index_data: &[u8], primary: &str) -> Result<Vec<String>> {
    let idx: ArchiveIndex = serde_json::from_slice(index_data)?;
    let mut seen = BTreeSet::new();
    for item in idx.directory.item {
        if is_package_artifact_name(&item.name, primary) {
            seen.insert(item.name);
        }
    }
    Ok(seen.into_iter().collect())
}

fn is_package_artifact_name(name: &str, primary: &str) -> bool {
    if name.is_empty()
        || Path::new(name).is_absolute()
        || Path::new(name)
            .components()
            .any(|c| matches!(c, std::path::Component::ParentDir))
    {
        return false;
    }
    let lower = name.to_ascii_lowercase();
    name == primary
        || lower.ends_with(".xml")
        || lower.ends_with(".xsd")
        || ((lower.ends_with(".htm") || lower.ends_with(".html")) && primary.is_empty())
}

fn archive_local_path(root: &Path, name: &str) -> Result<PathBuf> {
    if name.is_empty()
        || Path::new(name).is_absolute()
        || Path::new(name)
            .components()
            .any(|c| matches!(c, std::path::Component::ParentDir))
    {
        bail!("unsafe SEC archive item name: {name}");
    }
    Ok(root.join(name))
}

fn archive_url(cik10: &str, accession: &str, doc: Option<&str>) -> String {
    let cik_int = cik10.parse::<i64>().unwrap_or(0);
    let acc_no_dash = accession.replace('-', "");
    match doc.filter(|s| !s.is_empty()) {
        Some(doc) => {
            format!("https://www.sec.gov/Archives/edgar/data/{cik_int}/{acc_no_dash}/{doc}")
        }
        None => {
            format!("https://www.sec.gov/Archives/edgar/data/{cik_int}/{acc_no_dash}/index.json")
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn filing_ingest_groups_prefer_annual_and_quarterly_history() {
        let groups = filing_ingest_groups(DEFAULT_FUNDAMENTAL_FORMS, 1, 4, 1);
        assert_eq!(groups.len(), 2);
        assert_eq!(groups[0].name, "annual");
        assert_eq!(groups[0].forms, "10-K,10-K/A");
        assert_eq!(groups[0].limit, 1);
        assert_eq!(groups[1].name, "quarterly");
        assert_eq!(groups[1].forms, "10-Q,10-Q/A");
        assert_eq!(groups[1].limit, 4);
    }

    #[test]
    fn parse_databento_symbols_rejects_all_symbols() {
        assert!(parse_databento_symbols("AAPL,ALL_SYMBOLS", 10).is_err());
    }

    #[test]
    fn databento_seed_builder_computes_adv() {
        let symbols = vec!["AAPL".to_string()];
        let securities = vec![DatabentoSecurityMasterRow {
            symbol: "AAPL".to_string(),
            cik: "0000320193".to_string(),
            issuer_name: "Apple Inc.".to_string(),
            shares_outstanding: 15_000_000_000.0,
        }];
        let bars = vec![
            DatabentoBarRow {
                symbol: "AAPL".to_string(),
                time: "2026-05-04".to_string(),
                close: 100.0,
                volume: 10.0,
            },
            DatabentoBarRow {
                symbol: "AAPL".to_string(),
                time: "2026-05-05".to_string(),
                close: 110.0,
                volume: 20.0,
            },
        ];
        let seeds = build_databento_security_seeds(&symbols, &securities, &bars, 2, true).unwrap();
        assert_eq!(seeds.len(), 1);
        assert_eq!(seeds[0].price_usd, 110.0);
        assert_eq!(seeds[0].adv_usd, 1600.0);
        assert!(seeds[0].investable);
    }
}
