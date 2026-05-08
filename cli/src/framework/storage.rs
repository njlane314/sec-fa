use std::collections::{BTreeMap, BTreeSet};

use anyhow::{Context as _, Result};
use rusqlite::{params, Connection, OptionalExtension, Transaction};
use serde_json::{json, Value};

use crate::fa_core::utc_now;

use super::canonical_json::to_canonical_json;
use super::hash;
use super::product::{DataProduct, NewProduct};

pub fn persist_product(tx: &Transaction<'_>, product: &NewProduct) -> Result<String> {
    let parent_ids = product
        .parent_product_ids
        .iter()
        .map(|(product_id, _)| product_id.clone())
        .collect::<Vec<_>>();
    let product_id = hash::product_id(
        &product.product_type,
        &product.schema_name,
        product.schema_version,
        &product.cell_id,
        product.family_id.as_deref(),
        &product.content_sha256,
        &product.created_by_algorithm,
        &product.algorithm_version,
        &product.config_sha256,
        &parent_ids,
        &product.available_at,
    )?;
    let created_at = utc_now();
    let changed = tx.execute(
        "INSERT OR IGNORE INTO data_products(product_id, product_type, schema_name, schema_version, cell_id, family_id, content_sha256, storage_kind, storage_ref, created_by_algorithm, algorithm_version, config_sha256, code_sha256, valid_time_start, valid_time_end, source_time, accepted_at, ingested_at, available_at, created_at, hazard_class) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        params![
            product_id,
            product.product_type,
            product.schema_name,
            product.schema_version,
            product.cell_id,
            product.family_id,
            product.content_sha256,
            product.storage_kind,
            product.storage_ref,
            product.created_by_algorithm,
            product.algorithm_version,
            product.config_sha256,
            product.code_sha256,
            product.valid_time_start,
            product.valid_time_end,
            product.source_time,
            product.accepted_at,
            product.ingested_at,
            product.available_at,
            created_at,
            product.hazard_class,
        ],
    )?;
    let stored_product_id = if changed == 0 {
        tx.query_row(
            "SELECT product_id FROM data_products WHERE product_type=? AND cell_id=? AND content_sha256=? AND created_by_algorithm=? AND config_sha256=?",
            params![
                product.product_type,
                product.cell_id,
                product.content_sha256,
                product.created_by_algorithm,
                product.config_sha256,
            ],
            |row| row.get::<_, String>(0),
        )
        .with_context(|| format!("looking up existing product {}", product.product_type))?
    } else {
        product_id
    };

    for (parent_id, role) in &product.parent_product_ids {
        tx.execute(
            "INSERT OR IGNORE INTO data_product_lineage(child_product_id, parent_product_id, role) VALUES (?, ?, ?)",
            params![stored_product_id, parent_id, role],
        )?;
    }
    Ok(stored_product_id)
}

pub fn load_product(conn: &Connection, product_id: &str) -> Result<Option<DataProduct>> {
    conn.query_row(
        "SELECT product_id, product_type, schema_name, schema_version, cell_id, family_id, content_sha256, storage_kind, storage_ref, created_by_algorithm, algorithm_version, config_sha256, code_sha256, valid_time_start, valid_time_end, source_time, accepted_at, ingested_at, available_at, created_at, hazard_class FROM data_products WHERE product_id=?",
        [product_id],
        |row| {
            Ok(DataProduct {
                product_id: row.get(0)?,
                product_type: row.get(1)?,
                schema_name: row.get(2)?,
                schema_version: row.get(3)?,
                cell_id: row.get(4)?,
                family_id: row.get(5)?,
                content_sha256: row.get(6)?,
                storage_kind: row.get(7)?,
                storage_ref: row.get(8)?,
                created_by_algorithm: row.get(9)?,
                algorithm_version: row.get(10)?,
                config_sha256: row.get(11)?,
                code_sha256: row.get(12)?,
                valid_time_start: row.get(13)?,
                valid_time_end: row.get(14)?,
                source_time: row.get(15)?,
                accepted_at: row.get(16)?,
                ingested_at: row.get(17)?,
                available_at: row.get(18)?,
                created_at: row.get(19)?,
                hazard_class: row.get(20)?,
            })
        },
    )
    .optional()
    .with_context(|| format!("loading product {product_id}"))
}

pub fn product_json(product: &DataProduct) -> Value {
    json!({
        "product_id": product.product_id,
        "product_type": product.product_type,
        "schema_name": product.schema_name,
        "schema_version": product.schema_version,
        "cell_id": product.cell_id,
        "family_id": product.family_id,
        "content_sha256": product.content_sha256,
        "storage_kind": product.storage_kind,
        "storage_ref": product.storage_ref,
        "created_by_algorithm": product.created_by_algorithm,
        "algorithm_version": product.algorithm_version,
        "config_sha256": product.config_sha256,
        "code_sha256": product.code_sha256,
        "valid_time_start": product.valid_time_start,
        "valid_time_end": product.valid_time_end,
        "source_time": product.source_time,
        "accepted_at": product.accepted_at,
        "ingested_at": product.ingested_at,
        "available_at": product.available_at,
        "created_at": product.created_at,
        "hazard_class": product.hazard_class
    })
}

pub fn print_product(conn: &Connection, product_id: &str) -> Result<()> {
    let Some(product) = load_product(conn, product_id)? else {
        anyhow::bail!("product {product_id} not found");
    };
    println!("{}", to_canonical_json(&product_json(&product))?);
    Ok(())
}

pub fn lineage_json(conn: &Connection, product_id: &str) -> Result<Value> {
    let mut seen = BTreeSet::<String>::new();
    let mut products = BTreeMap::<String, Value>::new();
    let mut edges = Vec::<Value>::new();
    walk_lineage(conn, product_id, &mut seen, &mut products, &mut edges)?;
    Ok(json!({
        "root_product_id": product_id,
        "products": products,
        "edges": edges
    }))
}

pub fn print_lineage(conn: &Connection, product_id: &str) -> Result<()> {
    let value = lineage_json(conn, product_id)?;
    println!("{}", to_canonical_json(&value)?);
    Ok(())
}

fn walk_lineage(
    conn: &Connection,
    product_id: &str,
    seen: &mut BTreeSet<String>,
    products: &mut BTreeMap<String, Value>,
    edges: &mut Vec<Value>,
) -> Result<()> {
    if !seen.insert(product_id.to_string()) {
        return Ok(());
    }
    let Some(product) = load_product(conn, product_id)? else {
        anyhow::bail!("product {product_id} not found");
    };
    products.insert(product_id.to_string(), product_json(&product));
    let mut stmt = conn.prepare(
        "SELECT parent_product_id, role FROM data_product_lineage WHERE child_product_id=? ORDER BY parent_product_id, role",
    )?;
    let rows = stmt.query_map([product_id], |row| {
        Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
    })?;
    for row in rows {
        let (parent_id, role) = row?;
        edges.push(json!({
            "child_product_id": product_id,
            "parent_product_id": parent_id,
            "role": role
        }));
        walk_lineage(conn, &parent_id, seen, products, edges)?;
    }
    Ok(())
}

pub fn output_product_ids_for_workflow_node(
    conn: &Connection,
    workflow_run_id: &str,
    node_id: &str,
) -> Result<Vec<String>> {
    let ids_json: String = conn.query_row(
        "SELECT output_product_ids_json FROM workflow_nodes WHERE workflow_run_id=? AND node_id=? ORDER BY finished_at DESC LIMIT 1",
        params![workflow_run_id, node_id],
        |row| row.get(0),
    )?;
    Ok(serde_json::from_str(&ids_json).unwrap_or_default())
}

pub fn latest_product_for_node_output(
    conn: &Connection,
    product_ids: &[String],
    product_type: &str,
) -> Result<Option<DataProduct>> {
    for product_id in product_ids {
        if let Some(product) = load_product(conn, product_id)? {
            if product.product_type == product_type {
                return Ok(Some(product));
            }
        }
    }
    Ok(None)
}
