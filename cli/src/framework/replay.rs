use anyhow::{anyhow, bail, Result};
use rusqlite::{params, Connection};
use serde_json::{json, Value};

use super::graph::parse_workflow_spec;
use super::hash;
use super::registry::load_algorithm;
use super::storage::{latest_product_for_node_output, output_product_ids_for_workflow_node};

pub fn replay_workflow(conn: &Connection, workflow_run_id: &str) -> Result<Value> {
    let (spec_json, spec_sha256): (String, String) = conn.query_row(
        "SELECT workflow_spec_json, workflow_spec_sha256 FROM workflow_run_specs WHERE workflow_run_id=?",
        [workflow_run_id],
        |row| Ok((row.get(0)?, row.get(1)?)),
    )?;
    let parsed = parse_workflow_spec(&spec_json)?;
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
        if algorithm.executable_kind != "noop_test" {
            skipped.push(json!({
                "node_id": node.node_id,
                "algorithm_id": algorithm.algorithm_id,
                "reason": "deterministic_command_replay_not_implemented"
            }));
            continue;
        }
        let output_ids = output_product_ids_for_workflow_node(conn, workflow_run_id, &node.node_id)?;
        let Some(product) = latest_product_for_node_output(conn, &output_ids, "command_output.v1")?
        else {
            bail!("noop node {} has no command_output.v1 product", node.node_id);
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
