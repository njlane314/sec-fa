use anyhow::{bail, Context as _, Result};
use rusqlite::{params, Connection, OptionalExtension, Transaction};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};
use time::macros::format_description;
use time::Date;
use uuid::Uuid;

const RESOLVER_VERSION: &str = "canonical_resolver_v3";
const TOTAL_DIMENSIONS_JSON: &str = "{}";
const SEC_ARCHIVE_BASE_URL: &str = "https://www.sec.gov/Archives/edgar/data";
const QUALITY_DIMENSIONAL_FACT: i64 = 1 << 16;

#[derive(Debug, Clone, Default)]
pub struct ParseRequest {
    pub accession: Option<String>,
    pub cik: Option<String>,
    pub form: Option<String>,
    pub filed_at: Option<String>,
    pub accepted_at: Option<String>,
    pub raw_root: PathBuf,
    pub package_root: Option<PathBuf>,
    pub latest: bool,
    pub forms: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct ParseSummary {
    pub event: Value,
    pub accession_number: String,
    pub cik: String,
    pub documents_stored: usize,
    pub raw_facts_inserted: usize,
    pub canonical_selected_inserted: usize,
    pub parse_errors: Vec<String>,
}

#[derive(Debug, Clone, Default)]
struct PeriodInfo {
    raw_start_date: Option<String>,
    raw_end_date: Option<String>,
    raw_instant_date: Option<String>,
    start_date_inclusive: Option<String>,
    end_date_exclusive: Option<String>,
    duration_days: i64,
    period_kind: String,
    period_semantics: String,
    fiscal_year: Option<i64>,
    fiscal_period: Option<String>,
    fiscal_period_ordinal: Option<i64>,
    period_length_class: String,
}

#[derive(Debug, Clone)]
struct ContextInfo {
    raw_id: String,
    context_id: String,
    entity_identifier: String,
    period_id: String,
    period: PeriodInfo,
    dimensions_hash: String,
    dimensions_json: String,
    segment_json: Option<String>,
    scenario_json: Option<String>,
    scope_class: String,
    axis_count: i64,
}

#[derive(Debug, Clone)]
struct UnitInfo {
    raw_id: String,
    unit_id: String,
    signature: String,
    numerator_json: Option<String>,
    denominator_json: Option<String>,
}

#[derive(Debug, Clone)]
struct RawFact {
    fact_id: String,
    source_doc_id: String,
    security_id: i64,
    cik: String,
    accession: String,
    taxonomy: String,
    concept_qname: String,
    concept_local_name: String,
    context: ContextInfo,
    unit: Option<UnitInfo>,
    value_decimal: Option<f64>,
    value_text: Option<String>,
    decimals_attr: Option<String>,
    precision_attr: Option<String>,
    is_nil: i64,
    form: String,
    filed_at: String,
    accepted_at: String,
    available_at: String,
    raw_fact_hash: String,
}

#[derive(Debug, Clone, Default)]
struct FilingMetadata {
    accession: String,
    cik: String,
    form: String,
    filing_date: Option<String>,
    accepted_at: Option<String>,
    primary_document: Option<String>,
}

#[derive(Debug, Clone, Copy)]
struct CanonicalConcept {
    metric_id: i64,
    basis_id: i64,
    priority: i64,
    unit: &'static str,
}

pub fn parse_xbrl_package(conn: &mut Connection, mut req: ParseRequest) -> Result<ParseSummary> {
    let (meta, found) = match req.accession.as_deref() {
        Some(accession) if !accession.is_empty() => {
            match lookup_filing_metadata(conn, accession)? {
                Some(meta) => (meta, true),
                None => (FilingMetadata::default(), false),
            }
        }
        _ if req.latest => {
            latest_fetched_filing_metadata(conn, req.cik.as_deref(), req.forms.as_deref())?
                .map(|m| (m, true))
                .context("no fetched filing found; run watch and pull first or pass --accession")?
        }
        _ => bail!("--accession is required unless --latest is set"),
    };

    let accession = req
        .accession
        .take()
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| meta.accession.clone());
    let cik = req
        .cik
        .take()
        .filter(|s| !s.is_empty())
        .or_else(|| if found { Some(meta.cik.clone()) } else { None })
        .context("--cik is required when accession metadata is not present; run watch first or pass --cik")?;
    let cik10 = normalize_cik(&cik);
    let form = req
        .form
        .take()
        .filter(|s| !s.is_empty())
        .or_else(|| if found { Some(meta.form.clone()) } else { None })
        .unwrap_or_else(|| "10-Q".to_string());
    let accepted_at = req
        .accepted_at
        .take()
        .filter(|s| !s.is_empty())
        .or(meta.accepted_at.clone())
        .unwrap_or_else(utc_now);
    let filed_at = req
        .filed_at
        .take()
        .filter(|s| !s.is_empty())
        .or(meta.filing_date.clone())
        .unwrap_or_else(|| {
            accepted_at
                .split('T')
                .next()
                .unwrap_or(&accepted_at)
                .to_string()
        });
    let security_id = cik10.parse::<i64>().unwrap_or(0);

    let package_root = req
        .package_root
        .clone()
        .unwrap_or_else(|| req.raw_root.join(&cik10).join(accession.replace('-', "")));
    let files = load_package_files(
        &package_root,
        &cik10,
        &accession,
        meta.primary_document.as_deref(),
    )?;

    let tx = conn.transaction()?;
    tx.execute(
        "INSERT OR IGNORE INTO filings(accession_number, cik, form, filing_date, accepted_at, source_url, ingested_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
        params![accession, cik10, form, filed_at, accepted_at, archive_url(&cik10, &accession, None), utc_now()],
    )?;

    let mut index_doc_id = String::new();
    let mut documents_stored = 0usize;
    let mut raw_inserted = 0usize;
    let mut selected_inserted = 0usize;
    let mut parse_errors = Vec::<String>::new();

    for file in files {
        let doc_id = source_document_id(&cik10, &file.url, &file.hash);
        if file.name == "index.json" {
            index_doc_id = doc_id.clone();
        }
        let local_path = file.path.to_string_lossy().into_owned();
        tx.execute(
            "INSERT OR IGNORE INTO source_documents(doc_id, cik, accession_number, form, filed_at, source_url, local_path, sha256, document_kind, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            params![doc_id, cik10, accession, form, filed_at, file.url, local_path, file.hash, file.kind, utc_now()],
        )?;
        documents_stored += 1;
        if file.kind != "inline_xbrl" && file.kind != "xbrl_instance" {
            continue;
        }

        let xml = match std::str::from_utf8(&file.data) {
            Ok(s) => s,
            Err(err) => {
                parse_errors.push(format!("{}: non-UTF8 XML: {err}", file.name));
                continue;
            }
        };
        let doc = match roxmltree::Document::parse(xml) {
            Ok(d) => d,
            Err(err) => {
                parse_errors.push(format!("{}: {err}", file.name));
                continue;
            }
        };
        let root = doc.root_element();
        let mut contexts = parse_contexts(root, &cik10, &accession);
        let fiscal_meta = parse_filing_fiscal_metadata(root);
        apply_filing_fiscal_metadata(&mut contexts, &fiscal_meta, &cik10, &accession);
        let units = parse_units(root, &cik10, &accession);

        for ctx in contexts.values() {
            insert_context(&tx, &cik10, &accession, ctx)?;
        }
        for unit in units.values() {
            insert_unit(&tx, &cik10, &accession, unit)?;
        }

        let facts = if file.kind == "inline_xbrl" {
            parse_inline_facts(
                root,
                &doc_id,
                security_id,
                &cik10,
                &accession,
                &form,
                &filed_at,
                &accepted_at,
                &contexts,
                &units,
            )
        } else {
            parse_classic_facts(
                root,
                &doc_id,
                security_id,
                &cik10,
                &accession,
                &form,
                &filed_at,
                &accepted_at,
                &contexts,
                &units,
            )
        };

        for fact in facts {
            if insert_raw_fact(&tx, &fact)? {
                raw_inserted += 1;
            }
            if insert_canonical_observation(&tx, &fact)? {
                selected_inserted += 1;
            }
        }
        eprintln!(
            "parsed {}: kind={} facts={}",
            file.name, file.kind, raw_inserted
        );
    }

    let payload = json!({
        "accession_number": accession.clone(),
        "cik": cik10.clone(),
        "security_id": security_id,
        "form": form.clone(),
        "index_doc_id": index_doc_id,
        "documents_stored": documents_stored,
        "raw_facts_inserted": raw_inserted,
        "canonical_candidates": selected_inserted,
        "canonical_selected_inserted": selected_inserted,
        "derived_quarter_observations_inserted": 0,
        "canonical_exceptions_inserted": 0,
        "resolver_version": RESOLVER_VERSION,
        "parse_errors": parse_errors.clone(),
    });
    let event = append_event(&tx, "xbrl_package_parsed", payload)?;
    tx.commit()?;

    Ok(ParseSummary {
        event,
        accession_number: accession,
        cik: cik10,
        documents_stored,
        raw_facts_inserted: raw_inserted,
        canonical_selected_inserted: selected_inserted,
        parse_errors,
    })
}

fn lookup_filing_metadata(conn: &Connection, accession: &str) -> Result<Option<FilingMetadata>> {
    conn.query_row(
        "SELECT accession_number, cik, form, filing_date, accepted_at, primary_document FROM filings WHERE accession_number=?",
        [accession],
        |row| Ok(FilingMetadata {
            accession: row.get(0)?,
            cik: row.get(1)?,
            form: row.get(2)?,
            filing_date: row.get(3)?,
            accepted_at: row.get(4)?,
            primary_document: row.get(5)?,
        }),
    ).optional().map_err(Into::into)
}

fn latest_fetched_filing_metadata(
    conn: &Connection,
    cik: Option<&str>,
    forms: Option<&str>,
) -> Result<Option<FilingMetadata>> {
    let mut query = "SELECT accession_number, cik, form, filing_date, accepted_at, primary_document FROM filings WHERE raw_index_uri IS NOT NULL".to_string();
    let mut args: Vec<String> = Vec::new();
    if let Some(cik) = cik.filter(|s| !s.is_empty()) {
        query.push_str(" AND cik=?");
        args.push(normalize_cik(cik));
    }
    if let Some((clause, mut form_args)) = sql_form_filter_clause("form", forms.unwrap_or_default())
    {
        query.push_str(&clause);
        args.append(&mut form_args);
    }
    query.push_str(" ORDER BY filing_date DESC, accepted_at DESC LIMIT 1");
    let refs: Vec<&dyn rusqlite::ToSql> = args.iter().map(|s| s as &dyn rusqlite::ToSql).collect();
    conn.query_row(&query, refs.as_slice(), |row| {
        Ok(FilingMetadata {
            accession: row.get(0)?,
            cik: row.get(1)?,
            form: row.get(2)?,
            filing_date: row.get(3)?,
            accepted_at: row.get(4)?,
            primary_document: row.get(5)?,
        })
    })
    .optional()
    .map_err(Into::into)
}

#[derive(Debug)]
struct PackageFile {
    name: String,
    path: PathBuf,
    url: String,
    hash: String,
    kind: String,
    data: Vec<u8>,
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

fn load_package_files(
    root: &Path,
    cik: &str,
    accession: &str,
    primary: Option<&str>,
) -> Result<Vec<PackageFile>> {
    let index_path = root.join("index.json");
    let index_data =
        fs::read(&index_path).with_context(|| format!("reading {}", index_path.display()))?;
    let index_hash = sha256_hex(&index_data);
    let mut out = vec![PackageFile {
        name: "index.json".to_string(),
        path: index_path,
        url: archive_url(cik, accession, None),
        hash: index_hash,
        kind: "sec_archive_index".to_string(),
        data: index_data.clone(),
    }];
    let idx: ArchiveIndex = serde_json::from_slice(&index_data)?;
    let has_extracted_instance = idx
        .directory
        .item
        .iter()
        .any(|i| is_extracted_inline_instance_name(&i.name));
    for item in idx.directory.item {
        if !is_package_artifact_name(&item.name, primary.unwrap_or_default()) {
            continue;
        }
        let path = archive_local_path(root, &item.name)?;
        let data = fs::read(&path).with_context(|| format!("reading {}", path.display()))?;
        out.push(PackageFile {
            kind: package_file_kind(
                &item.name,
                primary.unwrap_or_default(),
                has_extracted_instance,
            ),
            url: archive_url(cik, accession, Some(&item.name)),
            hash: sha256_hex(&data),
            name: item.name,
            path,
            data,
        });
    }
    Ok(out)
}

fn package_file_kind(name: &str, primary: &str, has_extracted_instance: bool) -> String {
    let lower = name.to_ascii_lowercase();
    if lower.ends_with(".htm") || lower.ends_with(".html") {
        if has_extracted_instance && name == primary {
            "primary_document".to_string()
        } else {
            "inline_xbrl".to_string()
        }
    } else if lower.ends_with(".xsd") {
        "xbrl_schema".to_string()
    } else if lower.ends_with("_lab.xml")
        || lower.ends_with("_pre.xml")
        || lower.ends_with("_def.xml")
        || lower.ends_with("_cal.xml")
    {
        "xbrl_linkbase".to_string()
    } else if lower == "filingsummary.xml" || lower == "metalinks.json" {
        "xbrl_report_metadata".to_string()
    } else {
        "xbrl_instance".to_string()
    }
}

fn is_extracted_inline_instance_name(name: &str) -> bool {
    name.to_ascii_lowercase().ends_with("_htm.xml")
}

fn parse_contexts(
    root: roxmltree::Node<'_, '_>,
    cik: &str,
    accession: &str,
) -> BTreeMap<String, ContextInfo> {
    let mut out = BTreeMap::new();
    for n in elements(root, "context") {
        let Some(raw_id) = attr(n, "id") else {
            continue;
        };
        let entity = first_descendant(n, "identifier")
            .map(text)
            .unwrap_or_default();
        let period_node = first_child(n, "period");
        let mut period = period_node.map(parse_period).unwrap_or_default();
        fill_period_ids(&mut period, cik, accession);
        let mut info = ContextInfo {
            raw_id: raw_id.to_string(),
            context_id: stable_id(
                "ctx",
                json!({"cik": cik, "accession_number": accession, "raw_context_id": raw_id}),
            ),
            entity_identifier: entity,
            period_id: stable_id("period", period_identity_json(cik, accession, &period)),
            period,
            dimensions_hash: total_dimensions_hash(),
            dimensions_json: TOTAL_DIMENSIONS_JSON.to_string(),
            segment_json: None,
            scenario_json: None,
            scope_class: "consolidated_total".to_string(),
            axis_count: 0,
        };
        let mut dims = Vec::<Value>::new();
        for member in elements(n, "explicitMember") {
            let dimension = attr(member, "dimension").unwrap_or_default().to_string();
            dims.push(json!({"dimension": dimension, "member": text(member)}));
        }
        if !dims.is_empty() {
            dims.sort_by(|a, b| a["dimension"].as_str().cmp(&b["dimension"].as_str()));
            let dims_json = canonical_json(&dims);
            info.dimensions_hash = sha256_hex(dims_json.as_bytes());
            info.dimensions_json = dims_json;
            info.axis_count = dims.len() as i64;
            info.scope_class = if dims.iter().any(|d| {
                d["dimension"]
                    .as_str()
                    .unwrap_or_default()
                    .to_ascii_lowercase()
                    .contains("product")
            }) {
                "product".to_string()
            } else {
                "segment".to_string()
            };
        }
        out.insert(raw_id.to_string(), info);
    }
    out
}

#[derive(Debug, Clone, Default)]
struct FilingFiscalMetadata {
    fiscal_year_focus: Option<i64>,
    fiscal_period_focus: Option<String>,
    fiscal_period_ordinal: Option<i64>,
    document_period_end: Option<String>,
    current_fiscal_year_end: Option<String>,
}

fn parse_filing_fiscal_metadata(root: roxmltree::Node<'_, '_>) -> FilingFiscalMetadata {
    let mut out = FilingFiscalMetadata::default();
    for n in elements(root, "") {
        let concept = attr(n, "name").map(|s| split_qname(s).1).or_else(|| {
            taxonomy_for_space(n.tag_name().namespace())
                .filter(|tax| *tax == "dei")
                .map(|_| n.tag_name().name().to_string())
        });
        let Some(concept) = concept else {
            continue;
        };
        let value = text(n);
        match concept.as_str() {
            "DocumentFiscalYearFocus" => {
                out.fiscal_year_focus = parse_integer_text(&value).map(i64::from);
            }
            "DocumentFiscalPeriodFocus" => {
                let period = normalize_fiscal_period(&value);
                if !period.is_empty() {
                    out.fiscal_period_ordinal = fiscal_quarter_ordinal(&period).map(i64::from);
                    out.fiscal_period_focus = Some(period);
                }
            }
            "DocumentPeriodEndDate" => {
                if parse_date(&value).is_some() {
                    out.document_period_end = Some(value);
                }
            }
            "CurrentFiscalYearEndDate" => {
                if !value.is_empty() {
                    out.current_fiscal_year_end = Some(value);
                }
            }
            _ => {}
        }
    }
    out
}

fn apply_filing_fiscal_metadata(
    contexts: &mut BTreeMap<String, ContextInfo>,
    meta: &FilingFiscalMetadata,
    cik: &str,
    accession: &str,
) {
    let (Some(fy), Some(fp), Some(ord), Some(doc_end)) = (
        meta.fiscal_year_focus,
        meta.fiscal_period_focus.as_ref(),
        meta.fiscal_period_ordinal,
        meta.document_period_end.as_ref(),
    ) else {
        return;
    };
    for ctx in contexts.values_mut() {
        let end = ctx
            .period
            .raw_end_date
            .as_ref()
            .or(ctx.period.raw_instant_date.as_ref())
            .cloned()
            .unwrap_or_default();
        if end.is_empty() {
            continue;
        }
        let Some(year) = comparable_fiscal_year(&end, doc_end, fy as i32) else {
            continue;
        };
        ctx.period.fiscal_year = Some(i64::from(year));
        if ctx.period.period_kind == "duration" {
            if is_fiscal_ytd_duration(ctx.period.duration_days, ord) {
                ctx.period.period_semantics = "fiscal_ytd".to_string();
                ctx.period.fiscal_period = Some(fp.clone());
                ctx.period.fiscal_period_ordinal = Some(ord);
            } else if is_quarter_duration(ctx.period.duration_days) {
                ctx.period.period_semantics = "fiscal_quarter".to_string();
                ctx.period.fiscal_period = Some(fp.clone());
                ctx.period.fiscal_period_ordinal = Some(ord);
            }
        }
        ctx.period_id = stable_id("period", period_identity_json(cik, accession, &ctx.period));
    }
}

fn parse_period(period: roxmltree::Node<'_, '_>) -> PeriodInfo {
    let start = first_child(period, "startDate")
        .map(text)
        .unwrap_or_default();
    let end = first_child(period, "endDate").map(text).unwrap_or_default();
    let instant = first_child(period, "instant").map(text).unwrap_or_default();
    if !instant.is_empty() {
        return PeriodInfo {
            raw_instant_date: Some(instant.clone()),
            start_date_inclusive: Some(instant.clone()),
            end_date_exclusive: Some(instant.clone()),
            duration_days: 0,
            period_kind: "instant".to_string(),
            period_semantics: "instant".to_string(),
            fiscal_year: instant.get(0..4).and_then(|s| s.parse::<i64>().ok()),
            period_length_class: "instant".to_string(),
            ..Default::default()
        };
    }
    let duration = duration_days(&start, &end);
    let mut p = PeriodInfo {
        raw_start_date: (!start.is_empty()).then_some(start.clone()),
        raw_end_date: (!end.is_empty()).then_some(end.clone()),
        start_date_inclusive: (!start.is_empty()).then_some(start.clone()),
        end_date_exclusive: next_date(&end),
        duration_days: duration,
        period_kind: "duration".to_string(),
        period_semantics: "irregular".to_string(),
        fiscal_year: end.get(0..4).and_then(|s| s.parse::<i64>().ok()),
        period_length_class: period_length_class(duration).to_string(),
        ..Default::default()
    };
    if is_quarter_duration(duration) {
        p.period_semantics = "fiscal_quarter".to_string();
    }
    if duration >= 330 {
        p.period_semantics = "fiscal_year".to_string();
    }
    if p.period_semantics == "fiscal_quarter" {
        let q = quarter_from_end(&end);
        if q > 0 {
            p.fiscal_period = Some(format!("Q{q}"));
            p.fiscal_period_ordinal = Some(i64::from(q));
        }
    }
    p
}

fn fill_period_ids(_p: &mut PeriodInfo, _cik: &str, _accession: &str) {}

fn parse_units(
    root: roxmltree::Node<'_, '_>,
    cik: &str,
    accession: &str,
) -> BTreeMap<String, UnitInfo> {
    let mut out = BTreeMap::new();
    for n in elements(root, "unit") {
        let Some(raw_id) = attr(n, "id") else {
            continue;
        };
        let measures: Vec<String> = elements(n, "measure")
            .map(text)
            .map(|s| normalize_measure(&s))
            .collect();
        let signature = if measures.is_empty() {
            "unknown".to_string()
        } else {
            measures[0].clone()
        };
        out.insert(
            raw_id.to_string(),
            UnitInfo {
                raw_id: raw_id.to_string(),
                unit_id: stable_id(
                    "unit",
                    json!({"cik": cik, "accession_number": accession, "raw_unit_id": raw_id}),
                ),
                signature,
                numerator_json: None,
                denominator_json: None,
            },
        );
    }
    out
}

#[allow(clippy::too_many_arguments)]
fn parse_inline_facts(
    root: roxmltree::Node<'_, '_>,
    doc_id: &str,
    security_id: i64,
    cik: &str,
    accession: &str,
    form: &str,
    filed_at: &str,
    accepted_at: &str,
    contexts: &BTreeMap<String, ContextInfo>,
    units: &BTreeMap<String, UnitInfo>,
) -> Vec<RawFact> {
    let mut out = Vec::new();
    for n in elements(root, "") {
        if n.tag_name().name() != "nonFraction" && n.tag_name().name() != "nonNumeric" {
            continue;
        }
        let Some(name) = attr(n, "name") else {
            continue;
        };
        let (tax, local) = split_qname(name);
        let Some(ctx_id) = attr(n, "contextRef") else {
            continue;
        };
        let Some(ctx) = contexts.get(ctx_id) else {
            continue;
        };
        let unit = attr(n, "unitRef").and_then(|u| units.get(u)).cloned();
        let value_text = text(n);
        let (value_decimal, value_text_opt) = if n.tag_name().name() == "nonFraction" {
            (
                parse_scaled_number(&value_text, attr(n, "scale").unwrap_or("0")),
                None,
            )
        } else {
            (None, Some(value_text))
        };
        out.push(build_raw_fact(
            doc_id,
            security_id,
            cik,
            accession,
            &tax,
            &local,
            ctx.clone(),
            unit,
            value_decimal,
            value_text_opt,
            attr(n, "decimals").map(ToOwned::to_owned),
            attr(n, "precision").map(ToOwned::to_owned),
            form,
            filed_at,
            accepted_at,
        ));
    }
    out
}

#[allow(clippy::too_many_arguments)]
fn parse_classic_facts(
    root: roxmltree::Node<'_, '_>,
    doc_id: &str,
    security_id: i64,
    cik: &str,
    accession: &str,
    form: &str,
    filed_at: &str,
    accepted_at: &str,
    contexts: &BTreeMap<String, ContextInfo>,
    units: &BTreeMap<String, UnitInfo>,
) -> Vec<RawFact> {
    let mut out = Vec::new();
    for n in elements(root, "") {
        let Some(ctx_id) = attr(n, "contextRef") else {
            continue;
        };
        let Some(tax) = taxonomy_for_space(n.tag_name().namespace()) else {
            continue;
        };
        let Some(ctx) = contexts.get(ctx_id) else {
            continue;
        };
        let unit = attr(n, "unitRef").and_then(|u| units.get(u)).cloned();
        let raw = text(n);
        let (value_decimal, value_text_opt) = match parse_scaled_number(&raw, "0") {
            Some(v) => (Some(v), None),
            None => (None, Some(raw)),
        };
        out.push(build_raw_fact(
            doc_id,
            security_id,
            cik,
            accession,
            tax,
            n.tag_name().name(),
            ctx.clone(),
            unit,
            value_decimal,
            value_text_opt,
            attr(n, "decimals").map(ToOwned::to_owned),
            attr(n, "precision").map(ToOwned::to_owned),
            form,
            filed_at,
            accepted_at,
        ));
    }
    out
}

#[allow(clippy::too_many_arguments)]
fn build_raw_fact(
    doc_id: &str,
    security_id: i64,
    cik: &str,
    accession: &str,
    taxonomy: &str,
    local: &str,
    context: ContextInfo,
    unit: Option<UnitInfo>,
    value_decimal: Option<f64>,
    value_text: Option<String>,
    decimals_attr: Option<String>,
    precision_attr: Option<String>,
    form: &str,
    filed_at: &str,
    accepted_at: &str,
) -> RawFact {
    let concept_qname = format!("{taxonomy}:{local}");
    let payload = json!({
        "source_doc_id": doc_id,
        "cik": cik,
        "accession_number": accession,
        "taxonomy": taxonomy,
        "concept_qname": concept_qname.as_str(),
        "raw_context_id": context.raw_id.as_str(),
        "unit_id": unit.as_ref().map(|u| u.raw_id.as_str()),
        "value_decimal": value_decimal,
        "value_text": value_text.as_deref(),
    });
    let raw_hash = sha256_hex(canonical_json(&payload).as_bytes());
    let fact_id = stable_id("fact", json!({"raw_fact_hash": raw_hash.as_str()}));
    RawFact {
        fact_id,
        source_doc_id: doc_id.to_string(),
        security_id,
        cik: cik.to_string(),
        accession: accession.to_string(),
        taxonomy: taxonomy.to_string(),
        concept_qname,
        concept_local_name: local.to_string(),
        context,
        unit,
        value_decimal,
        value_text,
        decimals_attr,
        precision_attr,
        is_nil: 0,
        form: form.to_string(),
        filed_at: filed_at.to_string(),
        accepted_at: accepted_at.to_string(),
        available_at: max_string(&[filed_at, accepted_at]),
        raw_fact_hash: raw_hash,
    }
}

fn insert_context(
    tx: &Transaction<'_>,
    cik: &str,
    accession: &str,
    ctx: &ContextInfo,
) -> Result<()> {
    tx.execute(
        "INSERT OR IGNORE INTO dimension_signatures(dimensions_hash, dimensions_json, has_dimensions, scope_class, axis_count, created_at) VALUES (?, ?, ?, ?, ?, ?)",
        params![ctx.dimensions_hash, ctx.dimensions_json, (ctx.axis_count > 0) as i64, ctx.scope_class, ctx.axis_count, utc_now()],
    )?;
    tx.execute(
        "INSERT OR IGNORE INTO reporting_periods(period_id, cik, accession_number, raw_start_date, raw_end_date, raw_instant_date, start_date_inclusive, end_date_exclusive, duration_days, period_kind, period_semantics, fiscal_year, fiscal_period, fiscal_period_ordinal, period_length_class, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'xbrl_context', ?)",
        params![
            ctx.period_id, cik, accession, ctx.period.raw_start_date, ctx.period.raw_end_date,
            ctx.period.raw_instant_date, ctx.period.start_date_inclusive, ctx.period.end_date_exclusive,
            ctx.period.duration_days, ctx.period.period_kind, ctx.period.period_semantics,
            ctx.period.fiscal_year, ctx.period.fiscal_period, ctx.period.fiscal_period_ordinal,
            ctx.period.period_length_class, utc_now()
        ],
    )?;
    let raw_hash = sha256_hex(
        canonical_json(&json!({
            "raw_context_id": ctx.raw_id.as_str(),
            "period_id": ctx.period_id.as_str(),
            "dimensions_hash": ctx.dimensions_hash.as_str(),
        }))
        .as_bytes(),
    );
    tx.execute(
        "INSERT OR IGNORE INTO xbrl_contexts(context_id, cik, accession_number, raw_context_id, entity_identifier, period_id, period_kind, raw_start_date, raw_end_date, raw_instant_date, start_date_inclusive, end_date_exclusive, duration_days, dimensions_hash, dimensions_json, segment_json, scenario_json, raw_context_sha256) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        params![
            ctx.context_id, cik, accession, ctx.raw_id, ctx.entity_identifier, ctx.period_id,
            ctx.period.period_kind, ctx.period.raw_start_date, ctx.period.raw_end_date, ctx.period.raw_instant_date,
            ctx.period.start_date_inclusive, ctx.period.end_date_exclusive, ctx.period.duration_days,
            ctx.dimensions_hash, ctx.dimensions_json, ctx.segment_json, ctx.scenario_json, raw_hash
        ],
    )?;
    Ok(())
}

fn insert_unit(tx: &Transaction<'_>, cik: &str, accession: &str, unit: &UnitInfo) -> Result<()> {
    let raw_hash = sha256_hex(
        canonical_json(
            &json!({"raw_unit_id": unit.raw_id.as_str(), "signature": unit.signature.as_str()}),
        )
        .as_bytes(),
    );
    tx.execute(
        "INSERT OR IGNORE INTO xbrl_units(unit_id, cik, accession_number, raw_unit_id, unit_signature, numerator_json, denominator_json, raw_unit_sha256) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
        params![unit.unit_id, cik, accession, unit.raw_id, unit.signature, unit.numerator_json, unit.denominator_json, raw_hash],
    )?;
    Ok(())
}

fn insert_raw_fact(tx: &Transaction<'_>, fact: &RawFact) -> Result<bool> {
    let unit_id = fact.unit.as_ref().map(|u| u.unit_id.as_str());
    let changed = tx.execute(
        "INSERT OR IGNORE INTO xbrl_facts(fact_id, source_doc_id, security_id, cik, accession_number, taxonomy, concept_qname, concept_local_name, context_id, unit_id, value_decimal, value_text, decimals_attr, precision_attr, is_nil, raw_fact_hash, raw_context_id, form, filed_at, accepted_at, available_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        params![
            fact.fact_id, fact.source_doc_id, fact.security_id, fact.cik, fact.accession,
            fact.taxonomy, fact.concept_qname, fact.concept_local_name, fact.context.context_id, unit_id,
            fact.value_decimal, fact.value_text, fact.decimals_attr, fact.precision_attr, fact.is_nil,
            fact.raw_fact_hash, fact.context.raw_id, fact.form, fact.filed_at, fact.accepted_at, fact.available_at, utc_now()
        ],
    )?;
    Ok(changed > 0)
}

fn insert_canonical_observation(tx: &Transaction<'_>, fact: &RawFact) -> Result<bool> {
    let Some(value) = fact.value_decimal else {
        return Ok(false);
    };
    if fact.context.scope_class != "consolidated_total" {
        return Ok(false);
    }
    let key = format!("{}:{}", fact.taxonomy, fact.concept_local_name);
    let Some(concept) = concept_candidate(&key) else {
        return Ok(false);
    };
    let unit_signature = fact
        .unit
        .as_ref()
        .map(|u| u.signature.as_str())
        .unwrap_or("unknown");
    if concept.unit != unit_signature && !(concept.unit == "USD/shares" && unit_signature == "USD")
    {
        return Ok(false);
    }
    let metric_name = metric_name(concept.metric_id);
    let metric_kind = metric_kind(concept.metric_id);
    let quality_flags = if fact.context.scope_class == "consolidated_total" {
        0
    } else {
        QUALITY_DIMENSIONAL_FACT
    };
    let obs_hash_payload = json!({
        "resolver_version": RESOLVER_VERSION,
        "security_id": fact.security_id,
        "metric_id": concept.metric_id,
        "basis_id": concept.basis_id,
        "period_id": fact.context.period_id.as_str(),
        "value_decimal": value,
        "unit_signature": unit_signature,
        "dimensions_hash": fact.context.dimensions_hash.as_str(),
        "source_fact_id": fact.fact_id.as_str(),
    });
    let observation_hash = sha256_hex(canonical_json(&obs_hash_payload).as_bytes());
    let observation_id = stable_id(
        "obs",
        json!({"observation_hash": observation_hash.as_str()}),
    );
    let changed = tx.execute(
        "INSERT OR IGNORE INTO canonical_observations(observation_id, observation_hash, resolver_version, security_id, cik, metric_id, metric_name, metric_kind, basis_id, period_id, period_semantics, duration_days, fiscal_year, fiscal_period, value_decimal, unit_signature, dimensions_hash, dimensional_scope, observation_status, source_fact_id, source_accession, taxonomy, concept_qname, selection_reason, quality_flags, quality_flags_json, quality_score, accepted_at, available_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'selected', ?, ?, ?, ?, ?, ?, '[]', ?, ?, ?, ?)",
        params![
            observation_id, observation_hash, RESOLVER_VERSION, fact.security_id, fact.cik,
            concept.metric_id, metric_name, metric_kind, concept.basis_id, fact.context.period_id,
            fact.context.period.period_semantics, fact.context.period.duration_days,
            fact.context.period.fiscal_year, fact.context.period.fiscal_period, value, unit_signature,
            fact.context.dimensions_hash, fact.context.scope_class, fact.fact_id, fact.accession,
            fact.taxonomy, fact.concept_qname, format!("concept_candidate_priority_{}", concept.priority),
            quality_flags, 1.0f64, fact.accepted_at, fact.available_at, utc_now()
        ],
    )?;
    Ok(changed > 0)
}

fn append_event(tx: &Transaction<'_>, event_type: &str, payload: Value) -> Result<Value> {
    let event_id = Uuid::new_v4().to_string();
    let occurred_at = utc_now();
    let payload_json = canonical_json(&payload);
    let payload_sha256 = sha256_hex(payload_json.as_bytes());
    tx.execute(
        "INSERT INTO events(event_id, event_type, event_version, occurred_at, payload_json, payload_sha256) VALUES (?, ?, 1, ?, ?, ?)",
        params![event_id, event_type, occurred_at, payload_json, payload_sha256],
    )?;
    Ok(json!({
        "event_id": event_id,
        "event_type": event_type,
        "event_version": 1,
        "occurred_at": occurred_at,
        "payload": payload,
    }))
}

fn elements<'a, 'input>(
    root: roxmltree::Node<'a, 'input>,
    local: &'static str,
) -> impl Iterator<Item = roxmltree::Node<'a, 'input>> {
    root.descendants()
        .filter(move |n| n.is_element() && (local.is_empty() || n.tag_name().name() == local))
}

fn first_child<'a, 'input>(
    n: roxmltree::Node<'a, 'input>,
    local: &'static str,
) -> Option<roxmltree::Node<'a, 'input>> {
    n.children()
        .find(|c| c.is_element() && c.tag_name().name() == local)
}

fn first_descendant<'a, 'input>(
    n: roxmltree::Node<'a, 'input>,
    local: &'static str,
) -> Option<roxmltree::Node<'a, 'input>> {
    n.descendants()
        .find(|c| c.is_element() && c.tag_name().name() == local)
}

fn attr<'a>(n: roxmltree::Node<'a, '_>, local: &str) -> Option<&'a str> {
    n.attributes()
        .find(|a| a.name() == local)
        .map(|a| a.value())
}

fn text(n: roxmltree::Node<'_, '_>) -> String {
    let mut out = String::new();
    for d in n.descendants() {
        if d.is_text() {
            if let Some(t) = d.text() {
                out.push_str(t);
            }
        }
    }
    out.trim().to_string()
}

fn taxonomy_for_space(ns: Option<&str>) -> Option<&'static str> {
    let ns = ns.unwrap_or_default();
    if ns.contains("us-gaap") {
        Some("us-gaap")
    } else if ns.contains("xbrl.sec.gov/dei") {
        Some("dei")
    } else {
        None
    }
}

fn split_qname(value: &str) -> (String, String) {
    value
        .split_once(':')
        .map(|(a, b)| (a.to_string(), b.to_string()))
        .unwrap_or_else(|| ("".to_string(), value.to_string()))
}

fn parse_integer_text(value: &str) -> Option<i32> {
    let clean = value.trim();
    if clean.is_empty() {
        return None;
    }
    if clean.contains('.') {
        clean.parse::<f64>().ok().map(|v| v as i32)
    } else {
        clean.parse::<i32>().ok()
    }
}

fn normalize_fiscal_period(value: &str) -> String {
    let clean = value.trim().to_ascii_uppercase();
    match fiscal_quarter_ordinal(&clean) {
        Some(q) => format!("Q{q}"),
        None => clean,
    }
}

fn fiscal_quarter_ordinal(value: &str) -> Option<i32> {
    match value.trim().to_ascii_uppercase().as_str() {
        "Q1" | "1" => Some(1),
        "Q2" | "2" => Some(2),
        "Q3" | "3" => Some(3),
        "Q4" | "4" => Some(4),
        _ => None,
    }
}

fn parse_date(value: &str) -> Option<Date> {
    Date::parse(value, format_description!("[year]-[month]-[day]")).ok()
}

fn format_date(date: Date) -> String {
    date.format(format_description!("[year]-[month]-[day]"))
        .unwrap_or_default()
}

fn next_date(value: &str) -> Option<String> {
    parse_date(value)
        .and_then(|d| d.next_day())
        .map(format_date)
}

fn duration_days(start: &str, end: &str) -> i64 {
    let (Some(s), Some(e)) = (parse_date(start), parse_date(end)) else {
        return 0;
    };
    (e - s).whole_days() + 1
}

fn comparable_fiscal_year(
    period_end: &str,
    document_end: &str,
    document_fiscal_year: i32,
) -> Option<i32> {
    let end = parse_date(period_end)?;
    let doc = parse_date(document_end)?;
    let year_delta = doc.year() - end.year();
    if !(0..=10).contains(&year_delta) {
        return None;
    }
    let shifted = end.replace_year(end.year() + year_delta).ok()?;
    if ((shifted - doc).whole_days()).abs() > 10 {
        return None;
    }
    Some(document_fiscal_year - year_delta)
}

fn is_quarter_duration(days: i64) -> bool {
    (70..=110).contains(&days)
}
fn is_fiscal_ytd_duration(days: i64, fiscal_quarter: i64) -> bool {
    fiscal_quarter > 1 && days >= fiscal_quarter * 70 && days <= fiscal_quarter * 110
}
fn quarter_from_end(date: &str) -> i32 {
    date.get(5..7)
        .and_then(|m| m.parse::<i32>().ok())
        .map(|m| ((m - 1) / 3) + 1)
        .unwrap_or(0)
}
fn period_length_class(days: i64) -> &'static str {
    if days == 0 {
        "instant"
    } else if (70..=110).contains(&days) {
        "13w"
    } else if (330..=380).contains(&days) {
        "year"
    } else {
        "irregular"
    }
}

fn normalize_measure(value: &str) -> String {
    let clean = value.trim();
    match clean {
        "iso4217:USD" | "USD" => "USD".to_string(),
        "xbrli:shares" | "shares" => "shares".to_string(),
        _ if clean.contains("USD") => "USD".to_string(),
        _ => clean.to_string(),
    }
}

fn parse_scaled_number(value: &str, scale: &str) -> Option<f64> {
    let mut clean = value.trim().replace(',', "").replace('\u{a0}', "");
    if clean.is_empty() {
        return None;
    }
    let mut neg = false;
    if clean.starts_with('(') && clean.ends_with(')') {
        neg = true;
        clean = clean
            .trim_start_matches('(')
            .trim_end_matches(')')
            .to_string();
    }
    let mut number = clean.parse::<f64>().ok()?;
    if neg {
        number = -number;
    }
    let scale = scale.trim().parse::<i32>().unwrap_or(0);
    Some(number * 10f64.powi(scale))
}

fn concept_candidate(key: &str) -> Option<CanonicalConcept> {
    match key {
        "us-gaap:RevenueFromContractWithCustomerExcludingAssessedTax" => Some(CanonicalConcept {
            metric_id: 1,
            basis_id: 101,
            priority: 1,
            unit: "USD",
        }),
        "us-gaap:RevenueFromContractWithCustomerIncludingAssessedTax" => Some(CanonicalConcept {
            metric_id: 1,
            basis_id: 102,
            priority: 2,
            unit: "USD",
        }),
        "us-gaap:SalesRevenueNet" => Some(CanonicalConcept {
            metric_id: 1,
            basis_id: 103,
            priority: 3,
            unit: "USD",
        }),
        "us-gaap:Revenues" => Some(CanonicalConcept {
            metric_id: 1,
            basis_id: 104,
            priority: 4,
            unit: "USD",
        }),
        "us-gaap:NetIncomeLoss" => Some(CanonicalConcept {
            metric_id: 2,
            basis_id: 201,
            priority: 1,
            unit: "USD",
        }),
        "us-gaap:ProfitLoss" => Some(CanonicalConcept {
            metric_id: 2,
            basis_id: 201,
            priority: 2,
            unit: "USD",
        }),
        "us-gaap:EarningsPerShareDiluted" => Some(CanonicalConcept {
            metric_id: 3,
            basis_id: 301,
            priority: 1,
            unit: "USD/shares",
        }),
        "us-gaap:WeightedAverageNumberOfDilutedSharesOutstanding" => Some(CanonicalConcept {
            metric_id: 4,
            basis_id: 401,
            priority: 1,
            unit: "shares",
        }),
        "us-gaap:NetCashProvidedByUsedInOperatingActivities" => Some(CanonicalConcept {
            metric_id: 5,
            basis_id: 501,
            priority: 1,
            unit: "USD",
        }),
        "us-gaap:PaymentsToAcquirePropertyPlantAndEquipment" => Some(CanonicalConcept {
            metric_id: 6,
            basis_id: 601,
            priority: 1,
            unit: "USD",
        }),
        "us-gaap:CashAndCashEquivalentsAtCarryingValue" => Some(CanonicalConcept {
            metric_id: 7,
            basis_id: 701,
            priority: 1,
            unit: "USD",
        }),
        "us-gaap:CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents" => {
            Some(CanonicalConcept {
                metric_id: 7,
                basis_id: 701,
                priority: 2,
                unit: "USD",
            })
        }
        "us-gaap:LongTermDebtAndFinanceLeaseObligationsCurrent" => Some(CanonicalConcept {
            metric_id: 8,
            basis_id: 801,
            priority: 1,
            unit: "USD",
        }),
        "us-gaap:LongTermDebtCurrent" => Some(CanonicalConcept {
            metric_id: 8,
            basis_id: 801,
            priority: 2,
            unit: "USD",
        }),
        "us-gaap:LongTermDebtNoncurrent" => Some(CanonicalConcept {
            metric_id: 8,
            basis_id: 801,
            priority: 3,
            unit: "USD",
        }),
        _ => None,
    }
}

fn metric_name(metric_id: i64) -> &'static str {
    match metric_id {
        1 => "revenue",
        2 => "net_income",
        3 => "eps_diluted",
        4 => "diluted_shares",
        5 => "operating_cash_flow",
        6 => "capex",
        7 => "cash",
        8 => "debt",
        _ => "unknown",
    }
}
fn metric_kind(metric_id: i64) -> &'static str {
    match metric_id {
        1 | 2 | 4 | 5 | 6 => "flow",
        3 => "per_share_flow",
        7 | 8 => "instant",
        _ => "unknown",
    }
}

fn period_identity_json(cik: &str, accession: &str, p: &PeriodInfo) -> Value {
    json!({
        "cik": cik,
        "accession_number": accession,
        "raw_start_date": p.raw_start_date.as_deref(),
        "raw_end_date": p.raw_end_date.as_deref(),
        "raw_instant_date": p.raw_instant_date.as_deref(),
        "period_semantics": p.period_semantics.as_str(),
        "fiscal_year": p.fiscal_year,
        "fiscal_period": p.fiscal_period.as_deref(),
    })
}

fn canonical_json<T: Serialize + ?Sized>(value: &T) -> String {
    serde_json::to_string(value).expect("canonical json")
}

fn sha256_hex(data: impl AsRef<[u8]>) -> String {
    let mut hasher = Sha256::new();
    hasher.update(data.as_ref());
    hex::encode(hasher.finalize())
}

fn stable_id(prefix: &str, value: Value) -> String {
    format!("{prefix}-{}", sha256_hex(canonical_json(&value).as_bytes()))
}

fn total_dimensions_hash() -> String {
    sha256_hex(TOTAL_DIMENSIONS_JSON.as_bytes())
}

fn normalize_cik(cik: &str) -> String {
    let mut digits: String = cik.chars().filter(|c| c.is_ascii_digit()).collect();
    if digits.len() > 10 {
        digits = digits[digits.len() - 10..].to_string();
    }
    format!("{:0>10}", digits)
}

fn utc_now() -> String {
    let now = time::OffsetDateTime::now_utc();
    now.format(format_description!(
        "[year]-[month]-[day]T[hour]:[minute]:[second].[subsecond digits:3]Z"
    ))
    .unwrap_or_default()
}

fn max_string(values: &[&str]) -> String {
    values
        .iter()
        .copied()
        .max()
        .filter(|s| !s.is_empty())
        .unwrap_or("")
        .to_string()
}

fn sql_form_filter_clause(column: &str, forms: &str) -> Option<(String, Vec<String>)> {
    let mut values = forms
        .split(',')
        .map(|s| s.trim().to_ascii_uppercase())
        .filter(|s| !s.is_empty())
        .collect::<Vec<_>>();
    if values.is_empty() || values.iter().any(|s| s == "*" || s == "ALL") {
        return None;
    }
    values.sort();
    values.dedup();
    let placeholders = std::iter::repeat("?")
        .take(values.len())
        .collect::<Vec<_>>()
        .join(",");
    Some((format!(" AND upper({column}) IN ({placeholders})"), values))
}

fn archive_url(cik10: &str, accession: &str, doc: Option<&str>) -> String {
    let cik_int = cik10.parse::<i64>().unwrap_or(0);
    let acc_no_dash = accession.replace('-', "");
    match doc {
        Some(doc) if !doc.is_empty() => {
            format!("{SEC_ARCHIVE_BASE_URL}/{cik_int}/{acc_no_dash}/{doc}")
        }
        _ => format!("{SEC_ARCHIVE_BASE_URL}/{cik_int}/{acc_no_dash}/index.json"),
    }
}

fn source_document_id(cik: &str, url: &str, hash: &str) -> String {
    stable_id("doc", json!({"cik": cik, "url": url, "sha256": hash}))
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
    if !is_package_artifact_name(name, "") && !Path::new(name).extension().is_some() {
        bail!("unsafe SEC archive item name: {name}");
    }
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn element_text_is_not_duplicated() {
        let doc = roxmltree::Document::parse("<root><value>2024-03-31</value></root>").unwrap();
        let value = first_descendant(doc.root_element(), "value").unwrap();
        assert_eq!(text(value), "2024-03-31");
    }
}
