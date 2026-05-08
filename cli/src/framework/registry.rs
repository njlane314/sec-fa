use anyhow::{Context as _, Result};
use rusqlite::{params, Connection, OptionalExtension};
use serde::Deserialize;
use serde_json::{json, Value};

use super::canonical_json::to_canonical_json;

const PRODUCT_TYPES_JSON: &str = include_str!("../../../docs/contracts/product_types.json");
const ALGORITHM_REGISTRY_JSON: &str =
    include_str!("../../../docs/contracts/algorithm_registry.bootstrap.json");

#[derive(Debug, Clone)]
#[allow(dead_code)]
pub struct ProductType {
    pub product_type: String,
    pub schema_name: String,
    pub schema_version: i64,
    pub hazard_class: String,
    pub description: String,
}

#[derive(Debug, Clone)]
#[allow(dead_code)]
pub struct Algorithm {
    pub algorithm_id: String,
    pub algorithm_name: String,
    pub algorithm_version: String,
    pub hazard_class: String,
    pub purity: String,
    pub deterministic: bool,
    pub executable_kind: String,
    pub executable_ref: String,
    pub inputs: Vec<String>,
    pub outputs: Vec<String>,
    pub resources: Vec<String>,
    pub forbidden_resources: Vec<String>,
}

#[derive(Debug, Clone)]
pub struct ResourceDeclaration {
    pub resource_id: String,
    pub resource_kind: String,
    pub max_concurrent: i64,
    pub min_interval_ms: i64,
    pub allowed_modes: Vec<String>,
    pub hazard_class: String,
    pub description: String,
}

#[derive(Debug, Deserialize)]
struct ProductTypeBootstrap {
    spec_version: i64,
    product_types: Vec<ProductTypeBootstrapRow>,
}

#[derive(Debug, Deserialize)]
struct ProductTypeBootstrapRow {
    product_type: String,
    schema_name: String,
    schema_version: i64,
    hazard_class: String,
    description: String,
}

#[derive(Debug, Deserialize)]
struct AlgorithmBootstrap {
    spec_version: i64,
    algorithms: Vec<AlgorithmBootstrapRow>,
}

#[derive(Debug, Deserialize)]
struct AlgorithmBootstrapRow {
    algorithm_id: String,
    algorithm_name: String,
    algorithm_version: String,
    hazard_class: String,
    purity: String,
    deterministic: bool,
    executable_kind: String,
    executable_ref: String,
    inputs: Vec<String>,
    outputs: Vec<String>,
    resources: Vec<String>,
    forbidden_resources: Vec<String>,
}

pub fn seed_product_graph_reference_data(conn: &Connection) -> Result<()> {
    seed_product_types(conn)?;
    seed_algorithm_registry(conn)?;
    seed_resource_declarations(conn)?;
    Ok(())
}

pub fn seed_product_types(conn: &Connection) -> Result<()> {
    let parsed: ProductTypeBootstrap = serde_json::from_str(PRODUCT_TYPES_JSON)?;
    if parsed.spec_version != 1 {
        anyhow::bail!(
            "unsupported product type bootstrap spec_version={}",
            parsed.spec_version
        );
    }
    for row in parsed.product_types {
        conn.execute(
            "INSERT INTO product_types(product_type, schema_name, schema_version, hazard_class, description)
             VALUES (?, ?, ?, ?, ?)
             ON CONFLICT(product_type) DO UPDATE SET
               schema_name=excluded.schema_name,
               schema_version=excluded.schema_version,
               hazard_class=excluded.hazard_class,
               description=excluded.description",
            params![
                row.product_type,
                row.schema_name,
                row.schema_version,
                row.hazard_class,
                row.description
            ],
        )?;
    }
    Ok(())
}

pub fn seed_algorithm_registry(conn: &Connection) -> Result<()> {
    let parsed: AlgorithmBootstrap = serde_json::from_str(ALGORITHM_REGISTRY_JSON)?;
    if parsed.spec_version != 1 {
        anyhow::bail!(
            "unsupported algorithm bootstrap spec_version={}",
            parsed.spec_version
        );
    }
    for row in parsed.algorithms {
        conn.execute(
            "INSERT INTO algorithm_registry(algorithm_id, algorithm_name, algorithm_version, hazard_class, purity, deterministic, executable_kind, executable_ref, input_contract_json, output_contract_json, resource_contract_json, forbidden_resource_json, created_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
             ON CONFLICT(algorithm_id) DO UPDATE SET
               algorithm_name=excluded.algorithm_name,
               algorithm_version=excluded.algorithm_version,
               hazard_class=excluded.hazard_class,
               purity=excluded.purity,
               deterministic=excluded.deterministic,
               executable_kind=excluded.executable_kind,
               executable_ref=excluded.executable_ref,
               input_contract_json=excluded.input_contract_json,
               output_contract_json=excluded.output_contract_json,
               resource_contract_json=excluded.resource_contract_json,
               forbidden_resource_json=excluded.forbidden_resource_json,
               created_at=excluded.created_at",
            params![
                row.algorithm_id,
                row.algorithm_name,
                row.algorithm_version,
                row.hazard_class,
                row.purity,
                if row.deterministic { 1 } else { 0 },
                row.executable_kind,
                row.executable_ref,
                to_canonical_json(&row.inputs)?,
                to_canonical_json(&row.outputs)?,
                to_canonical_json(&row.resources)?,
                to_canonical_json(&row.forbidden_resources)?,
            ],
        )?;
    }
    Ok(())
}

pub fn seed_resource_declarations(conn: &Connection) -> Result<()> {
    for resource in known_resource_declarations() {
        conn.execute(
            "INSERT INTO resource_declarations(resource_id, resource_kind, max_concurrent, min_interval_ms, allowed_modes_json, hazard_class, description)
             VALUES (?, ?, ?, ?, ?, ?, ?)
             ON CONFLICT(resource_id) DO UPDATE SET
               resource_kind=excluded.resource_kind,
               max_concurrent=excluded.max_concurrent,
               min_interval_ms=excluded.min_interval_ms,
               allowed_modes_json=excluded.allowed_modes_json,
               hazard_class=excluded.hazard_class,
               description=excluded.description",
            params![
                resource.resource_id,
                resource.resource_kind,
                resource.max_concurrent,
                resource.min_interval_ms,
                to_canonical_json(&resource.allowed_modes)?,
                resource.hazard_class,
                resource.description
            ],
        )?;
    }
    Ok(())
}

pub fn known_resource_declarations() -> Vec<ResourceDeclaration> {
    vec![
        ResourceDeclaration {
            resource_id: "cpu.local".to_string(),
            resource_kind: "cpu".to_string(),
            max_concurrent: 1,
            min_interval_ms: 0,
            allowed_modes: all_modes(),
            hazard_class: "C".to_string(),
            description: "Local CPU execution.".to_string(),
        },
        ResourceDeclaration {
            resource_id: "sqlite.local".to_string(),
            resource_kind: "sqlite".to_string(),
            max_concurrent: 1,
            min_interval_ms: 0,
            allowed_modes: all_modes(),
            hazard_class: "B".to_string(),
            description: "Local SQLite operational database.".to_string(),
        },
        ResourceDeclaration {
            resource_id: "filesystem.local".to_string(),
            resource_kind: "filesystem".to_string(),
            max_concurrent: 1,
            min_interval_ms: 0,
            allowed_modes: all_modes(),
            hazard_class: "B".to_string(),
            description: "Local filesystem reads and writes.".to_string(),
        },
        ResourceDeclaration {
            resource_id: "sec.http".to_string(),
            resource_kind: "sec_http".to_string(),
            max_concurrent: 1,
            min_interval_ms: 150,
            allowed_modes: vec![
                "observe".to_string(),
                "shadow".to_string(),
                "stage".to_string(),
                "paper".to_string(),
                "live_limited".to_string(),
                "live".to_string(),
            ],
            hazard_class: "B".to_string(),
            description: "SEC HTTP reads with rate moderation.".to_string(),
        },
        ResourceDeclaration {
            resource_id: "market_data_http".to_string(),
            resource_kind: "market_data_http".to_string(),
            max_concurrent: 1,
            min_interval_ms: 150,
            allowed_modes: vec![
                "observe".to_string(),
                "shadow".to_string(),
                "stage".to_string(),
                "paper".to_string(),
                "live_limited".to_string(),
                "live".to_string(),
            ],
            hazard_class: "B".to_string(),
            description: "Market-data HTTP reads.".to_string(),
        },
        ResourceDeclaration {
            resource_id: "broker.adapter".to_string(),
            resource_kind: "broker".to_string(),
            max_concurrent: 1,
            min_interval_ms: 0,
            allowed_modes: vec![
                "paper".to_string(),
                "live_limited".to_string(),
                "live".to_string(),
            ],
            hazard_class: "A".to_string(),
            description: "Isolated broker adapter boundary.".to_string(),
        },
        ResourceDeclaration {
            resource_id: "notification.local".to_string(),
            resource_kind: "notification".to_string(),
            max_concurrent: 1,
            min_interval_ms: 0,
            allowed_modes: all_modes(),
            hazard_class: "C".to_string(),
            description: "Operator notification channel.".to_string(),
        },
        ResourceDeclaration {
            resource_id: "clock.utc".to_string(),
            resource_kind: "clock".to_string(),
            max_concurrent: 1,
            min_interval_ms: 0,
            allowed_modes: all_modes(),
            hazard_class: "B".to_string(),
            description: "UTC wall-clock access outside the pure C++ core.".to_string(),
        },
    ]
}

fn all_modes() -> Vec<String> {
    [
        "halted",
        "observe",
        "shadow",
        "stage",
        "paper",
        "live_limited",
        "live",
    ]
    .iter()
    .map(|v| v.to_string())
    .collect()
}

pub fn load_product_type(conn: &Connection, product_type: &str) -> Result<Option<ProductType>> {
    conn.query_row(
        "SELECT product_type, schema_name, schema_version, hazard_class, description FROM product_types WHERE product_type=?",
        [product_type],
        |row| {
            Ok(ProductType {
                product_type: row.get(0)?,
                schema_name: row.get(1)?,
                schema_version: row.get(2)?,
                hazard_class: row.get(3)?,
                description: row.get(4)?,
            })
        },
    )
    .optional()
    .with_context(|| format!("loading product type {product_type}"))
}

pub fn load_algorithm(conn: &Connection, algorithm_id: &str) -> Result<Option<Algorithm>> {
    conn.query_row(
        "SELECT algorithm_id, algorithm_name, algorithm_version, hazard_class, purity, deterministic, executable_kind, executable_ref, input_contract_json, output_contract_json, resource_contract_json, forbidden_resource_json FROM algorithm_registry WHERE algorithm_id=?",
        [algorithm_id],
        |row| {
            let inputs_json: String = row.get(8)?;
            let outputs_json: String = row.get(9)?;
            let resources_json: String = row.get(10)?;
            let forbidden_json: String = row.get(11)?;
            Ok(Algorithm {
                algorithm_id: row.get(0)?,
                algorithm_name: row.get(1)?,
                algorithm_version: row.get(2)?,
                hazard_class: row.get(3)?,
                purity: row.get(4)?,
                deterministic: row.get::<_, i64>(5)? != 0,
                executable_kind: row.get(6)?,
                executable_ref: row.get(7)?,
                inputs: serde_json::from_str(&inputs_json).unwrap_or_default(),
                outputs: serde_json::from_str(&outputs_json).unwrap_or_default(),
                resources: serde_json::from_str(&resources_json).unwrap_or_default(),
                forbidden_resources: serde_json::from_str(&forbidden_json).unwrap_or_default(),
            })
        },
    )
    .optional()
    .with_context(|| format!("loading algorithm {algorithm_id}"))
}

pub fn load_resource(conn: &Connection, resource_id: &str) -> Result<Option<ResourceDeclaration>> {
    conn.query_row(
        "SELECT resource_id, resource_kind, max_concurrent, min_interval_ms, allowed_modes_json, hazard_class, description FROM resource_declarations WHERE resource_id=?",
        [resource_id],
        |row| {
            let allowed_modes_json: String = row.get(4)?;
            Ok(ResourceDeclaration {
                resource_id: row.get(0)?,
                resource_kind: row.get(1)?,
                max_concurrent: row.get(2)?,
                min_interval_ms: row.get(3)?,
                allowed_modes: serde_json::from_str(&allowed_modes_json).unwrap_or_default(),
                hazard_class: row.get(5)?,
                description: row.get(6)?,
            })
        },
    )
    .optional()
    .with_context(|| format!("loading resource {resource_id}"))
}

#[allow(dead_code)]
pub fn algorithm_payload(algorithm: &Algorithm) -> Value {
    json!({
        "algorithm_id": algorithm.algorithm_id,
        "algorithm_name": algorithm.algorithm_name,
        "algorithm_version": algorithm.algorithm_version,
        "hazard_class": algorithm.hazard_class,
        "purity": algorithm.purity,
        "deterministic": algorithm.deterministic,
        "executable_kind": algorithm.executable_kind,
        "executable_ref": algorithm.executable_ref,
        "inputs": algorithm.inputs,
        "outputs": algorithm.outputs,
        "resources": algorithm.resources,
        "forbidden_resources": algorithm.forbidden_resources
    })
}
