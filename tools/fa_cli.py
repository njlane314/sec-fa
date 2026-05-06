#!/usr/bin/env python3
"""sec-fa command-line/service shell.

The C++ core is the authority for deterministic model output and hard risk checks.
This Python shell handles orchestration, SEC retrieval, local persistence, reports,
and guarded mock execution. It intentionally uses only the Python standard library.
"""

from __future__ import annotations

import argparse
import ctypes
import datetime as dt
import gzip
import hashlib
import io
import json
import math
import os
from pathlib import Path
import sqlite3
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
import xml.etree.ElementTree as ET
import zlib
from typing import Any, Iterable

ABI_VERSION = 2
SYMBOL_BYTES = 16
REASON_BYTES = 128
DIAGNOSTIC_BYTES = 1024

METRIC_REVENUE = 1
METRIC_NET_INCOME = 2
METRIC_EPS_DILUTED = 3
METRIC_DILUTED_SHARES = 4
METRIC_OPERATING_CASH_FLOW = 5
METRIC_CAPEX = 6
METRIC_CASH = 7
METRIC_DEBT = 8

SIDE_NONE = 0
SIDE_BUY = 1
SIDE_SELL = 2

DECISION_REJECTED = 0
DECISION_APPROVED = 1

DEFAULT_USER_AGENT = os.environ.get("FA_SEC_USER_AGENT", "sec-fa operator@example.invalid")
SEC_SUBMISSIONS_URL = "https://data.sec.gov/submissions/CIK{cik10}.json"
SEC_COMPANYFACTS_URL = "https://data.sec.gov/api/xbrl/companyfacts/CIK{cik10}.json"
SEC_ARCHIVE_BASE = "https://www.sec.gov/Archives/edgar/data/{cik_int}/{acc_no_dash}"

MAX_XBRL_DOCUMENT_BYTES = 64 * 1024 * 1024
XBRL_INSTANCE_NS = "http://www.xbrl.org/2003/instance"
INLINE_XBRL_NS = "http://www.xbrl.org/2013/inlineXBRL"
XBRLDI_NS = "http://xbrl.org/2006/xbrldi"
XSI_NS = "http://www.w3.org/2001/XMLSchema-instance"

METRIC_KIND_UNKNOWN = 0
METRIC_KIND_FLOW = 1
METRIC_KIND_INSTANT = 2
METRIC_KIND_PER_SHARE_FLOW = 3
METRIC_KIND_RATIO = 4
METRIC_KIND_DERIVED = 5

PERIOD_UNKNOWN = 0
PERIOD_FISCAL_QUARTER = 1
PERIOD_FISCAL_YTD = 2
PERIOD_FISCAL_YEAR = 3
PERIOD_TTM = 4
PERIOD_INSTANT = 5
PERIOD_STUB = 6
PERIOD_TRANSITION = 7
PERIOD_IRREGULAR = 8

OBSERVATION_UNKNOWN = 0
OBSERVATION_SELECTED = 1
OBSERVATION_DERIVED = 2
OBSERVATION_REJECTED = 3
OBSERVATION_SUPERSEDED = 4

QUALITY_NONE = 0
QUALITY_EXTENSION_TAG = 1 << 0
QUALITY_UNIT_CONVERTED = 1 << 1
QUALITY_AMENDED_FILING = 1 << 2
QUALITY_RESTATEMENT = 1 << 3
QUALITY_FISCAL_CHANGE = 1 << 4
QUALITY_MISSING_COMPARABLE = 1 << 5
QUALITY_STALE = 1 << 6
QUALITY_LOW_CONFIDENCE = 1 << 7
QUALITY_PERIOD_YTD = 1 << 8
QUALITY_PERIOD_FISCAL_YEAR = 1 << 9
QUALITY_PERIOD_STUB = 1 << 10
QUALITY_PERIOD_IRREGULAR = 1 << 11
QUALITY_DURATION_MISMATCH = 1 << 12
QUALITY_DURATION_NORMALIZED = 1 << 13
QUALITY_DERIVED = 1 << 14
QUALITY_BASIS_TRANSITION = 1 << 15
QUALITY_DIMENSIONAL_FACT = 1 << 16
QUALITY_AMBIGUOUS_CANDIDATES = 1 << 17
QUALITY_LOW_PRECISION = 1 << 18

STMT_QUALITY_NONE = 0
STMT_MISSING_REVENUE = 1 << 0
STMT_MISSING_SHARES = 1 << 1
STMT_MISSING_CASH = 1 << 2
STMT_MISSING_DEBT = 1 << 3
STMT_NEGATIVE_FCF = 1 << 4
STMT_NON_COMPARABLE_PERIOD = 1 << 5
STMT_AMENDED_OR_RESTATED = 1 << 6
STMT_LOW_CONFIDENCE = 1 << 7

BASIS_UNKNOWN = 0
BASIS_REVENUE_EXCLUDING_TAX = 101
BASIS_REVENUE_INCLUDING_TAX = 102
BASIS_REVENUE_SALES_NET = 103
BASIS_REVENUE_GENERIC = 104
BASIS_NET_INCOME = 201
BASIS_EPS_DILUTED = 301
BASIS_DILUTED_SHARES = 401
BASIS_OPERATING_CASH_FLOW = 501
BASIS_CAPEX = 601
BASIS_CASH = 701
BASIS_DEBT = 801

RESOLVER_VERSION = "canonical_resolver_v3"
TOTAL_DIMENSIONS_JSON = "{}"
TOTAL_DIMENSIONS_HASH = hashlib.sha256(TOTAL_DIMENSIONS_JSON.encode()).hexdigest()
TOTAL_DIMENSIONS_HASH_U64 = int(TOTAL_DIMENSIONS_HASH[:16], 16)

METRIC_NAMES: dict[int, str] = {
    METRIC_REVENUE: "revenue",
    METRIC_NET_INCOME: "net_income",
    METRIC_EPS_DILUTED: "eps_diluted",
    METRIC_DILUTED_SHARES: "diluted_shares",
    METRIC_OPERATING_CASH_FLOW: "operating_cash_flow",
    METRIC_CAPEX: "capex",
    METRIC_CASH: "cash",
    METRIC_DEBT: "debt",
}

DEFAULT_OPERATING_COMPANY_ASSUMPTIONS: dict[str, Any] = {
    "scenario_count": 3,
    "scenarios": [
        {
            "name": "bear",
            "probability_weight": 0.25,
            "revenue_cagr_5y": -0.02,
            "terminal_revenue_growth": 0.00,
            "target_fcf_margin": 0.06,
            "discount_rate": 0.12,
            "terminal_fcf_multiple": 10.0,
        },
        {
            "name": "base",
            "probability_weight": 0.50,
            "revenue_cagr_5y": 0.04,
            "terminal_revenue_growth": 0.02,
            "target_fcf_margin": 0.12,
            "discount_rate": 0.10,
            "terminal_fcf_multiple": 16.0,
        },
        {
            "name": "bull",
            "probability_weight": 0.25,
            "revenue_cagr_5y": 0.10,
            "terminal_revenue_growth": 0.03,
            "target_fcf_margin": 0.18,
            "discount_rate": 0.09,
            "terminal_fcf_multiple": 22.0,
        },
    ],
}

METRIC_KIND_BY_ID: dict[int, str] = {
    METRIC_REVENUE: "flow",
    METRIC_NET_INCOME: "flow",
    METRIC_EPS_DILUTED: "per_share_flow",
    METRIC_DILUTED_SHARES: "flow",
    METRIC_OPERATING_CASH_FLOW: "flow",
    METRIC_CAPEX: "flow",
    METRIC_CASH: "instant",
    METRIC_DEBT: "instant",
}

METRIC_KIND_CODE: dict[str, int] = {
    "unknown": METRIC_KIND_UNKNOWN,
    "flow": METRIC_KIND_FLOW,
    "instant": METRIC_KIND_INSTANT,
    "per_share_flow": METRIC_KIND_PER_SHARE_FLOW,
    "ratio": METRIC_KIND_RATIO,
    "derived": METRIC_KIND_DERIVED,
}

PERIOD_CODE: dict[str, int] = {
    "unknown": PERIOD_UNKNOWN,
    "fiscal_quarter": PERIOD_FISCAL_QUARTER,
    "fiscal_ytd": PERIOD_FISCAL_YTD,
    "fiscal_year": PERIOD_FISCAL_YEAR,
    "trailing_twelve_month": PERIOD_TTM,
    "instant": PERIOD_INSTANT,
    "stub": PERIOD_STUB,
    "transition": PERIOD_TRANSITION,
    "irregular": PERIOD_IRREGULAR,
}

OBSERVATION_STATUS_CODE: dict[str, int] = {
    "selected": OBSERVATION_SELECTED,
    "derived": OBSERVATION_DERIVED,
    "rejected": OBSERVATION_REJECTED,
    "superseded": OBSERVATION_SUPERSEDED,
}

CONCEPT_CANDIDATES: dict[tuple[str, str], dict[str, Any]] = {}
CANONICAL_CONCEPTS: list[dict[str, Any]] = [
    {"metric_id": METRIC_REVENUE, "basis_id": BASIS_REVENUE_EXCLUDING_TAX, "taxonomy": "us-gaap", "concept": "RevenueFromContractWithCustomerExcludingAssessedTax", "priority": 1, "allowed_units": ["USD"]},
    {"metric_id": METRIC_REVENUE, "basis_id": BASIS_REVENUE_INCLUDING_TAX, "taxonomy": "us-gaap", "concept": "RevenueFromContractWithCustomerIncludingAssessedTax", "priority": 2, "allowed_units": ["USD"]},
    {"metric_id": METRIC_REVENUE, "basis_id": BASIS_REVENUE_SALES_NET, "taxonomy": "us-gaap", "concept": "SalesRevenueNet", "priority": 3, "allowed_units": ["USD"]},
    {"metric_id": METRIC_REVENUE, "basis_id": BASIS_REVENUE_GENERIC, "taxonomy": "us-gaap", "concept": "Revenues", "priority": 4, "allowed_units": ["USD"]},
    {"metric_id": METRIC_NET_INCOME, "basis_id": BASIS_NET_INCOME, "taxonomy": "us-gaap", "concept": "NetIncomeLoss", "priority": 1, "allowed_units": ["USD"]},
    {"metric_id": METRIC_NET_INCOME, "basis_id": BASIS_NET_INCOME, "taxonomy": "us-gaap", "concept": "ProfitLoss", "priority": 2, "allowed_units": ["USD"]},
    {"metric_id": METRIC_EPS_DILUTED, "basis_id": BASIS_EPS_DILUTED, "taxonomy": "us-gaap", "concept": "EarningsPerShareDiluted", "priority": 1, "allowed_units": ["USD/shares", "USD / shares"]},
    {"metric_id": METRIC_DILUTED_SHARES, "basis_id": BASIS_DILUTED_SHARES, "taxonomy": "us-gaap", "concept": "WeightedAverageNumberOfDilutedSharesOutstanding", "priority": 1, "allowed_units": ["shares"]},
    {"metric_id": METRIC_OPERATING_CASH_FLOW, "basis_id": BASIS_OPERATING_CASH_FLOW, "taxonomy": "us-gaap", "concept": "NetCashProvidedByUsedInOperatingActivities", "priority": 1, "allowed_units": ["USD"]},
    {"metric_id": METRIC_CAPEX, "basis_id": BASIS_CAPEX, "taxonomy": "us-gaap", "concept": "PaymentsToAcquirePropertyPlantAndEquipment", "priority": 1, "allowed_units": ["USD"]},
    {"metric_id": METRIC_CASH, "basis_id": BASIS_CASH, "taxonomy": "us-gaap", "concept": "CashAndCashEquivalentsAtCarryingValue", "priority": 1, "allowed_units": ["USD"]},
    {"metric_id": METRIC_CASH, "basis_id": BASIS_CASH, "taxonomy": "us-gaap", "concept": "CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents", "priority": 2, "allowed_units": ["USD"]},
    {"metric_id": METRIC_DEBT, "basis_id": BASIS_DEBT, "taxonomy": "us-gaap", "concept": "LongTermDebtAndFinanceLeaseObligationsCurrent", "priority": 1, "allowed_units": ["USD"]},
    {"metric_id": METRIC_DEBT, "basis_id": BASIS_DEBT, "taxonomy": "us-gaap", "concept": "LongTermDebtCurrent", "priority": 2, "allowed_units": ["USD"]},
    {"metric_id": METRIC_DEBT, "basis_id": BASIS_DEBT, "taxonomy": "us-gaap", "concept": "LongTermDebtNoncurrent", "priority": 3, "allowed_units": ["USD"]},
]

for _candidate in CANONICAL_CONCEPTS:
    CONCEPT_CANDIDATES[(_candidate["taxonomy"], _candidate["concept"])] = _candidate


MUTATING_COMMANDS = {
    "init-db", "security-upsert", "position-upsert", "sec-watch", "sec-fetch",
    "facts-companyfacts", "xbrl-parse", "broker-reconcile", "model-run", "model-run-v2", "risk-check",
    "order-stage", "broker-submit", "set-mode", "trading-halt"
}


class FaError(Exception):
    def __init__(self, message: str, exit_code: int = 1) -> None:
        super().__init__(message)
        self.exit_code = exit_code


def eprint(*args: object) -> None:
    print(*args, file=sys.stderr)


def utc_now() -> str:
    return dt.datetime.now(dt.UTC).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def normalize_cik(cik: str | int) -> str:
    digits = "".join(ch for ch in str(cik) if ch.isdigit())
    if not digits:
        raise FaError(f"invalid CIK: {cik!r}")
    if len(digits) > 10:
        raise FaError(f"CIK too long: {cik!r}")
    return digits.zfill(10)


def security_id_from_cik(cik: str | int) -> int:
    return int(normalize_cik(cik))


def parse_date_to_epoch_day(value: str | None) -> int:
    if not value:
        return 0
    try:
        date = dt.date.fromisoformat(value[:10])
    except ValueError:
        return 0
    epoch = dt.date(1970, 1, 1)
    return (date - epoch).days


def parse_instant_to_epoch_s(value: str | None) -> int:
    if not value:
        return 0
    raw = value.strip()
    if not raw:
        return 0
    try:
        if raw.endswith("Z"):
            raw = raw[:-1] + "+00:00"
        if len(raw) == 10:
            parsed = dt.datetime.fromisoformat(raw).replace(tzinfo=dt.UTC)
        else:
            parsed = dt.datetime.fromisoformat(raw)
            if parsed.tzinfo is None:
                parsed = parsed.replace(tzinfo=dt.UTC)
        return int(parsed.timestamp())
    except ValueError:
        return 0


def filed_date_to_available_at(filed: str | None) -> str:
    if not filed:
        return utc_now()
    try:
        parsed = dt.date.fromisoformat(filed[:10])
    except ValueError:
        return utc_now()
    return dt.datetime(parsed.year, parsed.month, parsed.day, 23, 59, 59, tzinfo=dt.UTC).isoformat().replace("+00:00", "Z")


def canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def stable_id(prefix: str, payload: dict[str, Any]) -> str:
    return f"{prefix}-{hashlib.sha256(canonical_json(payload).encode()).hexdigest()}"


def hash64_from_hex(hex_hash: str) -> int:
    return int(hex_hash[:16], 16)


def parse_iso_date(value: str | None) -> dt.date | None:
    if not value:
        return None
    try:
        return dt.date.fromisoformat(value[:10])
    except ValueError:
        return None


def date_plus_one(value: str | None) -> str | None:
    parsed = parse_iso_date(value)
    if parsed is None:
        return None
    return (parsed + dt.timedelta(days=1)).isoformat()


def inclusive_duration_days(start: str | None, end: str | None) -> int:
    start_date = parse_iso_date(start)
    end_date = parse_iso_date(end)
    if start_date is None or end_date is None or end_date < start_date:
        return 0
    return (end_date - start_date).days + 1


def fiscal_period_ordinal(fp: str | None) -> int | None:
    mapping = {"Q1": 1, "Q2": 2, "Q3": 3, "Q4": 4, "FY": 4}
    return mapping.get((fp or "").upper())


def period_length_class(duration_days: int, semantics: str) -> str:
    if semantics == "instant":
        return "instant"
    if duration_days <= 0:
        return "unknown"
    if 84 <= duration_days <= 94:
        return "13w"
    if 95 <= duration_days <= 101:
        return "14w"
    if 350 <= duration_days <= 371:
        return "52w"
    if 372 <= duration_days <= 378:
        return "53w"
    if semantics == "stub":
        return "stub"
    return "irregular"


def classify_period(metric_id: int, item: dict[str, Any]) -> dict[str, Any] | None:
    end = item.get("end") if isinstance(item.get("end"), str) else None
    start = item.get("start") if isinstance(item.get("start"), str) else None
    fy = item.get("fy") if isinstance(item.get("fy"), int) else None
    fp = str(item.get("fp") or "").upper() or None
    metric_kind = METRIC_KIND_BY_ID.get(metric_id, "unknown")

    if metric_kind == "instant" or (metric_kind == "unknown" and start is None and end is not None):
        if end is None:
            return None
        return {
            "period_kind": "instant",
            "period_semantics": "instant",
            "raw_start_date": None,
            "raw_end_date": None,
            "raw_instant_date": end[:10],
            "start_date_inclusive": end[:10],
            "end_date_exclusive": date_plus_one(end),
            "duration_days": 0,
            "fiscal_year": fy,
            "fiscal_period": fp,
            "fiscal_period_ordinal": fiscal_period_ordinal(fp),
        }

    if start is None or end is None:
        return None
    duration = inclusive_duration_days(start, end)
    if duration <= 0:
        return None

    if fp == "FY" or duration >= 320:
        semantics = "fiscal_year"
    elif fp in {"Q2", "Q3", "Q4"} and duration > 120:
        semantics = "fiscal_ytd"
    elif 70 <= duration <= 110:
        semantics = "fiscal_quarter"
    elif duration < 70:
        semantics = "stub"
    else:
        semantics = "irregular"

    return {
        "period_kind": "duration",
        "period_semantics": semantics,
        "raw_start_date": start[:10],
        "raw_end_date": end[:10],
        "raw_instant_date": None,
        "start_date_inclusive": start[:10],
        "end_date_exclusive": date_plus_one(end),
        "duration_days": duration,
        "fiscal_year": fy,
        "fiscal_period": fp,
        "fiscal_period_ordinal": fiscal_period_ordinal(fp),
    }


def period_quality_flags(semantics: str, duration_days: int, form: str, accession: str | None) -> int:
    flags = QUALITY_NONE
    if semantics == "fiscal_ytd":
        flags |= QUALITY_PERIOD_YTD
    elif semantics == "fiscal_year":
        flags |= QUALITY_PERIOD_FISCAL_YEAR
    elif semantics == "stub":
        flags |= QUALITY_PERIOD_STUB
    elif semantics == "irregular":
        flags |= QUALITY_PERIOD_IRREGULAR
    if semantics == "fiscal_quarter" and not (70 <= duration_days <= 110):
        flags |= QUALITY_DURATION_MISMATCH
    if form.endswith("/A") or (accession and accession.endswith("A")):
        flags |= QUALITY_AMENDED_FILING
    return flags


def quality_flag_names(flags: int) -> list[str]:
    known = [
        (QUALITY_EXTENSION_TAG, "extension_tag"),
        (QUALITY_UNIT_CONVERTED, "unit_converted"),
        (QUALITY_AMENDED_FILING, "amended_filing"),
        (QUALITY_RESTATEMENT, "restatement"),
        (QUALITY_FISCAL_CHANGE, "fiscal_change"),
        (QUALITY_MISSING_COMPARABLE, "missing_comparable"),
        (QUALITY_STALE, "stale"),
        (QUALITY_LOW_CONFIDENCE, "low_confidence"),
        (QUALITY_PERIOD_YTD, "period_ytd"),
        (QUALITY_PERIOD_FISCAL_YEAR, "period_fiscal_year"),
        (QUALITY_PERIOD_STUB, "period_stub"),
        (QUALITY_PERIOD_IRREGULAR, "period_irregular"),
        (QUALITY_DURATION_MISMATCH, "duration_mismatch"),
        (QUALITY_DURATION_NORMALIZED, "duration_normalized"),
        (QUALITY_DERIVED, "derived"),
        (QUALITY_BASIS_TRANSITION, "basis_transition"),
        (QUALITY_DIMENSIONAL_FACT, "dimensional_fact"),
        (QUALITY_AMBIGUOUS_CANDIDATES, "ambiguous_candidates"),
        (QUALITY_LOW_PRECISION, "low_precision"),
    ]
    return [name for bit, name in known if flags & bit]

def json_line(value: Any) -> None:
    print(canonical_json(value))


def open_db(path: str) -> sqlite3.Connection:
    conn = sqlite3.connect(path)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = ON")
    return conn


def ensure_db(conn: sqlite3.Connection) -> None:
    try:
        conn.execute("SELECT 1 FROM settings LIMIT 1")
    except sqlite3.Error as exc:
        raise FaError("database is not initialized; run `fa init-db` first") from exc


def append_event(conn: sqlite3.Connection, event_type: str, payload: dict[str, Any], version: int = 1) -> dict[str, Any]:
    event = {
        "event_id": str(uuid.uuid4()),
        "event_type": event_type,
        "event_version": version,
        "occurred_at": utc_now(),
        "payload": payload,
    }
    payload_json = canonical_json(payload)
    conn.execute(
        """
        INSERT INTO events(event_id, event_type, event_version, occurred_at, payload_json, payload_sha256)
        VALUES (?, ?, ?, ?, ?, ?)
        """,
        (event["event_id"], event_type, version, event["occurred_at"], payload_json, hashlib.sha256(payload_json.encode()).hexdigest()),
    )
    return event


def require_user_agent(user_agent: str) -> str:
    user_agent = user_agent.strip()
    if not user_agent or user_agent == DEFAULT_USER_AGENT:
        eprint("warning: set a real SEC User-Agent identifying operator/contact before production use")
    return user_agent


def http_get_bytes(url: str, user_agent: str, timeout_s: float = 30.0) -> bytes:
    headers = {
        "User-Agent": require_user_agent(user_agent),
        "Accept-Encoding": "gzip, deflate",
        "Accept": "application/json,text/html,application/xhtml+xml,text/plain,*/*",
    }
    req = urllib.request.Request(url, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout_s) as resp:
            data = resp.read()
            encoding = resp.headers.get("Content-Encoding", "").lower()
            if encoding == "gzip":
                return gzip.decompress(data)
            if encoding == "deflate":
                return zlib.decompress(data)
            return data
    except urllib.error.HTTPError as exc:
        raise FaError(f"HTTP {exc.code} for {url}", 2) from exc
    except urllib.error.URLError as exc:
        raise FaError(f"network error for {url}: {exc}", 2) from exc


def http_get_json(url: str, user_agent: str, timeout_s: float = 30.0) -> Any:
    data = http_get_bytes(url, user_agent, timeout_s)
    try:
        return json.loads(data.decode("utf-8"))
    except json.JSONDecodeError as exc:
        raise FaError(f"invalid JSON from {url}: {exc}", 2) from exc


def save_raw_bytes(root: Path, parts: Iterable[str], data: bytes) -> tuple[str, str]:
    path = root.joinpath(*parts)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_bytes(data)
    tmp.replace(path)
    return str(path), sha256_bytes(data)


def accession_archive_url(cik: str, accession_number: str, document: str | None = None) -> str:
    cik_int = str(int(normalize_cik(cik)))
    acc_no_dash = accession_number.replace("-", "")
    base = SEC_ARCHIVE_BASE.format(cik_int=cik_int, acc_no_dash=acc_no_dash)
    if document:
        return f"{base}/{urllib.parse.quote(document)}"
    return f"{base}/index.json"


def db_schema_path() -> Path:
    return Path(__file__).resolve().parents[1] / "db" / "sqlite" / "001_schema.sql"


class FaCanonicalFact(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("security_id", ctypes.c_uint64),
        ("metric_id", ctypes.c_int32),
        ("value", ctypes.c_double),
        ("period_start_day", ctypes.c_int64),
        ("period_end_day", ctypes.c_int64),
        ("available_at_epoch_s", ctypes.c_int64),
        ("quality_flags", ctypes.c_uint32),
        ("metric_kind", ctypes.c_uint32),
        ("period_semantics", ctypes.c_uint32),
        ("basis_id", ctypes.c_uint32),
        ("observation_status", ctypes.c_uint32),
        ("duration_days", ctypes.c_uint32),
        ("dimensions_hash", ctypes.c_uint64),
    ]


class FaSecurity(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("security_id", ctypes.c_uint64),
        ("symbol", ctypes.c_char * SYMBOL_BYTES),
        ("investable", ctypes.c_uint8),
        ("price_usd", ctypes.c_double),
        ("adv_usd", ctypes.c_double),
    ]


class FaPosition(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("security_id", ctypes.c_uint64),
        ("quantity_shares", ctypes.c_double),
        ("market_value_usd", ctypes.c_double),
        ("weight_ratio", ctypes.c_double),
    ]


class FaModelConfig(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("target_gross_exposure_ratio", ctypes.c_double),
        ("max_name_weight_ratio", ctypes.c_double),
        ("min_expected_return_proxy", ctypes.c_double),
        ("min_abs_order_notional_usd", ctypes.c_double),
        ("max_forecast_abs_growth_ratio", ctypes.c_double),
        ("max_fact_age_s", ctypes.c_int64),
    ]


class FaRiskLimits(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("portfolio_value_usd", ctypes.c_double),
        ("cash_usd", ctypes.c_double),
        ("max_name_weight_ratio", ctypes.c_double),
        ("max_order_notional_usd", ctypes.c_double),
        ("min_adv_usd", ctypes.c_double),
        ("max_adv_participation_ratio", ctypes.c_double),
        ("min_abs_order_notional_usd", ctypes.c_double),
        ("broker_reconciled", ctypes.c_uint8),
        ("reconciliation_checked_at_epoch_s", ctypes.c_int64),
        ("now_epoch_s", ctypes.c_int64),
        ("max_reconciliation_age_s", ctypes.c_int64),
    ]


class FaForecast(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("security_id", ctypes.c_uint64),
        ("revenue_growth_ratio", ctypes.c_double),
        ("earnings_growth_ratio", ctypes.c_double),
        ("expected_return_proxy", ctypes.c_double),
        ("confidence_ratio", ctypes.c_double),
        ("quality_flags", ctypes.c_uint32),
        ("reason", ctypes.c_char * REASON_BYTES),
    ]


class FaTargetWeight(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("security_id", ctypes.c_uint64),
        ("current_weight_ratio", ctypes.c_double),
        ("target_weight_ratio", ctypes.c_double),
        ("delta_weight_ratio", ctypes.c_double),
        ("reason", ctypes.c_char * REASON_BYTES),
    ]


class FaOrderIntent(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("security_id", ctypes.c_uint64),
        ("side", ctypes.c_int32),
        ("notional_usd", ctypes.c_double),
        ("current_weight_ratio", ctypes.c_double),
        ("target_weight_ratio", ctypes.c_double),
        ("reason", ctypes.c_char * REASON_BYTES),
    ]


class FaStatementSnapshot(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("security_id", ctypes.c_uint64),
        ("period_start_day", ctypes.c_int64),
        ("period_end_day", ctypes.c_int64),
        ("available_at_epoch_s", ctypes.c_int64),
        ("is_ttm", ctypes.c_uint8),
        ("revenue_usd", ctypes.c_double),
        ("net_income_usd", ctypes.c_double),
        ("diluted_eps_usd", ctypes.c_double),
        ("diluted_shares", ctypes.c_double),
        ("operating_cash_flow_usd", ctypes.c_double),
        ("capex_usd", ctypes.c_double),
        ("free_cash_flow_usd", ctypes.c_double),
        ("cash_usd", ctypes.c_double),
        ("debt_usd", ctypes.c_double),
        ("net_debt_usd", ctypes.c_double),
        ("quality_flags", ctypes.c_uint32),
    ]


class FaValuationScenario(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("revenue_cagr_5y", ctypes.c_double),
        ("terminal_revenue_growth", ctypes.c_double),
        ("target_fcf_margin", ctypes.c_double),
        ("discount_rate", ctypes.c_double),
        ("terminal_fcf_multiple", ctypes.c_double),
        ("probability_weight", ctypes.c_double),
    ]


class FaValuation(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("security_id", ctypes.c_uint64),
        ("current_price_usd", ctypes.c_double),
        ("intrinsic_value_per_share_usd", ctypes.c_double),
        ("expected_return_ratio", ctypes.c_double),
        ("market_cap_usd", ctypes.c_double),
        ("enterprise_value_usd", ctypes.c_double),
        ("fcf_yield_ratio", ctypes.c_double),
        ("net_debt_usd", ctypes.c_double),
        ("confidence_ratio", ctypes.c_double),
        ("quality_flags", ctypes.c_uint32),
        ("reason", ctypes.c_char * REASON_BYTES),
    ]


class FaModelOutput(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("forecast_count", ctypes.c_size_t),
        ("forecasts", ctypes.POINTER(FaForecast)),
        ("target_weight_count", ctypes.c_size_t),
        ("target_weights", ctypes.POINTER(FaTargetWeight)),
        ("order_intent_count", ctypes.c_size_t),
        ("order_intents", ctypes.POINTER(FaOrderIntent)),
        ("diagnostics", ctypes.c_char * DIAGNOSTIC_BYTES),
    ]


class FaModelOutputV2(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("valuation_count", ctypes.c_size_t),
        ("valuations", ctypes.POINTER(FaValuation)),
        ("target_weight_count", ctypes.c_size_t),
        ("target_weights", ctypes.POINTER(FaTargetWeight)),
        ("order_intent_count", ctypes.c_size_t),
        ("order_intents", ctypes.POINTER(FaOrderIntent)),
        ("diagnostics", ctypes.c_char * DIAGNOSTIC_BYTES),
    ]


class FaRiskDecision(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("security_id", ctypes.c_uint64),
        ("side", ctypes.c_int32),
        ("decision", ctypes.c_int32),
        ("notional_usd", ctypes.c_double),
        ("reason", ctypes.c_char * REASON_BYTES),
    ]


class FaRiskOutput(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("decision_count", ctypes.c_size_t),
        ("decisions", ctypes.POINTER(FaRiskDecision)),
        ("diagnostics", ctypes.c_char * DIAGNOSTIC_BYTES),
    ]


def decode_c_string(value: bytes | ctypes.Array[Any]) -> str:
    return bytes(value).split(b"\0", 1)[0].decode("utf-8", errors="replace")


def load_core(path: str) -> ctypes.CDLL:
    lib_path = Path(path)
    if not lib_path.exists():
        raise FaError(f"core library not found: {path}; run `make build` first", 2)
    lib = ctypes.CDLL(str(lib_path))
    lib.fa_model_run_v1.argtypes = [
        ctypes.POINTER(FaCanonicalFact), ctypes.c_size_t,
        ctypes.POINTER(FaSecurity), ctypes.c_size_t,
        ctypes.POINTER(FaPosition), ctypes.c_size_t,
        ctypes.POINTER(FaModelConfig), ctypes.POINTER(FaRiskLimits), ctypes.POINTER(FaModelOutput),
    ]
    lib.fa_model_run_v1.restype = ctypes.c_int
    lib.fa_model_output_free_v1.argtypes = [ctypes.POINTER(FaModelOutput)]
    lib.fa_model_output_free_v1.restype = None
    lib.fa_model_run_v2.argtypes = [
        ctypes.POINTER(FaStatementSnapshot), ctypes.c_size_t,
        ctypes.POINTER(FaSecurity), ctypes.c_size_t,
        ctypes.POINTER(FaPosition), ctypes.c_size_t,
        ctypes.POINTER(FaValuationScenario), ctypes.c_size_t,
        ctypes.POINTER(FaModelConfig), ctypes.POINTER(FaRiskLimits), ctypes.POINTER(FaModelOutputV2),
    ]
    lib.fa_model_run_v2.restype = ctypes.c_int
    lib.fa_model_output_free_v2.argtypes = [ctypes.POINTER(FaModelOutputV2)]
    lib.fa_model_output_free_v2.restype = None
    lib.fa_risk_check_v1.argtypes = [
        ctypes.POINTER(FaOrderIntent), ctypes.c_size_t,
        ctypes.POINTER(FaSecurity), ctypes.c_size_t,
        ctypes.POINTER(FaRiskLimits), ctypes.POINTER(FaRiskOutput),
    ]
    lib.fa_risk_check_v1.restype = ctypes.c_int
    lib.fa_risk_output_free_v1.argtypes = [ctypes.POINTER(FaRiskOutput)]
    lib.fa_risk_output_free_v1.restype = None
    lib.fa_status_name.argtypes = [ctypes.c_int]
    lib.fa_status_name.restype = ctypes.c_char_p
    return lib


def core_status_name(lib: ctypes.CDLL, code: int) -> str:
    raw = lib.fa_status_name(code)
    return raw.decode("utf-8", errors="replace") if raw else f"status_{code}"


def make_security(row: sqlite3.Row) -> FaSecurity:
    sec = FaSecurity()
    sec.abi_version = ABI_VERSION
    sec.security_id = int(row["security_id"])
    symbol = str(row["symbol"] or "")[: SYMBOL_BYTES - 1]
    sec.symbol = symbol.encode("ascii", errors="ignore")
    sec.investable = 1 if int(row["investable"]) else 0
    sec.price_usd = float(row["price_usd"] or 0.0)
    sec.adv_usd = float(row["adv_usd"] or 0.0)
    return sec


def make_fact(row: sqlite3.Row) -> FaCanonicalFact:
    fact = FaCanonicalFact()
    fact.abi_version = ABI_VERSION
    fact.security_id = int(row["security_id"])
    fact.metric_id = int(row["metric_id"])
    fact.value = float(row["value_decimal"])
    fact.period_start_day = parse_date_to_epoch_day(row["raw_start_date"] or row["raw_instant_date"])
    fact.period_end_day = parse_date_to_epoch_day(row["raw_end_date"] or row["raw_instant_date"])
    fact.available_at_epoch_s = parse_instant_to_epoch_s(row["available_at"])
    fact.quality_flags = int(row["quality_flags"] or 0)
    fact.metric_kind = METRIC_KIND_CODE.get(str(row["metric_kind"] or "unknown"), METRIC_KIND_UNKNOWN)
    fact.period_semantics = PERIOD_CODE.get(str(row["period_semantics"] or "unknown"), PERIOD_UNKNOWN)
    fact.basis_id = int(row["basis_id"] or BASIS_UNKNOWN)
    fact.observation_status = OBSERVATION_STATUS_CODE.get(str(row["observation_status"] or ""), OBSERVATION_UNKNOWN)
    fact.duration_days = int(row["duration_days"] or 0)
    fact.dimensions_hash = hash64_from_hex(str(row["dimensions_hash"] or TOTAL_DIMENSIONS_HASH))
    return fact


def make_position(row: sqlite3.Row) -> FaPosition:
    pos = FaPosition()
    pos.abi_version = ABI_VERSION
    pos.security_id = int(row["security_id"])
    pos.quantity_shares = float(row["quantity_shares"] or 0.0)
    pos.market_value_usd = float(row["market_value_usd"] or 0.0)
    pos.weight_ratio = float(row["weight_ratio"] or 0.0)
    return pos


def safe_float(value: object | None) -> float:
    if value is None:
        return 0.0
    try:
        out = float(value)
    except (TypeError, ValueError):
        return 0.0
    return out if math.isfinite(out) else 0.0


def make_statement_snapshot(snapshot: dict[str, Any]) -> FaStatementSnapshot:
    out = FaStatementSnapshot()
    out.abi_version = ABI_VERSION
    out.security_id = int(snapshot["security_id"])
    out.period_start_day = parse_date_to_epoch_day(snapshot.get("period_start_date"))
    out.period_end_day = parse_date_to_epoch_day(snapshot.get("period_end_date"))
    out.available_at_epoch_s = parse_instant_to_epoch_s(str(snapshot.get("available_at") or ""))
    out.is_ttm = 1 if int(snapshot.get("is_ttm") or 0) else 0
    out.revenue_usd = safe_float(snapshot.get("revenue_usd"))
    out.net_income_usd = safe_float(snapshot.get("net_income_usd"))
    out.diluted_eps_usd = safe_float(snapshot.get("diluted_eps_usd"))
    out.diluted_shares = safe_float(snapshot.get("diluted_shares"))
    out.operating_cash_flow_usd = safe_float(snapshot.get("operating_cash_flow_usd"))
    out.capex_usd = safe_float(snapshot.get("capex_usd"))
    out.free_cash_flow_usd = safe_float(snapshot.get("free_cash_flow_usd"))
    out.cash_usd = safe_float(snapshot.get("cash_usd"))
    out.debt_usd = safe_float(snapshot.get("debt_usd"))
    out.net_debt_usd = safe_float(snapshot.get("net_debt_usd"))
    out.quality_flags = int(snapshot.get("quality_flags") or 0)
    return out


def make_valuation_scenario(scenario: dict[str, Any]) -> FaValuationScenario:
    out = FaValuationScenario()
    out.abi_version = ABI_VERSION
    out.revenue_cagr_5y = safe_float(scenario.get("revenue_cagr_5y"))
    out.terminal_revenue_growth = safe_float(scenario.get("terminal_revenue_growth"))
    out.target_fcf_margin = safe_float(scenario.get("target_fcf_margin"))
    out.discount_rate = safe_float(scenario.get("discount_rate"))
    out.terminal_fcf_multiple = safe_float(scenario.get("terminal_fcf_multiple"))
    out.probability_weight = safe_float(scenario.get("probability_weight"))
    return out


def make_risk_limits(args: argparse.Namespace, conn: sqlite3.Connection) -> FaRiskLimits:
    row = conn.execute("SELECT * FROM broker_state WHERE id = 1").fetchone()
    limits = FaRiskLimits()
    limits.abi_version = ABI_VERSION
    limits.portfolio_value_usd = float(getattr(args, "portfolio_value_usd", None) or (row["portfolio_value_usd"] if row else 1.0))
    limits.cash_usd = float(getattr(args, "cash_usd", None) or (row["cash_usd"] if row else 0.0))
    limits.max_name_weight_ratio = float(getattr(args, "max_name_weight_ratio", 0.05))
    limits.max_order_notional_usd = float(getattr(args, "max_order_notional_usd", 10000.0))
    limits.min_adv_usd = float(getattr(args, "min_adv_usd", 1000000.0))
    limits.max_adv_participation_ratio = float(getattr(args, "max_adv_participation_ratio", 0.01))
    limits.min_abs_order_notional_usd = float(getattr(args, "min_abs_order_notional_usd", 100.0))
    limits.broker_reconciled = 1 if row and int(row["reconciled"]) else 0
    limits.reconciliation_checked_at_epoch_s = parse_instant_to_epoch_s(row["checked_at"] if row else None)
    limits.now_epoch_s = int(dt.datetime.now(dt.UTC).timestamp())
    limits.max_reconciliation_age_s = int(float(getattr(args, "max_reconciliation_age_s", 3600)))
    return limits


def side_to_text(side: int) -> str:
    if side == SIDE_BUY:
        return "buy"
    if side == SIDE_SELL:
        return "sell"
    return "none"


def side_from_text(side: str) -> int:
    if side == "buy":
        return SIDE_BUY
    if side == "sell":
        return SIDE_SELL
    return SIDE_NONE


def cmd_init_db(args: argparse.Namespace) -> int:
    db_path = Path(args.db)
    db_path.parent.mkdir(parents=True, exist_ok=True)
    conn = open_db(str(db_path))
    conn.executescript(db_schema_path().read_text())
    with conn:
        seed_metric_concept_candidates(conn)
        insert_dimension_signature(conn)
    conn.commit()
    json_line({"db": str(db_path), "status": "initialized"})
    return 0


def cmd_security_upsert(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    cik10 = normalize_cik(args.cik)
    security_id = int(cik10)
    now = utc_now()
    with conn:
        conn.execute(
            """
            INSERT INTO securities(security_id, cik, symbol, investable, price_usd, adv_usd, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(security_id) DO UPDATE SET
              cik=excluded.cik,
              symbol=excluded.symbol,
              investable=excluded.investable,
              price_usd=excluded.price_usd,
              adv_usd=excluded.adv_usd,
              updated_at=excluded.updated_at
            """,
            (security_id, cik10, args.symbol.upper(), int(args.investable), args.price_usd, args.adv_usd, now),
        )
        event = append_event(conn, "security_upserted", {
            "security_id": security_id,
            "cik": cik10,
            "symbol": args.symbol.upper(),
            "investable": bool(args.investable),
            "price_usd": args.price_usd,
            "adv_usd": args.adv_usd,
        })
    json_line(event)
    return 0


def cmd_position_upsert(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    security_id = security_id_from_cik(args.cik)
    now = utc_now()
    with conn:
        conn.execute(
            """
            INSERT INTO positions(security_id, quantity_shares, market_value_usd, weight_ratio, updated_at)
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(security_id) DO UPDATE SET
              quantity_shares=excluded.quantity_shares,
              market_value_usd=excluded.market_value_usd,
              weight_ratio=excluded.weight_ratio,
              updated_at=excluded.updated_at
            """,
            (security_id, args.quantity_shares, args.market_value_usd, args.weight_ratio, now),
        )
        event = append_event(conn, "position_upserted", {
            "security_id": security_id,
            "quantity_shares": args.quantity_shares,
            "market_value_usd": args.market_value_usd,
            "weight_ratio": args.weight_ratio,
        })
    json_line(event)
    return 0


def cmd_sec_watch(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    forms = {form.strip().upper() for form in args.forms.split(",") if form.strip()}
    since_epoch = parse_instant_to_epoch_s(args.since) if args.since else 0
    emitted = 0
    user_agent = args.user_agent
    with conn:
        for cik in args.cik:
            cik10 = normalize_cik(cik)
            url = SEC_SUBMISSIONS_URL.format(cik10=cik10)
            submissions = http_get_json(url, user_agent)
            recent = submissions.get("filings", {}).get("recent", {})
            accessions = recent.get("accessionNumber", [])
            forms_arr = recent.get("form", [])
            filing_dates = recent.get("filingDate", [])
            accepted = recent.get("acceptanceDateTime", [])
            primary_docs = recent.get("primaryDocument", [])
            count = min(len(accessions), args.limit)
            for i in range(count):
                form = str(forms_arr[i]).upper() if i < len(forms_arr) else ""
                if forms and form not in forms:
                    continue
                filing_date = str(filing_dates[i]) if i < len(filing_dates) else None
                accepted_at_raw = str(accepted[i]) if i < len(accepted) else None
                accepted_at = None
                if accepted_at_raw:
                    # SEC format is often YYYY-MM-DDTHH:MM:SS.000Z or without zone.
                    accepted_at = accepted_at_raw if accepted_at_raw.endswith("Z") else accepted_at_raw + "Z"
                if since_epoch:
                    event_epoch = parse_instant_to_epoch_s(accepted_at) or parse_instant_to_epoch_s(filing_date)
                    if event_epoch and event_epoch < since_epoch:
                        continue
                accession_number = str(accessions[i])
                primary_document = str(primary_docs[i]) if i < len(primary_docs) else None
                source_url = accession_archive_url(cik10, accession_number, primary_document) if primary_document else accession_archive_url(cik10, accession_number)
                conn.execute(
                    """
                    INSERT INTO filings(accession_number, cik, form, filing_date, accepted_at, primary_document, source_url, ingested_at)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                    ON CONFLICT(accession_number) DO UPDATE SET
                      form=excluded.form,
                      filing_date=excluded.filing_date,
                      accepted_at=excluded.accepted_at,
                      primary_document=excluded.primary_document,
                      source_url=excluded.source_url
                    """,
                    (accession_number, cik10, form, filing_date, accepted_at, primary_document, source_url, utc_now()),
                )
                event = append_event(conn, "filing_seen", {
                    "cik": cik10,
                    "accession_number": accession_number,
                    "form": form,
                    "filing_date": filing_date,
                    "accepted_at": accepted_at,
                    "primary_document": primary_document,
                    "source_url": source_url,
                })
                json_line(event)
                emitted += 1
            time.sleep(args.sleep_s)
    eprint(f"filing_seen emitted={emitted}")
    return 0


def cmd_sec_fetch(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    raw_root = Path(args.raw_root)
    if args.accession:
        rows = conn.execute("SELECT * FROM filings WHERE accession_number = ?", (args.accession,)).fetchall()
    else:
        rows = conn.execute(
            "SELECT * FROM filings WHERE raw_index_uri IS NULL OR raw_primary_uri IS NULL ORDER BY filing_date DESC LIMIT ?",
            (args.limit,),
        ).fetchall()
    fetched = 0
    with conn:
        for row in rows:
            cik = row["cik"]
            accession = row["accession_number"]
            acc_no_dash = accession.replace("-", "")
            index_url = accession_archive_url(cik, accession)
            index_bytes = http_get_bytes(index_url, args.user_agent)
            index_uri, index_hash = save_raw_bytes(raw_root, ["sec", f"cik={cik}", f"accession={accession}", "index.json"], index_bytes)
            primary_uri = None
            primary_hash = None
            if row["primary_document"]:
                primary_url = accession_archive_url(cik, accession, row["primary_document"])
                primary_bytes = http_get_bytes(primary_url, args.user_agent)
                primary_uri, primary_hash = save_raw_bytes(raw_root, ["sec", f"cik={cik}", f"accession={accession}", row["primary_document"]], primary_bytes)
            combined_hash = hashlib.sha256((index_hash + (primary_hash or "")).encode()).hexdigest()
            conn.execute(
                """
                UPDATE filings
                SET raw_index_uri=?, raw_primary_uri=?, raw_sha256=?, ingested_at=?
                WHERE accession_number=?
                """,
                (index_uri, primary_uri, combined_hash, utc_now(), accession),
            )
            event = append_event(conn, "filing_fetched", {
                "cik": cik,
                "accession_number": accession,
                "archive_path": f"{cik}/{acc_no_dash}",
                "raw_index_uri": index_uri,
                "raw_primary_uri": primary_uri,
                "raw_sha256": combined_hash,
            })
            json_line(event)
            fetched += 1
            time.sleep(args.sleep_s)
    eprint(f"filing_fetched emitted={fetched}")
    return 0


def seed_metric_concept_candidates(conn: sqlite3.Connection) -> None:
    for candidate in CANONICAL_CONCEPTS:
        candidate_id = stable_id("mcc", {
            "metric_id": candidate["metric_id"],
            "basis_id": candidate["basis_id"],
            "taxonomy": candidate["taxonomy"],
            "concept": candidate["concept"],
        })
        conn.execute(
            """
            INSERT OR IGNORE INTO metric_concept_candidates(
              candidate_id, metric_id, basis_id, taxonomy, concept_qname, priority,
              allowed_units_json, dimension_policy, allowed_forms_json, notes
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                candidate_id,
                candidate["metric_id"],
                candidate["basis_id"],
                candidate["taxonomy"],
                candidate["concept"],
                candidate["priority"],
                canonical_json(candidate["allowed_units"]),
                "consolidated_total_only",
                canonical_json(["10-K", "10-Q", "10-K/A", "10-Q/A"]),
                "seeded by fa_cli constants",
            ),
        )


def source_document_id(cik: str, url: str, sha256: str) -> str:
    return stable_id("doc", {"cik": cik, "url": url, "sha256": sha256})


def context_id_for_fact(raw: dict[str, Any]) -> str:
    return stable_id("ctx", {
        "cik": raw["cik"],
        "accession_number": raw.get("accession_number"),
        "raw_context_id": raw.get("raw_context_id"),
        "raw_start_date": raw["period"]["raw_start_date"],
        "raw_end_date": raw["period"]["raw_end_date"],
        "raw_instant_date": raw["period"]["raw_instant_date"],
        "fiscal_year": raw["period"].get("fiscal_year"),
        "fiscal_period": raw["period"].get("fiscal_period"),
        "dimensions_hash": raw.get("dimensions_hash", TOTAL_DIMENSIONS_HASH),
    })


def unit_id_for_fact(raw: dict[str, Any]) -> str | None:
    if not raw.get("unit_signature"):
        return None
    return stable_id("unit", {
        "cik": raw["cik"],
        "accession_number": raw.get("accession_number"),
        "raw_unit_id": raw.get("raw_unit_id"),
        "unit_signature": raw["unit_signature"],
    })


def period_id_for(period: dict[str, Any], cik: str, accession: str | None) -> str:
    return stable_id("period", {
        "cik": cik,
        "accession_number": accession,
        "raw_start_date": period["raw_start_date"],
        "raw_end_date": period["raw_end_date"],
        "raw_instant_date": period["raw_instant_date"],
        "period_semantics": period["period_semantics"],
        "fiscal_year": period.get("fiscal_year"),
        "fiscal_period": period.get("fiscal_period"),
    })


def raw_fact_id(raw: dict[str, Any]) -> str:
    return stable_id("fact", {
        "security_id": raw["security_id"],
        "taxonomy": raw["taxonomy"],
        "concept": raw["concept_qname"],
        "unit": raw.get("unit_signature"),
        "value_decimal": raw.get("value_decimal"),
        "value_text": raw.get("value_text"),
        "accession_number": raw.get("accession_number"),
        "period_id": raw["period_id"],
        "context_id": raw.get("context_id"),
        "raw_context_id": raw.get("raw_context_id"),
        "dimensions_hash": raw.get("dimensions_hash"),
        "form": raw.get("form"),
    })


def observation_id_for(obs: dict[str, Any]) -> str:
    return stable_id("obs", {
        "resolver_version": obs["resolver_version"],
        "security_id": obs["security_id"],
        "metric_id": obs["metric_id"],
        "basis_id": obs["basis_id"],
        "period_id": obs["period_id"],
        "dimensions_hash": obs["dimensions_hash"],
        "source_fact_id": obs.get("source_fact_id"),
        "observation_status": obs["observation_status"],
    })


def insert_dimension_signature_row(
    conn: sqlite3.Connection,
    dimensions_hash: str,
    dimensions_json: str,
    scope_class: str,
    axis_count: int,
) -> None:
    conn.execute(
        """
        INSERT OR IGNORE INTO dimension_signatures(
          dimensions_hash, dimensions_json, has_dimensions, scope_class, axis_count, created_at
        ) VALUES (?, ?, ?, ?, ?, ?)
        """,
        (dimensions_hash, dimensions_json, 1 if axis_count > 0 else 0, scope_class, axis_count, utc_now()),
    )


def insert_dimension_signature(conn: sqlite3.Connection) -> None:
    insert_dimension_signature_row(conn, TOTAL_DIMENSIONS_HASH, TOTAL_DIMENSIONS_JSON, "consolidated_total", 0)


def insert_reporting_period(conn: sqlite3.Connection, raw: dict[str, Any]) -> None:
    period = raw["period"]
    conn.execute(
        """
        INSERT OR IGNORE INTO reporting_periods(
          period_id, cik, accession_number, raw_start_date, raw_end_date, raw_instant_date,
          start_date_inclusive, end_date_exclusive, duration_days, period_kind, period_semantics,
          fiscal_year, fiscal_period, fiscal_period_ordinal, period_length_class, source, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
        (
            raw["period_id"], raw["cik"], raw.get("accession_number"), period["raw_start_date"],
            period["raw_end_date"], period["raw_instant_date"], period["start_date_inclusive"],
            period["end_date_exclusive"], period["duration_days"], period["period_kind"],
            period["period_semantics"], period.get("fiscal_year"), period.get("fiscal_period"),
            period.get("fiscal_period_ordinal"), period_length_class(period["duration_days"], period["period_semantics"]),
            raw.get("period_source", "sec_companyfacts"), utc_now(),
        ),
    )


def insert_context_and_unit(conn: sqlite3.Connection, raw: dict[str, Any]) -> None:
    period = raw["period"]
    dimensions_hash = raw.get("dimensions_hash", TOTAL_DIMENSIONS_HASH)
    dimensions_json = raw.get("dimensions_json", TOTAL_DIMENSIONS_JSON)
    segment_json = raw.get("segment_json")
    scenario_json = raw.get("scenario_json")
    axis_count = int(raw.get("axis_count", 0) or 0)
    scope_class = raw.get("dimensional_scope", "consolidated_total")
    insert_dimension_signature_row(conn, dimensions_hash, dimensions_json, scope_class, axis_count)
    raw_context_json = canonical_json({
        "period": period,
        "dimensions_hash": dimensions_hash,
        "dimensions_json": dimensions_json,
        "source": raw.get("period_source", "sec_companyfacts"),
        "raw_context_id": raw.get("raw_context_id"),
    })
    conn.execute(
        """
        INSERT OR IGNORE INTO xbrl_contexts(
          context_id, cik, accession_number, raw_context_id, entity_identifier, period_id, period_kind,
          raw_start_date, raw_end_date, raw_instant_date, start_date_inclusive, end_date_exclusive,
          duration_days, dimensions_hash, dimensions_json, segment_json, scenario_json, raw_context_sha256
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
        (
            raw["context_id"], raw["cik"], raw.get("accession_number"), raw["raw_context_id"], raw.get("entity_identifier", raw["cik"]),
            raw["period_id"], period["period_kind"], period["raw_start_date"], period["raw_end_date"],
            period["raw_instant_date"], period["start_date_inclusive"], period["end_date_exclusive"],
            period["duration_days"], dimensions_hash, dimensions_json, segment_json, scenario_json,
            raw.get("raw_context_sha256", hashlib.sha256(raw_context_json.encode()).hexdigest()),
        ),
    )
    if raw.get("unit_id") and raw.get("unit_signature"):
        conn.execute(
            """
            INSERT OR IGNORE INTO xbrl_units(
              unit_id, cik, accession_number, raw_unit_id, unit_signature, numerator_json, denominator_json, raw_unit_sha256
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                raw["unit_id"], raw["cik"], raw.get("accession_number"), raw.get("raw_unit_id", raw["unit_signature"]),
                raw["unit_signature"], raw.get("unit_numerator_json", canonical_json([raw["unit_signature"]])),
                raw.get("unit_denominator_json"), raw.get("raw_unit_sha256", hashlib.sha256(str(raw["unit_signature"]).encode()).hexdigest()),
            ),
        )


def insert_raw_fact(conn: sqlite3.Connection, raw: dict[str, Any], source_doc_id: str) -> int:
    cur = conn.execute(
        """
        INSERT OR IGNORE INTO xbrl_facts(
          fact_id, source_doc_id, security_id, cik, accession_number, taxonomy, concept_qname, concept_local_name,
          context_id, unit_id, value_decimal, value_text, decimals_attr, precision_attr, is_nil,
          raw_fact_hash, raw_context_id, form, filed_at, accepted_at, available_at, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
        (
            raw["fact_id"], source_doc_id, raw["security_id"], raw["cik"], raw.get("accession_number"),
            raw["taxonomy"], raw["concept_qname"], raw.get("concept_local_name", raw["concept_qname"]), raw["context_id"], raw.get("unit_id"),
            raw.get("value_decimal"), raw.get("value_text"), raw.get("decimals"), raw.get("precision"),
            1 if raw.get("is_nil") else 0, raw["raw_fact_hash"], raw["raw_context_id"], raw.get("form"),
            raw.get("filed_at"), raw.get("accepted_at"), raw["available_at"], utc_now(),
        ),
    )
    return max(int(cur.rowcount), 0)


def split_xml_tag(tag: str) -> tuple[str | None, str]:
    if tag.startswith("{") and "}" in tag:
        namespace, local = tag[1:].split("}", 1)
        return namespace, local
    return None, tag


def local_name(tag: str) -> str:
    return split_xml_tag(tag)[1]


def namespace_prefix(namespace: str | None, ns_to_prefix: dict[str, str]) -> str:
    if namespace is None:
        return ""
    if namespace in ns_to_prefix:
        return ns_to_prefix[namespace]
    if "fasb.org/us-gaap" in namespace:
        return "us-gaap"
    if "xbrl.sec.gov/dei" in namespace:
        return "dei"
    if "fasb.org/srt" in namespace:
        return "srt"
    if namespace == XBRL_INSTANCE_NS:
        return "xbrli"
    return "ns"


def qname_from_etag(tag: str, ns_to_prefix: dict[str, str]) -> str:
    namespace, local = split_xml_tag(tag)
    prefix = namespace_prefix(namespace, ns_to_prefix)
    return f"{prefix}:{local}" if prefix else local


def qname_parts(qname: str) -> tuple[str, str]:
    if ":" in qname:
        return qname.split(":", 1)
    return "", qname


def find_child(element: ET.Element | None, child_local_name: str) -> ET.Element | None:
    if element is None:
        return None
    for child in list(element):
        if local_name(child.tag) == child_local_name:
            return child
    return None


def find_descendant(element: ET.Element | None, descendant_local_name: str) -> ET.Element | None:
    if element is None:
        return None
    for child in element.iter():
        if local_name(child.tag) == descendant_local_name:
            return child
    return None


def child_text(element: ET.Element | None, child_local_name: str) -> str | None:
    child = find_child(element, child_local_name)
    if child is None or child.text is None:
        return None
    text = child.text.strip()
    return text or None


def element_text(element: ET.Element | None) -> str:
    if element is None:
        return ""
    return "".join(element.itertext()).strip()


def collect_namespaces(data: bytes) -> dict[str, str]:
    ns_to_prefix: dict[str, str] = {}
    try:
        for _event, ns in ET.iterparse(io.BytesIO(data), events=("start-ns",)):
            prefix, uri = ns
            if uri and uri not in ns_to_prefix:
                ns_to_prefix[uri] = prefix or ""
    except ET.ParseError:
        return ns_to_prefix
    return ns_to_prefix


def parse_xml_root(data: bytes, source_name: str) -> ET.Element:
    head = data[:8192].upper()
    if b"<!ENTITY" in head or b"<!DOCTYPE" in head:
        raise FaError(f"refusing XML document with entity/doctype declaration: {source_name}", 2)
    try:
        return ET.fromstring(data)
    except ET.ParseError as exc:
        raise FaError(f"invalid XML/XHTML in {source_name}: {exc}", 2) from exc


def normalize_measure(measure: str) -> str:
    raw = measure.strip()
    if not raw:
        return ""
    local = raw.split(":", 1)[1] if ":" in raw else raw
    lower = local.lower()
    if lower == "usd":
        return "USD"
    if lower == "shares":
        return "shares"
    if lower == "pure":
        return "pure"
    return raw


def parse_unit_signature(unit: ET.Element, _ns_to_prefix: dict[str, str]) -> tuple[str, str, str | None]:
    divide = find_child(unit, "divide")
    if divide is not None:
        numerator = find_descendant(divide, "unitNumerator")
        denominator = find_descendant(divide, "unitDenominator")
        num_measures = [normalize_measure(element_text(m)) for m in (numerator.iter() if numerator is not None else []) if local_name(m.tag) == "measure"]
        den_measures = [normalize_measure(element_text(m)) for m in (denominator.iter() if denominator is not None else []) if local_name(m.tag) == "measure"]
        num_measures = [m for m in num_measures if m]
        den_measures = [m for m in den_measures if m]
        if num_measures and den_measures:
            return f"{'*'.join(num_measures)}/{'*'.join(den_measures)}", canonical_json(num_measures), canonical_json(den_measures)
        return canonical_json({"divide": ET.tostring(divide, encoding="unicode")}), canonical_json(num_measures), canonical_json(den_measures)
    measures = [normalize_measure(element_text(m)) for m in unit.iter() if local_name(m.tag) == "measure"]
    measures = [m for m in measures if m]
    if not measures:
        return "pure", canonical_json(["pure"]), None
    return "*".join(measures), canonical_json(measures), None


def parse_numeric_text(raw_text: str, scale: str | None = None, sign: str | None = None) -> float | None:
    text = raw_text.strip().replace("\u2212", "-").replace("\xa0", " ")
    if not text:
        return None
    negative = False
    if text.startswith("(") and text.endswith(")"):
        negative = True
        text = text[1:-1]
    text = text.replace(",", "").replace("$", "").replace("%", "").strip()
    if text in {"", "-", "--", "—", "–"}:
        return None
    try:
        value = float(text)
    except ValueError:
        return None
    if negative:
        value = -abs(value)
    if sign and sign.strip() == "-":
        value = -abs(value)
    if scale not in (None, ""):
        try:
            value *= 10.0 ** int(scale)
        except ValueError:
            return None
    return value if math.isfinite(value) else None


def continuation_map(root: ET.Element) -> dict[str, str]:
    cont: dict[str, str] = {}
    for element in root.iter():
        namespace, name = split_xml_tag(element.tag)
        if namespace == INLINE_XBRL_NS and name == "continuation" and element.get("id"):
            cont[str(element.get("id"))] = element_text(element)
    return cont


def inline_text(element: ET.Element, continuations: dict[str, str]) -> str:
    parts = [element_text(element)]
    seen: set[str] = set()
    continuation_id = element.get("continuedAt")
    while continuation_id and continuation_id not in seen and len(seen) < 32:
        seen.add(continuation_id)
        parts.append(continuations.get(continuation_id, ""))
        continuation_id = None
    return "".join(parts).strip()


def classify_dimension_scope(dimensions: dict[str, list[dict[str, Any]]]) -> str:
    axes = " ".join(str(item.get("dimension", "")) for scope in dimensions.values() for item in scope).lower()
    if not axes:
        return "consolidated_total"
    if "product" in axes or "service" in axes:
        return "product"
    if "geograph" in axes or "country" in axes or "region" in axes:
        return "geography"
    if "classofstock" in axes or "class_of_stock" in axes:
        return "class_of_stock"
    if "segment" in axes or "business" in axes:
        return "segment"
    return "other"


def dimension_payload_from_scope(scope: ET.Element | None, ns_to_prefix: dict[str, str]) -> list[dict[str, Any]]:
    if scope is None:
        return []
    payload: list[dict[str, Any]] = []
    for element in scope.iter():
        namespace, name = split_xml_tag(element.tag)
        if namespace == XBRLDI_NS and name == "explicitMember":
            payload.append({"kind": "explicit", "dimension": element.get("dimension"), "member": element_text(element)})
        elif namespace == XBRLDI_NS and name == "typedMember":
            payload.append({
                "kind": "typed",
                "dimension": element.get("dimension"),
                "value": canonical_json([qname_from_etag(child.tag, ns_to_prefix) + "=" + element_text(child) for child in list(element)]),
            })
    if payload:
        return payload
    for child in list(scope):
        text = element_text(child)
        if text:
            payload.append({"kind": "unknown", "name": qname_from_etag(child.tag, ns_to_prefix), "value": text})
    return payload


def build_dimensions(segment: ET.Element | None, scenario: ET.Element | None, ns_to_prefix: dict[str, str]) -> tuple[str, str, str | None, str | None, str, int]:
    dimensions = {
        "segment": dimension_payload_from_scope(segment, ns_to_prefix),
        "scenario": dimension_payload_from_scope(scenario, ns_to_prefix),
    }
    axis_count = len(dimensions["segment"]) + len(dimensions["scenario"])
    if axis_count == 0:
        return TOTAL_DIMENSIONS_HASH, TOTAL_DIMENSIONS_JSON, None, None, "consolidated_total", 0
    dimensions_json = canonical_json(dimensions)
    return (
        sha256_bytes(dimensions_json.encode()),
        dimensions_json,
        canonical_json(dimensions["segment"]),
        canonical_json(dimensions["scenario"]),
        classify_dimension_scope(dimensions),
        axis_count,
    )


def classify_period_from_dates(
    start: str | None,
    end: str | None,
    instant: str | None,
    fiscal_year: int | None,
    fiscal_period: str | None,
    form: str | None,
) -> dict[str, Any] | None:
    fp = (fiscal_period or "").upper() or None
    if instant:
        return {
            "period_kind": "instant",
            "period_semantics": "instant",
            "raw_start_date": None,
            "raw_end_date": None,
            "raw_instant_date": instant[:10],
            "start_date_inclusive": instant[:10],
            "end_date_exclusive": date_plus_one(instant),
            "duration_days": 0,
            "fiscal_year": fiscal_year,
            "fiscal_period": fp,
            "fiscal_period_ordinal": fiscal_period_ordinal(fp),
        }
    if not start or not end:
        return None
    duration = inclusive_duration_days(start, end)
    if duration <= 0:
        return None
    if fp == "FY" or duration >= 320:
        semantics = "fiscal_year"
    elif fp in {"Q2", "Q3", "Q4"} and duration > 120:
        semantics = "fiscal_ytd"
    elif 70 <= duration <= 110:
        semantics = "fiscal_quarter"
    elif duration < 70:
        semantics = "stub"
    else:
        semantics = "irregular"
    if form and form.upper() in {"8-K", "8-K/A"} and semantics == "irregular":
        semantics = "stub"
    return {
        "period_kind": "duration",
        "period_semantics": semantics,
        "raw_start_date": start[:10],
        "raw_end_date": end[:10],
        "raw_instant_date": None,
        "start_date_inclusive": start[:10],
        "end_date_exclusive": date_plus_one(end),
        "duration_days": duration,
        "fiscal_year": fiscal_year,
        "fiscal_period": fp,
        "fiscal_period_ordinal": fiscal_period_ordinal(fp),
    }


def infer_document_focus(root: ET.Element, ns_to_prefix: dict[str, str]) -> dict[str, Any]:
    focus: dict[str, Any] = {"fiscal_year": None, "fiscal_period": None, "document_type": None, "period_end_date": None}
    for element in root.iter():
        concept = element.get("name")
        if concept is None and element.get("contextRef") is not None:
            concept = qname_from_etag(element.tag, ns_to_prefix)
        if not concept:
            continue
        _prefix, local = qname_parts(str(concept))
        text = element_text(element)
        if not text:
            continue
        if local == "DocumentFiscalYearFocus":
            try:
                focus["fiscal_year"] = int(text[:4])
            except ValueError:
                pass
        elif local == "DocumentFiscalPeriodFocus":
            focus["fiscal_period"] = text.upper()
        elif local == "DocumentType":
            focus["document_type"] = text.upper()
        elif local == "DocumentPeriodEndDate":
            focus["period_end_date"] = text[:10]
    return focus


def parse_xbrl_contexts(
    root: ET.Element,
    ns_to_prefix: dict[str, str],
    cik10: str,
    accession: str,
    form: str | None,
    focus: dict[str, Any],
) -> dict[str, dict[str, Any]]:
    contexts: dict[str, dict[str, Any]] = {}
    for element in root.iter():
        namespace, name = split_xml_tag(element.tag)
        if namespace != XBRL_INSTANCE_NS or name != "context":
            continue
        raw_context_id = element.get("id")
        if not raw_context_id:
            continue
        entity = find_child(element, "entity")
        period_element = find_child(element, "period")
        identifier = find_descendant(entity, "identifier")
        entity_identifier = element_text(identifier) or cik10
        segment = find_descendant(entity, "segment")
        scenario = find_child(element, "scenario")
        period = classify_period_from_dates(
            child_text(period_element, "startDate"),
            child_text(period_element, "endDate"),
            child_text(period_element, "instant"),
            focus.get("fiscal_year"),
            focus.get("fiscal_period"),
            form,
        )
        if period is None:
            continue
        period_id = period_id_for(period, cik10, accession)
        dimensions_hash, dimensions_json, segment_json, scenario_json, scope_class, axis_count = build_dimensions(segment, scenario, ns_to_prefix)
        raw_context_json = canonical_json({
            "raw_context_id": raw_context_id,
            "entity_identifier": entity_identifier,
            "period": period,
            "dimensions_json": dimensions_json,
        })
        contexts[raw_context_id] = {
            "context_id": stable_id("ctx", {
                "cik": cik10,
                "accession_number": accession,
                "raw_context_id": raw_context_id,
                "period_id": period_id,
                "dimensions_hash": dimensions_hash,
            }),
            "raw_context_id": raw_context_id,
            "entity_identifier": entity_identifier,
            "period": period,
            "period_id": period_id,
            "dimensions_hash": dimensions_hash,
            "dimensions_json": dimensions_json,
            "segment_json": segment_json,
            "scenario_json": scenario_json,
            "dimensional_scope": scope_class,
            "axis_count": axis_count,
            "raw_context_sha256": sha256_bytes(raw_context_json.encode()),
        }
    return contexts


def parse_xbrl_units(root: ET.Element, ns_to_prefix: dict[str, str], cik10: str, accession: str) -> dict[str, dict[str, Any]]:
    units: dict[str, dict[str, Any]] = {}
    for element in root.iter():
        namespace, name = split_xml_tag(element.tag)
        if namespace != XBRL_INSTANCE_NS or name != "unit":
            continue
        raw_unit_id = element.get("id")
        if not raw_unit_id:
            continue
        signature, numerator_json, denominator_json = parse_unit_signature(element, ns_to_prefix)
        raw_xml = ET.tostring(element, encoding="unicode")
        units[raw_unit_id] = {
            "unit_id": stable_id("unit", {
                "cik": cik10,
                "accession_number": accession,
                "raw_unit_id": raw_unit_id,
                "unit_signature": signature,
            }),
            "raw_unit_id": raw_unit_id,
            "unit_signature": signature,
            "unit_numerator_json": numerator_json,
            "unit_denominator_json": denominator_json,
            "raw_unit_sha256": sha256_bytes(raw_xml.encode()),
        }
    return units


def inline_fraction_value(element: ET.Element) -> float | None:
    numerator = None
    denominator = None
    for child in element.iter():
        name = local_name(child.tag)
        if name == "numerator":
            numerator = parse_numeric_text(element_text(child))
        elif name == "denominator":
            denominator = parse_numeric_text(element_text(child))
    if numerator is None or denominator in (None, 0.0):
        return None
    return numerator / denominator


def xbrl_document_kind(name: str, data: bytes) -> str:
    lower_name = name.lower()
    sample = data[:20000].decode("utf-8", errors="ignore")
    lower_sample = sample.lower()
    if INLINE_XBRL_NS in sample or "<ix:" in sample or "nonfraction" in lower_sample:
        return "inline_xbrl"
    if "<xbrli:xbrl" in lower_sample or (XBRL_INSTANCE_NS in sample and "contextref" in lower_sample):
        return "xbrl_instance"
    if lower_name.endswith(".xsd"):
        return "xbrl_schema"
    if lower_name.endswith(".xml"):
        if any(token in lower_name for token in ("_cal", "_def", "_lab", "_pre")) or "linkbase" in lower_sample:
            return "xbrl_linkbase"
        return "xml"
    if lower_name.endswith((".htm", ".html", ".xhtml")):
        return "html"
    return "other"


def is_xbrl_relevant_name(name: str, store_all: bool) -> bool:
    if store_all:
        return True
    return name.lower().endswith((".htm", ".html", ".xhtml", ".xml", ".xsd"))


def index_items(index_json: Any) -> list[dict[str, Any]]:
    directory = index_json.get("directory", {}) if isinstance(index_json, dict) else {}
    items = directory.get("item", []) if isinstance(directory, dict) else []
    if isinstance(items, dict):
        items = [items]
    if not isinstance(items, list):
        return []
    result: list[dict[str, Any]] = []
    for item in items:
        if isinstance(item, dict) and isinstance(item.get("name"), str) and "/" not in item["name"] and ".." not in item["name"]:
            result.append(item)
    return result


def source_doc_insert(
    conn: sqlite3.Connection,
    cik10: str,
    accession: str | None,
    form: str | None,
    filed_at: str | None,
    source_url: str,
    local_path: str,
    raw_hash: str,
    document_kind: str,
) -> str:
    doc_id = source_document_id(cik10, source_url, raw_hash)
    conn.execute(
        """
        INSERT OR IGNORE INTO source_documents(
          doc_id, cik, accession_number, form, filed_at, source_url, local_path, sha256, document_kind, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
        (doc_id, cik10, accession, form, filed_at, source_url, local_path, raw_hash, document_kind, utc_now()),
    )
    return doc_id


def load_package_document(
    raw_root: Path,
    package_root: Path | None,
    cik10: str,
    accession: str,
    name: str,
    user_agent: str,
) -> tuple[bytes, str, str, str]:
    source_url = accession_archive_url(cik10, accession) if name == "index.json" else accession_archive_url(cik10, accession, name)
    if package_root is not None:
        path = package_root / name
        data = path.read_bytes()
        source_url = path.resolve().as_uri()
    else:
        data = http_get_bytes(source_url, user_agent)
    local_path, raw_hash = save_raw_bytes(raw_root, ["sec", f"cik={cik10}", f"accession={accession}", name], data)
    return data, local_path, raw_hash, source_url


def build_raw_fact_from_parsed(
    parsed: dict[str, Any],
    context: dict[str, Any],
    unit: dict[str, Any] | None,
    security_id: int,
    cik10: str,
    accession: str,
    form: str | None,
    filed_at: str | None,
    accepted_at: str | None,
    available_at: str,
) -> dict[str, Any]:
    taxonomy, local = qname_parts(parsed["concept_qname"])
    raw = {
        "security_id": security_id,
        "cik": cik10,
        "accession_number": accession,
        "taxonomy": taxonomy,
        "concept_qname": local,
        "concept_local_name": local,
        "unit_signature": unit["unit_signature"] if unit else None,
        "unit_id": unit["unit_id"] if unit else None,
        "raw_unit_id": unit["raw_unit_id"] if unit else None,
        "unit_numerator_json": unit.get("unit_numerator_json") if unit else None,
        "unit_denominator_json": unit.get("unit_denominator_json") if unit else None,
        "raw_unit_sha256": unit.get("raw_unit_sha256") if unit else None,
        "value_decimal": parsed.get("value_decimal"),
        "value_text": parsed.get("value_text"),
        "decimals": parsed.get("decimals"),
        "precision": parsed.get("precision"),
        "is_nil": bool(parsed.get("is_nil")),
        "form": form,
        "filed_at": filed_at,
        "accepted_at": accepted_at,
        "available_at": available_at,
        "period": context["period"],
        "period_id": context["period_id"],
        "period_source": "xbrl_package",
        "context_id": context["context_id"],
        "raw_context_id": context["raw_context_id"],
        "entity_identifier": context["entity_identifier"],
        "dimensions_hash": context["dimensions_hash"],
        "dimensions_json": context["dimensions_json"],
        "segment_json": context["segment_json"],
        "scenario_json": context["scenario_json"],
        "dimensional_scope": context["dimensional_scope"],
        "axis_count": context["axis_count"],
        "raw_context_sha256": context["raw_context_sha256"],
    }
    raw["fact_id"] = raw_fact_id(raw)
    raw["raw_fact_hash"] = raw["fact_id"].split("-", 1)[1]
    return raw


def parse_inline_xbrl_facts(root: ET.Element, continuations: dict[str, str]) -> list[dict[str, Any]]:
    facts: list[dict[str, Any]] = []
    for element in root.iter():
        namespace, name = split_xml_tag(element.tag)
        if namespace != INLINE_XBRL_NS or name not in {"nonFraction", "nonNumeric", "fraction"}:
            continue
        concept_qname = element.get("name")
        context_ref = element.get("contextRef")
        if not concept_qname or not context_ref:
            continue
        value_text = inline_text(element, continuations)
        value_decimal = None
        if name == "nonFraction":
            value_decimal = parse_numeric_text(value_text, element.get("scale"), element.get("sign"))
        elif name == "fraction":
            value_decimal = inline_fraction_value(element)
        facts.append({
            "concept_qname": str(concept_qname),
            "context_ref": str(context_ref),
            "unit_ref": element.get("unitRef"),
            "value_decimal": value_decimal,
            "value_text": value_text if value_decimal is None else None,
            "decimals": element.get("decimals"),
            "precision": element.get("precision"),
            "is_nil": element.get(f"{{{XSI_NS}}}nil") in {"true", "1"},
        })
    return facts


def parse_classic_xbrl_facts(root: ET.Element, ns_to_prefix: dict[str, str]) -> list[dict[str, Any]]:
    facts: list[dict[str, Any]] = []
    for element in root.iter():
        context_ref = element.get("contextRef")
        if not context_ref:
            continue
        concept_qname = qname_from_etag(element.tag, ns_to_prefix)
        text = element_text(element)
        value_decimal = parse_numeric_text(text)
        facts.append({
            "concept_qname": concept_qname,
            "context_ref": context_ref,
            "unit_ref": element.get("unitRef"),
            "value_decimal": value_decimal,
            "value_text": text if value_decimal is None else None,
            "decimals": element.get("decimals"),
            "precision": element.get("precision"),
            "is_nil": element.get(f"{{{XSI_NS}}}nil") in {"true", "1"},
        })
    return facts


def parse_xbrl_document_bytes(
    data: bytes,
    document_name: str,
    security_id: int,
    cik10: str,
    accession: str,
    form: str | None,
    filed_at: str | None,
    accepted_at: str | None,
    available_at: str,
) -> tuple[str, list[dict[str, Any]], list[str]]:
    kind = xbrl_document_kind(document_name, data)
    if kind not in {"inline_xbrl", "xbrl_instance"}:
        return kind, [], []
    ns_to_prefix = collect_namespaces(data)
    root = parse_xml_root(data, document_name)
    focus = infer_document_focus(root, ns_to_prefix)
    contexts = parse_xbrl_contexts(root, ns_to_prefix, cik10, accession, form or focus.get("document_type"), focus)
    units = parse_xbrl_units(root, ns_to_prefix, cik10, accession)
    parsed_facts = parse_inline_xbrl_facts(root, continuation_map(root)) if kind == "inline_xbrl" else parse_classic_xbrl_facts(root, ns_to_prefix)
    raw_facts: list[dict[str, Any]] = []
    warnings: list[str] = []
    for parsed in parsed_facts:
        context = contexts.get(str(parsed.get("context_ref")))
        if context is None:
            warnings.append(f"{document_name}: fact {parsed.get('concept_qname')} references missing context {parsed.get('context_ref')}")
            continue
        unit = units.get(str(parsed.get("unit_ref"))) if parsed.get("unit_ref") else None
        if parsed.get("unit_ref") and unit is None:
            warnings.append(f"{document_name}: fact {parsed.get('concept_qname')} references missing unit {parsed.get('unit_ref')}")
            continue
        raw_facts.append(build_raw_fact_from_parsed(parsed, context, unit, security_id, cik10, accession, form, filed_at, accepted_at, available_at))
    return kind, raw_facts, warnings


def rows_for_xbrl_parse(args: argparse.Namespace, conn: sqlite3.Connection) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    if args.accession:
        for accession in args.accession:
            row = conn.execute("SELECT * FROM filings WHERE accession_number = ?", (accession,)).fetchone()
            if row is not None:
                rows.append(dict(row))
            else:
                if not args.cik:
                    raise FaError(f"--cik is required for accession not present in filings: {accession}", 1)
                rows.append({
                    "accession_number": accession,
                    "cik": normalize_cik(args.cik),
                    "form": args.form,
                    "filing_date": args.filed_at,
                    "accepted_at": args.accepted_at,
                    "primary_document": None,
                })
    else:
        db_rows = conn.execute(
            """
            SELECT *
            FROM filings
            WHERE form IN ('10-K','10-Q','10-K/A','10-Q/A','20-F','20-F/A','40-F','40-F/A')
            ORDER BY filing_date DESC, accepted_at DESC
            LIMIT ?
            """,
            (args.limit,),
        ).fetchall()
        rows.extend(dict(row) for row in db_rows)
    return rows


def cmd_xbrl_parse(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    raw_root = Path(args.raw_root)
    package_root = Path(args.package_root) if args.package_root else None
    totals = {"documents": 0, "raw": 0, "selected": 0, "derived": 0, "exceptions": 0, "errors": 0}
    with conn:
        seed_metric_concept_candidates(conn)
        insert_dimension_signature(conn)
        for row in rows_for_xbrl_parse(args, conn):
            cik10 = normalize_cik(row.get("cik") or args.cik)
            security_id = int(cik10)
            accession = str(row["accession_number"])
            form = str(row.get("form") or args.form or "10-Q").upper()
            filed_at = row.get("filing_date") or args.filed_at
            accepted_at = row.get("accepted_at") or args.accepted_at
            available_at = accepted_at or filed_date_to_available_at(filed_at)
            if args.symbol:
                conn.execute(
                    """
                    INSERT INTO securities(security_id, cik, symbol, investable, price_usd, adv_usd, updated_at)
                    VALUES (?, ?, ?, 0, 0, 0, ?)
                    ON CONFLICT(security_id) DO UPDATE SET symbol=excluded.symbol, updated_at=excluded.updated_at
                    """,
                    (security_id, cik10, args.symbol.upper(), utc_now()),
                )
            if conn.execute("SELECT 1 FROM filings WHERE accession_number = ?", (accession,)).fetchone() is None:
                conn.execute(
                    """
                    INSERT INTO filings(accession_number, cik, form, filing_date, accepted_at, primary_document, source_url, ingested_at)
                    VALUES (?, ?, ?, ?, ?, NULL, ?, ?)
                    """,
                    (accession, cik10, form, filed_at, accepted_at, accession_archive_url(cik10, accession), utc_now()),
                )
            index_bytes, index_path, index_hash, index_url = load_package_document(raw_root, package_root, cik10, accession, "index.json", args.user_agent)
            index_doc_id = source_doc_insert(conn, cik10, accession, form, filed_at, index_url, index_path, index_hash, "sec_archive_index")
            try:
                index_json = json.loads(index_bytes.decode("utf-8"))
            except json.JSONDecodeError as exc:
                raise FaError(f"invalid SEC archive index for {accession}: {exc}", 2) from exc
            conn.execute(
                "UPDATE filings SET raw_index_uri = ?, raw_sha256 = COALESCE(raw_sha256, ?), ingested_at = ? WHERE accession_number = ?",
                (index_path, index_hash, utc_now(), accession),
            )
            candidates: list[dict[str, Any]] = []
            parse_errors: list[str] = []
            inserted_raw = 0
            documents_stored = 0
            for item in index_items(index_json):
                name = str(item["name"])
                if name == "index.json" or not is_xbrl_relevant_name(name, bool(args.store_all_documents)):
                    continue
                try:
                    data, local_path, raw_hash, source_url = load_package_document(raw_root, package_root, cik10, accession, name, args.user_agent)
                except (OSError, FaError) as exc:
                    parse_errors.append(f"{name}: fetch/read failed: {exc}")
                    continue
                if len(data) > int(args.max_document_bytes):
                    parse_errors.append(f"{name}: document exceeds max bytes ({len(data)} > {args.max_document_bytes})")
                    continue
                doc_kind = xbrl_document_kind(name, data)
                doc_id = source_doc_insert(conn, cik10, accession, form, filed_at, source_url, local_path, raw_hash, doc_kind)
                documents_stored += 1
                totals["documents"] += 1
                if doc_kind not in {"inline_xbrl", "xbrl_instance"}:
                    continue
                try:
                    parsed_kind, raw_facts, warnings = parse_xbrl_document_bytes(data, name, security_id, cik10, accession, form, filed_at, accepted_at, available_at)
                    parse_errors.extend(warnings)
                    eprint(f"parsed {name}: kind={parsed_kind} facts={len(raw_facts)}")
                except FaError as exc:
                    parse_errors.append(str(exc))
                    continue
                for raw in raw_facts:
                    insert_reporting_period(conn, raw)
                    insert_context_and_unit(conn, raw)
                    inserted_raw += insert_raw_fact(conn, raw, doc_id)
                    candidate = canonical_candidate_from_raw(raw)
                    if candidate is not None:
                        candidates.append(candidate)
            selected, exceptions = select_canonical_observations(candidates)
            inserted_selected = sum(insert_canonical_observation(conn, obs) for obs in selected)
            inserted_exceptions = sum(insert_exception(conn, exc) for exc in exceptions)
            inserted_derived = derive_quarters_from_ytd(conn, security_id)
            totals["raw"] += inserted_raw
            totals["selected"] += inserted_selected
            totals["derived"] += inserted_derived
            totals["exceptions"] += inserted_exceptions
            totals["errors"] += len(parse_errors)
            event = append_event(conn, "xbrl_package_parsed", {
                "cik": cik10,
                "security_id": security_id,
                "accession_number": accession,
                "form": form,
                "index_doc_id": index_doc_id,
                "documents_stored": documents_stored,
                "raw_facts_inserted": inserted_raw,
                "canonical_candidates": len(candidates),
                "canonical_selected_inserted": inserted_selected,
                "derived_quarter_observations_inserted": inserted_derived,
                "canonical_exceptions_inserted": inserted_exceptions,
                "parse_errors": parse_errors,
                "resolver_version": RESOLVER_VERSION,
            })
            json_line(event)
            if args.strict and parse_errors:
                raise FaError(f"xbrl parse produced {len(parse_errors)} warnings/errors for {accession}", 2)
            time.sleep(args.sleep_s)
    eprint(
        "xbrl packages processed "
        f"documents={totals['documents']} raw_inserted={totals['raw']} "
        f"selected_inserted={totals['selected']} derived_inserted={totals['derived']} "
        f"exceptions_inserted={totals['exceptions']} parse_errors={totals['errors']}"
    )
    return 0


def iter_companyfacts_raw(companyfacts: dict[str, Any], security_id: int, cik10: str) -> Iterable[dict[str, Any]]:
    facts_root = companyfacts.get("facts", {})
    if not isinstance(facts_root, dict):
        return
    for taxonomy, taxonomy_node in facts_root.items():
        if not isinstance(taxonomy_node, dict):
            continue
        for concept, tag_node in taxonomy_node.items():
            if not isinstance(tag_node, dict):
                continue
            units = tag_node.get("units", {})
            if not isinstance(units, dict):
                continue
            for unit, items in units.items():
                if not isinstance(items, list):
                    continue
                for item in items:
                    if not isinstance(item, dict):
                        continue
                    val = item.get("val")
                    value_decimal: float | None = None
                    value_text: str | None = None
                    if isinstance(val, (int, float)) and math.isfinite(float(val)):
                        value_decimal = float(val)
                    elif val is not None:
                        value_text = str(val)
                    accession = item.get("accn") if isinstance(item.get("accn"), str) else None
                    filed = item.get("filed") if isinstance(item.get("filed"), str) else None
                    form = str(item.get("form") or "")
                    candidate = CONCEPT_CANDIDATES.get((taxonomy, concept))
                    metric_id = int(candidate["metric_id"]) if candidate else 0
                    period = classify_period(metric_id, item)
                    if period is None:
                        continue
                    period_id = period_id_for(period, cik10, accession)
                    raw_context_id = stable_id("rawctx", {
                        "source": "sec_companyfacts",
                        "accession": accession,
                        "start": period["raw_start_date"],
                        "end": period["raw_end_date"],
                        "instant": period["raw_instant_date"],
                        "fy": period.get("fiscal_year"),
                        "fp": period.get("fiscal_period"),
                    })
                    raw = {
                        "security_id": security_id,
                        "cik": cik10,
                        "accession_number": accession,
                        "taxonomy": taxonomy,
                        "concept_qname": concept,
                        "unit_signature": str(unit),
                        "value_decimal": value_decimal,
                        "value_text": value_text,
                        "decimals": item.get("decimals"),
                        "precision": item.get("precision"),
                        "is_nil": False,
                        "form": form,
                        "filed_at": filed_date_to_available_at(filed),
                        "accepted_at": None,
                        "available_at": filed_date_to_available_at(filed),
                        "period": period,
                        "period_id": period_id,
                        "raw_context_id": raw_context_id,
                    }
                    raw["context_id"] = context_id_for_fact(raw)
                    raw["unit_id"] = unit_id_for_fact(raw)
                    raw["fact_id"] = raw_fact_id(raw)
                    raw["raw_fact_hash"] = raw["fact_id"].split("-", 1)[1]
                    yield raw


def canonical_candidate_from_raw(raw: dict[str, Any]) -> dict[str, Any] | None:
    candidate = CONCEPT_CANDIDATES.get((raw["taxonomy"], raw["concept_qname"]))
    if candidate is None:
        return None
    if raw.get("unit_signature") not in candidate["allowed_units"]:
        return None
    if raw.get("value_decimal") is None:
        return None
    if raw.get("dimensional_scope", "consolidated_total") != "consolidated_total":
        return None
    period = raw["period"]
    metric_id = int(candidate["metric_id"])
    metric_kind = METRIC_KIND_BY_ID[metric_id]
    if metric_kind == "instant" and period["period_semantics"] != "instant":
        return None
    if metric_kind in {"flow", "per_share_flow"} and period["period_semantics"] == "instant":
        return None
    flags = period_quality_flags(period["period_semantics"], int(period["duration_days"]), str(raw.get("form") or ""), raw.get("accession_number"))
    if raw["taxonomy"] != "us-gaap":
        flags |= QUALITY_EXTENSION_TAG
    if int(raw.get("axis_count", 0) or 0) > 0:
        flags |= QUALITY_DIMENSIONAL_FACT
    source_name = raw.get("period_source", "sec_companyfacts")
    obs = {
        "resolver_version": RESOLVER_VERSION,
        "security_id": raw["security_id"],
        "cik": raw["cik"],
        "metric_id": metric_id,
        "metric_name": METRIC_NAMES[metric_id],
        "metric_kind": metric_kind,
        "basis_id": int(candidate["basis_id"]),
        "period_id": raw["period_id"],
        "period_semantics": period["period_semantics"],
        "duration_days": int(period["duration_days"]),
        "fiscal_year": period.get("fiscal_year"),
        "fiscal_period": period.get("fiscal_period"),
        "value_decimal": float(raw["value_decimal"]),
        "unit_signature": raw["unit_signature"],
        "dimensions_hash": raw.get("dimensions_hash", TOTAL_DIMENSIONS_HASH),
        "dimensional_scope": raw.get("dimensional_scope", "consolidated_total"),
        "observation_status": "selected",
        "source_fact_id": raw["fact_id"],
        "source_accession": raw.get("accession_number"),
        "taxonomy": raw["taxonomy"],
        "concept_qname": raw["concept_qname"],
        "selection_reason": f"priority={candidate['priority']} total-company {source_name} fact",
        "quality_flags": flags,
        "quality_flags_json": quality_flag_names(flags),
        "quality_score": max(0.0, 1.0 - (0.10 * len(quality_flag_names(flags)))),
        "accepted_at": raw.get("accepted_at"),
        "available_at": raw["available_at"],
        "priority": int(candidate["priority"]),
    }
    obs["observation_id"] = observation_id_for(obs)
    obs["observation_hash"] = obs["observation_id"].split("-", 1)[1]
    return obs


def select_canonical_observations(candidates: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    grouped: dict[tuple[Any, ...], list[dict[str, Any]]] = {}
    for obs in candidates:
        key = (
            obs["security_id"], obs["metric_id"], obs["period_id"], obs["unit_signature"],
            obs["dimensions_hash"], obs["source_accession"],
        )
        grouped.setdefault(key, []).append(obs)

    selected: list[dict[str, Any]] = []
    exceptions: list[dict[str, Any]] = []
    for _key, group in grouped.items():
        group.sort(key=lambda item: (item["priority"], -float(item["quality_score"] or 0.0), item["concept_qname"]))
        best = group[0]
        ties = [item for item in group if item["priority"] == best["priority"] and abs(float(item["value_decimal"]) - float(best["value_decimal"])) > 1.0e-9]
        if len(ties) > 1:
            best = dict(best)
            best["quality_flags"] = int(best["quality_flags"]) | QUALITY_AMBIGUOUS_CANDIDATES
            best["quality_flags_json"] = quality_flag_names(int(best["quality_flags"]))
            best["selection_reason"] = "ambiguous same-priority candidates; retained but not model-comparable"
            exceptions.append({
                "resolver_version": RESOLVER_VERSION,
                "cik": best["cik"],
                "accession_number": best.get("source_accession"),
                "metric_id": best["metric_id"],
                "period_id": best["period_id"],
                "exception_type": "ambiguous",
                "candidate_fact_ids": [item["source_fact_id"] for item in group],
                "explanation": "same-priority canonical candidates disagree in value",
            })
        selected.append(best)
    return selected, exceptions


def insert_canonical_observation(conn: sqlite3.Connection, obs: dict[str, Any]) -> int:
    cur = conn.execute(
        """
        INSERT OR IGNORE INTO canonical_observations(
          observation_id, observation_hash, resolver_version, security_id, cik, metric_id, metric_name, metric_kind,
          basis_id, period_id, period_semantics, duration_days, fiscal_year, fiscal_period, value_decimal,
          unit_signature, dimensions_hash, dimensional_scope, observation_status, source_fact_id, source_accession,
          taxonomy, concept_qname, selection_reason, quality_flags, quality_flags_json, quality_score,
          accepted_at, available_at, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
        (
            obs["observation_id"], obs["observation_hash"], obs["resolver_version"], obs["security_id"], obs["cik"],
            obs["metric_id"], obs["metric_name"], obs["metric_kind"], obs["basis_id"], obs["period_id"],
            obs["period_semantics"], obs["duration_days"], obs.get("fiscal_year"), obs.get("fiscal_period"),
            obs["value_decimal"], obs["unit_signature"], obs["dimensions_hash"], obs["dimensional_scope"],
            obs["observation_status"], obs.get("source_fact_id"), obs.get("source_accession"), obs.get("taxonomy"),
            obs.get("concept_qname"), obs["selection_reason"], obs["quality_flags"], canonical_json(obs["quality_flags_json"]),
            obs.get("quality_score"), obs.get("accepted_at"), obs["available_at"], utc_now(),
        ),
    )
    return max(int(cur.rowcount), 0)


def insert_exception(conn: sqlite3.Connection, exc: dict[str, Any]) -> int:
    exception_id = stable_id("exc", exc)
    cur = conn.execute(
        """
        INSERT OR IGNORE INTO canonical_observation_exceptions(
          exception_id, resolver_version, cik, accession_number, metric_id, period_id,
          exception_type, candidate_fact_ids_json, explanation, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
        (
            exception_id, exc["resolver_version"], exc["cik"], exc.get("accession_number"), exc["metric_id"],
            exc.get("period_id"), exc["exception_type"], canonical_json(exc["candidate_fact_ids"]), exc["explanation"], utc_now(),
        ),
    )
    return max(int(cur.rowcount), 0)


def derive_quarters_from_ytd(conn: sqlite3.Connection, security_id: int) -> int:
    rows = conn.execute(
        """
        SELECT co.*, rp.raw_start_date, rp.raw_end_date, rp.start_date_inclusive, rp.end_date_exclusive
        FROM canonical_observations co
        JOIN reporting_periods rp ON rp.period_id = co.period_id
        WHERE co.security_id = ?
          AND co.observation_status = 'selected'
          AND co.metric_kind IN ('flow', 'per_share_flow')
          AND co.period_semantics IN ('fiscal_ytd', 'fiscal_quarter')
        ORDER BY co.metric_id, co.basis_id, co.dimensions_hash, co.fiscal_year, co.duration_days
        """,
        (security_id,),
    ).fetchall()
    by_key: dict[tuple[Any, ...], list[sqlite3.Row]] = {}
    for row in rows:
        key = (row["metric_id"], row["basis_id"], row["unit_signature"], row["dimensions_hash"], row["fiscal_year"])
        by_key.setdefault(key, []).append(row)

    inserted = 0
    for _key, group in by_key.items():
        ytd_rows = [row for row in group if row["period_semantics"] == "fiscal_ytd"]
        possible_prior = sorted(group, key=lambda row: int(row["duration_days"] or 0))
        for now_row in ytd_rows:
            prior_rows = [
                row for row in possible_prior
                if row["observation_id"] != now_row["observation_id"]
                and row["raw_start_date"] == now_row["raw_start_date"]
                and int(row["duration_days"] or 0) < int(now_row["duration_days"] or 0)
            ]
            if not prior_rows:
                continue
            prior = prior_rows[-1]
            quarter_start_date = date_plus_one(prior["raw_end_date"])
            quarter_end_date = now_row["raw_end_date"]
            duration = inclusive_duration_days(quarter_start_date, quarter_end_date)
            if not (70 <= duration <= 110):
                continue
            existing = conn.execute(
                """
                SELECT 1
                FROM canonical_observations co
                JOIN reporting_periods rp ON rp.period_id = co.period_id
                WHERE co.security_id=? AND co.metric_id=? AND co.basis_id=?
                  AND co.unit_signature=? AND co.dimensions_hash=?
                  AND co.period_semantics='fiscal_quarter' AND rp.raw_end_date=?
                LIMIT 1
                """,
                (security_id, now_row["metric_id"], now_row["basis_id"], now_row["unit_signature"], now_row["dimensions_hash"], quarter_end_date),
            ).fetchone()
            if existing:
                continue
            value = float(now_row["value_decimal"]) - float(prior["value_decimal"])
            period = {
                "period_kind": "duration",
                "period_semantics": "fiscal_quarter",
                "raw_start_date": quarter_start_date,
                "raw_end_date": quarter_end_date,
                "raw_instant_date": None,
                "start_date_inclusive": quarter_start_date,
                "end_date_exclusive": date_plus_one(quarter_end_date),
                "duration_days": duration,
                "fiscal_year": now_row["fiscal_year"],
                "fiscal_period": now_row["fiscal_period"],
                "fiscal_period_ordinal": fiscal_period_ordinal(now_row["fiscal_period"]),
            }
            period_id = period_id_for(period, now_row["cik"], now_row["source_accession"])
            raw = {"period": period, "period_id": period_id, "cik": now_row["cik"], "accession_number": now_row["source_accession"]}
            insert_reporting_period(conn, raw)
            flags = int(now_row["quality_flags"] or 0) | int(prior["quality_flags"] or 0) | QUALITY_DERIVED
            obs = {
                "resolver_version": RESOLVER_VERSION,
                "security_id": security_id,
                "cik": now_row["cik"],
                "metric_id": int(now_row["metric_id"]),
                "metric_name": now_row["metric_name"],
                "metric_kind": now_row["metric_kind"],
                "basis_id": int(now_row["basis_id"]),
                "period_id": period_id,
                "period_semantics": "fiscal_quarter",
                "duration_days": duration,
                "fiscal_year": now_row["fiscal_year"],
                "fiscal_period": now_row["fiscal_period"],
                "value_decimal": value,
                "unit_signature": now_row["unit_signature"],
                "dimensions_hash": now_row["dimensions_hash"],
                "dimensional_scope": now_row["dimensional_scope"],
                "observation_status": "derived",
                "source_fact_id": now_row["source_fact_id"],
                "source_accession": now_row["source_accession"],
                "taxonomy": now_row["taxonomy"],
                "concept_qname": now_row["concept_qname"],
                "selection_reason": "derived quarterly flow by subtracting prior YTD observation",
                "quality_flags": flags,
                "quality_flags_json": quality_flag_names(flags),
                "quality_score": min(float(now_row["quality_score"] or 0.0), float(prior["quality_score"] or 0.0)) * 0.90,
                "accepted_at": now_row["accepted_at"],
                "available_at": max(now_row["available_at"], prior["available_at"]),
            }
            obs["observation_id"] = observation_id_for(obs)
            obs["observation_hash"] = obs["observation_id"].split("-", 1)[1]
            inserted += insert_canonical_observation(conn, obs)
            conn.execute(
                """
                INSERT OR IGNORE INTO observation_lineage(child_observation_id, parent_observation_id, operation, operand_role, rule_version)
                VALUES (?, ?, 'subtract_ytd', 'minuend', ?)
                """,
                (obs["observation_id"], now_row["observation_id"], RESOLVER_VERSION),
            )
            conn.execute(
                """
                INSERT OR IGNORE INTO observation_lineage(child_observation_id, parent_observation_id, operation, operand_role, rule_version)
                VALUES (?, ?, 'subtract_ytd', 'subtrahend', ?)
                """,
                (obs["observation_id"], prior["observation_id"], RESOLVER_VERSION),
            )
    return inserted


def cmd_facts_companyfacts(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    raw_root = Path(args.raw_root)
    total_raw = 0
    total_selected = 0
    total_derived = 0
    total_exceptions = 0
    with conn:
        seed_metric_concept_candidates(conn)
        insert_dimension_signature(conn)
        for cik in args.cik:
            cik10 = normalize_cik(cik)
            security_id = int(cik10)
            if args.symbol:
                conn.execute(
                    """
                    INSERT INTO securities(security_id, cik, symbol, investable, price_usd, adv_usd, updated_at)
                    VALUES (?, ?, ?, 0, 0, 0, ?)
                    ON CONFLICT(security_id) DO UPDATE SET symbol=excluded.symbol, updated_at=excluded.updated_at
                    """,
                    (security_id, cik10, args.symbol.upper(), utc_now()),
                )
            url = SEC_COMPANYFACTS_URL.format(cik10=cik10)
            data_bytes = http_get_bytes(url, args.user_agent)
            raw_uri, raw_hash = save_raw_bytes(raw_root, ["sec", f"cik={cik10}", "companyfacts.json"], data_bytes)
            doc_id = source_document_id(cik10, url, raw_hash)
            conn.execute(
                """
                INSERT OR IGNORE INTO source_documents(
                  doc_id, cik, accession_number, form, filed_at, source_url, local_path, sha256, document_kind, created_at
                ) VALUES (?, ?, NULL, NULL, NULL, ?, ?, ?, 'sec_companyfacts', ?)
                """,
                (doc_id, cik10, url, raw_uri, raw_hash, utc_now()),
            )
            companyfacts = json.loads(data_bytes.decode("utf-8"))
            raw_facts = list(iter_companyfacts_raw(companyfacts, security_id, cik10))
            candidates: list[dict[str, Any]] = []
            inserted_raw = 0
            for raw in raw_facts:
                insert_reporting_period(conn, raw)
                insert_context_and_unit(conn, raw)
                inserted_raw += insert_raw_fact(conn, raw, doc_id)
                candidate = canonical_candidate_from_raw(raw)
                if candidate is not None:
                    candidates.append(candidate)
            selected, exceptions = select_canonical_observations(candidates)
            inserted_selected = sum(insert_canonical_observation(conn, obs) for obs in selected)
            inserted_exceptions = sum(insert_exception(conn, exc) for exc in exceptions)
            inserted_derived = derive_quarters_from_ytd(conn, security_id)
            event = append_event(conn, "companyfacts_imported", {
                "cik": cik10,
                "security_id": security_id,
                "raw_uri": raw_uri,
                "raw_sha256": raw_hash,
                "raw_fact_count": len(raw_facts),
                "raw_facts_inserted": inserted_raw,
                "canonical_candidates": len(candidates),
                "canonical_selected_inserted": inserted_selected,
                "derived_quarter_observations_inserted": inserted_derived,
                "canonical_exceptions_inserted": inserted_exceptions,
                "resolver_version": RESOLVER_VERSION,
            })
            json_line(event)
            total_raw += inserted_raw
            total_selected += inserted_selected
            total_derived += inserted_derived
            total_exceptions += inserted_exceptions
            time.sleep(args.sleep_s)
    eprint(
        "companyfacts processed "
        f"raw_inserted={total_raw} selected_inserted={total_selected} "
        f"derived_inserted={total_derived} exceptions_inserted={total_exceptions}"
    )
    return 0



def cmd_universe_build(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    rows = conn.execute(
        """
        SELECT s.*,
               (SELECT COUNT(*) FROM canonical_observations co
                WHERE co.security_id = s.security_id
                  AND co.observation_status IN ('selected', 'derived')) AS fact_count
        FROM securities s
        WHERE s.investable = 1 AND s.adv_usd >= ?
        ORDER BY s.security_id
        """,
        (args.min_adv_usd,),
    ).fetchall()
    selected = [row for row in rows if int(row["fact_count"] or 0) >= args.min_fact_count]
    snapshot_id = str(uuid.uuid4())
    rule = {
        "name": args.name,
        "min_adv_usd": args.min_adv_usd,
        "min_fact_count": args.min_fact_count,
    }
    with conn:
        conn.execute(
            "INSERT INTO universe_snapshots(snapshot_id, name, created_at, rule_json) VALUES (?, ?, ?, ?)",
            (snapshot_id, args.name, utc_now(), canonical_json(rule)),
        )
        for row in selected:
            conn.execute(
                "INSERT INTO universe_members(snapshot_id, security_id) VALUES (?, ?)",
                (snapshot_id, int(row["security_id"])),
            )
        event = append_event(conn, "universe_snapshot_created", {
            "snapshot_id": snapshot_id,
            "name": args.name,
            "member_count": len(selected),
            "rule": rule,
        })
    json_line(event)
    return 0

def load_securities_array(conn: sqlite3.Connection) -> tuple[Any, int, list[sqlite3.Row]]:
    rows = conn.execute("SELECT * FROM securities ORDER BY security_id").fetchall()
    ArrayType = FaSecurity * max(len(rows), 1)
    arr = ArrayType()
    for i, row in enumerate(rows):
        arr[i] = make_security(row)
    return arr, len(rows), rows


def load_facts_array(conn: sqlite3.Connection) -> tuple[Any, int]:
    rows = conn.execute(
        """
        SELECT co.*, rp.raw_start_date, rp.raw_end_date, rp.raw_instant_date
        FROM canonical_observations co
        JOIN reporting_periods rp ON rp.period_id = co.period_id
        WHERE co.observation_status IN ('selected', 'derived')
          AND co.dimensional_scope = 'consolidated_total'
        ORDER BY co.security_id, co.metric_id, rp.raw_end_date, co.available_at
        """
    ).fetchall()
    ArrayType = FaCanonicalFact * max(len(rows), 1)
    arr = ArrayType()
    for i, row in enumerate(rows):
        arr[i] = make_fact(row)
    return arr, len(rows)


def load_positions_array(conn: sqlite3.Connection) -> tuple[Any, int]:
    rows = conn.execute("SELECT * FROM positions ORDER BY security_id").fetchall()
    ArrayType = FaPosition * max(len(rows), 1)
    arr = ArrayType()
    for i, row in enumerate(rows):
        arr[i] = make_position(row)
    return arr, len(rows)


def observation_quality_to_statement_quality(rows: Iterable[sqlite3.Row]) -> int:
    quality = STMT_QUALITY_NONE
    for row in rows:
        flags = int(row["quality_flags"] or 0)
        if flags & (QUALITY_AMENDED_FILING | QUALITY_RESTATEMENT):
            quality |= STMT_AMENDED_OR_RESTATED
        if flags & (
            QUALITY_FISCAL_CHANGE
            | QUALITY_MISSING_COMPARABLE
            | QUALITY_PERIOD_STUB
            | QUALITY_PERIOD_IRREGULAR
            | QUALITY_DURATION_MISMATCH
            | QUALITY_AMBIGUOUS_CANDIDATES
        ):
            quality |= STMT_NON_COMPARABLE_PERIOD
        if flags & (QUALITY_STALE | QUALITY_LOW_CONFIDENCE | QUALITY_LOW_PRECISION):
            quality |= STMT_LOW_CONFIDENCE
    return quality


def metric_observation_rows(
    conn: sqlite3.Connection,
    security_id: int,
    metric_id: int,
    period_semantics: tuple[str, ...],
    anchor_end_date: str | None,
) -> list[sqlite3.Row]:
    params: list[Any] = [security_id, metric_id, *period_semantics]
    anchor_sql = ""
    if anchor_end_date is not None:
        anchor_sql = "AND COALESCE(rp.raw_end_date, rp.raw_instant_date) <= ?"
        params.append(anchor_end_date)
    placeholders = ",".join("?" for _ in period_semantics)
    return conn.execute(
        f"""
        SELECT
            co.observation_id,
            co.metric_id,
            co.period_semantics,
            co.duration_days,
            co.value_decimal,
            co.quality_flags,
            co.available_at,
            rp.raw_start_date,
            rp.raw_end_date,
            rp.raw_instant_date
        FROM canonical_observations co
        JOIN reporting_periods rp ON rp.period_id = co.period_id
        WHERE co.security_id = ?
          AND co.metric_id = ?
          AND co.observation_status IN ('selected', 'derived')
          AND co.dimensional_scope = 'consolidated_total'
          AND co.period_semantics IN ({placeholders})
          {anchor_sql}
        ORDER BY COALESCE(rp.raw_end_date, rp.raw_instant_date) DESC,
                 co.available_at DESC,
                 co.observation_id DESC
        """,
        tuple(params),
    ).fetchall()


def unique_observations_by_end(rows: Iterable[sqlite3.Row]) -> list[sqlite3.Row]:
    out: list[sqlite3.Row] = []
    seen: set[str] = set()
    for row in rows:
        end_date = str(row["raw_end_date"] or row["raw_instant_date"] or "")
        if not end_date or end_date in seen:
            continue
        seen.add(end_date)
        out.append(row)
    return out


def latest_flow_ttm_value(
    conn: sqlite3.Connection,
    security_id: int,
    metric_id: int,
    anchor_end_date: str,
) -> dict[str, Any] | None:
    rows = metric_observation_rows(
        conn,
        security_id,
        metric_id,
        ("fiscal_quarter", "fiscal_year"),
        anchor_end_date,
    )
    quarter_rows = unique_observations_by_end(
        row for row in rows
        if row["period_semantics"] == "fiscal_quarter"
        and 70 <= int(row["duration_days"] or 0) <= 110
    )
    if len(quarter_rows) >= 4:
        selected = quarter_rows[:4]
        return {
            "value": sum(safe_float(row["value_decimal"]) for row in selected),
            "period_start_date": min(str(row["raw_start_date"]) for row in selected if row["raw_start_date"]),
            "period_end_date": max(str(row["raw_end_date"]) for row in selected if row["raw_end_date"]),
            "available_at": max(str(row["available_at"]) for row in selected),
            "quality_flags": observation_quality_to_statement_quality(selected),
            "source_ids": [str(row["observation_id"]) for row in selected],
        }

    fiscal_year_rows = [
        row for row in rows
        if row["period_semantics"] == "fiscal_year"
        and int(row["duration_days"] or 0) >= 330
    ]
    if fiscal_year_rows:
        row = fiscal_year_rows[0]
        return {
            "value": safe_float(row["value_decimal"]),
            "period_start_date": str(row["raw_start_date"]) if row["raw_start_date"] else None,
            "period_end_date": str(row["raw_end_date"]),
            "available_at": str(row["available_at"]),
            "quality_flags": observation_quality_to_statement_quality([row]),
            "source_ids": [str(row["observation_id"])],
        }

    return None


def latest_point_value(
    conn: sqlite3.Connection,
    security_id: int,
    metric_id: int,
    period_semantics: tuple[str, ...],
    anchor_end_date: str,
) -> dict[str, Any] | None:
    rows = metric_observation_rows(conn, security_id, metric_id, period_semantics, anchor_end_date)
    if not rows:
        return None
    row = rows[0]
    return {
        "value": safe_float(row["value_decimal"]),
        "period_start_date": str(row["raw_start_date"] or row["raw_instant_date"] or ""),
        "period_end_date": str(row["raw_end_date"] or row["raw_instant_date"] or ""),
        "available_at": str(row["available_at"]),
        "quality_flags": observation_quality_to_statement_quality([row]),
        "source_ids": [str(row["observation_id"])],
    }


def candidate_statement_end_dates(conn: sqlite3.Connection, security_id: int) -> list[str]:
    rows = metric_observation_rows(
        conn,
        security_id,
        METRIC_REVENUE,
        ("fiscal_quarter", "fiscal_year"),
        None,
    )
    end_dates: list[str] = []
    seen: set[str] = set()
    for row in rows:
        end_date = str(row["raw_end_date"] or "")
        if not end_date or end_date in seen:
            continue
        seen.add(end_date)
        end_dates.append(end_date)
        if len(end_dates) >= 8:
            break
    return end_dates


def build_statement_snapshot_for_end(
    conn: sqlite3.Connection,
    security_id: int,
    period_end_date: str,
) -> dict[str, Any] | None:
    revenue = latest_flow_ttm_value(conn, security_id, METRIC_REVENUE, period_end_date)
    net_income = latest_flow_ttm_value(conn, security_id, METRIC_NET_INCOME, period_end_date)
    eps = latest_flow_ttm_value(conn, security_id, METRIC_EPS_DILUTED, period_end_date)
    ocf = latest_flow_ttm_value(conn, security_id, METRIC_OPERATING_CASH_FLOW, period_end_date)
    capex = latest_flow_ttm_value(conn, security_id, METRIC_CAPEX, period_end_date)
    shares = latest_point_value(
        conn,
        security_id,
        METRIC_DILUTED_SHARES,
        ("fiscal_quarter", "fiscal_year", "fiscal_ytd"),
        period_end_date,
    )
    cash = latest_point_value(conn, security_id, METRIC_CASH, ("instant",), period_end_date)
    debt = latest_point_value(conn, security_id, METRIC_DEBT, ("instant",), period_end_date)

    if revenue is None and net_income is None and ocf is None:
        return None

    source_parts: list[str] = []
    quality = STMT_QUALITY_NONE
    for item in [revenue, net_income, eps, shares, ocf, capex, cash, debt]:
        if item is None:
            continue
        quality |= int(item["quality_flags"])
        source_parts.extend(str(source_id) for source_id in item["source_ids"])

    revenue_usd = safe_float(revenue["value"] if revenue else None)
    net_income_usd = safe_float(net_income["value"] if net_income else None)
    diluted_eps_usd = safe_float(eps["value"] if eps else None)
    diluted_shares = safe_float(shares["value"] if shares else None)
    operating_cash_flow_usd = safe_float(ocf["value"] if ocf else None)
    capex_usd = safe_float(capex["value"] if capex else None)
    cash_usd = safe_float(cash["value"] if cash else None)
    debt_usd = safe_float(debt["value"] if debt else None)

    if revenue is None or revenue_usd <= 0.0:
        quality |= STMT_MISSING_REVENUE | STMT_LOW_CONFIDENCE
    if shares is None or diluted_shares <= 0.0:
        quality |= STMT_MISSING_SHARES | STMT_LOW_CONFIDENCE
    if ocf is None or capex is None:
        quality |= STMT_LOW_CONFIDENCE
    if cash is None:
        quality |= STMT_MISSING_CASH
    if debt is None:
        quality |= STMT_MISSING_DEBT

    free_cash_flow_usd = operating_cash_flow_usd - capex_usd
    if free_cash_flow_usd < 0.0:
        quality |= STMT_NEGATIVE_FCF

    period_starts = [
        str(item["period_start_date"])
        for item in [revenue, net_income, eps, ocf, capex]
        if item is not None and item.get("period_start_date")
    ]
    period_ends = [
        str(item["period_end_date"])
        for item in [revenue, net_income, eps, ocf, capex]
        if item is not None and item.get("period_end_date")
    ]
    available_times = [
        str(item["available_at"])
        for item in [revenue, net_income, eps, shares, ocf, capex, cash, debt]
        if item is not None and item.get("available_at")
    ]

    if not period_ends or not available_times:
        return None

    source_hash = hashlib.sha256(canonical_json(sorted(source_parts)).encode()).hexdigest()
    snapshot = {
        "security_id": security_id,
        "period_start_date": min(period_starts) if period_starts else None,
        "period_end_date": max(period_ends),
        "available_at": max(available_times),
        "is_ttm": 1,
        "revenue_usd": revenue_usd,
        "net_income_usd": net_income_usd,
        "diluted_eps_usd": diluted_eps_usd,
        "diluted_shares": diluted_shares,
        "operating_cash_flow_usd": operating_cash_flow_usd,
        "capex_usd": capex_usd,
        "free_cash_flow_usd": free_cash_flow_usd,
        "cash_usd": cash_usd,
        "debt_usd": debt_usd,
        "net_debt_usd": debt_usd - cash_usd,
        "quality_flags": quality,
        "source_hash": source_hash,
    }
    snapshot["snapshot_id"] = stable_id("stmt", {
        "security_id": snapshot["security_id"],
        "period_end_date": snapshot["period_end_date"],
        "available_at": snapshot["available_at"],
        "source_hash": snapshot["source_hash"],
    })
    return snapshot


def build_latest_statement_snapshots(conn: sqlite3.Connection) -> list[dict[str, Any]]:
    rows = conn.execute(
        "SELECT security_id FROM securities WHERE investable = 1 ORDER BY security_id"
    ).fetchall()
    snapshots: list[dict[str, Any]] = []
    for row in rows:
        security_id = int(row["security_id"])
        seen_snapshot_ids: set[str] = set()
        for end_date in candidate_statement_end_dates(conn, security_id):
            snapshot = build_statement_snapshot_for_end(conn, security_id, end_date)
            if snapshot is None:
                continue
            snapshot_id = str(snapshot["snapshot_id"])
            if snapshot_id in seen_snapshot_ids:
                continue
            seen_snapshot_ids.add(snapshot_id)
            snapshots.append(snapshot)
            if len(seen_snapshot_ids) >= 2:
                break
    return snapshots


def persist_statement_snapshots(conn: sqlite3.Connection, snapshots: Iterable[dict[str, Any]]) -> int:
    inserted = 0
    for snapshot in snapshots:
        cur = conn.execute(
            """
            INSERT OR IGNORE INTO statement_snapshots(
                snapshot_id, security_id, period_start_date, period_end_date, available_at, is_ttm,
                revenue_usd, net_income_usd, diluted_eps_usd, diluted_shares,
                operating_cash_flow_usd, capex_usd, free_cash_flow_usd, cash_usd,
                debt_usd, net_debt_usd, quality_flags, source_hash, inserted_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                snapshot["snapshot_id"], snapshot["security_id"], snapshot["period_start_date"],
                snapshot["period_end_date"], snapshot["available_at"], snapshot["is_ttm"],
                snapshot["revenue_usd"], snapshot["net_income_usd"], snapshot["diluted_eps_usd"],
                snapshot["diluted_shares"], snapshot["operating_cash_flow_usd"], snapshot["capex_usd"],
                snapshot["free_cash_flow_usd"], snapshot["cash_usd"], snapshot["debt_usd"],
                snapshot["net_debt_usd"], snapshot["quality_flags"], snapshot["source_hash"], utc_now(),
            ),
        )
        inserted += max(int(cur.rowcount), 0)
    return inserted


def load_statement_snapshots_array(snapshots: list[dict[str, Any]]) -> tuple[Any, int]:
    ArrayType = FaStatementSnapshot * max(len(snapshots), 1)
    arr = ArrayType()
    for i, snapshot in enumerate(snapshots):
        arr[i] = make_statement_snapshot(snapshot)
    return arr, len(snapshots)


def insert_assumption_set(
    conn: sqlite3.Connection,
    assumptions: dict[str, Any],
    operator_label: str,
) -> str:
    assumption_json = canonical_json(assumptions)
    assumption_sha256 = hashlib.sha256(assumption_json.encode()).hexdigest()
    assumption_set_id = str(uuid.uuid4())
    conn.execute(
        """
        INSERT INTO model_assumptions(
            assumption_set_id, created_at, assumption_json, assumption_sha256, operator_label
        ) VALUES (?, ?, ?, ?, ?)
        """,
        (assumption_set_id, utc_now(), assumption_json, assumption_sha256, operator_label),
    )
    return assumption_set_id


def assumption_set(conn: sqlite3.Connection, assumption_set_id: str) -> sqlite3.Row:
    row = conn.execute(
        "SELECT * FROM model_assumptions WHERE assumption_set_id = ?",
        (assumption_set_id,),
    ).fetchone()
    if row is None:
        raise FaError(f"missing assumption set: {assumption_set_id}", 3)
    return row


def load_scenarios_from_assumption_set(
    conn: sqlite3.Connection,
    assumption_set_id: str,
) -> list[dict[str, Any]]:
    row = assumption_set(conn, assumption_set_id)
    try:
        assumptions = json.loads(str(row["assumption_json"]))
    except json.JSONDecodeError as exc:
        raise FaError(f"invalid assumption_json for {assumption_set_id}") from exc
    scenarios = assumptions.get("scenarios")
    if not isinstance(scenarios, list) or not (1 <= len(scenarios) <= 3):
        raise FaError("assumption set must contain 1 to 3 scenarios", 3)
    return [dict(item) for item in scenarios if isinstance(item, dict)]


def load_scenarios_array(scenarios: list[dict[str, Any]]) -> tuple[Any, int]:
    ArrayType = FaValuationScenario * max(len(scenarios), 1)
    arr = ArrayType()
    for i, scenario in enumerate(scenarios):
        arr[i] = make_valuation_scenario(scenario)
    return arr, len(scenarios)


def model_input_digest(
    statements: list[dict[str, Any]],
    securities_rows: list[sqlite3.Row],
    positions_rows: list[sqlite3.Row],
    assumption_sha256: str,
    config: dict[str, Any],
) -> str:
    payload = {
        "assumption_sha256": assumption_sha256,
        "statements": sorted(
            [
                {
                    key: value
                    for key, value in snapshot.items()
                    if key != "snapshot_id"
                }
                for snapshot in statements
            ],
            key=lambda item: (int(item["security_id"]), str(item["period_end_date"]), str(item["available_at"])),
        ),
        "securities": [dict(row) for row in securities_rows],
        "positions": [dict(row) for row in positions_rows],
        "config": config,
    }
    return hashlib.sha256(canonical_json(payload).encode()).hexdigest()


def record_valuation_forecasts(
    conn: sqlite3.Connection,
    run_id: str,
    horizon_days: int,
) -> int:
    rows = conn.execute(
        """
        SELECT security_id, expected_return_ratio, confidence_ratio, current_price_usd
        FROM valuations
        WHERE run_id = ?
        """,
        (run_id,),
    ).fetchall()
    inserted = 0
    for row in rows:
        conn.execute(
            """
            INSERT INTO forecast_outcomes(
                forecast_id, run_id, security_id, forecast_made_at, horizon_days,
                expected_return_ratio, confidence_ratio, price_at_forecast_usd
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                str(uuid.uuid4()), run_id, int(row["security_id"]), utc_now(), horizon_days,
                float(row["expected_return_ratio"]), float(row["confidence_ratio"]),
                float(row["current_price_usd"]),
            ),
        )
        inserted += 1
    return inserted


def get_setting(conn: sqlite3.Connection, key: str) -> str | None:
    row = conn.execute("SELECT value FROM settings WHERE key = ?", (key,)).fetchone()
    return str(row["value"]) if row else None


def recent_hit_rate(conn: sqlite3.Connection, horizon_days: int, min_count: int) -> float | None:
    rows = conn.execute(
        """
        SELECT expected_return_ratio, realized_return_ratio
        FROM forecast_outcomes
        WHERE horizon_days = ?
          AND realized_return_ratio IS NOT NULL
        ORDER BY outcome_recorded_at DESC
        LIMIT ?
        """,
        (horizon_days, min_count),
    ).fetchall()
    if len(rows) < min_count:
        return None
    hits = 0
    for row in rows:
        expected = float(row["expected_return_ratio"])
        realized = float(row["realized_return_ratio"])
        if expected == 0.0:
            continue
        if (expected > 0.0 and realized > 0.0) or (expected < 0.0 and realized < 0.0):
            hits += 1
    return hits / len(rows)


def proposed_turnover_ratio(conn: sqlite3.Connection, run_id: str, portfolio_value_usd: float) -> float:
    row = conn.execute(
        """
        SELECT COALESCE(SUM(notional_usd), 0.0) AS gross_notional
        FROM order_intents
        WHERE run_id = ?
        """,
        (run_id,),
    ).fetchone()
    gross_notional = float(row["gross_notional"] or 0.0)
    if portfolio_value_usd <= 0.0:
        return 1.0
    return gross_notional / portfolio_value_usd


def autonomy_gate(conn: sqlite3.Connection, run_id: str, portfolio_value_usd: float) -> dict[str, Any]:
    trading_mode = get_setting(conn, "trading_mode")
    if trading_mode not in {"paper", "live_limited", "live"}:
        return {"approved": False, "reason": f"trading_mode={trading_mode} blocks autonomous staging"}

    controls = conn.execute("SELECT * FROM autonomy_controls WHERE id = 1").fetchone()
    if controls is None or int(controls["enabled"]) == 0:
        return {"approved": False, "reason": "autonomy disabled"}

    hit_rate = recent_hit_rate(conn, horizon_days=90, min_count=50)
    if hit_rate is None:
        return {"approved": False, "reason": "insufficient online forecast history"}
    if hit_rate < float(controls["min_online_hit_rate"]):
        return {"approved": False, "reason": f"online hit rate too low: {hit_rate:.3f}"}

    turnover = proposed_turnover_ratio(conn, run_id, portfolio_value_usd)
    if turnover > float(controls["max_daily_turnover_ratio"]):
        return {"approved": False, "reason": f"turnover too high: {turnover:.3f}"}

    bad_valuations = conn.execute(
        """
        SELECT COUNT(*) AS n
        FROM valuations
        WHERE run_id = ?
          AND confidence_ratio < 0.50
          AND expected_return_ratio > 0.0
        """,
        (run_id,),
    ).fetchone()
    if int(bad_valuations["n"]) > 0:
        return {"approved": False, "reason": "positive recommendations include low-confidence valuations"}

    return {"approved": True, "reason": "autonomy gate approved"}


def cmd_model_run(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    lib = load_core(args.core_lib)
    facts_arr, fact_count = load_facts_array(conn)
    securities_arr, security_count, _security_rows = load_securities_array(conn)
    positions_arr, position_count = load_positions_array(conn)
    config = FaModelConfig()
    config.abi_version = ABI_VERSION
    config.target_gross_exposure_ratio = args.target_gross_exposure_ratio
    config.max_name_weight_ratio = args.max_name_weight_ratio
    config.min_expected_return_proxy = args.min_expected_return_proxy
    config.min_abs_order_notional_usd = args.min_abs_order_notional_usd
    config.max_forecast_abs_growth_ratio = args.max_forecast_abs_growth_ratio
    config.max_fact_age_s = int(args.max_fact_age_days * 86400)
    limits = make_risk_limits(args, conn)
    out = FaModelOutput()
    status = lib.fa_model_run_v1(
        facts_arr, fact_count, securities_arr, security_count, positions_arr, position_count,
        ctypes.byref(config), ctypes.byref(limits), ctypes.byref(out),
    )
    diagnostics = decode_c_string(out.diagnostics)
    if status != 0:
        lib.fa_model_output_free_v1(ctypes.byref(out))
        raise FaError(f"fa_model_run_v1 failed: {core_status_name(lib, status)}: {diagnostics}", 5)
    run_id = str(uuid.uuid4())
    config_json = {
        "target_gross_exposure_ratio": args.target_gross_exposure_ratio,
        "max_name_weight_ratio": args.max_name_weight_ratio,
        "min_expected_return_proxy": args.min_expected_return_proxy,
        "min_abs_order_notional_usd": args.min_abs_order_notional_usd,
        "max_forecast_abs_growth_ratio": args.max_forecast_abs_growth_ratio,
        "max_fact_age_days": args.max_fact_age_days,
        "core_lib": args.core_lib,
    }
    try:
        with conn:
            conn.execute(
                "INSERT INTO model_runs(run_id, occurred_at, config_json, diagnostics) VALUES (?, ?, ?, ?)",
                (run_id, utc_now(), canonical_json(config_json), diagnostics),
            )
            for i in range(out.forecast_count):
                f = out.forecasts[i]
                conn.execute(
                    """
                    INSERT INTO forecasts(run_id, security_id, revenue_growth_ratio, earnings_growth_ratio,
                                          expected_return_proxy, confidence_ratio, quality_flags, reason)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (run_id, int(f.security_id), f.revenue_growth_ratio, f.earnings_growth_ratio,
                     f.expected_return_proxy, f.confidence_ratio, int(f.quality_flags), decode_c_string(f.reason)),
                )
            for i in range(out.target_weight_count):
                t = out.target_weights[i]
                conn.execute(
                    """
                    INSERT INTO target_weights(run_id, security_id, current_weight_ratio, target_weight_ratio,
                                               delta_weight_ratio, reason)
                    VALUES (?, ?, ?, ?, ?, ?)
                    """,
                    (run_id, int(t.security_id), t.current_weight_ratio, t.target_weight_ratio,
                     t.delta_weight_ratio, decode_c_string(t.reason)),
                )
            for i in range(out.order_intent_count):
                oi = out.order_intents[i]
                intent_id = str(uuid.uuid4())
                conn.execute(
                    """
                    INSERT INTO order_intents(intent_id, run_id, security_id, side, notional_usd,
                                              current_weight_ratio, target_weight_ratio, reason, created_at)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (intent_id, run_id, int(oi.security_id), side_to_text(int(oi.side)), oi.notional_usd,
                     oi.current_weight_ratio, oi.target_weight_ratio, decode_c_string(oi.reason), utc_now()),
                )
            event = append_event(conn, "model_run_completed", {
                "run_id": run_id,
                "forecast_count": int(out.forecast_count),
                "target_weight_count": int(out.target_weight_count),
                "order_intent_count": int(out.order_intent_count),
                "diagnostics": diagnostics,
            })
        json_line(event)
    finally:
        lib.fa_model_output_free_v1(ctypes.byref(out))
    return 0


def cmd_model_run_v2(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    lib = load_core(args.core_lib)

    assumption_sources = [
        bool(args.assumption_set_id),
        bool(args.assumption_json),
        bool(args.assumption_file),
    ]
    if sum(1 for item in assumption_sources if item) > 1:
        raise FaError("choose only one of --assumption-set-id, --assumption-json, or --assumption-file", 3)

    if args.assumption_set_id:
        assumption_set_id = args.assumption_set_id
    else:
        if args.assumption_file:
            try:
                assumptions = json.loads(Path(args.assumption_file).read_text())
            except (OSError, json.JSONDecodeError) as exc:
                raise FaError(f"failed to read assumption file: {args.assumption_file}", 3) from exc
        elif args.assumption_json:
            try:
                assumptions = json.loads(args.assumption_json)
            except json.JSONDecodeError as exc:
                raise FaError("invalid --assumption-json", 3) from exc
        else:
            assumptions = DEFAULT_OPERATING_COMPANY_ASSUMPTIONS
        if not isinstance(assumptions, dict):
            raise FaError("assumptions must be a JSON object", 3)
        with conn:
            assumption_set_id = insert_assumption_set(
                conn,
                assumptions,
                args.operator_label,
            )

    assumption_row = assumption_set(conn, assumption_set_id)
    scenario_rows = load_scenarios_from_assumption_set(conn, assumption_set_id)
    scenarios_arr, scenario_count = load_scenarios_array(scenario_rows)

    statements_py = build_latest_statement_snapshots(conn)
    statements_arr, statement_count = load_statement_snapshots_array(statements_py)
    securities_arr, security_count, security_rows = load_securities_array(conn)
    positions_arr, position_count = load_positions_array(conn)
    position_rows = conn.execute("SELECT * FROM positions ORDER BY security_id").fetchall()

    config = FaModelConfig()
    config.abi_version = ABI_VERSION
    config.target_gross_exposure_ratio = args.target_gross_exposure_ratio
    config.max_name_weight_ratio = args.max_name_weight_ratio
    config.min_expected_return_proxy = args.min_expected_return_proxy
    config.min_abs_order_notional_usd = args.min_abs_order_notional_usd
    config.max_forecast_abs_growth_ratio = args.max_forecast_abs_growth_ratio
    config.max_fact_age_s = int(args.max_statement_age_days * 86400)
    limits = make_risk_limits(args, conn)

    config_json = {
        "model_version": "valuation_v2",
        "target_gross_exposure_ratio": args.target_gross_exposure_ratio,
        "max_name_weight_ratio": args.max_name_weight_ratio,
        "min_expected_return_proxy": args.min_expected_return_proxy,
        "min_abs_order_notional_usd": args.min_abs_order_notional_usd,
        "max_forecast_abs_growth_ratio": args.max_forecast_abs_growth_ratio,
        "max_statement_age_days": args.max_statement_age_days,
        "assumption_set_id": assumption_set_id,
        "assumption_sha256": str(assumption_row["assumption_sha256"]),
        "core_lib": args.core_lib,
    }
    config_json["model_input_sha256"] = model_input_digest(
        statements_py,
        security_rows,
        position_rows,
        str(assumption_row["assumption_sha256"]),
        config_json,
    )

    out = FaModelOutputV2()
    status = lib.fa_model_run_v2(
        statements_arr,
        statement_count,
        securities_arr,
        security_count,
        positions_arr,
        position_count,
        scenarios_arr,
        scenario_count,
        ctypes.byref(config),
        ctypes.byref(limits),
        ctypes.byref(out),
    )
    diagnostics = decode_c_string(out.diagnostics)
    if status != 0:
        lib.fa_model_output_free_v2(ctypes.byref(out))
        raise FaError(f"fa_model_run_v2 failed: {core_status_name(lib, status)}: {diagnostics}", 5)

    run_id = str(uuid.uuid4())
    try:
        with conn:
            statement_inserted = persist_statement_snapshots(conn, statements_py)
            conn.execute(
                "INSERT INTO model_runs(run_id, occurred_at, config_json, diagnostics) VALUES (?, ?, ?, ?)",
                (run_id, utc_now(), canonical_json(config_json), diagnostics),
            )
            for i in range(out.valuation_count):
                valuation = out.valuations[i]
                conn.execute(
                    """
                    INSERT INTO valuations(
                        run_id, security_id, current_price_usd, intrinsic_value_per_share_usd,
                        expected_return_ratio, market_cap_usd, enterprise_value_usd,
                        fcf_yield_ratio, net_debt_usd, confidence_ratio, quality_flags, reason
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        run_id, int(valuation.security_id), valuation.current_price_usd,
                        valuation.intrinsic_value_per_share_usd, valuation.expected_return_ratio,
                        valuation.market_cap_usd, valuation.enterprise_value_usd,
                        valuation.fcf_yield_ratio, valuation.net_debt_usd,
                        valuation.confidence_ratio, int(valuation.quality_flags),
                        decode_c_string(valuation.reason),
                    ),
                )
            for i in range(out.target_weight_count):
                target = out.target_weights[i]
                conn.execute(
                    """
                    INSERT INTO target_weights(run_id, security_id, current_weight_ratio, target_weight_ratio,
                                               delta_weight_ratio, reason)
                    VALUES (?, ?, ?, ?, ?, ?)
                    """,
                    (
                        run_id, int(target.security_id), target.current_weight_ratio,
                        target.target_weight_ratio, target.delta_weight_ratio,
                        decode_c_string(target.reason),
                    ),
                )
            for i in range(out.order_intent_count):
                intent = out.order_intents[i]
                intent_id = str(uuid.uuid4())
                conn.execute(
                    """
                    INSERT INTO order_intents(intent_id, run_id, security_id, side, notional_usd,
                                              current_weight_ratio, target_weight_ratio, reason, created_at)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        intent_id, run_id, int(intent.security_id), side_to_text(int(intent.side)),
                        intent.notional_usd, intent.current_weight_ratio,
                        intent.target_weight_ratio, decode_c_string(intent.reason), utc_now(),
                    ),
                )
            forecast_count = record_valuation_forecasts(conn, run_id, args.forecast_horizon_days)
            gate = autonomy_gate(conn, run_id, limits.portfolio_value_usd)
            event = append_event(conn, "model_run_v2_completed", {
                "run_id": run_id,
                "statement_count": statement_count,
                "statement_snapshots_inserted": statement_inserted,
                "valuation_count": int(out.valuation_count),
                "target_weight_count": int(out.target_weight_count),
                "order_intent_count": int(out.order_intent_count),
                "forecast_outcome_count": forecast_count,
                "assumption_set_id": assumption_set_id,
                "model_input_sha256": config_json["model_input_sha256"],
                "autonomy_approved": bool(gate["approved"]),
                "autonomy_reason": str(gate["reason"]),
                "diagnostics": diagnostics,
            })
        json_line(event)
    finally:
        lib.fa_model_output_free_v2(ctypes.byref(out))
    return 0


def cmd_risk_check(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    lib = load_core(args.core_lib)
    if args.run_id:
        rows = conn.execute(
            """
            SELECT oi.* FROM order_intents oi
            LEFT JOIN risk_decisions rd ON rd.intent_id = oi.intent_id
            WHERE oi.run_id = ? AND rd.intent_id IS NULL
            ORDER BY oi.created_at
            """,
            (args.run_id,),
        ).fetchall()
    else:
        rows = conn.execute(
            """
            SELECT oi.* FROM order_intents oi
            LEFT JOIN risk_decisions rd ON rd.intent_id = oi.intent_id
            WHERE rd.intent_id IS NULL
            ORDER BY oi.created_at
            """
        ).fetchall()
    ArrayType = FaOrderIntent * max(len(rows), 1)
    intents = ArrayType()
    intent_ids: list[str] = []
    for i, row in enumerate(rows):
        oi = FaOrderIntent()
        oi.abi_version = ABI_VERSION
        oi.security_id = int(row["security_id"])
        oi.side = side_from_text(row["side"])
        oi.notional_usd = float(row["notional_usd"])
        oi.current_weight_ratio = float(row["current_weight_ratio"])
        oi.target_weight_ratio = float(row["target_weight_ratio"])
        oi.reason = str(row["reason"] or "")[: REASON_BYTES - 1].encode("utf-8", errors="ignore")
        intents[i] = oi
        intent_ids.append(row["intent_id"])
    securities_arr, security_count, _security_rows = load_securities_array(conn)
    limits = make_risk_limits(args, conn)
    out = FaRiskOutput()
    status = lib.fa_risk_check_v1(intents, len(rows), securities_arr, security_count, ctypes.byref(limits), ctypes.byref(out))
    diagnostics = decode_c_string(out.diagnostics)
    if status != 0:
        lib.fa_risk_output_free_v1(ctypes.byref(out))
        raise FaError(f"fa_risk_check_v1 failed: {core_status_name(lib, status)}: {diagnostics}", 5)
    try:
        with conn:
            approved_count = 0
            for i in range(out.decision_count):
                decision = out.decisions[i]
                decision_id = str(uuid.uuid4())
                approved = int(decision.decision) == DECISION_APPROVED
                if approved:
                    approved_count += 1
                conn.execute(
                    """
                    INSERT INTO risk_decisions(decision_id, intent_id, security_id, approved, reason, notional_usd, created_at)
                    VALUES (?, ?, ?, ?, ?, ?, ?)
                    """,
                    (decision_id, intent_ids[i], int(decision.security_id), 1 if approved else 0,
                     decode_c_string(decision.reason), decision.notional_usd, utc_now()),
                )
            event = append_event(conn, "risk_check_completed", {
                "intent_count": int(out.decision_count),
                "approved_count": approved_count,
                "rejected_count": int(out.decision_count) - approved_count,
                "diagnostics": diagnostics,
            })
        json_line(event)
    finally:
        lib.fa_risk_output_free_v1(ctypes.byref(out))
    return 0


def cmd_broker_reconcile(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    with conn:
        conn.execute(
            """
            INSERT INTO broker_state(id, reconciled, checked_at, portfolio_value_usd, cash_usd)
            VALUES (1, ?, ?, ?, ?)
            ON CONFLICT(id) DO UPDATE SET
              reconciled=excluded.reconciled,
              checked_at=excluded.checked_at,
              portfolio_value_usd=excluded.portfolio_value_usd,
              cash_usd=excluded.cash_usd
            """,
            (int(args.reconciled), utc_now(), args.portfolio_value_usd, args.cash_usd),
        )
        event = append_event(conn, "broker_reconciliation_snapshot", {
            "reconciled": bool(args.reconciled),
            "portfolio_value_usd": args.portfolio_value_usd,
            "cash_usd": args.cash_usd,
            "source": "manual_or_mock",
        })
    json_line(event)
    return 0


def cmd_order_stage(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    mode = conn.execute("SELECT value FROM settings WHERE key = 'trading_mode'").fetchone()
    mode_value = mode["value"] if mode else "observe"
    if mode_value not in {"stage", "paper", "live_limited", "live"}:
        raise FaError(f"cannot stage orders in mode={mode_value!r}; set mode explicitly", 3)
    if args.autonomous:
        if not args.run_id:
            raise FaError("--autonomous staging requires --run-id", 3)
        broker = conn.execute("SELECT * FROM broker_state WHERE id = 1").fetchone()
        portfolio_value_usd = float(broker["portfolio_value_usd"] if broker else 0.0)
        gate = autonomy_gate(conn, args.run_id, portfolio_value_usd)
        with conn:
            append_event(conn, "autonomy_gate_checked", {
                "run_id": args.run_id,
                "approved": bool(gate["approved"]),
                "reason": str(gate["reason"]),
                "command": "order-stage",
            })
        if not gate["approved"]:
            raise FaError(f"autonomy gate blocked staging: {gate['reason']}", 3)
    params: tuple[Any, ...]
    run_filter = ""
    if args.run_id:
        run_filter = "AND oi.run_id = ?"
        params = (args.run_id, args.limit)
    else:
        params = (args.limit,)
    rows = conn.execute(
        f"""
        SELECT rd.*, oi.side
        FROM risk_decisions rd
        JOIN order_intents oi ON oi.intent_id = rd.intent_id
        LEFT JOIN staged_orders so ON so.decision_id = rd.decision_id
        WHERE rd.approved = 1 AND so.decision_id IS NULL
          {run_filter}
        ORDER BY rd.created_at
        LIMIT ?
        """,
        params,
    ).fetchall()
    staged = 0
    with conn:
        for row in rows:
            staged_order_id = str(uuid.uuid4())
            conn.execute(
                """
                INSERT INTO staged_orders(staged_order_id, decision_id, intent_id, security_id, side, notional_usd, staged_at)
                VALUES (?, ?, ?, ?, ?, ?, ?)
                """,
                (staged_order_id, row["decision_id"], row["intent_id"], row["security_id"], row["side"], row["notional_usd"], utc_now()),
            )
            event = append_event(conn, "order_staged", {
                "staged_order_id": staged_order_id,
                "decision_id": row["decision_id"],
                "intent_id": row["intent_id"],
                "security_id": row["security_id"],
                "side": row["side"],
                "notional_usd": row["notional_usd"],
            })
            json_line(event)
            staged += 1
    eprint(f"orders staged={staged}")
    return 0


def cmd_broker_submit(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    mode = conn.execute("SELECT value FROM settings WHERE key = 'trading_mode'").fetchone()
    mode_value = mode["value"] if mode else "observe"
    if args.adapter != "mock":
        raise FaError("only --adapter mock is implemented in this repository; live IBKR adapter must run as an isolated replacement", 3)
    if mode_value not in {"paper", "live_limited", "live"}:
        raise FaError(f"cannot submit broker orders in mode={mode_value!r}", 3)
    rows = conn.execute(
        """
        SELECT so.* FROM staged_orders so
        LEFT JOIN broker_events be ON be.staged_order_id = so.staged_order_id AND be.event_type = 'mock_order_submitted'
        WHERE be.broker_event_id IS NULL
        ORDER BY so.staged_at
        LIMIT ?
        """,
        (args.limit,),
    ).fetchall()
    submitted = 0
    with conn:
        for row in rows:
            broker_event_id = str(uuid.uuid4())
            payload = {
                "adapter": "mock",
                "staged_order_id": row["staged_order_id"],
                "security_id": row["security_id"],
                "side": row["side"],
                "notional_usd": row["notional_usd"],
                "mode": mode_value,
            }
            conn.execute(
                """
                INSERT INTO broker_events(broker_event_id, staged_order_id, event_type, payload_json, occurred_at)
                VALUES (?, ?, ?, ?, ?)
                """,
                (broker_event_id, row["staged_order_id"], "mock_order_submitted", canonical_json(payload), utc_now()),
            )
            event = append_event(conn, "broker_order_submitted_mock", payload | {"broker_event_id": broker_event_id})
            json_line(event)
            submitted += 1
    eprint(f"mock broker submissions={submitted}")
    return 0


def cmd_set_mode(args: argparse.Namespace) -> int:
    allowed = {"observe", "shadow", "stage", "paper", "live_limited", "live", "halted"}
    if args.mode not in allowed:
        raise FaError(f"invalid mode {args.mode!r}; allowed={sorted(allowed)}")
    conn = open_db(args.db)
    ensure_db(conn)
    with conn:
        conn.execute(
            """
            INSERT INTO settings(key, value, updated_at) VALUES ('trading_mode', ?, ?)
            ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
            """,
            (args.mode, utc_now()),
        )
        event = append_event(conn, "trading_mode_set", {"mode": args.mode})
    json_line(event)
    return 0


def cmd_trading_halt(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    with conn:
        conn.execute(
            """
            INSERT INTO settings(key, value, updated_at) VALUES ('trading_mode', 'halted', ?)
            ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
            """,
            (utc_now(),),
        )
        event = append_event(conn, "trading_halted", {"reason": args.reason})
    json_line(event)
    return 0


def cmd_status(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    mode = conn.execute("SELECT value FROM settings WHERE key='trading_mode'").fetchone()
    broker = conn.execute("SELECT * FROM broker_state WHERE id=1").fetchone()
    counts = {}
    for table in ["events", "securities", "filings", "source_documents", "xbrl_facts", "canonical_observations", "canonical_observation_exceptions", "universe_snapshots", "universe_members", "model_runs", "statement_snapshots", "model_assumptions", "valuations", "forecast_outcomes", "order_intents", "risk_decisions", "staged_orders", "broker_events"]:
        counts[table] = int(conn.execute(f"SELECT COUNT(*) AS n FROM {table}").fetchone()["n"])
    json_line({
        "trading_mode": mode["value"] if mode else "unknown",
        "broker_state": dict(broker) if broker else None,
        "counts": counts,
    })
    return 0


def cmd_report_daily(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    date = args.date if args.date != "today" else dt.datetime.now(dt.UTC).date().isoformat()
    prefix = f"{date}%"
    def count(table: str, where: str = "", params: tuple[Any, ...] = ()) -> int:
        sql = f"SELECT COUNT(*) AS n FROM {table} {where}"
        return int(conn.execute(sql, params).fetchone()["n"])
    mode = conn.execute("SELECT value FROM settings WHERE key='trading_mode'").fetchone()
    broker = conn.execute("SELECT * FROM broker_state WHERE id=1").fetchone()
    report = [
        f"FA DAILY REPORT — {date} UTC",
        "",
        "state:",
        f"  trading_mode: {mode['value'] if mode else 'unknown'}",
        f"  broker_reconciled: {broker['reconciled'] if broker else 'missing'}",
        f"  broker_checked_at: {broker['checked_at'] if broker else 'missing'}",
        "",
        "events:",
        f"  today: {count('events', 'WHERE occurred_at LIKE ?', (prefix,))}",
        "",
        "data:",
        f"  securities: {count('securities')}",
        f"  filings: {count('filings')}",
        f"  source_documents: {count('source_documents')}",
        f"  xbrl_facts: {count('xbrl_facts')}",
        f"  canonical_observations: {count('canonical_observations')}",
        f"  canonical_exceptions: {count('canonical_observation_exceptions')}",
        "",
        "model/execution:",
        f"  model_runs: {count('model_runs')}",
        f"  statement_snapshots: {count('statement_snapshots')}",
        f"  valuations: {count('valuations')}",
        f"  forecast_outcomes: {count('forecast_outcomes')}",
        f"  order_intents: {count('order_intents')}",
        f"  risk_decisions: {count('risk_decisions')}",
        f"  staged_orders: {count('staged_orders')}",
        f"  broker_events: {count('broker_events')}",
    ]
    print("\n".join(report))
    return 0


def cmd_notify(args: argparse.Namespace) -> int:
    if args.method == "ntfy":
        topic = args.ntfy_topic or os.environ.get("NTFY_TOPIC")
        if not topic:
            raise FaError("ntfy requires --ntfy-topic or NTFY_TOPIC", 1)
        url = f"https://ntfy.sh/{urllib.parse.quote(topic)}"
        data = args.message.encode("utf-8")
        headers = {"Title": args.title, "Priority": args.priority}
        req = urllib.request.Request(url, data=data, headers=headers, method="POST")
    elif args.method == "pushover":
        token = os.environ.get("PUSHOVER_TOKEN")
        user = os.environ.get("PUSHOVER_USER")
        if not token or not user:
            raise FaError("pushover requires PUSHOVER_TOKEN and PUSHOVER_USER", 1)
        data = urllib.parse.urlencode({"token": token, "user": user, "title": args.title, "message": args.message}).encode()
        req = urllib.request.Request("https://api.pushover.net/1/messages.json", data=data, method="POST")
    else:
        raise FaError(f"unsupported notification method {args.method!r}")
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            body = resp.read().decode("utf-8", errors="replace")
    except urllib.error.URLError as exc:
        raise FaError(f"notification failed: {exc}", 2) from exc
    json_line({"method": args.method, "status": "sent", "response": body[:200]})
    return 0


def add_common_db(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--db", required=True, help="SQLite database path")


def add_core_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--core-lib", required=True, help="Path to libfa_core shared library")
    parser.add_argument("--portfolio-value-usd", type=float, default=None)
    parser.add_argument("--cash-usd", type=float, default=None)
    parser.add_argument("--max-name-weight-ratio", type=float, default=0.05)
    parser.add_argument("--max-order-notional-usd", type=float, default=10000.0)
    parser.add_argument("--min-adv-usd", type=float, default=1000000.0)
    parser.add_argument("--max-adv-participation-ratio", type=float, default=0.01)
    parser.add_argument("--min-abs-order-notional-usd", type=float, default=100.0)
    parser.add_argument("--max-reconciliation-age-s", type=int, default=3600)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="fa", description="sec-fa toolchain")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("init-db")
    add_common_db(p)
    p.set_defaults(func=cmd_init_db)

    p = sub.add_parser("security-upsert")
    add_common_db(p)
    p.add_argument("--cik", required=True)
    p.add_argument("--symbol", required=True)
    p.add_argument("--price-usd", type=float, default=0.0)
    p.add_argument("--adv-usd", type=float, default=0.0)
    p.add_argument("--investable", type=int, choices=[0, 1], default=0)
    p.set_defaults(func=cmd_security_upsert)

    p = sub.add_parser("position-upsert")
    add_common_db(p)
    p.add_argument("--cik", required=True)
    p.add_argument("--quantity-shares", type=float, required=True)
    p.add_argument("--market-value-usd", type=float, required=True)
    p.add_argument("--weight-ratio", type=float, required=True)
    p.set_defaults(func=cmd_position_upsert)

    p = sub.add_parser("sec-watch")
    add_common_db(p)
    p.add_argument("--cik", action="append", required=True)
    p.add_argument("--forms", default="10-K,10-Q,8-K,10-K/A,10-Q/A,8-K/A")
    p.add_argument("--since")
    p.add_argument("--limit", type=int, default=50)
    p.add_argument("--sleep-s", type=float, default=0.12)
    p.add_argument("--user-agent", default=DEFAULT_USER_AGENT)
    p.set_defaults(func=cmd_sec_watch)

    p = sub.add_parser("sec-fetch")
    add_common_db(p)
    p.add_argument("--raw-root", required=True)
    p.add_argument("--accession")
    p.add_argument("--limit", type=int, default=100)
    p.add_argument("--sleep-s", type=float, default=0.12)
    p.add_argument("--user-agent", default=DEFAULT_USER_AGENT)
    p.set_defaults(func=cmd_sec_fetch)

    p = sub.add_parser("facts-companyfacts")
    add_common_db(p)
    p.add_argument("--cik", action="append", required=True)
    p.add_argument("--symbol")
    p.add_argument("--raw-root", required=True)
    p.add_argument("--sleep-s", type=float, default=0.12)
    p.add_argument("--user-agent", default=DEFAULT_USER_AGENT)
    p.set_defaults(func=cmd_facts_companyfacts)

    p = sub.add_parser("xbrl-parse")
    add_common_db(p)
    p.add_argument("--accession", action="append")
    p.add_argument("--cik")
    p.add_argument("--symbol")
    p.add_argument("--form")
    p.add_argument("--filed-at")
    p.add_argument("--accepted-at")
    p.add_argument("--raw-root", required=True)
    p.add_argument("--package-root", help="Local accession directory containing index.json; used by deterministic tests")
    p.add_argument("--limit", type=int, default=25)
    p.add_argument("--sleep-s", type=float, default=0.12)
    p.add_argument("--user-agent", default=DEFAULT_USER_AGENT)
    p.add_argument("--store-all-documents", action="store_true")
    p.add_argument("--max-document-bytes", type=int, default=MAX_XBRL_DOCUMENT_BYTES)
    p.add_argument("--strict", action="store_true")
    p.set_defaults(func=cmd_xbrl_parse)


    p = sub.add_parser("universe-build")
    add_common_db(p)
    p.add_argument("--name", required=True)
    p.add_argument("--min-adv-usd", type=float, default=0.0)
    p.add_argument("--min-fact-count", type=int, default=0)
    p.set_defaults(func=cmd_universe_build)

    p = sub.add_parser("broker-reconcile")
    add_common_db(p)
    p.add_argument("--portfolio-value-usd", type=float, required=True)
    p.add_argument("--cash-usd", type=float, required=True)
    p.add_argument("--reconciled", type=int, choices=[0, 1], required=True)
    p.set_defaults(func=cmd_broker_reconcile)

    p = sub.add_parser("model-run")
    add_common_db(p)
    add_core_args(p)
    p.add_argument("--target-gross-exposure-ratio", type=float, default=0.50)
    p.add_argument("--min-expected-return-proxy", type=float, default=0.01)
    p.add_argument("--max-forecast-abs-growth-ratio", type=float, default=2.0)
    p.add_argument("--max-fact-age-days", type=float, default=3650.0)
    p.set_defaults(func=cmd_model_run)

    p = sub.add_parser("model-run-v2")
    add_common_db(p)
    add_core_args(p)
    p.add_argument("--target-gross-exposure-ratio", type=float, default=0.50)
    p.add_argument("--min-expected-return-proxy", type=float, default=0.01)
    p.add_argument("--max-forecast-abs-growth-ratio", type=float, default=2.0)
    p.add_argument("--max-statement-age-days", type=float, default=3650.0)
    p.add_argument("--assumption-set-id")
    p.add_argument("--assumption-json")
    p.add_argument("--assumption-file")
    p.add_argument("--operator-label", default="default")
    p.add_argument("--forecast-horizon-days", type=int, default=90)
    p.set_defaults(func=cmd_model_run_v2)

    p = sub.add_parser("risk-check")
    add_common_db(p)
    add_core_args(p)
    p.add_argument("--run-id")
    p.set_defaults(func=cmd_risk_check)

    p = sub.add_parser("order-stage")
    add_common_db(p)
    p.add_argument("--limit", type=int, default=100)
    p.add_argument("--run-id")
    p.add_argument("--autonomous", action="store_true")
    p.set_defaults(func=cmd_order_stage)

    p = sub.add_parser("broker-submit")
    add_common_db(p)
    p.add_argument("--adapter", default="mock")
    p.add_argument("--limit", type=int, default=100)
    p.set_defaults(func=cmd_broker_submit)

    p = sub.add_parser("set-mode")
    add_common_db(p)
    p.add_argument("--mode", required=True)
    p.set_defaults(func=cmd_set_mode)

    p = sub.add_parser("trading-halt")
    add_common_db(p)
    p.add_argument("--reason", required=True)
    p.set_defaults(func=cmd_trading_halt)

    p = sub.add_parser("status")
    add_common_db(p)
    p.set_defaults(func=cmd_status)

    p = sub.add_parser("report")
    report_sub = p.add_subparsers(dest="report_command", required=True)
    rp = report_sub.add_parser("daily")
    add_common_db(rp)
    rp.add_argument("--date", default="today")
    rp.set_defaults(func=cmd_report_daily)

    p = sub.add_parser("notify")
    p.add_argument("--method", choices=["ntfy", "pushover"], required=True)
    p.add_argument("--title", default="sec-fa")
    p.add_argument("--message", required=True)
    p.add_argument("--priority", default="default")
    p.add_argument("--ntfy-topic")
    p.set_defaults(func=cmd_notify)

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return int(args.func(args))
    except FaError as exc:
        eprint(f"error: {exc}")
        return exc.exit_code
    except KeyboardInterrupt:
        eprint("interrupted")
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
