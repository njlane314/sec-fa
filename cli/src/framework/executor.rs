use std::collections::BTreeMap;
use std::path::Path;

use anyhow::{anyhow, bail, Context as _, Result};
use rusqlite::{params, Connection};
use serde_json::{json, Value};

use crate::fa_core::utc_now;

use super::canonical_json::to_canonical_json;
use super::graph::{self, NodeSpec, ValidatedWorkflow};
use super::hash;
use super::invariants::{validate_runtime_input_product, InvariantViolation};
use super::product::NewProduct;
use super::registry::{load_algorithm, load_product_type, Algorithm};
use super::storage::persist_product;

#[derive(Debug, Clone)]
pub struct CommandResult {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

#[derive(Debug, Clone)]
#[allow(dead_code)]
struct ProducedProduct {
    product_id: String,
    product_type: String,
    role: String,
    run_id: Option<String>,
}

pub fn run_sec_command(args: &[String]) -> Result<CommandResult> {
    let exe = std::env::current_exe()?;
    let out = std::process::Command::new(exe).args(args).output()?;

    Ok(CommandResult {
        stdout: String::from_utf8_lossy(&out.stdout).to_string(),
        stderr: String::from_utf8_lossy(&out.stderr).to_string(),
        exit_code: out.status.code().unwrap_or(127),
    })
}

pub fn run_validated_workflow(
    conn: &mut Connection,
    db_path: &Path,
    validated: ValidatedWorkflow,
) -> Result<String> {
    let spec = &validated.parsed.spec;
    let created_at = utc_now();
    let workflow_run_id = hash::workflow_run_id(
        &spec.workflow_name,
        &validated.parsed.spec_sha256,
        &spec.driver.mode,
        &spec.driver.decision_as_of,
        &created_at,
    )?;
    {
        let tx = conn.transaction()?;
        tx.execute(
            "INSERT INTO workflow_runs(workflow_run_id, workflow_name, workflow_spec_sha256, driver_json, mode, decision_as_of, status, created_at, started_at, diagnostics) VALUES (?, ?, ?, ?, ?, ?, 'running', ?, ?, '')",
            params![
                workflow_run_id,
                spec.workflow_name,
                validated.parsed.spec_sha256,
                to_canonical_json(&spec.driver)?,
                spec.driver.mode,
                spec.driver.decision_as_of,
                created_at,
                created_at,
            ],
        )?;
        tx.execute(
            "INSERT INTO workflow_run_specs(workflow_run_id, workflow_spec_sha256, workflow_spec_json, created_at) VALUES (?, ?, ?, ?)",
            params![
                workflow_run_id,
                validated.parsed.spec_sha256,
                validated.parsed.canonical_json,
                created_at,
            ],
        )?;
        for edge in &validated.edges {
            tx.execute(
                "INSERT OR IGNORE INTO workflow_edges(workflow_run_id, from_node_id, to_node_id, product_role) VALUES (?, ?, ?, ?)",
                params![
                    workflow_run_id,
                    edge.from_node_id,
                    edge.to_node_id,
                    edge.product_role
                ],
            )?;
        }
        tx.commit()?;
    }
    print_jsonl(&json!({
        "event": "workflow_started",
        "workflow_run_id": workflow_run_id,
        "mode": spec.driver.mode,
        "decision_as_of": spec.driver.decision_as_of
    }))?;

    let mut produced = BTreeMap::<(String, String), ProducedProduct>::new();
    for node_id in &validated.order {
        let node = graph::node_by_id(spec, node_id)
            .ok_or_else(|| anyhow!("validated node {node_id} missing from workflow spec"))?;
        let algorithm = load_algorithm(conn, &node.algorithm)?
            .ok_or_else(|| anyhow!("algorithm {} disappeared from registry", node.algorithm))?;
        let input_products = resolve_inputs(node, &produced)?;
        let runtime_violations =
            runtime_input_violations(conn, node, &input_products, &spec.driver.decision_as_of)?;
        if !runtime_violations.is_empty() {
            persist_violations(conn, &workflow_run_id, &runtime_violations)?;
            mark_workflow_finished(conn, &workflow_run_id, "rejected", &runtime_violations)?;
            bail!("workflow rejected by runtime invariants");
        }

        let input_product_ids = input_products
            .iter()
            .map(|product| product.product_id.clone())
            .collect::<Vec<_>>();
        let cell_id = graph::cell_id(node)?;
        let config_sha256 = graph::config_hash(node)?;
        let node_run_id = hash::node_run_id(
            &workflow_run_id,
            &node.node_id,
            &cell_id,
            &algorithm.algorithm_id,
            &config_sha256,
            &input_product_ids,
        )?;
        insert_node_running(
            conn,
            &workflow_run_id,
            &node_run_id,
            node,
            &algorithm,
            &cell_id,
            &config_sha256,
            &input_product_ids,
        )?;

        let node_execution = execute_algorithm(db_path, node, &algorithm, &input_products)
            .and_then(|execution| {
                persist_node_outputs(
                    conn,
                    node,
                    &algorithm,
                    &workflow_run_id,
                    &node_run_id,
                    &cell_id,
                    &config_sha256,
                    &input_products,
                    execution,
                    &spec.driver.decision_as_of,
                )
            })
            .with_context(|| format!("executing node {}", node.node_id));
        let node_outputs = match node_execution {
            Ok(node_outputs) => node_outputs,
            Err(err) => {
                mark_node_finished(conn, &node_run_id, "failed", "[]", &format!("{err:#}"))?;
                mark_workflow_status(conn, &workflow_run_id, "failed", &format!("{err:#}"))?;
                return Err(err);
            }
        };

        let output_ids = node_outputs
            .iter()
            .map(|product| product.product_id.clone())
            .collect::<Vec<_>>();
        mark_node_finished(
            conn,
            &node_run_id,
            "succeeded",
            &to_canonical_json(&output_ids)?,
            "",
        )?;
        for output in node_outputs {
            produced.insert((node.node_id.clone(), output.role.clone()), output);
        }
        print_jsonl(&json!({
            "event": "node_succeeded",
            "node_run_id": node_run_id,
            "node_id": node.node_id,
            "output_count": output_ids.len()
        }))?;
    }

    mark_workflow_status(conn, &workflow_run_id, "succeeded", "")?;
    print_jsonl(&json!({
        "event": "workflow_succeeded",
        "workflow_run_id": workflow_run_id
    }))?;
    Ok(workflow_run_id)
}

enum AlgorithmExecution {
    Noop { payload: Value },
    Command {
        command_result: CommandResult,
        run_id: Option<String>,
    },
}

fn execute_algorithm(
    db_path: &Path,
    node: &NodeSpec,
    algorithm: &Algorithm,
    input_products: &[ProducedProduct],
) -> Result<AlgorithmExecution> {
    if algorithm.executable_kind == "noop_test" {
        let config_sha256 = hash::config_sha256(&node.config)?;
        let message = node
            .config
            .get("message")
            .and_then(Value::as_str)
            .unwrap_or_default();
        return Ok(AlgorithmExecution::Noop {
            payload: json!({
                "algorithm": algorithm.algorithm_id,
                "config_sha256": config_sha256,
                "message": message
            }),
        });
    }
    if algorithm.executable_kind != "command" {
        bail!(
            "unsupported executable_kind={} for algorithm {}",
            algorithm.executable_kind,
            algorithm.algorithm_id
        );
    }
    let inherited_run_id = input_products.iter().find_map(|product| product.run_id.clone());
    let args = command_args(db_path, node, algorithm, inherited_run_id.as_deref())?;
    let command_result = run_sec_command(&args)?;
    let extracted_run_id = run_id_from_stdout(&command_result.stdout).or(inherited_run_id);
    Ok(AlgorithmExecution::Command {
        command_result,
        run_id: extracted_run_id,
    })
}

fn persist_node_outputs(
    conn: &mut Connection,
    node: &NodeSpec,
    algorithm: &Algorithm,
    workflow_run_id: &str,
    _node_run_id: &str,
    cell_id: &str,
    config_sha256: &str,
    input_products: &[ProducedProduct],
    execution: AlgorithmExecution,
    decision_as_of: &str,
) -> Result<Vec<ProducedProduct>> {
    let parent_product_ids = input_products
        .iter()
        .map(|product| (product.product_id.clone(), product.role.clone()))
        .collect::<Vec<_>>();
    let mut output_products = Vec::<ProducedProduct>::new();
    let tx = conn.transaction()?;
    match execution {
        AlgorithmExecution::Noop { payload } => {
            let roles = output_roles_for_type(node, "command_output.v1", "output");
            let content_sha256 = hash::content_sha256(&payload)?;
            let storage_ref = to_canonical_json(&payload)?;
            for role in roles {
                let product_id = persist_typed_product(
                    &tx,
                    "command_output.v1",
                    cell_id,
                    config_sha256,
                    algorithm,
                    content_sha256.clone(),
                    "inline_json",
                    storage_ref.clone(),
                    decision_as_of,
                    parent_product_ids.clone(),
                )?;
                output_products.push(ProducedProduct {
                    product_id,
                    product_type: "command_output.v1".to_string(),
                    role,
                    run_id: None,
                });
            }
        }
        AlgorithmExecution::Command {
            command_result,
            run_id,
        } => {
            let command_payload = json!({
                "stdout": command_result.stdout,
                "stderr": command_result.stderr,
                "exit_code": command_result.exit_code
            });
            let command_content_sha256 = hash::content_sha256(&command_payload)?;
            let command_storage_ref = to_canonical_json(&command_payload)?;
            let command_roles = output_roles_for_type(node, "command_output.v1", "command_output");
            let mut command_output_product_id = None::<String>;
            for role in command_roles {
                let product_id = persist_typed_product(
                    &tx,
                    "command_output.v1",
                    cell_id,
                    config_sha256,
                    algorithm,
                    command_content_sha256.clone(),
                    "inline_json",
                    command_storage_ref.clone(),
                    decision_as_of,
                    parent_product_ids.clone(),
                )?;
                command_output_product_id = Some(product_id.clone());
                output_products.push(ProducedProduct {
                    product_id,
                    product_type: "command_output.v1".to_string(),
                    role,
                    run_id: run_id.clone(),
                });
            }

            if command_result.exit_code != 0 {
                tx.commit()?;
                bail!(
                    "algorithm {} exited with code {}",
                    algorithm.algorithm_id,
                    command_result.exit_code
                );
            }

            for output in node
                .outputs
                .iter()
                .filter(|output| output.product_type != "command_output.v1")
            {
                let storage_ref = domain_storage_ref(
                    &algorithm.algorithm_id,
                    &output.product_type,
                    run_id.as_deref(),
                    command_output_product_id.as_deref(),
                );
                let created_at = utc_now();
                let payload = json!({
                    "storage_ref": storage_ref,
                    "algorithm_id": algorithm.algorithm_id,
                    "node_id": node.node_id,
                    "workflow_run_id": workflow_run_id,
                    "run_id": run_id,
                    "created_at": created_at
                });
                let content_sha256 = hash::content_sha256(&payload)?;
                let product_id = persist_typed_product(
                    &tx,
                    &output.product_type,
                    cell_id,
                    config_sha256,
                    algorithm,
                    content_sha256,
                    "sqlite_ref",
                    storage_ref,
                    decision_as_of,
                    parent_product_ids.clone(),
                )?;
                output_products.push(ProducedProduct {
                    product_id,
                    product_type: output.product_type.clone(),
                    role: output.role.clone(),
                    run_id: run_id.clone(),
                });
            }
        }
    }
    tx.commit()?;
    Ok(output_products)
}

#[allow(clippy::too_many_arguments)]
fn persist_typed_product(
    tx: &rusqlite::Transaction<'_>,
    product_type: &str,
    cell_id: &str,
    config_sha256: &str,
    algorithm: &Algorithm,
    content_sha256: String,
    storage_kind: &str,
    storage_ref: String,
    available_at: &str,
    parent_product_ids: Vec<(String, String)>,
) -> Result<String> {
    let type_row = load_product_type(tx, product_type)?
        .ok_or_else(|| anyhow!("unknown product type {product_type}"))?;
    let product = NewProduct {
        product_type: type_row.product_type,
        schema_name: type_row.schema_name,
        schema_version: type_row.schema_version,
        cell_id: cell_id.to_string(),
        family_id: None,
        content_sha256,
        storage_kind: storage_kind.to_string(),
        storage_ref,
        created_by_algorithm: algorithm.algorithm_id.clone(),
        algorithm_version: algorithm.algorithm_version.clone(),
        config_sha256: config_sha256.to_string(),
        code_sha256: None,
        valid_time_start: None,
        valid_time_end: None,
        source_time: None,
        accepted_at: None,
        ingested_at: None,
        available_at: available_at.to_string(),
        hazard_class: type_row.hazard_class,
        parent_product_ids,
    };
    persist_product(tx, &product)
}

fn output_roles_for_type(node: &NodeSpec, product_type: &str, fallback: &str) -> Vec<String> {
    let roles = node
        .outputs
        .iter()
        .filter_map(|output| {
            (output.product_type == product_type).then_some(output.role.clone())
        })
        .collect::<Vec<_>>();
    if roles.is_empty() {
        vec![fallback.to_string()]
    } else {
        roles
    }
}

fn resolve_inputs(
    node: &NodeSpec,
    produced: &BTreeMap<(String, String), ProducedProduct>,
) -> Result<Vec<ProducedProduct>> {
    let mut inputs = Vec::new();
    for input in &node.inputs {
        let key = (input.from.clone(), input.role.clone());
        let product = produced.get(&key).ok_or_else(|| {
            anyhow!(
                "node {} missing input product from {} role {}",
                node.node_id,
                input.from,
                input.role
            )
        })?;
        inputs.push(product.clone());
    }
    Ok(inputs)
}

fn runtime_input_violations(
    conn: &Connection,
    node: &NodeSpec,
    input_products: &[ProducedProduct],
    decision_as_of: &str,
) -> Result<Vec<InvariantViolation>> {
    let mut violations = Vec::new();
    for product in input_products {
        violations.extend(validate_runtime_input_product(
            conn,
            &node.algorithm,
            &product.product_id,
            decision_as_of,
        )?);
    }
    Ok(violations)
}

fn insert_node_running(
    conn: &Connection,
    workflow_run_id: &str,
    node_run_id: &str,
    node: &NodeSpec,
    algorithm: &Algorithm,
    cell_id: &str,
    config_sha256: &str,
    input_product_ids: &[String],
) -> Result<()> {
    conn.execute(
        "INSERT INTO workflow_nodes(node_run_id, workflow_run_id, node_id, algorithm_id, cell_id, config_sha256, input_product_ids_json, output_product_ids_json, status, started_at, diagnostics) VALUES (?, ?, ?, ?, ?, ?, ?, '[]', 'running', ?, '')",
        params![
            node_run_id,
            workflow_run_id,
            node.node_id,
            algorithm.algorithm_id,
            cell_id,
            config_sha256,
            to_canonical_json(input_product_ids)?,
            utc_now(),
        ],
    )?;
    Ok(())
}

fn mark_node_finished(
    conn: &Connection,
    node_run_id: &str,
    status: &str,
    output_product_ids_json: &str,
    diagnostics: &str,
) -> Result<()> {
    conn.execute(
        "UPDATE workflow_nodes SET status=?, output_product_ids_json=?, finished_at=?, diagnostics=? WHERE node_run_id=?",
        params![status, output_product_ids_json, utc_now(), diagnostics, node_run_id],
    )?;
    Ok(())
}

fn mark_workflow_status(
    conn: &Connection,
    workflow_run_id: &str,
    status: &str,
    diagnostics: &str,
) -> Result<()> {
    conn.execute(
        "UPDATE workflow_runs SET status=?, finished_at=CASE WHEN ? IN ('succeeded','failed','rejected') THEN ? ELSE finished_at END, diagnostics=? WHERE workflow_run_id=?",
        params![status, status, utc_now(), diagnostics, workflow_run_id],
    )?;
    Ok(())
}

fn mark_workflow_finished(
    conn: &Connection,
    workflow_run_id: &str,
    status: &str,
    violations: &[InvariantViolation],
) -> Result<()> {
    mark_workflow_status(
        conn,
        workflow_run_id,
        status,
        &to_canonical_json(&violations)?,
    )
}

fn persist_violations(
    conn: &mut Connection,
    workflow_run_id: &str,
    violations: &[InvariantViolation],
) -> Result<()> {
    let tx = conn.transaction()?;
    for violation in violations {
        let subject_json = to_canonical_json(&violation.subject)?;
        let violation_id = format!(
            "viol_{}",
            &hash::sha256_hex(
                to_canonical_json(&json!({
                    "workflow_run_id": workflow_run_id,
                    "invariant_id": violation.invariant_id,
                    "subject": violation.subject,
                    "created_at": utc_now()
                }))?
                .as_bytes()
            )[..48]
        );
        tx.execute(
            "INSERT OR IGNORE INTO graph_invariant_violations(violation_id, workflow_run_id, invariant_id, subject_json, severity, explanation, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
            params![
                violation_id,
                workflow_run_id,
                violation.invariant_id,
                subject_json,
                violation.severity,
                violation.explanation,
                utc_now(),
            ],
        )?;
    }
    tx.commit()?;
    Ok(())
}

fn command_args(
    db_path: &Path,
    node: &NodeSpec,
    algorithm: &Algorithm,
    inherited_run_id: Option<&str>,
) -> Result<Vec<String>> {
    let db = db_path.to_string_lossy().to_string();
    let config = &node.config;
    match algorithm.algorithm_id.as_str() {
        "cmd.recon.v1" => Ok(vec![
            "recon".to_string(),
            "--db".to_string(),
            db,
            "--portfolio-value-usd".to_string(),
            required_f64(config, "portfolio_value_usd")?.to_string(),
            "--cash-usd".to_string(),
            required_f64(config, "cash_usd")?.to_string(),
            "--reconciled".to_string(),
            if config
                .get("reconciled")
                .and_then(Value::as_bool)
                .unwrap_or(false)
            {
                "1".to_string()
            } else {
                "0".to_string()
            },
        ]),
        "cmd.value.v1" => Ok(vec![
            "value".to_string(),
            "--db".to_string(),
            db,
            "--core-lib".to_string(),
            required_string(config, "core_lib")?,
            "--portfolio-value-usd".to_string(),
            required_f64(config, "portfolio_value_usd")?.to_string(),
            "--cash-usd".to_string(),
            required_f64(config, "cash_usd")?.to_string(),
            "--max-name-weight-ratio".to_string(),
            required_f64(config, "max_name_weight_ratio")?.to_string(),
            "--target-gross-exposure-ratio".to_string(),
            required_f64(config, "target_gross_exposure_ratio")?.to_string(),
            "--max-order-notional-usd".to_string(),
            required_f64(config, "max_order_notional_usd")?.to_string(),
            "--min-adv-usd".to_string(),
            required_f64(config, "min_adv_usd")?.to_string(),
            "--max-adv-participation-ratio".to_string(),
            required_f64(config, "max_adv_participation_ratio")?.to_string(),
            "--max-reconciliation-age-s".to_string(),
            required_i64(config, "max_reconciliation_age_s")?.to_string(),
        ]),
        "cmd.gate.v1" => {
            let run_id = inherited_run_id
                .or_else(|| config.get("run_id").and_then(Value::as_str))
                .ok_or_else(|| anyhow!("cmd.gate.v1 requires resolved run_id"))?;
            Ok(vec![
                "gate".to_string(),
                "--db".to_string(),
                db,
                "--core-lib".to_string(),
                required_string(config, "core_lib")?,
                "--run-id".to_string(),
                run_id.to_string(),
                "--portfolio-value-usd".to_string(),
                required_f64(config, "portfolio_value_usd")?.to_string(),
                "--cash-usd".to_string(),
                required_f64(config, "cash_usd")?.to_string(),
                "--max-name-weight-ratio".to_string(),
                required_f64(config, "max_name_weight_ratio")?.to_string(),
                "--max-order-notional-usd".to_string(),
                required_f64(config, "max_order_notional_usd")?.to_string(),
                "--min-adv-usd".to_string(),
                required_f64(config, "min_adv_usd")?.to_string(),
                "--max-adv-participation-ratio".to_string(),
                required_f64(config, "max_adv_participation_ratio")?.to_string(),
                "--max-reconciliation-age-s".to_string(),
                required_i64(config, "max_reconciliation_age_s")?.to_string(),
            ])
        }
        "cmd.stage.v1" => {
            let run_id = inherited_run_id
                .or_else(|| config.get("run_id").and_then(Value::as_str))
                .ok_or_else(|| anyhow!("cmd.stage.v1 requires resolved run_id"))?;
            Ok(vec![
                "stage".to_string(),
                "--db".to_string(),
                db,
                "--run-id".to_string(),
                run_id.to_string(),
            ])
        }
        "cmd.send.mock.v1" => Ok(vec![
            "send".to_string(),
            "--db".to_string(),
            db,
            "--adapter".to_string(),
            "mock".to_string(),
            "--max-reconciliation-age-s".to_string(),
            required_i64(config, "max_reconciliation_age_s")?.to_string(),
        ]),
        "cmd.report.daily.v1" => Ok(vec![
            "report".to_string(),
            "daily".to_string(),
            "--db".to_string(),
            db,
            "--date".to_string(),
            config
                .get("date")
                .and_then(Value::as_str)
                .unwrap_or("today")
                .to_string(),
        ]),
        _ => bail!("no command mapping for algorithm {}", algorithm.algorithm_id),
    }
}

fn required_f64(config: &Value, key: &str) -> Result<f64> {
    config
        .get(key)
        .and_then(Value::as_f64)
        .ok_or_else(|| anyhow!("missing numeric config.{key}"))
}

fn required_i64(config: &Value, key: &str) -> Result<i64> {
    config
        .get(key)
        .and_then(Value::as_i64)
        .ok_or_else(|| anyhow!("missing integer config.{key}"))
}

fn required_string(config: &Value, key: &str) -> Result<String> {
    config
        .get(key)
        .and_then(Value::as_str)
        .map(ToOwned::to_owned)
        .ok_or_else(|| anyhow!("missing string config.{key}"))
}

fn run_id_from_stdout(stdout: &str) -> Option<String> {
    for line in stdout.lines() {
        let Ok(value) = serde_json::from_str::<Value>(line) else {
            continue;
        };
        if let Some(run_id) = value
            .get("payload")
            .and_then(|payload| payload.get("run_id"))
            .and_then(Value::as_str)
        {
            return Some(run_id.to_string());
        }
        if let Some(run_id) = value.get("run_id").and_then(Value::as_str) {
            return Some(run_id.to_string());
        }
    }
    None
}

fn domain_storage_ref(
    algorithm_id: &str,
    product_type: &str,
    run_id: Option<&str>,
    command_output_product_id: Option<&str>,
) -> String {
    match (algorithm_id, product_type) {
        ("cmd.recon.v1", "reconciliation_snapshot.v1") => "sqlite:broker_state:id=1".to_string(),
        ("cmd.value.v1", "statement_snapshot_set.v1") => {
            "sqlite:statement_snapshots:latest_run_or_time".to_string()
        }
        ("cmd.value.v1", "valuation_set.v1") => {
            format!("sqlite:valuations:run_id={}", run_id.unwrap_or(""))
        }
        ("cmd.value.v1", "target_weight_set.v1") => {
            format!("sqlite:target_weights:run_id={}", run_id.unwrap_or(""))
        }
        ("cmd.value.v1", "order_intent_set.v1") => {
            format!("sqlite:order_intents:run_id={}", run_id.unwrap_or(""))
        }
        ("cmd.gate.v1", "risk_decision_set.v1") => {
            format!("sqlite:risk_decisions:run_id={}", run_id.unwrap_or(""))
        }
        ("cmd.stage.v1", "staged_order_set.v1") => {
            format!("sqlite:staged_orders:run_id={}", run_id.unwrap_or(""))
        }
        ("cmd.send.mock.v1", "broker_event_set.v1") => {
            format!("sqlite:broker_events:after={}", utc_now())
        }
        ("cmd.report.daily.v1", "report.v1") => {
            format!("stdout:{}", command_output_product_id.unwrap_or(""))
        }
        _ => format!("sqlite:{}:run_id={}", product_type, run_id.unwrap_or("")),
    }
}

fn print_jsonl(value: &Value) -> Result<()> {
    println!("{}", serde_json::to_string(value)?);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::framework::graph::{parse_workflow_spec, validate_workflow};
    use crate::framework::registry::seed_product_graph_reference_data;
    use crate::framework::replay::replay_workflow;
    use rusqlite::Connection;
    use std::path::Path;

    fn memory_conn() -> Connection {
        let conn = Connection::open_in_memory().unwrap();
        conn.execute_batch(include_str!("../../../schema.sql")).unwrap();
        seed_product_graph_reference_data(&conn).unwrap();
        conn
    }

    #[test]
    fn run_id_from_stdout_reads_payload_or_top_level() {
        assert_eq!(
            run_id_from_stdout(r#"{"payload":{"run_id":"abc"}}"#),
            Some("abc".to_string())
        );
        assert_eq!(
            run_id_from_stdout("not json\n{\"run_id\":\"def\"}"),
            Some("def".to_string())
        );
    }

    #[test]
    fn graph_allows_noop() {
        let mut conn = memory_conn();
        let parsed = parse_workflow_spec(include_str!("../../../tests/fixtures/workflow_noop.json"))
            .unwrap();
        let (_report, validated) = validate_workflow(&conn, parsed).unwrap();
        let workflow_run_id = run_validated_workflow(
            &mut conn,
            Path::new(".fa.db"),
            validated.expect("noop fixture validates"),
        )
        .unwrap();
        let product_count: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM data_products WHERE product_type='command_output.v1'",
                [],
                |row| row.get(0),
            )
            .unwrap();
        assert_eq!(product_count, 1);
        let status: String = conn
            .query_row(
                "SELECT status FROM workflow_runs WHERE workflow_run_id=?",
                [workflow_run_id],
                |row| row.get(0),
            )
            .unwrap();
        assert_eq!(status, "succeeded");
    }

    #[test]
    fn graph_run_records_lineage() {
        let mut conn = memory_conn();
        let text = r#"{
          "spec_version": 1,
          "workflow_name": "lineage",
          "driver": {"driver_id":"test","mode":"observe","decision_as_of":"2026-05-08T13:00:00.000Z","allow_external_write":false,"allow_broker_resource":false},
          "layers": [{"layer":"job","parent":null,"key_fields":["id"]}],
          "resources": [],
          "nodes": [
            {"node_id":"a","algorithm":"noop.test.v1","cell":{"layer":"job","key":{"id":"a"}},"config":{"message":"a"},"inputs":[],"outputs":[{"role":"output","product_type":"command_output.v1"}]},
            {"node_id":"b","algorithm":"noop.test.v1","cell":{"layer":"job","key":{"id":"b"}},"config":{"message":"b"},"inputs":[{"from":"a","role":"output"}],"outputs":[{"role":"output","product_type":"command_output.v1"}]}
          ]
        }"#;
        let parsed = parse_workflow_spec(text).unwrap();
        let (_report, validated) = validate_workflow(&conn, parsed).unwrap();
        run_validated_workflow(
            &mut conn,
            Path::new(".fa.db"),
            validated.expect("lineage graph validates"),
        )
        .unwrap();
        let lineage_count: i64 = conn
            .query_row("SELECT COUNT(*) FROM data_product_lineage", [], |row| row.get(0))
            .unwrap();
        assert!(lineage_count > 0);
    }

    #[test]
    fn replay_deterministic_noop() {
        let mut conn = memory_conn();
        let parsed = parse_workflow_spec(include_str!("../../../tests/fixtures/workflow_noop.json"))
            .unwrap();
        let (_report, validated) = validate_workflow(&conn, parsed).unwrap();
        let workflow_run_id = run_validated_workflow(
            &mut conn,
            Path::new(".fa.db"),
            validated.expect("noop fixture validates"),
        )
        .unwrap();
        let replay = replay_workflow(&conn, &workflow_run_id).unwrap();
        assert_eq!(
            replay.get("event").and_then(Value::as_str),
            Some("replay_succeeded")
        );
    }
}
