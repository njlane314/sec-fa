use anyhow::{anyhow, bail, Result};
use rusqlite::{params, Connection};
use serde_json::{json, Value};

use super::graph::parse_workflow_ir_json;
use super::hash;
use super::registry::load_algorithm;
use super::storage::{
    latest_product_for_node_output, load_product, output_product_ids_for_workflow_node,
};

pub fn replay_workflow(conn: &Connection, workflow_run_id: &str) -> Result<Value> {
    let (spec_json, spec_sha256): (String, String) = conn.query_row(
        "SELECT workflow_spec_json, workflow_spec_sha256 FROM workflow_run_specs WHERE workflow_run_id=?",
        [workflow_run_id],
        |row| Ok((row.get(0)?, row.get(1)?)),
    )?;
    let parsed = parse_workflow_ir_json(&spec_json)?;
    if parsed.spec_sha256 != spec_sha256 {
        bail!("workflow spec hash mismatch for {workflow_run_id}");
    }
    let mut checked = Vec::<Value>::new();
    let mut skipped = Vec::<Value>::new();
    for node in &parsed.spec.nodes {
        let algorithm = load_algorithm(conn, &node.algorithm)?
            .ok_or_else(|| anyhow!("algorithm {} not found", node.algorithm))?;
        if !algorithm.deterministic {
            skipped.push(json!({
                "node_id": node.node_id,
                "algorithm_id": algorithm.algorithm_id,
                "reason": "algorithm_not_deterministic"
            }));
            continue;
        }
        let (node_status, input_product_ids) =
            workflow_node_record(conn, workflow_run_id, &node.node_id)?;
        if node_status != "succeeded" {
            bail!(
                "node {} cannot be replayed because status={node_status}",
                node.node_id
            );
        }
        if algorithm.executable_kind == "rust_operation" || algorithm.executable_kind == "command" {
            let output_ids =
                output_product_ids_for_workflow_node(conn, workflow_run_id, &node.node_id)?;
            let config_sha256 = hash::config_sha256(&node.config)?;
            let mut matched_products = Vec::<Value>::new();
            for product_id in &output_ids {
                let Some(product) = load_product(conn, product_id)? else {
                    bail!(
                        "node {} references missing product {product_id}",
                        node.node_id
                    );
                };
                if product.created_by_algorithm != algorithm.algorithm_id {
                    bail!(
                        "product {} was created by {}, not {}",
                        product.product_id,
                        product.created_by_algorithm,
                        algorithm.algorithm_id
                    );
                }
                if product.config_sha256 != config_sha256 {
                    bail!(
                        "product {} config hash mismatch: original={} replay={}",
                        product.product_id,
                        product.config_sha256,
                        config_sha256
                    );
                }
                let mut actual_parents = product_parent_ids(conn, &product.product_id)?;
                let mut expected_parents = input_product_ids.clone();
                actual_parents.sort();
                expected_parents.sort();
                if actual_parents != expected_parents {
                    bail!(
                        "product {} parent mismatch: original={:?} replay={:?}",
                        product.product_id,
                        actual_parents,
                        expected_parents
                    );
                }
                if product.product_type == "command_output.v1" {
                    let payload: Value = serde_json::from_str(&product.storage_ref)?;
                    let replay_content_sha256 = hash::content_sha256(&payload)?;
                    if replay_content_sha256 != product.content_sha256 {
                        bail!(
                            "command output hash mismatch for node {}: original={} replay={}",
                            node.node_id,
                            product.content_sha256,
                            replay_content_sha256
                        );
                    }
                    let exit_code = payload
                        .get("exit_code")
                        .and_then(Value::as_i64)
                        .unwrap_or(-1);
                    if exit_code != 0 {
                        bail!("node {} command output exit_code={exit_code}", node.node_id);
                    }
                }
                matched_products.push(json!({
                    "product_id": product.product_id,
                    "product_type": product.product_type,
                    "content_sha256": product.content_sha256
                }));
            }
            checked.push(json!({
                "node_id": node.node_id,
                "algorithm_id": algorithm.algorithm_id,
                "product_count": matched_products.len(),
                "products": matched_products,
                "status": "record_matched"
            }));
            continue;
        }
        if algorithm.executable_kind != "noop_test" {
            skipped.push(json!({
                "node_id": node.node_id,
                "algorithm_id": algorithm.algorithm_id,
                "reason": "executable_kind_not_replayable"
            }));
            continue;
        }
        let output_ids =
            output_product_ids_for_workflow_node(conn, workflow_run_id, &node.node_id)?;
        let Some(product) = latest_product_for_node_output(conn, &output_ids, "command_output.v1")?
        else {
            bail!(
                "noop node {} has no command_output.v1 product",
                node.node_id
            );
        };
        let config_sha256 = hash::config_sha256(&node.config)?;
        let payload = json!({
            "algorithm": algorithm.algorithm_id,
            "config_sha256": config_sha256,
            "message": node.config.get("message").and_then(Value::as_str).unwrap_or_default()
        });
        let replay_content_sha256 = hash::content_sha256(&payload)?;
        if replay_content_sha256 != product.content_sha256 {
            bail!(
                "replay hash mismatch for node {}: original={} replay={}",
                node.node_id,
                product.content_sha256,
                replay_content_sha256
            );
        }
        checked.push(json!({
            "node_id": node.node_id,
            "algorithm_id": algorithm.algorithm_id,
            "product_id": product.product_id,
            "content_sha256": product.content_sha256,
            "status": "matched"
        }));
    }
    Ok(json!({
        "event": "replay_succeeded",
        "workflow_run_id": workflow_run_id,
        "workflow_name": parsed.spec.workflow_name,
        "checked": checked,
        "skipped": skipped
    }))
}

fn workflow_node_record(
    conn: &Connection,
    workflow_run_id: &str,
    node_id: &str,
) -> Result<(String, Vec<String>)> {
    let (status, input_ids_json): (String, String) = conn.query_row(
        "SELECT status, input_product_ids_json FROM workflow_nodes WHERE workflow_run_id=? AND node_id=? ORDER BY started_at DESC LIMIT 1",
        params![workflow_run_id, node_id],
        |row| Ok((row.get(0)?, row.get(1)?)),
    )?;
    Ok((status, serde_json::from_str(&input_ids_json)?))
}

fn product_parent_ids(conn: &Connection, product_id: &str) -> Result<Vec<String>> {
    let mut stmt = conn.prepare(
        "SELECT parent_product_id FROM data_product_lineage WHERE child_product_id=? ORDER BY parent_product_id",
    )?;
    let rows = stmt.query_map([product_id], |row| row.get::<_, String>(0))?;
    let mut ids = Vec::new();
    for row in rows {
        ids.push(row?);
    }
    Ok(ids)
}

#[allow(dead_code)]
pub fn deterministic_node_ids(conn: &Connection, workflow_run_id: &str) -> Result<Vec<String>> {
    let mut stmt = conn.prepare(
        "SELECT node_id FROM workflow_nodes WHERE workflow_run_id=? ORDER BY started_at, node_id",
    )?;
    let rows = stmt.query_map(params![workflow_run_id], |row| row.get::<_, String>(0))?;
    let mut out = Vec::new();
    for row in rows {
        out.push(row?);
    }
    Ok(out)
}
