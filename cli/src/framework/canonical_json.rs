use anyhow::Result;
use serde::Serialize;
use serde_json::Value;

pub fn to_canonical_json<T: Serialize + ?Sized>(value: &T) -> Result<String> {
    let value = serde_json::to_value(value)?;
    Ok(canonical_value(&value))
}

pub fn canonical_value(value: &Value) -> String {
    match value {
        Value::Null => "null".to_string(),
        Value::Bool(v) => {
            if *v {
                "true".to_string()
            } else {
                "false".to_string()
            }
        }
        Value::Number(v) => v.to_string(),
        Value::String(v) => serde_json::to_string(v).expect("json string encoding"),
        Value::Array(values) => {
            let inner = values
                .iter()
                .map(canonical_value)
                .collect::<Vec<_>>()
                .join(",");
            format!("[{inner}]")
        }
        Value::Object(map) => {
            let mut entries = map.iter().collect::<Vec<_>>();
            entries.sort_by(|(left, _), (right, _)| left.cmp(right));
            let inner = entries
                .into_iter()
                .map(|(key, value)| {
                    format!(
                        "{}:{}",
                        serde_json::to_string(key).expect("json key encoding"),
                        canonical_value(value)
                    )
                })
                .collect::<Vec<_>>()
                .join(",");
            format!("{{{inner}}}")
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::framework::hash::sha256_hex;
    use serde_json::json;

    #[test]
    fn canonical_json_orders_keys() {
        let left = json!({"b": 2, "a": {"d": 4, "c": 3}});
        let right = json!({"a": {"c": 3, "d": 4}, "b": 2});

        let left_json = to_canonical_json(&left).unwrap();
        let right_json = to_canonical_json(&right).unwrap();

        assert_eq!(left_json, right_json);
        assert_eq!(left_json, r#"{"a":{"c":3,"d":4},"b":2}"#);
        assert_eq!(sha256_hex(left_json.as_bytes()), sha256_hex(right_json.as_bytes()));
    }
}
