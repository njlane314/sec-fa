use std::collections::{BTreeMap, BTreeSet, VecDeque};
use std::fs;
use std::path::Path;

use anyhow::{Context as _, Result};
use rusqlite::Connection;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use super::canonical_json::to_canonical_json;
use super::hash;
use super::invariants::{
    hazard_class_for_product_type, is_valid_mode, validate_algorithm_mode,
    validate_class_a_input_product, validate_class_c_outputs, InvariantViolation,
};
use super::registry::{load_algorithm, load_product_type, Algorithm};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WorkflowSpec {
    pub spec_version: u32,
    pub workflow_name: String,
    pub driver: DriverSpec,
    pub layers: Vec<LayerSpec>,
    pub resources: Vec<ResourceSpec>,
    pub nodes: Vec<NodeSpec>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DriverSpec {
    pub driver_id: String,
    pub mode: String,
    pub decision_as_of: String,
    pub allow_external_write: bool,
    pub allow_broker_resource: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LayerSpec {
    pub layer: String,
    pub parent: Option<String>,
    pub key_fields: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResourceSpec {
    pub resource_id: String,
    pub resource_kind: String,
    pub max_concurrent: i64,
    pub min_interval_ms: i64,
    pub allowed_modes: Vec<String>,
    pub hazard_class: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NodeSpec {
    pub node_id: String,
    pub algorithm: String,
    pub cell: CellSpec,
    pub config: Value,
    pub inputs: Vec<NodeInputSpec>,
    pub outputs: Vec<NodeOutputSpec>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CellSpec {
    pub layer: String,
    pub key: Value,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NodeInputSpec {
    pub from: String,
    pub role: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NodeOutputSpec {
    pub role: String,
    pub product_type: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct WorkflowEdge {
    pub from_node_id: String,
    pub to_node_id: String,
    pub product_role: String,
}

#[derive(Debug, Clone)]
pub struct ParsedWorkflow {
    pub spec: WorkflowSpec,
    pub canonical_json: String,
    pub spec_sha256: String,
}

#[derive(Debug, Clone)]
pub struct ValidatedWorkflow {
    pub parsed: ParsedWorkflow,
    pub order: Vec<String>,
    pub edges: Vec<WorkflowEdge>,
}

#[derive(Debug, Clone, Serialize)]
pub struct ValidationReport {
    pub event: String,
    pub workflow_name: String,
    pub node_count: usize,
    pub edge_count: usize,
    pub topological_order: Vec<String>,
    pub violations: Vec<InvariantViolation>,
}

pub fn load_workflow_spec(path: &Path) -> Result<ParsedWorkflow> {
    let text = fs::read_to_string(path)
        .with_context(|| format!("reading workflow spec {}", path.display()))?;
    parse_workflow_spec(&text)
}

pub fn parse_workflow_spec(text: &str) -> Result<ParsedWorkflow> {
    let value: Value = serde_json::from_str(text)?;
    let canonical_json = to_canonical_json(&value)?;
    let spec: WorkflowSpec = serde_json::from_value(value)?;
    let spec_sha256 = hash::sha256_hex(canonical_json.as_bytes());
    Ok(ParsedWorkflow {
        spec,
        canonical_json,
        spec_sha256,
    })
}

pub fn validate_workflow(
    conn: &Connection,
    parsed: ParsedWorkflow,
) -> Result<(ValidationReport, Option<ValidatedWorkflow>)> {
    let spec = &parsed.spec;
    let mut violations = Vec::new();

    if spec.spec_version != 1 {
        violations.push(InvariantViolation::error(
            "UNSUPPORTED_SPEC_VERSION",
            json!({"spec_version": spec.spec_version}),
            "workflow spec_version must be 1",
        ));
    }
    if spec.workflow_name.trim().is_empty() {
        violations.push(InvariantViolation::error(
            "WORKFLOW_NAME_REQUIRED",
            json!({}),
            "workflow_name is required",
        ));
    }
    if !is_valid_mode(&spec.driver.mode) {
        violations.push(InvariantViolation::error(
            "INVALID_MODE",
            json!({"mode": spec.driver.mode}),
            format!("invalid workflow mode {:?}", spec.driver.mode),
        ));
    }

    let layers = collect_layers(spec, &mut violations);
    let nodes = collect_nodes(spec, &mut violations);
    let output_index = collect_outputs(spec, &mut violations);
    validate_layer_references(spec, &layers, &mut violations);

    let edges = collect_edges(spec, &nodes, &output_index, &mut violations);
    let order = match topological_order(&spec.nodes, &edges) {
        Ok(order) => order,
        Err(v) => {
            violations.push(v);
            Vec::new()
        }
    };

    validate_nodes(
        conn,
        spec,
        &layers,
        &output_index,
        &mut violations,
    )?;

    let report = ValidationReport {
        event: if violations.iter().any(|v| v.severity == "error") {
            "workflow_rejected".to_string()
        } else {
            "workflow_validated".to_string()
        },
        workflow_name: spec.workflow_name.clone(),
        node_count: spec.nodes.len(),
        edge_count: edges.len(),
        topological_order: order.clone(),
        violations: violations.clone(),
    };

    let valid = if violations.iter().any(|v| v.severity == "error") {
        None
    } else {
        Some(ValidatedWorkflow {
            parsed,
            order,
            edges,
        })
    };
    Ok((report, valid))
}

pub fn node_by_id<'a>(spec: &'a WorkflowSpec, node_id: &str) -> Option<&'a NodeSpec> {
    spec.nodes.iter().find(|node| node.node_id == node_id)
}

pub fn output_product_types(node: &NodeSpec) -> Vec<String> {
    node.outputs
        .iter()
        .map(|output| output.product_type.clone())
        .collect()
}

pub fn cell_id(node: &NodeSpec) -> Result<String> {
    hash::cell_id(&node.cell.layer, &node.cell.key)
}

pub fn config_hash(node: &NodeSpec) -> Result<String> {
    hash::config_sha256(&node.config)
}

fn collect_layers(spec: &WorkflowSpec, violations: &mut Vec<InvariantViolation>) -> BTreeSet<String> {
    let mut layers = BTreeSet::new();
    for layer in &spec.layers {
        if layer.layer.trim().is_empty() {
            violations.push(InvariantViolation::error(
                "LAYER_ID_REQUIRED",
                json!({}),
                "layer id is required",
            ));
        }
        if !layers.insert(layer.layer.clone()) {
            violations.push(InvariantViolation::error(
                "DUPLICATE_LAYER",
                json!({"layer": layer.layer}),
                format!("duplicate layer {}", layer.layer),
            ));
        }
    }
    layers
}

fn collect_nodes<'a>(
    spec: &'a WorkflowSpec,
    violations: &mut Vec<InvariantViolation>,
) -> BTreeMap<String, &'a NodeSpec> {
    let mut nodes = BTreeMap::new();
    for node in &spec.nodes {
        if node.node_id.trim().is_empty() {
            violations.push(InvariantViolation::error(
                "NODE_ID_REQUIRED",
                json!({}),
                "node_id is required",
            ));
        }
        if nodes.insert(node.node_id.clone(), node).is_some() {
            violations.push(InvariantViolation::error(
                "DUPLICATE_NODE",
                json!({"node_id": node.node_id}),
                format!("duplicate node {}", node.node_id),
            ));
        }
    }
    nodes
}

fn collect_outputs(
    spec: &WorkflowSpec,
    violations: &mut Vec<InvariantViolation>,
) -> BTreeMap<(String, String), String> {
    let mut outputs = BTreeMap::new();
    for node in &spec.nodes {
        let mut roles = BTreeSet::new();
        for output in &node.outputs {
            if !roles.insert(output.role.clone()) {
                violations.push(InvariantViolation::error(
                    "DUPLICATE_OUTPUT_ROLE",
                    json!({"node_id": node.node_id, "role": output.role}),
                    format!("duplicate output role {} on node {}", output.role, node.node_id),
                ));
            }
            outputs.insert(
                (node.node_id.clone(), output.role.clone()),
                output.product_type.clone(),
            );
        }
    }
    outputs
}

fn validate_layer_references(
    spec: &WorkflowSpec,
    layers: &BTreeSet<String>,
    violations: &mut Vec<InvariantViolation>,
) {
    for layer in &spec.layers {
        if let Some(parent) = &layer.parent {
            if !layers.contains(parent) {
                violations.push(InvariantViolation::error(
                    "UNKNOWN_PARENT_LAYER",
                    json!({"layer": layer.layer, "parent": parent}),
                    format!("layer {} references unknown parent {parent}", layer.layer),
                ));
            }
        }
    }
}

fn collect_edges(
    spec: &WorkflowSpec,
    nodes: &BTreeMap<String, &NodeSpec>,
    outputs: &BTreeMap<(String, String), String>,
    violations: &mut Vec<InvariantViolation>,
) -> Vec<WorkflowEdge> {
    let mut edges = Vec::new();
    for node in &spec.nodes {
        for input in &node.inputs {
            if !nodes.contains_key(&input.from) {
                violations.push(InvariantViolation::error(
                    "UNKNOWN_INPUT_NODE",
                    json!({"node_id": node.node_id, "from": input.from}),
                    format!("node {} references unknown input node {}", node.node_id, input.from),
                ));
                continue;
            }
            if !outputs.contains_key(&(input.from.clone(), input.role.clone())) {
                violations.push(InvariantViolation::error(
                    "UNKNOWN_INPUT_ROLE",
                    json!({"node_id": node.node_id, "from": input.from, "role": input.role}),
                    format!(
                        "node {} references missing role {} from {}",
                        node.node_id, input.role, input.from
                    ),
                ));
                continue;
            }
            edges.push(WorkflowEdge {
                from_node_id: input.from.clone(),
                to_node_id: node.node_id.clone(),
                product_role: input.role.clone(),
            });
        }
    }
    edges
}

fn validate_nodes(
    conn: &Connection,
    spec: &WorkflowSpec,
    layers: &BTreeSet<String>,
    outputs: &BTreeMap<(String, String), String>,
    violations: &mut Vec<InvariantViolation>,
) -> Result<()> {
    for node in &spec.nodes {
        if !layers.contains(&node.cell.layer) {
            violations.push(InvariantViolation::error(
                "UNKNOWN_CELL_LAYER",
                json!({"node_id": node.node_id, "layer": node.cell.layer}),
                format!("node {} uses unknown cell layer {}", node.node_id, node.cell.layer),
            ));
        }
        let Some(algorithm) = load_algorithm(conn, &node.algorithm)? else {
            violations.push(InvariantViolation::error(
                "UNKNOWN_ALGORITHM",
                json!({"node_id": node.node_id, "algorithm_id": node.algorithm}),
                format!("node {} uses unknown algorithm {}", node.node_id, node.algorithm),
            ));
            continue;
        };
        validate_algorithm_contract(conn, node, &algorithm, violations)?;
        violations.extend(validate_algorithm_mode(
            conn,
            &algorithm,
            &spec.driver.mode,
            spec.driver.allow_external_write,
            spec.driver.allow_broker_resource,
        )?);
        violations.extend(validate_class_c_outputs(&algorithm, &output_product_types(node)));

        for input in &node.inputs {
            if let Some(product_type) = outputs.get(&(input.from.clone(), input.role.clone())) {
                if let Some(hazard_class) = hazard_class_for_product_type(conn, product_type)? {
                    if let Some(v) =
                        validate_class_a_input_product(&algorithm, product_type, &hazard_class)
                    {
                        violations.push(v);
                    }
                }
            }
        }
    }
    Ok(())
}

fn validate_algorithm_contract(
    conn: &Connection,
    node: &NodeSpec,
    algorithm: &Algorithm,
    violations: &mut Vec<InvariantViolation>,
) -> Result<()> {
    for output in &node.outputs {
        if load_product_type(conn, &output.product_type)?.is_none() {
            violations.push(InvariantViolation::error(
                "UNKNOWN_PRODUCT_TYPE",
                json!({"node_id": node.node_id, "product_type": output.product_type}),
                format!("node {} declares unknown product type {}", node.node_id, output.product_type),
            ));
        }
        if !algorithm.outputs.iter().any(|allowed| allowed == &output.product_type) {
            violations.push(InvariantViolation::error(
                "OUTPUT_CONTRACT_MISMATCH",
                json!({"node_id": node.node_id, "algorithm_id": algorithm.algorithm_id, "product_type": output.product_type}),
                format!(
                    "algorithm {} does not declare output {}",
                    algorithm.algorithm_id, output.product_type
                ),
            ));
        }
    }
    Ok(())
}

pub fn topological_order(
    nodes: &[NodeSpec],
    edges: &[WorkflowEdge],
) -> std::result::Result<Vec<String>, InvariantViolation> {
    let mut indegree = BTreeMap::<String, usize>::new();
    let mut children = BTreeMap::<String, Vec<String>>::new();
    for node in nodes {
        indegree.insert(node.node_id.clone(), 0);
    }
    for edge in edges {
        *indegree.entry(edge.to_node_id.clone()).or_insert(0) += 1;
        children
            .entry(edge.from_node_id.clone())
            .or_default()
            .push(edge.to_node_id.clone());
    }
    let mut queue = indegree
        .iter()
        .filter_map(|(node_id, degree)| (*degree == 0).then_some(node_id.clone()))
        .collect::<VecDeque<_>>();
    let mut order = Vec::new();
    while let Some(node_id) = queue.pop_front() {
        order.push(node_id.clone());
        for child in children.get(&node_id).into_iter().flatten() {
            let Some(degree) = indegree.get_mut(child) else {
                continue;
            };
            *degree = degree.saturating_sub(1);
            if *degree == 0 {
                queue.push_back(child.clone());
            }
        }
    }
    if order.len() != nodes.len() {
        Err(InvariantViolation::error(
            "GRAPH_MUST_BE_ACYCLIC",
            json!({}),
            "workflow graph contains a directed cycle",
        ))
    } else {
        Ok(order)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::framework::registry::seed_product_graph_reference_data;

    fn memory_conn() -> Connection {
        let conn = Connection::open_in_memory().unwrap();
        conn.execute_batch(include_str!("../../../schema.sql")).unwrap();
        seed_product_graph_reference_data(&conn).unwrap();
        conn
    }

    #[test]
    fn graph_rejects_cycle() {
        let text = r#"{
          "spec_version": 1,
          "workflow_name": "cycle",
          "driver": {"driver_id":"test","mode":"observe","decision_as_of":"2026-05-08T13:00:00.000Z","allow_external_write":false,"allow_broker_resource":false},
          "layers": [{"layer":"job","parent":null,"key_fields":["id"]}],
          "resources": [],
          "nodes": [
            {"node_id":"a","algorithm":"noop.test.v1","cell":{"layer":"job","key":{"id":"a"}},"config":{},"inputs":[{"from":"b","role":"output"}],"outputs":[{"role":"output","product_type":"command_output.v1"}]},
            {"node_id":"b","algorithm":"noop.test.v1","cell":{"layer":"job","key":{"id":"b"}},"config":{},"inputs":[{"from":"a","role":"output"}],"outputs":[{"role":"output","product_type":"command_output.v1"}]}
          ]
        }"#;
        let conn = memory_conn();
        let (report, valid) = validate_workflow(&conn, parse_workflow_spec(text).unwrap()).unwrap();
        assert!(valid.is_none());
        assert!(report
            .violations
            .iter()
            .any(|v| v.invariant_id == "GRAPH_MUST_BE_ACYCLIC"));
    }

    #[test]
    fn graph_rejects_class_a_consuming_class_c() {
        let text = r#"{
          "spec_version": 1,
          "workflow_name": "class",
          "driver": {"driver_id":"test","mode":"shadow","decision_as_of":"2026-05-08T13:00:00.000Z","allow_external_write":false,"allow_broker_resource":false},
          "layers": [{"layer":"job","parent":null,"key_fields":["id"]}],
          "resources": [],
          "nodes": [
            {"node_id":"report","algorithm":"cmd.report.daily.v1","cell":{"layer":"job","key":{"id":"r"}},"config":{"date":"today"},"inputs":[],"outputs":[{"role":"report","product_type":"report.v1"}]},
            {"node_id":"gate","algorithm":"cmd.gate.v1","cell":{"layer":"job","key":{"id":"g"}},"config":{"core_lib":"build/libfolio.so"},"inputs":[{"from":"report","role":"report"}],"outputs":[{"role":"risk_decisions","product_type":"risk_decision_set.v1"}]}
          ]
        }"#;
        let conn = memory_conn();
        let (report, valid) = validate_workflow(&conn, parse_workflow_spec(text).unwrap()).unwrap();
        assert!(valid.is_none());
        assert!(report
            .violations
            .iter()
            .any(|v| v.invariant_id == "NO_CLASS_A_FROM_CLASS_C"));
    }

    #[test]
    fn graph_rejects_broker_in_shadow() {
        let text = r#"{
          "spec_version": 1,
          "workflow_name": "broker_shadow",
          "driver": {"driver_id":"test","mode":"shadow","decision_as_of":"2026-05-08T13:00:00.000Z","allow_external_write":false,"allow_broker_resource":false},
          "layers": [{"layer":"job","parent":null,"key_fields":["id"]}],
          "resources": [],
          "nodes": [
            {"node_id":"send","algorithm":"cmd.send.mock.v1","cell":{"layer":"job","key":{"id":"s"}},"config":{"max_reconciliation_age_s":3600},"inputs":[],"outputs":[{"role":"broker_events","product_type":"broker_event_set.v1"}]}
          ]
        }"#;
        let conn = memory_conn();
        let (report, valid) = validate_workflow(&conn, parse_workflow_spec(text).unwrap()).unwrap();
        assert!(valid.is_none());
        assert!(report
            .violations
            .iter()
            .any(|v| v.invariant_id == "BROKER_RESOURCE_MODE"));
    }
}
