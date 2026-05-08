use anyhow::Result;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};

use super::canonical_json::to_canonical_json;

pub fn sha256_hex(data: impl AsRef<[u8]>) -> String {
    let mut h = Sha256::new();
    h.update(data.as_ref());
    hex::encode(h.finalize())
}

pub fn canonical_sha256(value: &Value) -> Result<String> {
    Ok(sha256_hex(to_canonical_json(value)?.as_bytes()))
}

pub fn content_sha256(product_payload: &Value) -> Result<String> {
    canonical_sha256(product_payload)
}

pub fn config_sha256(config_json: &Value) -> Result<String> {
    canonical_sha256(config_json)
}

#[allow(clippy::too_many_arguments)]
pub fn product_id(
    product_type: &str,
    schema_name: &str,
    schema_version: i64,
    cell_id: &str,
    family_id: Option<&str>,
    content_sha256: &str,
    algorithm_id: &str,
    algorithm_version: &str,
    config_sha256: &str,
    parent_product_ids: &[String],
    available_at: &str,
) -> Result<String> {
    let mut parents = parent_product_ids.to_vec();
    parents.sort();
    let payload = json!({
        "product_type": product_type,
        "schema_name": schema_name,
        "schema_version": schema_version,
        "cell_id": cell_id,
        "family_id": family_id,
        "content_sha256": content_sha256,
        "created_by_algorithm": algorithm_id,
        "algorithm_version": algorithm_version,
        "config_sha256": config_sha256,
        "parents": parents,
        "available_at": available_at
    });
    Ok(format!("prod_{}", &canonical_sha256(&payload)?[..48]))
}

pub fn workflow_run_id(
    workflow_name: &str,
    workflow_spec_sha256: &str,
    mode: &str,
    decision_as_of: &str,
    created_at: &str,
) -> Result<String> {
    let payload = json!({
        "workflow_name": workflow_name,
        "workflow_spec_sha256": workflow_spec_sha256,
        "mode": mode,
        "decision_as_of": decision_as_of,
        "created_at": created_at
    });
    Ok(format!("wf_{}", &canonical_sha256(&payload)?[..48]))
}

pub fn node_run_id(
    workflow_run_id: &str,
    node_id: &str,
    cell_id: &str,
    algorithm_id: &str,
    config_sha256: &str,
    input_product_ids: &[String],
) -> Result<String> {
    let mut inputs = input_product_ids.to_vec();
    inputs.sort();
    let payload = json!({
        "workflow_run_id": workflow_run_id,
        "node_id": node_id,
        "cell_id": cell_id,
        "algorithm_id": algorithm_id,
        "config_sha256": config_sha256,
        "input_product_ids": inputs
    });
    Ok(format!("node_{}", &canonical_sha256(&payload)?[..48]))
}

pub fn cell_id(layer: &str, key: &Value) -> Result<String> {
    Ok(format!(
        "{}:{}",
        layer,
        &canonical_sha256(key)?[..24]
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn product_id_is_deterministic() {
        let parents = vec!["prod_a".to_string(), "prod_b".to_string()];
        let left = product_id(
            "command_output.v1",
            "command_output",
            1,
            "job:abc",
            None,
            "a".repeat(64).as_str(),
            "noop.test.v1",
            "1",
            "b".repeat(64).as_str(),
            &parents,
            "2026-05-08T13:00:00.000Z",
        )
        .unwrap();
        let right = product_id(
            "command_output.v1",
            "command_output",
            1,
            "job:abc",
            None,
            "a".repeat(64).as_str(),
            "noop.test.v1",
            "1",
            "b".repeat(64).as_str(),
            &parents.iter().rev().cloned().collect::<Vec<_>>(),
            "2026-05-08T13:00:00.000Z",
        )
        .unwrap();
        assert_eq!(left, right);
    }

    #[test]
    fn product_id_changes_when_parent_changes() {
        let left = product_id(
            "command_output.v1",
            "command_output",
            1,
            "job:abc",
            None,
            "a".repeat(64).as_str(),
            "noop.test.v1",
            "1",
            "b".repeat(64).as_str(),
            &["prod_a".to_string()],
            "2026-05-08T13:00:00.000Z",
        )
        .unwrap();
        let right = product_id(
            "command_output.v1",
            "command_output",
            1,
            "job:abc",
            None,
            "a".repeat(64).as_str(),
            "noop.test.v1",
            "1",
            "b".repeat(64).as_str(),
            &["prod_b".to_string()],
            "2026-05-08T13:00:00.000Z",
        )
        .unwrap();
        assert_ne!(left, right);
    }
}
