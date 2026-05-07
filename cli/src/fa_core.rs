use anyhow::{bail, Context as _, Result};
use libloading::{Library, Symbol};
use rusqlite::{params, Connection, OptionalExtension, Transaction};
use serde::Serialize;
use serde_json::json;
use sha2::{Digest, Sha256};
use std::ffi::CStr;
use std::os::raw::{c_char, c_int};
use std::path::Path;
use std::ptr;
use std::slice;
use time::macros::format_description;
use time::{Date, OffsetDateTime};
use uuid::Uuid;

pub const ABI_VERSION: u32 = 4;
pub const REASON_BYTES: usize = 128;
pub const DIAGNOSTIC_BYTES: usize = 1024;

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct FaCanonicalFactV1 {
    pub abi_version: u32,
    pub security_id: u64,
    pub metric_id: i32,
    pub value: f64,
    pub period_start_day: i64,
    pub period_end_day: i64,
    pub available_at_epoch_s: i64,
    pub quality_flags: u32,
    pub metric_kind: u32,
    pub period_semantics: u32,
    pub basis_id: u32,
    pub observation_status: u32,
    pub duration_days: u32,
    pub dimensions_hash: u64,
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct FaSecurityV1 {
    pub abi_version: u32,
    pub security_id: u64,
    pub symbol: [c_char; 16],
    pub investable: u8,
    pub price_usd: f64,
    pub adv_usd: f64,
}
impl Default for FaSecurityV1 {
    fn default() -> Self {
        Self {
            abi_version: ABI_VERSION,
            security_id: 0,
            symbol: [0; 16],
            investable: 0,
            price_usd: 0.0,
            adv_usd: 0.0,
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct FaPositionV1 {
    pub abi_version: u32,
    pub security_id: u64,
    pub quantity_shares: f64,
    pub market_value_usd: f64,
    pub weight_ratio: f64,
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct FaModelConfigV1 {
    pub abi_version: u32,
    pub target_gross_exposure_ratio: f64,
    pub max_name_weight_ratio: f64,
    pub min_expected_return_proxy: f64,
    pub min_abs_order_notional_usd: f64,
    pub max_forecast_abs_growth_ratio: f64,
    pub max_fact_age_s: i64,
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct FaRiskLimitsV1 {
    pub abi_version: u32,
    pub portfolio_value_usd: f64,
    pub cash_usd: f64,
    pub max_name_weight_ratio: f64,
    pub max_order_notional_usd: f64,
    pub min_adv_usd: f64,
    pub max_adv_participation_ratio: f64,
    pub min_abs_order_notional_usd: f64,
    pub broker_reconciled: u8,
    pub reconciliation_checked_at_epoch_s: i64,
    pub now_epoch_s: i64,
    pub max_reconciliation_age_s: i64,
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct FaForecastV1 {
    pub abi_version: u32,
    pub security_id: u64,
    pub revenue_growth_ratio: f64,
    pub earnings_growth_ratio: f64,
    pub expected_return_proxy: f64,
    pub confidence_ratio: f64,
    pub quality_flags: u32,
    pub reason: [c_char; REASON_BYTES],
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct FaTargetWeightV1 {
    pub abi_version: u32,
    pub security_id: u64,
    pub current_weight_ratio: f64,
    pub target_weight_ratio: f64,
    pub delta_weight_ratio: f64,
    pub reason: [c_char; REASON_BYTES],
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct FaOrderIntentV1 {
    pub abi_version: u32,
    pub security_id: u64,
    pub side: i32,
    pub notional_usd: f64,
    pub current_weight_ratio: f64,
    pub target_weight_ratio: f64,
    pub reason: [c_char; REASON_BYTES],
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct FaStatementSnapshotV1 {
    pub abi_version: u32,
    pub security_id: u64,
    pub period_start_day: i64,
    pub period_end_day: i64,
    pub available_at_epoch_s: i64,
    pub is_ttm: u8,
    pub revenue_usd: f64,
    pub net_income_usd: f64,
    pub diluted_eps_usd: f64,
    pub diluted_shares: f64,
    pub operating_cash_flow_usd: f64,
    pub capex_usd: f64,
    pub free_cash_flow_usd: f64,
    pub cash_usd: f64,
    pub debt_usd: f64,
    pub net_debt_usd: f64,
    pub quality_flags: u32,
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct FaStatementBuildOutputV1 {
    pub abi_version: u32,
    pub statement_count: usize,
    pub statements: *mut FaStatementSnapshotV1,
    pub diagnostics: [c_char; DIAGNOSTIC_BYTES],
}

impl Default for FaStatementBuildOutputV1 {
    fn default() -> Self {
        Self {
            abi_version: 0,
            statement_count: 0,
            statements: ptr::null_mut(),
            diagnostics: [0; DIAGNOSTIC_BYTES],
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct FaValuationScenarioV1 {
    pub abi_version: u32,
    pub revenue_cagr_5y: f64,
    pub terminal_revenue_growth: f64,
    pub target_fcf_margin: f64,
    pub discount_rate: f64,
    pub terminal_fcf_multiple: f64,
    pub probability_weight: f64,
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct FaValuationV1 {
    pub abi_version: u32,
    pub security_id: u64,
    pub current_price_usd: f64,
    pub intrinsic_value_per_share_usd: f64,
    pub expected_return_ratio: f64,
    pub market_cap_usd: f64,
    pub enterprise_value_usd: f64,
    pub fcf_yield_ratio: f64,
    pub net_debt_usd: f64,
    pub confidence_ratio: f64,
    pub quality_flags: u32,
    pub reason: [c_char; REASON_BYTES],
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct FaPlanOutputV1 {
    pub abi_version: u32,
    pub forecast_count: usize,
    pub forecasts: *mut FaForecastV1,
    pub target_weight_count: usize,
    pub target_weights: *mut FaTargetWeightV1,
    pub order_intent_count: usize,
    pub order_intents: *mut FaOrderIntentV1,
    pub diagnostics: [c_char; DIAGNOSTIC_BYTES],
}

impl Default for FaPlanOutputV1 {
    fn default() -> Self {
        Self {
            abi_version: 0,
            forecast_count: 0,
            forecasts: ptr::null_mut(),
            target_weight_count: 0,
            target_weights: ptr::null_mut(),
            order_intent_count: 0,
            order_intents: ptr::null_mut(),
            diagnostics: [0; DIAGNOSTIC_BYTES],
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct FaValueOutputV1 {
    pub abi_version: u32,
    pub valuation_count: usize,
    pub valuations: *mut FaValuationV1,
    pub target_weight_count: usize,
    pub target_weights: *mut FaTargetWeightV1,
    pub order_intent_count: usize,
    pub order_intents: *mut FaOrderIntentV1,
    pub diagnostics: [c_char; DIAGNOSTIC_BYTES],
}

impl Default for FaValueOutputV1 {
    fn default() -> Self {
        Self {
            abi_version: 0,
            valuation_count: 0,
            valuations: ptr::null_mut(),
            target_weight_count: 0,
            target_weights: ptr::null_mut(),
            order_intent_count: 0,
            order_intents: ptr::null_mut(),
            diagnostics: [0; DIAGNOSTIC_BYTES],
        }
    }
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct FaRiskDecisionV1 {
    pub abi_version: u32,
    pub security_id: u64,
    pub side: i32,
    pub decision: i32,
    pub notional_usd: f64,
    pub reason: [c_char; REASON_BYTES],
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct FaGateOutputV1 {
    pub abi_version: u32,
    pub decision_count: usize,
    pub decisions: *mut FaRiskDecisionV1,
    pub diagnostics: [c_char; DIAGNOSTIC_BYTES],
}

impl Default for FaGateOutputV1 {
    fn default() -> Self {
        Self {
            abi_version: 0,
            decision_count: 0,
            decisions: ptr::null_mut(),
            diagnostics: [0; DIAGNOSTIC_BYTES],
        }
    }
}

type StatusNameFn = unsafe extern "C" fn(c_int) -> *const c_char;
type BuildPortfolioPlanFn = unsafe extern "C" fn(
    *const FaCanonicalFactV1,
    usize,
    *const FaSecurityV1,
    usize,
    *const FaPositionV1,
    usize,
    *const FaModelConfigV1,
    *const FaRiskLimitsV1,
    *mut FaPlanOutputV1,
) -> c_int;
type PlanFreeFn = unsafe extern "C" fn(*mut FaPlanOutputV1);
type BuildStatementSnapshotsFn = unsafe extern "C" fn(
    *const FaCanonicalFactV1,
    usize,
    *const FaSecurityV1,
    usize,
    *const FaModelConfigV1,
    *const FaRiskLimitsV1,
    *mut FaStatementBuildOutputV1,
) -> c_int;
type StatementFreeFn = unsafe extern "C" fn(*mut FaStatementBuildOutputV1);
type BuildValuationPlanFn = unsafe extern "C" fn(
    *const FaStatementSnapshotV1,
    usize,
    *const FaSecurityV1,
    usize,
    *const FaPositionV1,
    usize,
    *const FaValuationScenarioV1,
    usize,
    *const FaModelConfigV1,
    *const FaRiskLimitsV1,
    *mut FaValueOutputV1,
) -> c_int;
type ValueFreeFn = unsafe extern "C" fn(*mut FaValueOutputV1);
type CheckRiskLimitsFn = unsafe extern "C" fn(
    *const FaOrderIntentV1,
    usize,
    *const FaSecurityV1,
    usize,
    *const FaRiskLimitsV1,
    *mut FaGateOutputV1,
) -> c_int;
type GateFreeFn = unsafe extern "C" fn(*mut FaGateOutputV1);

pub struct Core {
    lib: Library,
}

impl Core {
    pub fn load(path: &str) -> Result<Self> {
        if path.is_empty() {
            bail!("--core-lib is required");
        }
        if !Path::new(path).exists() {
            bail!("core library not found: {path}; run `make build` first");
        }
        let lib = unsafe { Library::new(path) }
            .with_context(|| format!("loading core library {path}"))?;
        Ok(Self { lib })
    }

    unsafe fn sym<T>(&self, name: &[u8]) -> Result<Symbol<'_, T>> {
        self.lib
            .get(name)
            .with_context(|| format!("missing core symbol {}", String::from_utf8_lossy(name)))
    }

    pub fn status_name(&self, code: c_int) -> String {
        unsafe {
            match self.sym::<StatusNameFn>(b"fa_status_name\0") {
                Ok(f) => {
                    let p = f(code);
                    if p.is_null() {
                        format!("status_{code}")
                    } else {
                        CStr::from_ptr(p).to_string_lossy().into_owned()
                    }
                }
                Err(_) => format!("status_{code}"),
            }
        }
    }

    pub unsafe fn build_plan(
        &self,
        facts: &[FaCanonicalFactV1],
        securities: &[FaSecurityV1],
        positions: &[FaPositionV1],
        config: &FaModelConfigV1,
        limits: &FaRiskLimitsV1,
    ) -> Result<FaPlanOutputV1> {
        let f = self.sym::<BuildPortfolioPlanFn>(b"fa_build_portfolio_plan_v1\0")?;
        let mut out = FaPlanOutputV1::default();
        let status = f(
            ptr_or_null(facts),
            facts.len(),
            ptr_or_null(securities),
            securities.len(),
            ptr_or_null(positions),
            positions.len(),
            config,
            limits,
            &mut out,
        );
        if status != 0 {
            let diag = diagnostic_string(&out.diagnostics);
            self.free_plan(&mut out);
            bail!(
                "fa_build_portfolio_plan_v1 failed: {}: {diag}",
                self.status_name(status)
            );
        }
        Ok(out)
    }

    pub unsafe fn free_plan(&self, out: &mut FaPlanOutputV1) {
        if let Ok(f) = self.sym::<PlanFreeFn>(b"fa_plan_output_free_v1\0") {
            f(out);
        }
    }

    pub unsafe fn build_statements(
        &self,
        facts: &[FaCanonicalFactV1],
        securities: &[FaSecurityV1],
        config: &FaModelConfigV1,
        limits: &FaRiskLimitsV1,
    ) -> Result<FaStatementBuildOutputV1> {
        let f = self.sym::<BuildStatementSnapshotsFn>(b"fa_build_statement_snapshots_v1\0")?;
        let mut out = FaStatementBuildOutputV1::default();
        let status = f(
            ptr_or_null(facts),
            facts.len(),
            ptr_or_null(securities),
            securities.len(),
            config,
            limits,
            &mut out,
        );
        if status != 0 {
            let diag = diagnostic_string(&out.diagnostics);
            self.free_statements(&mut out);
            bail!(
                "fa_build_statement_snapshots_v1 failed: {}: {diag}",
                self.status_name(status)
            );
        }
        Ok(out)
    }

    pub unsafe fn free_statements(&self, out: &mut FaStatementBuildOutputV1) {
        if let Ok(f) = self.sym::<StatementFreeFn>(b"fa_statement_build_output_free_v1\0") {
            f(out);
        }
    }

    pub unsafe fn build_value(
        &self,
        statements: &[FaStatementSnapshotV1],
        securities: &[FaSecurityV1],
        positions: &[FaPositionV1],
        scenarios: &[FaValuationScenarioV1],
        config: &FaModelConfigV1,
        limits: &FaRiskLimitsV1,
    ) -> Result<FaValueOutputV1> {
        let f = self.sym::<BuildValuationPlanFn>(b"fa_build_valuation_plan_v1\0")?;
        let mut out = FaValueOutputV1::default();
        let status = f(
            ptr_or_null(statements),
            statements.len(),
            ptr_or_null(securities),
            securities.len(),
            ptr_or_null(positions),
            positions.len(),
            ptr_or_null(scenarios),
            scenarios.len(),
            config,
            limits,
            &mut out,
        );
        if status != 0 {
            let diag = diagnostic_string(&out.diagnostics);
            self.free_value(&mut out);
            bail!(
                "fa_build_valuation_plan_v1 failed: {}: {diag}",
                self.status_name(status)
            );
        }
        Ok(out)
    }

    pub unsafe fn free_value(&self, out: &mut FaValueOutputV1) {
        if let Ok(f) = self.sym::<ValueFreeFn>(b"fa_value_output_free_v1\0") {
            f(out);
        }
    }

    pub unsafe fn check_risk(
        &self,
        intents: &[FaOrderIntentV1],
        securities: &[FaSecurityV1],
        limits: &FaRiskLimitsV1,
    ) -> Result<FaGateOutputV1> {
        let f = self.sym::<CheckRiskLimitsFn>(b"fa_check_risk_limits_v1\0")?;
        let mut out = FaGateOutputV1::default();
        let status = f(
            ptr_or_null(intents),
            intents.len(),
            ptr_or_null(securities),
            securities.len(),
            limits,
            &mut out,
        );
        if status != 0 {
            let diag = diagnostic_string(&out.diagnostics);
            self.free_gate(&mut out);
            bail!(
                "fa_check_risk_limits_v1 failed: {}: {diag}",
                self.status_name(status)
            );
        }
        Ok(out)
    }

    pub unsafe fn free_gate(&self, out: &mut FaGateOutputV1) {
        if let Ok(f) = self.sym::<GateFreeFn>(b"fa_gate_output_free_v1\0") {
            f(out);
        }
    }
}

fn ptr_or_null<T>(s: &[T]) -> *const T {
    if s.is_empty() {
        ptr::null()
    } else {
        s.as_ptr()
    }
}

pub fn diagnostic_string(buf: &[c_char]) -> String {
    c_char_array_string(buf)
}
pub fn reason_string(buf: &[c_char]) -> String {
    c_char_array_string(buf)
}

fn c_char_array_string(buf: &[c_char]) -> String {
    let bytes: Vec<u8> = buf
        .iter()
        .take_while(|c| **c != 0)
        .map(|c| *c as u8)
        .collect();
    String::from_utf8_lossy(&bytes).into_owned()
}

fn set_c_char_array<const N: usize>(dst: &mut [c_char; N], value: &str) {
    dst.fill(0);
    for (slot, byte) in dst
        .iter_mut()
        .take(N.saturating_sub(1))
        .zip(value.as_bytes())
    {
        *slot = *byte as c_char;
    }
}

pub fn load_securities(conn: &Connection) -> Result<Vec<FaSecurityV1>> {
    let mut stmt = conn.prepare("SELECT security_id, symbol, investable, price_usd, adv_usd FROM securities ORDER BY security_id")?;
    let rows = stmt.query_map([], |row| {
        let mut sec = FaSecurityV1::default();
        sec.abi_version = ABI_VERSION;
        sec.security_id = row.get::<_, i64>(0)? as u64;
        let symbol: String = row.get(1)?;
        set_c_char_array(&mut sec.symbol, &symbol);
        sec.investable = row.get::<_, i64>(2)? as u8;
        sec.price_usd = row.get(3)?;
        sec.adv_usd = row.get(4)?;
        Ok(sec)
    })?;
    rows.collect::<rusqlite::Result<Vec<_>>>()
        .map_err(Into::into)
}

pub fn load_positions(conn: &Connection) -> Result<Vec<FaPositionV1>> {
    let mut stmt = conn.prepare("SELECT security_id, quantity_shares, market_value_usd, weight_ratio FROM positions ORDER BY security_id")?;
    let rows = stmt.query_map([], |row| {
        Ok(FaPositionV1 {
            abi_version: ABI_VERSION,
            security_id: row.get::<_, i64>(0)? as u64,
            quantity_shares: row.get(1)?,
            market_value_usd: row.get(2)?,
            weight_ratio: row.get(3)?,
        })
    })?;
    rows.collect::<rusqlite::Result<Vec<_>>>()
        .map_err(Into::into)
}

pub fn load_facts(conn: &Connection) -> Result<Vec<FaCanonicalFactV1>> {
    let mut stmt = conn.prepare(
        "SELECT co.security_id, co.metric_id, co.value_decimal, rp.raw_start_date, rp.raw_end_date, rp.raw_instant_date,
                co.available_at, co.quality_flags, co.metric_kind, co.period_semantics, co.basis_id,
                co.observation_status, co.duration_days, co.dimensions_hash
         FROM canonical_observations co
         JOIN reporting_periods rp ON rp.period_id = co.period_id
         WHERE co.observation_status IN ('selected', 'derived') AND co.dimensional_scope = 'consolidated_total'
         ORDER BY co.security_id, co.metric_id, rp.raw_end_date, co.available_at"
    )?;
    let rows = stmt.query_map([], |row| {
        let start: Option<String> = row.get(3)?;
        let end: Option<String> = row.get(4)?;
        let instant: Option<String> = row.get(5)?;
        let start_date = start.or_else(|| instant.clone()).unwrap_or_default();
        let end_date = end.or(instant).unwrap_or_default();
        let metric_kind: String = row.get(8)?;
        let period_semantics: String = row.get(9)?;
        let status: String = row.get(11)?;
        let dim_hash: String = row.get(13)?;
        Ok(FaCanonicalFactV1 {
            abi_version: ABI_VERSION,
            security_id: row.get::<_, i64>(0)? as u64,
            metric_id: row.get(1)?,
            value: row.get(2)?,
            period_start_day: epoch_day(&start_date),
            period_end_day: epoch_day(&end_date),
            available_at_epoch_s: epoch_second(&row.get::<_, String>(6)?),
            quality_flags: row.get::<_, i64>(7)? as u32,
            metric_kind: metric_kind_code(&metric_kind),
            period_semantics: period_code(&period_semantics),
            basis_id: row.get::<_, i64>(10)? as u32,
            observation_status: match status.as_str() {
                "selected" => 1,
                "derived" => 2,
                _ => 0,
            },
            duration_days: row.get::<_, i64>(12)? as u32,
            dimensions_hash: hash64(&dim_hash),
        })
    })?;
    rows.collect::<rusqlite::Result<Vec<_>>>()
        .map_err(Into::into)
}

#[derive(Debug, Clone, Copy)]
pub struct RiskArgs {
    pub portfolio_value: f64,
    pub cash: f64,
    pub max_name_weight: f64,
    pub max_order_notional: f64,
    pub min_adv: f64,
    pub max_adv_participation: f64,
    pub max_reconciliation_age_s: i64,
}

impl Default for RiskArgs {
    fn default() -> Self {
        Self {
            portfolio_value: 0.0,
            cash: 0.0,
            max_name_weight: 0.05,
            max_order_notional: 10_000.0,
            min_adv: 1_000_000.0,
            max_adv_participation: 0.01,
            max_reconciliation_age_s: 3600,
        }
    }
}

pub fn make_risk_limits(conn: &Connection, args: RiskArgs) -> Result<FaRiskLimitsV1> {
    let row = conn.query_row(
        "SELECT reconciled, checked_at, portfolio_value_usd, cash_usd FROM broker_state WHERE id=1",
        [],
        |row| Ok((row.get::<_, i64>(0)?, row.get::<_, String>(1)?, row.get::<_, f64>(2)?, row.get::<_, f64>(3)?)),
    ).optional()?;
    let (reconciled, checked_at, mut portfolio, mut cash) =
        row.unwrap_or((0, String::new(), 0.0, 0.0));
    if args.portfolio_value > 0.0 {
        portfolio = args.portfolio_value;
    }
    if args.cash > 0.0 {
        cash = args.cash;
    }
    Ok(FaRiskLimitsV1 {
        abi_version: ABI_VERSION,
        portfolio_value_usd: portfolio,
        cash_usd: cash,
        max_name_weight_ratio: args.max_name_weight,
        max_order_notional_usd: args.max_order_notional,
        min_adv_usd: args.min_adv,
        max_adv_participation_ratio: args.max_adv_participation,
        min_abs_order_notional_usd: 100.0,
        broker_reconciled: reconciled as u8,
        reconciliation_checked_at_epoch_s: epoch_second(&checked_at),
        now_epoch_s: OffsetDateTime::now_utc().unix_timestamp(),
        max_reconciliation_age_s: args.max_reconciliation_age_s,
    })
}

pub fn load_pending_intents(
    conn: &Connection,
    run_id: Option<&str>,
) -> Result<(Vec<FaOrderIntentV1>, Vec<String>)> {
    let mut query = "SELECT oi.intent_id, oi.security_id, oi.side, oi.notional_usd, oi.current_weight_ratio, oi.target_weight_ratio, oi.reason FROM order_intents oi LEFT JOIN risk_decisions rd ON rd.intent_id=oi.intent_id WHERE rd.intent_id IS NULL".to_string();
    let mut args = Vec::<String>::new();
    if let Some(run_id) = run_id.filter(|s| !s.is_empty()) {
        query.push_str(" AND oi.run_id=?");
        args.push(run_id.to_string());
    }
    query.push_str(" ORDER BY oi.created_at");
    let refs = args
        .iter()
        .map(|s| s as &dyn rusqlite::ToSql)
        .collect::<Vec<_>>();
    let mut stmt = conn.prepare(&query)?;
    let mut rows = stmt.query(refs.as_slice())?;
    let mut out = Vec::new();
    let mut ids = Vec::new();
    while let Some(row) = rows.next()? {
        let id: String = row.get(0)?;
        let side: String = row.get(2)?;
        let reason: String = row.get(6)?;
        let mut oi = FaOrderIntentV1 {
            abi_version: ABI_VERSION,
            security_id: row.get::<_, i64>(1)? as u64,
            side: side_code(&side),
            notional_usd: row.get(3)?,
            current_weight_ratio: row.get(4)?,
            target_weight_ratio: row.get(5)?,
            reason: [0; REASON_BYTES],
        };
        set_c_char_array(&mut oi.reason, &reason);
        out.push(oi);
        ids.push(id);
    }
    Ok((out, ids))
}

pub fn persist_targets(
    tx: &Transaction<'_>,
    run_id: &str,
    targets: &[FaTargetWeightV1],
) -> Result<()> {
    for t in targets {
        tx.execute(
            "INSERT INTO target_weights(run_id, security_id, current_weight_ratio, target_weight_ratio, delta_weight_ratio, reason) VALUES (?, ?, ?, ?, ?, ?)",
            params![run_id, t.security_id as i64, t.current_weight_ratio, t.target_weight_ratio, t.delta_weight_ratio, reason_string(&t.reason)],
        )?;
    }
    Ok(())
}

pub fn persist_intents(
    tx: &Transaction<'_>,
    run_id: &str,
    intents: &[FaOrderIntentV1],
) -> Result<()> {
    for oi in intents {
        tx.execute(
            "INSERT INTO order_intents(intent_id, run_id, security_id, side, notional_usd, current_weight_ratio, target_weight_ratio, reason, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
            params![Uuid::new_v4().to_string(), run_id, oi.security_id as i64, side_text(oi.side), oi.notional_usd, oi.current_weight_ratio, oi.target_weight_ratio, reason_string(&oi.reason), utc_now()],
        )?;
    }
    Ok(())
}

pub unsafe fn plan_slices(
    out: &FaPlanOutputV1,
) -> (&[FaForecastV1], &[FaTargetWeightV1], &[FaOrderIntentV1]) {
    (
        slice::from_raw_parts(out.forecasts, out.forecast_count),
        slice::from_raw_parts(out.target_weights, out.target_weight_count),
        slice::from_raw_parts(out.order_intents, out.order_intent_count),
    )
}

pub unsafe fn statement_slice(out: &FaStatementBuildOutputV1) -> &[FaStatementSnapshotV1] {
    slice::from_raw_parts(out.statements, out.statement_count)
}

pub unsafe fn value_slices(
    out: &FaValueOutputV1,
) -> (&[FaValuationV1], &[FaTargetWeightV1], &[FaOrderIntentV1]) {
    (
        slice::from_raw_parts(out.valuations, out.valuation_count),
        slice::from_raw_parts(out.target_weights, out.target_weight_count),
        slice::from_raw_parts(out.order_intents, out.order_intent_count),
    )
}

pub unsafe fn gate_decisions(out: &FaGateOutputV1) -> &[FaRiskDecisionV1] {
    slice::from_raw_parts(out.decisions, out.decision_count)
}

pub fn valuation_scenarios() -> Vec<FaValuationScenarioV1> {
    vec![
        FaValuationScenarioV1 {
            abi_version: ABI_VERSION,
            revenue_cagr_5y: -0.02,
            terminal_revenue_growth: 0.0,
            target_fcf_margin: 0.06,
            discount_rate: 0.12,
            terminal_fcf_multiple: 10.0,
            probability_weight: 0.25,
        },
        FaValuationScenarioV1 {
            abi_version: ABI_VERSION,
            revenue_cagr_5y: 0.04,
            terminal_revenue_growth: 0.02,
            target_fcf_margin: 0.12,
            discount_rate: 0.10,
            terminal_fcf_multiple: 16.0,
            probability_weight: 0.50,
        },
        FaValuationScenarioV1 {
            abi_version: ABI_VERSION,
            revenue_cagr_5y: 0.10,
            terminal_revenue_growth: 0.03,
            target_fcf_margin: 0.18,
            discount_rate: 0.09,
            terminal_fcf_multiple: 22.0,
            probability_weight: 0.25,
        },
    ]
}

pub fn statement_snapshot_source_hash(s: &FaStatementSnapshotV1) -> String {
    sha256_hex(
        canonical_json(&json!({
            "security_id": s.security_id,
            "period_start_day": s.period_start_day,
            "period_end_day": s.period_end_day,
            "available_at_epoch_s": s.available_at_epoch_s,
            "is_ttm": s.is_ttm,
            "revenue_usd": s.revenue_usd,
            "net_income_usd": s.net_income_usd,
            "diluted_eps_usd": s.diluted_eps_usd,
            "diluted_shares": s.diluted_shares,
            "operating_cash_flow_usd": s.operating_cash_flow_usd,
            "capex_usd": s.capex_usd,
            "free_cash_flow_usd": s.free_cash_flow_usd,
            "cash_usd": s.cash_usd,
            "debt_usd": s.debt_usd,
            "net_debt_usd": s.net_debt_usd,
            "quality_flags": s.quality_flags,
            "statement_builder_version": ABI_VERSION,
        }))
        .as_bytes(),
    )
}

pub fn statement_snapshot_id(s: &FaStatementSnapshotV1, source_hash: &str) -> String {
    stable_id(
        "stmt",
        json!({
            "security_id": s.security_id,
            "period_end_date": date_from_epoch_day(s.period_end_day),
            "available_at": utc_from_epoch_second(s.available_at_epoch_s),
            "source_hash": source_hash,
        }),
    )
}

pub fn utc_now() -> String {
    OffsetDateTime::now_utc()
        .format(format_description!(
            "[year]-[month]-[day]T[hour]:[minute]:[second].[subsecond digits:3]Z"
        ))
        .unwrap_or_default()
}

pub fn date_from_epoch_day(day: i64) -> String {
    if day <= 0 {
        return String::new();
    }
    let dt =
        OffsetDateTime::from_unix_timestamp(day * 86_400).unwrap_or(OffsetDateTime::UNIX_EPOCH);
    dt.date()
        .format(format_description!("[year]-[month]-[day]"))
        .unwrap_or_default()
}

pub fn utc_from_epoch_second(epoch_second: i64) -> String {
    if epoch_second <= 0 {
        return String::new();
    }
    OffsetDateTime::from_unix_timestamp(epoch_second)
        .unwrap_or(OffsetDateTime::UNIX_EPOCH)
        .format(format_description!(
            "[year]-[month]-[day]T[hour]:[minute]:[second].[subsecond digits:3]Z"
        ))
        .unwrap_or_default()
}

pub fn epoch_day(date: &str) -> i64 {
    Date::parse(date, format_description!("[year]-[month]-[day]"))
        .map(|d| d.midnight().assume_utc().unix_timestamp() / 86_400)
        .unwrap_or(0)
}

pub fn epoch_second(value: &str) -> i64 {
    if value.is_empty() {
        return 0;
    }
    if let Ok(dt) = OffsetDateTime::parse(value, &time::format_description::well_known::Rfc3339) {
        return dt.unix_timestamp();
    }
    if let Ok(dt) = OffsetDateTime::parse(
        value,
        format_description!("[year]-[month]-[day]T[hour]:[minute]:[second].[subsecond digits:3]Z"),
    ) {
        return dt.unix_timestamp();
    }
    if let Ok(dt) = OffsetDateTime::parse(
        value,
        format_description!("[year]-[month]-[day]T[hour]:[minute]:[second]Z"),
    ) {
        return dt.unix_timestamp();
    }
    Date::parse(value, format_description!("[year]-[month]-[day]"))
        .map(|d| d.midnight().assume_utc().unix_timestamp())
        .unwrap_or(0)
}

fn metric_kind_code(v: &str) -> u32 {
    match v {
        "flow" => 1,
        "instant" => 2,
        "per_share_flow" => 3,
        "ratio" => 4,
        "derived" => 5,
        _ => 0,
    }
}
fn period_code(v: &str) -> u32 {
    match v {
        "fiscal_quarter" => 1,
        "fiscal_ytd" => 2,
        "fiscal_year" => 3,
        "trailing_twelve_month" => 4,
        "instant" => 5,
        "stub" => 6,
        "transition" => 7,
        "irregular" => 8,
        _ => 0,
    }
}
fn side_text(side: i32) -> &'static str {
    match side {
        1 => "buy",
        2 => "sell",
        _ => "none",
    }
}
fn side_code(side: &str) -> i32 {
    match side {
        "buy" => 1,
        "sell" => 2,
        _ => 0,
    }
}
fn hash64(hex_string: &str) -> u64 {
    hex_string
        .get(0..16)
        .and_then(|s| u64::from_str_radix(s, 16).ok())
        .unwrap_or(0)
}

pub fn canonical_json<T: Serialize + ?Sized>(value: &T) -> String {
    serde_json::to_string(value).expect("canonical json")
}
pub fn sha256_hex(data: impl AsRef<[u8]>) -> String {
    let mut h = Sha256::new();
    h.update(data.as_ref());
    hex::encode(h.finalize())
}
pub fn stable_id(prefix: &str, value: serde_json::Value) -> String {
    format!("{prefix}-{}", sha256_hex(canonical_json(&value).as_bytes()))
}
