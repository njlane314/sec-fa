use anyhow::Result;
use rusqlite::Connection;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use crate::fa_core::epoch_second;

use super::registry::{load_algorithm, load_product_type, load_resource, Algorithm};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InvariantViolation {
    pub invariant_id: String,
    pub subject: Value,
    pub severity: String,
    pub explanation: String,
}

impl InvariantViolation {
    pub fn error(id: &str, subject: Value, explanation: impl Into<String>) -> Self {
        Self {
            invariant_id: id.to_string(),
            subject,
            severity: "error".to_string(),
            explanation: explanation.into(),
        }
    }
}

pub fn is_valid_mode(mode: &str) -> bool {
    matches!(
        mode,
        "halted" | "observe" | "shadow" | "stage" | "paper" | "live_limited" | "live"
    )
}

pub fn can_stage(mode: &str) -> bool {
    matches!(mode, "stage" | "paper" | "live_limited" | "live")
}

pub fn can_submit(mode: &str) -> bool {
    matches!(mode, "paper" | "live_limited" | "live")
}

pub fn can_use_broker(mode: &str) -> bool {
    can_submit(mode)
}

pub fn can_run_external_write(mode: &str) -> bool {
    can_submit(mode)
}

pub fn authority_product_type(product_type: &str) -> bool {
    matches!(
        product_type,
        "order_intent_set.v1"
            | "risk_decision_set.v1"
            | "staged_order_set.v1"
            | "broker_event_set.v1"
            | "reconciliation_snapshot.v1"
    )
}

pub fn validate_algorithm_mode(
    conn: &Connection,
    algorithm: &Algorithm,
    mode: &str,
    allow_external_write: bool,
    allow_broker_resource: bool,
) -> Result<Vec<InvariantViolation>> {
    let mut violations = Vec::new();
    if algorithm.purity == "external_write"
        && (!can_run_external_write(mode) || !allow_external_write)
    {
        violations.push(InvariantViolation::error(
            "NO_EXTERNAL_WRITE_IN_OBSERVE_OR_SHADOW",
            json!({"algorithm_id": algorithm.algorithm_id, "mode": mode}),
            format!(
                "algorithm {} has external_write purity in mode={mode}",
                algorithm.algorithm_id
            ),
        ));
    }
    if algorithm.algorithm_id == "cmd.stage.v1" && !can_stage(mode) {
        violations.push(InvariantViolation::error(
            "STAGE_MODE",
            json!({"algorithm_id": algorithm.algorithm_id, "mode": mode}),
            format!(
                "algorithm {} cannot stage orders in mode={mode}",
                algorithm.algorithm_id
            ),
        ));
    }
    for resource_id in &algorithm.resources {
        let Some(resource) = load_resource(conn, resource_id)? else {
            violations.push(InvariantViolation::error(
                "UNKNOWN_RESOURCE",
                json!({"algorithm_id": algorithm.algorithm_id, "resource_id": resource_id}),
                format!(
                    "algorithm {} uses unknown resource {resource_id}",
                    algorithm.algorithm_id
                ),
            ));
            continue;
        };
        if !resource.allowed_modes.iter().any(|allowed| allowed == mode) {
            violations.push(InvariantViolation::error(
                "RESOURCE_MODE",
                json!({"algorithm_id": algorithm.algorithm_id, "resource_id": resource_id, "mode": mode}),
                format!("resource {resource_id} is not allowed in mode={mode}"),
            ));
        }
        if resource.resource_kind == "broker" && (!can_use_broker(mode) || !allow_broker_resource) {
            violations.push(InvariantViolation::error(
                "BROKER_RESOURCE_MODE",
                json!({"algorithm_id": algorithm.algorithm_id, "resource_id": resource_id, "mode": mode}),
                format!("broker resource {resource_id} is not allowed in mode={mode}"),
            ));
        }
    }
    Ok(violations)
}

pub fn validate_class_c_outputs(
    algorithm: &Algorithm,
    output_product_types: &[String],
) -> Vec<InvariantViolation> {
    let mut violations = Vec::new();
    if algorithm.hazard_class == "C" {
        for product_type in output_product_types {
            if authority_product_type(product_type) {
                violations.push(InvariantViolation::error(
                    "CLASS_C_CANNOT_CREATE_AUTHORITY",
                    json!({"algorithm_id": algorithm.algorithm_id, "product_type": product_type}),
                    format!(
                        "Class C algorithm {} cannot create authority product {product_type}",
                        algorithm.algorithm_id
                    ),
                ));
            }
        }
    }
    violations
}

pub fn validate_class_a_input_product(
    algorithm: &Algorithm,
    product_type: &str,
    product_hazard_class: &str,
) -> Option<InvariantViolation> {
    if algorithm.hazard_class == "A" && product_hazard_class == "C" {
        Some(InvariantViolation::error(
            "NO_CLASS_A_FROM_CLASS_C",
            json!({"algorithm_id": algorithm.algorithm_id, "product_type": product_type}),
            format!(
                "Class A algorithm {} cannot consume Class C product {product_type}",
                algorithm.algorithm_id
            ),
        ))
    } else {
        None
    }
}

pub fn validate_product_available_for_decision(
    product_id: &str,
    available_at: &str,
    decision_as_of: &str,
) -> Option<InvariantViolation> {
    let available = epoch_second(available_at);
    let decision = epoch_second(decision_as_of);
    if available > 0 && decision > 0 && available > decision {
        Some(InvariantViolation::error(
            "NO_LOOKAHEAD",
            json!({"product_id": product_id, "available_at": available_at, "decision_as_of": decision_as_of}),
            format!("product {product_id} became available after decision_as_of"),
        ))
    } else {
        None
    }
}

pub fn validate_runtime_input_product(
    conn: &Connection,
    algorithm_id: &str,
    product_id: &str,
    decision_as_of: &str,
) -> Result<Vec<InvariantViolation>> {
    let mut violations = Vec::new();
    let Some(algorithm) = load_algorithm(conn, algorithm_id)? else {
        violations.push(InvariantViolation::error(
            "UNKNOWN_ALGORITHM",
            json!({"algorithm_id": algorithm_id}),
            format!("unknown algorithm {algorithm_id}"),
        ));
        return Ok(violations);
    };
    let product = conn.query_row(
        "SELECT product_type, hazard_class, available_at FROM data_products WHERE product_id=?",
        [product_id],
        |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
            ))
        },
    )?;
    if let Some(v) = validate_product_available_for_decision(product_id, &product.2, decision_as_of)
    {
        violations.push(v);
    }
    if let Some(v) = validate_class_a_input_product(&algorithm, &product.0, &product.1) {
        violations.push(v);
    }
    Ok(violations)
}

pub fn hazard_class_for_product_type(
    conn: &Connection,
    product_type: &str,
) -> Result<Option<String>> {
    Ok(load_product_type(conn, product_type)?.map(|p| p.hazard_class))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn graph_rejects_lookahead() {
        let violation = validate_product_available_for_decision(
            "prod_test",
            "2026-05-08T14:00:00.000Z",
            "2026-05-08T13:00:00.000Z",
        );
        assert!(violation.is_some());
        assert_eq!(violation.unwrap().invariant_id, "NO_LOOKAHEAD");
    }
}
