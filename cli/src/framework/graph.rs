use std::collections::{BTreeMap, BTreeSet, VecDeque};
use std::fs;
use std::path::Path;

use anyhow::{bail, Context as _, Result};
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
    if path.extension().and_then(|ext| ext.to_str()) != Some("flow") {
        bail!("workflow specs must use the .flow format");
    }
    let text = fs::read_to_string(path)
        .with_context(|| format!("reading workflow spec {}", path.display()))?;
    parse_workflow_spec(&text)
}

pub fn parse_workflow_spec(text: &str) -> Result<ParsedWorkflow> {
    let spec = parse_flow_workflow(text)?;
    parsed_from_spec(spec)
}

pub fn parse_workflow_ir_json(text: &str) -> Result<ParsedWorkflow> {
    let value: Value = serde_json::from_str(text)?;
    let spec: WorkflowSpec = serde_json::from_value(value)?;
    parsed_from_spec(spec)
}

fn parsed_from_spec(spec: WorkflowSpec) -> Result<ParsedWorkflow> {
    let canonical_json = to_canonical_json(&spec)?;
    let spec_sha256 = hash::sha256_hex(canonical_json.as_bytes());
    Ok(ParsedWorkflow {
        spec,
        canonical_json,
        spec_sha256,
    })
}

#[derive(Debug)]
struct NodeBuilder {
    node_id: String,
    algorithm: Option<String>,
    cell: Option<CellSpec>,
    config: serde_json::Map<String, Value>,
    inputs: Vec<NodeInputSpec>,
    outputs: Vec<NodeOutputSpec>,
}

fn parse_flow_workflow(text: &str) -> Result<WorkflowSpec> {
    let mut workflow_name = None::<String>;
    let mut driver_id = None::<String>;
    let mut mode = None::<String>;
    let mut decision_as_of = None::<String>;
    let mut allow_external_write = false;
    let mut allow_broker_resource = false;
    let mut layers = Vec::<LayerSpec>::new();
    let mut resources = Vec::<ResourceSpec>::new();
    let mut nodes = Vec::<NodeSpec>::new();
    let mut current_node = None::<NodeBuilder>;

    for (index, raw_line) in text.lines().enumerate() {
        let line_no = index + 1;
        let Some(line) = strip_comment(raw_line)? else {
            continue;
        };
        let tokens = split_words(&line).with_context(|| format!("line {line_no}"))?;
        if tokens.is_empty() {
            continue;
        }

        if let Some(node) = current_node.as_mut() {
            match tokens[0].as_str() {
                "alg" | "algorithm" => {
                    require_len(&tokens, 2, line_no)?;
                    node.algorithm = Some(tokens[1].clone());
                }
                "cell" => {
                    if tokens.len() < 2 {
                        bail!("line {line_no}: cell requires a layer");
                    }
                    let mut key = serde_json::Map::<String, Value>::new();
                    for token in &tokens[2..] {
                        let (field, value) = parse_key_value(token, line_no)?;
                        key.insert(field.to_string(), parse_flow_value(value)?);
                    }
                    node.cell = Some(CellSpec {
                        layer: tokens[1].clone(),
                        key: Value::Object(key),
                    });
                }
                "input" => {
                    if tokens.len() != 4 || tokens[2] != "from" {
                        bail!("line {line_no}: input syntax is `input <role> from <node>.<role>`");
                    }
                    let (from_node, from_role) = parse_node_role(&tokens[3], line_no)?;
                    if from_role != tokens[1] {
                        bail!("line {line_no}: input role must match source role");
                    }
                    node.inputs.push(NodeInputSpec {
                        from: from_node.to_string(),
                        role: from_role.to_string(),
                    });
                }
                "set" => {
                    if tokens.len() != 3 {
                        bail!("line {line_no}: set syntax is `set <key> <value>`");
                    }
                    node.config
                        .insert(tokens[1].clone(), parse_flow_value(&tokens[2])?);
                }
                "output" => {
                    if tokens.len() != 3 {
                        bail!("line {line_no}: output syntax is `output <role> <product_type>`");
                    }
                    node.outputs.push(NodeOutputSpec {
                        role: tokens[1].clone(),
                        product_type: tokens[2].clone(),
                    });
                }
                "end" => {
                    if tokens.len() != 1 {
                        bail!("line {line_no}: end takes no arguments");
                    }
                    let node = current_node.take().expect("node exists");
                    nodes.push(NodeSpec {
                        node_id: node.node_id,
                        algorithm: node.algorithm.context("node is missing an alg line")?,
                        cell: node.cell.context("node is missing a cell line")?,
                        config: Value::Object(node.config),
                        inputs: node.inputs,
                        outputs: node.outputs,
                    });
                }
                _ => bail!("line {line_no}: unknown node directive `{}`", tokens[0]),
            }
            continue;
        }

        match tokens[0].as_str() {
            "workflow" => {
                require_len(&tokens, 2, line_no)?;
                workflow_name = Some(tokens[1].clone());
            }
            "driver" => {
                require_len(&tokens, 2, line_no)?;
                driver_id = Some(tokens[1].clone());
            }
            "mode" => {
                require_len(&tokens, 2, line_no)?;
                mode = Some(tokens[1].clone());
            }
            "decision_as_of" => {
                require_len(&tokens, 2, line_no)?;
                decision_as_of = Some(tokens[1].clone());
            }
            "allow_external_write" => {
                require_len(&tokens, 2, line_no)?;
                allow_external_write = parse_bool(&tokens[1], line_no)?;
            }
            "allow_broker_resource" => {
                require_len(&tokens, 2, line_no)?;
                allow_broker_resource = parse_bool(&tokens[1], line_no)?;
            }
            "layer" => layers.push(parse_layer(&tokens, line_no)?),
            "resource" => resources.push(parse_resource(&tokens, line_no)?),
            "node" => {
                require_len(&tokens, 2, line_no)?;
                current_node = Some(NodeBuilder {
                    node_id: tokens[1].clone(),
                    algorithm: None,
                    cell: None,
                    config: serde_json::Map::new(),
                    inputs: Vec::new(),
                    outputs: Vec::new(),
                });
            }
            _ => bail!("line {line_no}: unknown workflow directive `{}`", tokens[0]),
        }
    }

    if current_node.is_some() {
        bail!("workflow ended before current node was closed with `end`");
    }

    Ok(WorkflowSpec {
        spec_version: 1,
        workflow_name: workflow_name.context("workflow line is required")?,
        driver: DriverSpec {
            driver_id: driver_id.context("driver line is required")?,
            mode: mode.context("mode line is required")?,
            decision_as_of: decision_as_of.context("decision_as_of line is required")?,
            allow_external_write,
            allow_broker_resource,
        },
        layers,
        resources,
        nodes,
    })
}

fn strip_comment(raw: &str) -> Result<Option<String>> {
    let mut out = String::new();
    let mut quote = None::<char>;
    let mut escaped = false;
    for ch in raw.chars() {
        if escaped {
            out.push(ch);
            escaped = false;
            continue;
        }
        if ch == '\\' && quote.is_some() {
            out.push(ch);
            escaped = true;
            continue;
        }
        if let Some(q) = quote {
            out.push(ch);
            if ch == q {
                quote = None;
            }
            continue;
        }
        if ch == '"' || ch == '\'' {
            quote = Some(ch);
            out.push(ch);
            continue;
        }
        if ch == '#' {
            break;
        }
        out.push(ch);
    }
    if quote.is_some() {
        bail!("unterminated quoted string");
    }
    let trimmed = out.trim();
    if trimmed.is_empty() {
        Ok(None)
    } else {
        Ok(Some(trimmed.to_string()))
    }
}

fn split_words(line: &str) -> Result<Vec<String>> {
    let mut words = Vec::<String>::new();
    let mut current = String::new();
    let mut quote = None::<char>;
    let mut escaped = false;
    for ch in line.chars() {
        if escaped {
            current.push(ch);
            escaped = false;
            continue;
        }
        if ch == '\\' && quote.is_some() {
            escaped = true;
            continue;
        }
        if let Some(q) = quote {
            if ch == q {
                quote = None;
            } else {
                current.push(ch);
            }
            continue;
        }
        if ch == '"' || ch == '\'' {
            quote = Some(ch);
            continue;
        }
        if ch.is_whitespace() {
            if !current.is_empty() {
                words.push(std::mem::take(&mut current));
            }
            continue;
        }
        current.push(ch);
    }
    if quote.is_some() {
        bail!("unterminated quoted string");
    }
    if !current.is_empty() {
        words.push(current);
    }
    Ok(words)
}

fn require_len(tokens: &[String], len: usize, line_no: usize) -> Result<()> {
    if tokens.len() != len {
        bail!(
            "line {line_no}: `{}` expects {} argument(s)",
            tokens[0],
            len.saturating_sub(1)
        );
    }
    Ok(())
}

fn parse_layer(tokens: &[String], line_no: usize) -> Result<LayerSpec> {
    if tokens.len() < 4 {
        bail!("line {line_no}: layer syntax is `layer <name> [parent <name>] key <fields...>`");
    }
    let layer = tokens[1].clone();
    let mut parent = None::<String>;
    let mut index = 2usize;
    if tokens.get(index).map(String::as_str) == Some("parent") {
        let value = tokens
            .get(index + 1)
            .context("layer parent requires a value")?;
        parent = Some(value.clone());
        index += 2;
    }
    if tokens.get(index).map(String::as_str) != Some("key") {
        bail!("line {line_no}: layer requires a key field list");
    }
    let key_fields = tokens[index + 1..].to_vec();
    if key_fields.is_empty() {
        bail!("line {line_no}: layer key list must not be empty");
    }
    Ok(LayerSpec {
        layer,
        parent,
        key_fields,
    })
}

fn parse_resource(tokens: &[String], line_no: usize) -> Result<ResourceSpec> {
    if tokens.len() < 3 {
        bail!("line {line_no}: resource syntax is `resource <id> <kind> ...`");
    }
    let mut resource = ResourceSpec {
        resource_id: tokens[1].clone(),
        resource_kind: tokens[2].clone(),
        max_concurrent: 1,
        min_interval_ms: 0,
        allowed_modes: Vec::new(),
        hazard_class: "C".to_string(),
    };
    let mut index = 3usize;
    while index < tokens.len() {
        match tokens[index].as_str() {
            "max_concurrent" => {
                resource.max_concurrent = parse_i64(tokens.get(index + 1), line_no)?;
                index += 2;
            }
            "min_interval_ms" => {
                resource.min_interval_ms = parse_i64(tokens.get(index + 1), line_no)?;
                index += 2;
            }
            "allowed_modes" => {
                let value = tokens
                    .get(index + 1)
                    .context("allowed_modes requires a value")?;
                resource.allowed_modes = value
                    .split(',')
                    .filter(|mode| !mode.trim().is_empty())
                    .map(|mode| mode.trim().to_string())
                    .collect();
                index += 2;
            }
            "hazard_class" => {
                let value = tokens
                    .get(index + 1)
                    .context("hazard_class requires a value")?;
                resource.hazard_class = value.clone();
                index += 2;
            }
            _ => bail!("line {line_no}: unknown resource field `{}`", tokens[index]),
        }
    }
    Ok(resource)
}

fn parse_key_value(token: &str, line_no: usize) -> Result<(&str, &str)> {
    token
        .split_once('=')
        .filter(|(key, _)| !key.is_empty())
        .with_context(|| format!("line {line_no}: expected key=value, got `{token}`"))
}

fn parse_node_role(value: &str, line_no: usize) -> Result<(&str, &str)> {
    value
        .split_once('.')
        .filter(|(node, role)| !node.is_empty() && !role.is_empty())
        .with_context(|| format!("line {line_no}: expected <node>.<role>, got `{value}`"))
}

fn parse_bool(value: &str, line_no: usize) -> Result<bool> {
    match value {
        "true" => Ok(true),
        "false" => Ok(false),
        _ => bail!("line {line_no}: expected true or false, got `{value}`"),
    }
}

fn parse_i64(value: Option<&String>, line_no: usize) -> Result<i64> {
    value
        .context("integer value is required")?
        .parse::<i64>()
        .with_context(|| format!("line {line_no}: invalid integer"))
}

fn parse_flow_value(value: &str) -> Result<Value> {
    if value == "true" {
        return Ok(Value::Bool(true));
    }
    if value == "false" {
        return Ok(Value::Bool(false));
    }
    if let Ok(v) = value.parse::<i64>() {
        return Ok(json!(v));
    }
    if let Ok(v) = value.parse::<f64>() {
        return Ok(json!(v));
    }
    Ok(Value::String(value.to_string()))
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

    validate_nodes(conn, spec, &layers, &output_index, &mut violations)?;

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

fn collect_layers(
    spec: &WorkflowSpec,
    violations: &mut Vec<InvariantViolation>,
) -> BTreeSet<String> {
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
                    format!(
                        "duplicate output role {} on node {}",
                        output.role, node.node_id
                    ),
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
                    format!(
                        "node {} references unknown input node {}",
                        node.node_id, input.from
                    ),
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
                format!(
                    "node {} uses unknown cell layer {}",
                    node.node_id, node.cell.layer
                ),
            ));
        }
        let Some(algorithm) = load_algorithm(conn, &node.algorithm)? else {
            violations.push(InvariantViolation::error(
                "UNKNOWN_ALGORITHM",
                json!({"node_id": node.node_id, "algorithm_id": node.algorithm}),
                format!(
                    "node {} uses unknown algorithm {}",
                    node.node_id, node.algorithm
                ),
            ));
            continue;
        };
        validate_algorithm_contract(conn, node, &algorithm, outputs, violations)?;
        validate_authority_lineage_shape(node, outputs, violations);
        violations.extend(validate_algorithm_mode(
            conn,
            &algorithm,
            &spec.driver.mode,
            spec.driver.allow_external_write,
            spec.driver.allow_broker_resource,
        )?);
        violations.extend(validate_class_c_outputs(
            &algorithm,
            &output_product_types(node),
        ));

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
    outputs: &BTreeMap<(String, String), String>,
    violations: &mut Vec<InvariantViolation>,
) -> Result<()> {
    for input in &node.inputs {
        if let Some(product_type) = outputs.get(&(input.from.clone(), input.role.clone())) {
            if !algorithm
                .inputs
                .iter()
                .any(|allowed| allowed == product_type)
            {
                violations.push(InvariantViolation::error(
                    "INPUT_CONTRACT_MISMATCH",
                    json!({"node_id": node.node_id, "algorithm_id": algorithm.algorithm_id, "product_type": product_type, "from": input.from, "role": input.role}),
                    format!(
                        "algorithm {} does not declare input {}",
                        algorithm.algorithm_id, product_type
                    ),
                ));
            }
        }
    }
    for output in &node.outputs {
        if load_product_type(conn, &output.product_type)?.is_none() {
            violations.push(InvariantViolation::error(
                "UNKNOWN_PRODUCT_TYPE",
                json!({"node_id": node.node_id, "product_type": output.product_type}),
                format!(
                    "node {} declares unknown product type {}",
                    node.node_id, output.product_type
                ),
            ));
        }
        if !algorithm
            .outputs
            .iter()
            .any(|allowed| allowed == &output.product_type)
        {
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

fn validate_authority_lineage_shape(
    node: &NodeSpec,
    outputs: &BTreeMap<(String, String), String>,
    violations: &mut Vec<InvariantViolation>,
) {
    for output in &node.outputs {
        match output.product_type.as_str() {
            "staged_order_set.v1" => {
                if !node_has_input_product_type(node, outputs, "risk_decision_set.v1") {
                    violations.push(InvariantViolation::error(
                        "STAGED_ORDER_REQUIRES_APPROVED_RISK",
                        json!({"node_id": node.node_id, "product_type": output.product_type}),
                        format!(
                            "node {} produces staged orders without a risk decision parent",
                            node.node_id
                        ),
                    ));
                }
            }
            "broker_event_set.v1" => {
                if !node_has_input_product_type(node, outputs, "staged_order_set.v1") {
                    violations.push(InvariantViolation::error(
                        "BROKER_EVENT_REQUIRES_STAGED_ORDER",
                        json!({"node_id": node.node_id, "product_type": output.product_type}),
                        format!(
                            "node {} produces broker events without a staged order parent",
                            node.node_id
                        ),
                    ));
                }
                if !node_has_input_product_type(node, outputs, "reconciliation_snapshot.v1") {
                    violations.push(InvariantViolation::error(
                        "BROKER_EVENT_REQUIRES_RECONCILIATION",
                        json!({"node_id": node.node_id, "product_type": output.product_type}),
                        format!(
                            "node {} produces broker events without a reconciliation parent",
                            node.node_id
                        ),
                    ));
                }
            }
            _ => {}
        }
    }
}

fn node_has_input_product_type(
    node: &NodeSpec,
    outputs: &BTreeMap<(String, String), String>,
    expected_product_type: &str,
) -> bool {
    node.inputs.iter().any(|input| {
        outputs
            .get(&(input.from.clone(), input.role.clone()))
            .is_some_and(|product_type| product_type == expected_product_type)
    })
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
        conn.execute_batch(include_str!("../../../schema.sql"))
            .unwrap();
        seed_product_graph_reference_data(&conn).unwrap();
        conn
    }

    #[test]
    fn graph_rejects_cycle() {
        let text = r#"
workflow cycle
driver test
mode observe
decision_as_of 2026-05-08T13:00:00.000Z
allow_external_write false
allow_broker_resource false
layer job key id

node a
  alg noop.test.v1
  cell job id=a
  input output from b.output
  output output command_output.v1
end

node b
  alg noop.test.v1
  cell job id=b
  input output from a.output
  output output command_output.v1
end
"#;
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
        let text = r#"
workflow class
driver test
mode shadow
decision_as_of 2026-05-08T13:00:00.000Z
allow_external_write false
allow_broker_resource false
layer job key id

node report
  alg cmd.report.daily.v1
  cell job id=r
  set date today
  output report report.v1
end

node gate
  alg cmd.gate.v1
  cell job id=g
  set core_lib build/libfolio.so
  input report from report.report
  output risk_decisions risk_decision_set.v1
end
"#;
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
        let text = r#"
workflow broker_shadow
driver test
mode shadow
decision_as_of 2026-05-08T13:00:00.000Z
allow_external_write false
allow_broker_resource false
layer job key id

node send
  alg cmd.send.mock.v1
  cell job id=s
  set max_reconciliation_age_s 3600
  output broker_events broker_event_set.v1
end
"#;
        let conn = memory_conn();
        let (report, valid) = validate_workflow(&conn, parse_workflow_spec(text).unwrap()).unwrap();
        assert!(valid.is_none());
        assert!(report
            .violations
            .iter()
            .any(|v| v.invariant_id == "BROKER_RESOURCE_MODE"));
    }

    #[test]
    fn graph_rejects_input_contract_mismatch() {
        let text = r#"
workflow input_contract
driver test
mode shadow
decision_as_of 2026-05-08T13:00:00.000Z
allow_external_write false
allow_broker_resource false
layer job key id

node value
  alg cmd.value.v1
  cell job id=v
  set core_lib build/libfolio.so
  set portfolio_value_usd 100000
  set cash_usd 100000
  set target_gross_exposure_ratio 0.50
  set max_name_weight_ratio 0.05
  set max_order_notional_usd 10000
  set min_adv_usd 1000000
  set max_adv_participation_ratio 0.01
  set max_reconciliation_age_s 3600
  output valuations valuation_set.v1
end

node gate
  alg cmd.gate.v1
  cell job id=g
  set core_lib build/libfolio.so
  set portfolio_value_usd 100000
  set cash_usd 100000
  set max_name_weight_ratio 0.05
  set max_order_notional_usd 10000
  set min_adv_usd 1000000
  set max_adv_participation_ratio 0.01
  set max_reconciliation_age_s 3600
  input valuations from value.valuations
  output risk_decisions risk_decision_set.v1
end
"#;
        let conn = memory_conn();
        let (report, valid) = validate_workflow(&conn, parse_workflow_spec(text).unwrap()).unwrap();
        assert!(valid.is_none());
        assert!(report
            .violations
            .iter()
            .any(|v| v.invariant_id == "INPUT_CONTRACT_MISMATCH"));
    }

    #[test]
    fn graph_rejects_broker_event_without_staged_parent() {
        let text = r#"
workflow send_without_stage
driver test
mode paper
decision_as_of 2026-05-08T13:00:00.000Z
allow_external_write true
allow_broker_resource true
layer job key id

node recon
  alg cmd.recon.v1
  cell job id=r
  set portfolio_value_usd 100000
  set cash_usd 100000
  set reconciled true
  output reconciliation reconciliation_snapshot.v1
end

node send
  alg cmd.send.mock.v1
  cell job id=s
  input reconciliation from recon.reconciliation
  set max_reconciliation_age_s 3600
  output broker_events broker_event_set.v1
end
"#;
        let conn = memory_conn();
        let (report, valid) = validate_workflow(&conn, parse_workflow_spec(text).unwrap()).unwrap();
        assert!(valid.is_none());
        assert!(report
            .violations
            .iter()
            .any(|v| v.invariant_id == "BROKER_EVENT_REQUIRES_STAGED_ORDER"));
    }

    #[test]
    fn graph_validates_full_analysis_flow() {
        let conn = memory_conn();
        let parsed = parse_workflow_spec(include_str!(
            "../../../docs/contracts/workflow.full_analysis.flow"
        ))
        .unwrap();
        let (report, valid) = validate_workflow(&conn, parsed).unwrap();
        assert!(valid.is_some(), "{:?}", report.violations);
        assert_eq!(
            report.topological_order.first().map(String::as_str),
            Some("reconcile")
        );
        assert_eq!(
            report.topological_order.last().map(String::as_str),
            Some("report")
        );
        assert_eq!(report.node_count, 6);
    }

    #[test]
    fn graph_validates_stage_and_paper_flows() {
        let conn = memory_conn();
        for text in [
            include_str!("../../../docs/contracts/workflow.stage_orders.flow"),
            include_str!("../../../docs/contracts/workflow.paper_mock.flow"),
        ] {
            let parsed = parse_workflow_spec(text).unwrap();
            let (report, valid) = validate_workflow(&conn, parsed).unwrap();
            assert!(valid.is_some(), "{:?}", report.violations);
        }
    }
}
