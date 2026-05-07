WITH
  securities(security_id, cik) AS (
    VALUES
      (1, '0000000001'),
      (2, '0000000002')
  ),
  quarters(idx, start_date, end_date, duration_days, fiscal_year, fiscal_period, fiscal_period_ordinal) AS (
    VALUES
      (1, '2022-07-01', '2022-09-30', 92, 2022, 'Q3', 3),
      (2, '2022-10-01', '2022-12-31', 92, 2022, 'Q4', 4),
      (3, '2023-01-01', '2023-03-31', 90, 2023, 'Q1', 1),
      (4, '2023-04-01', '2023-06-30', 91, 2023, 'Q2', 2),
      (5, '2023-07-01', '2023-09-30', 92, 2023, 'Q3', 3)
  )
INSERT OR IGNORE INTO reporting_periods(
  period_id, cik, accession_number, raw_start_date, raw_end_date, raw_instant_date,
  start_date_inclusive, end_date_exclusive, duration_days, period_kind, period_semantics,
  fiscal_year, fiscal_period, fiscal_period_ordinal, period_length_class, source, created_at)
SELECT
  'fixture-period-' || security_id || '-q' || idx,
  cik,
  'fixture-accession-' || security_id || '-q' || idx,
  start_date,
  end_date,
  NULL,
  start_date,
  end_date,
  duration_days,
  'duration',
  'fiscal_quarter',
  fiscal_year,
  fiscal_period,
  fiscal_period_ordinal,
  '13w',
  'check_fixture',
  strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM securities CROSS JOIN quarters;

WITH securities(security_id, cik) AS (
  VALUES
    (1, '0000000001'),
    (2, '0000000002')
)
INSERT OR IGNORE INTO reporting_periods(
  period_id, cik, accession_number, raw_start_date, raw_end_date, raw_instant_date,
  start_date_inclusive, end_date_exclusive, duration_days, period_kind, period_semantics,
  fiscal_year, fiscal_period, fiscal_period_ordinal, period_length_class, source, created_at)
SELECT
  'fixture-period-' || security_id || '-instant-2023-09-30',
  cik,
  'fixture-accession-' || security_id || '-instant',
  NULL,
  NULL,
  '2023-09-30',
  '2023-09-30',
  '2023-09-30',
  0,
  'instant',
  'instant',
  2023,
  'Q3',
  3,
  'instant',
  'check_fixture',
  strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM securities;

WITH
  revenue(security_id, idx, value_decimal) AS (
    VALUES
      (1, 1, 90.0),
      (1, 2, 95.0),
      (1, 3, 100.0),
      (1, 4, 115.0),
      (1, 5, 125.0),
      (2, 1, 110.0),
      (2, 2, 105.0),
      (2, 3, 100.0),
      (2, 4, 95.0),
      (2, 5, 90.0)
  ),
  metrics(metric_id, metric_name, metric_kind, basis_id, unit_signature, multiplier) AS (
    VALUES
      (1, 'revenue', 'flow', 101, 'USD', 1.00),
      (2, 'net_income', 'flow', 201, 'USD', 0.10),
      (4, 'diluted_shares', 'flow', 401, 'shares', 0.00),
      (5, 'operating_cash_flow', 'flow', 501, 'USD', 0.18),
      (6, 'capex', 'flow', 601, 'USD', 0.04)
  ),
  rows AS (
    SELECT
      r.security_id,
      printf('%010d', r.security_id) AS cik,
      r.idx,
      m.metric_id,
      m.metric_name,
      m.metric_kind,
      m.basis_id,
      m.unit_signature,
      CASE WHEN m.metric_id = 4 THEN 20.0 ELSE r.value_decimal * m.multiplier END AS value_decimal
    FROM revenue r CROSS JOIN metrics m
  )
INSERT OR IGNORE INTO canonical_observations(
  observation_id, observation_hash, resolver_version, security_id, cik, metric_id, metric_name, metric_kind,
  basis_id, period_id, period_semantics, duration_days, fiscal_year, fiscal_period, value_decimal,
  unit_signature, dimensions_hash, dimensional_scope, observation_status, source_fact_id, source_accession,
  taxonomy, concept_qname, selection_reason, quality_flags, quality_flags_json, quality_score,
  accepted_at, available_at, created_at)
SELECT
  'fixture-observation-' || security_id || '-q' || idx || '-m' || metric_id,
  printf('%064d', security_id * 1000 + idx * 10 + metric_id),
  'check_fixture',
  security_id,
  cik,
  metric_id,
  metric_name,
  metric_kind,
  basis_id,
  'fixture-period-' || security_id || '-q' || idx,
  'fiscal_quarter',
  (SELECT duration_days FROM reporting_periods WHERE period_id = 'fixture-period-' || security_id || '-q' || idx),
  (SELECT fiscal_year FROM reporting_periods WHERE period_id = 'fixture-period-' || security_id || '-q' || idx),
  (SELECT fiscal_period FROM reporting_periods WHERE period_id = 'fixture-period-' || security_id || '-q' || idx),
  value_decimal,
  unit_signature,
  '44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',
  'consolidated_total',
  'selected',
  NULL,
  'fixture-accession-' || security_id || '-q' || idx,
  'test',
  'test',
  'check fixture',
  0,
  '[]',
  1.0,
  NULL,
  strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
  strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM rows;

WITH
  rows(security_id, metric_id, metric_name, basis_id, value_decimal) AS (
    VALUES
      (1, 7, 'cash', 701, 50.0),
      (1, 8, 'debt', 801, 10.0),
      (2, 7, 'cash', 701, 50.0),
      (2, 8, 'debt', 801, 10.0)
  )
INSERT OR IGNORE INTO canonical_observations(
  observation_id, observation_hash, resolver_version, security_id, cik, metric_id, metric_name, metric_kind,
  basis_id, period_id, period_semantics, duration_days, fiscal_year, fiscal_period, value_decimal,
  unit_signature, dimensions_hash, dimensional_scope, observation_status, source_fact_id, source_accession,
  taxonomy, concept_qname, selection_reason, quality_flags, quality_flags_json, quality_score,
  accepted_at, available_at, created_at)
SELECT
  'fixture-observation-' || security_id || '-instant-m' || metric_id,
  printf('%064d', security_id * 1000 + metric_id),
  'check_fixture',
  security_id,
  printf('%010d', security_id),
  metric_id,
  metric_name,
  'instant',
  basis_id,
  'fixture-period-' || security_id || '-instant-2023-09-30',
  'instant',
  0,
  2023,
  'Q3',
  value_decimal,
  'USD',
  '44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',
  'consolidated_total',
  'selected',
  NULL,
  'fixture-accession-' || security_id || '-instant',
  'test',
  'test',
  'check fixture',
  0,
  '[]',
  1.0,
  NULL,
  strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
  strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM rows;
