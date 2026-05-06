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
import zlib
from typing import Any, Iterable

ABI_VERSION = 1
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

CANONICAL_TAGS: dict[int, list[tuple[str, str, list[str]]]] = {
    METRIC_REVENUE: [
        ("us-gaap", "RevenueFromContractWithCustomerExcludingAssessedTax", ["USD"]),
        ("us-gaap", "RevenueFromContractWithCustomerIncludingAssessedTax", ["USD"]),
        ("us-gaap", "SalesRevenueNet", ["USD"]),
        ("us-gaap", "Revenues", ["USD"]),
    ],
    METRIC_NET_INCOME: [
        ("us-gaap", "NetIncomeLoss", ["USD"]),
        ("us-gaap", "ProfitLoss", ["USD"]),
    ],
    METRIC_EPS_DILUTED: [
        ("us-gaap", "EarningsPerShareDiluted", ["USD/shares", "USD / shares"]),
    ],
    METRIC_DILUTED_SHARES: [
        ("us-gaap", "WeightedAverageNumberOfDilutedSharesOutstanding", ["shares"]),
    ],
    METRIC_OPERATING_CASH_FLOW: [
        ("us-gaap", "NetCashProvidedByUsedInOperatingActivities", ["USD"]),
    ],
    METRIC_CAPEX: [
        ("us-gaap", "PaymentsToAcquirePropertyPlantAndEquipment", ["USD"]),
    ],
    METRIC_CASH: [
        ("us-gaap", "CashAndCashEquivalentsAtCarryingValue", ["USD"]),
        ("us-gaap", "CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents", ["USD"]),
    ],
    METRIC_DEBT: [
        ("us-gaap", "LongTermDebtAndFinanceLeaseObligationsCurrent", ["USD"]),
        ("us-gaap", "LongTermDebtCurrent", ["USD"]),
        ("us-gaap", "LongTermDebtNoncurrent", ["USD"]),
    ],
}

MUTATING_COMMANDS = {
    "init-db", "security-upsert", "position-upsert", "sec-watch", "sec-fetch",
    "facts-companyfacts", "broker-reconcile", "model-run", "risk-check",
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


def fact_identity_hash(fact: dict[str, Any]) -> str:
    keys = [
        "security_id", "metric_id", "period_start_date", "period_end_date", "available_at",
        "accession_number", "taxonomy", "tag", "unit",
    ]
    return hashlib.sha256(canonical_json({key: fact.get(key) for key in keys}).encode()).hexdigest()


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
    fact.value = float(row["value"])
    fact.period_start_day = parse_date_to_epoch_day(row["period_start_date"])
    fact.period_end_day = parse_date_to_epoch_day(row["period_end_date"])
    fact.available_at_epoch_s = parse_instant_to_epoch_s(row["available_at"])
    fact.quality_flags = int(row["quality_flags"] or 0)
    return fact


def make_position(row: sqlite3.Row) -> FaPosition:
    pos = FaPosition()
    pos.abi_version = ABI_VERSION
    pos.security_id = int(row["security_id"])
    pos.quantity_shares = float(row["quantity_shares"] or 0.0)
    pos.market_value_usd = float(row["market_value_usd"] or 0.0)
    pos.weight_ratio = float(row["weight_ratio"] or 0.0)
    return pos


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


def iter_companyfacts_canonical(companyfacts: dict[str, Any], security_id: int) -> Iterable[dict[str, Any]]:
    facts_root = companyfacts.get("facts", {})
    for metric_id, tag_specs in CANONICAL_TAGS.items():
        for taxonomy, tag, allowed_units in tag_specs:
            tag_node = facts_root.get(taxonomy, {}).get(tag)
            if not tag_node:
                continue
            units = tag_node.get("units", {})
            for unit in allowed_units:
                for item in units.get(unit, []):
                    val = item.get("val")
                    if not isinstance(val, (int, float)) or not math.isfinite(float(val)):
                        continue
                    end = item.get("end") or item.get("fy")
                    if not isinstance(end, str) or len(end) < 10:
                        continue
                    start = item.get("start") if isinstance(item.get("start"), str) else None
                    filed = item.get("filed") if isinstance(item.get("filed"), str) else None
                    accession = item.get("accn") if isinstance(item.get("accn"), str) else None
                    quality_flags = 0
                    form = str(item.get("form") or "")
                    if form.endswith("/A") or (accession and accession.endswith("A")):
                        quality_flags |= 1 << 2
                    yield {
                        "security_id": security_id,
                        "metric_id": metric_id,
                        "value": float(val),
                        "period_start_date": start,
                        "period_end_date": end[:10],
                        "available_at": filed_date_to_available_at(filed),
                        "accession_number": accession,
                        "taxonomy": taxonomy,
                        "tag": tag,
                        "unit": unit,
                        "quality_flags": quality_flags,
                    }
            # Prefer the first available explicit mapping for a metric; later tags are fallbacks.
            if tag_node:
                break


def cmd_facts_companyfacts(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    raw_root = Path(args.raw_root)
    total = 0
    with conn:
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
            companyfacts = json.loads(data_bytes.decode("utf-8"))
            inserted = 0
            for fact in iter_companyfacts_canonical(companyfacts, security_id):
                cur = conn.execute(
                    """
                    INSERT OR IGNORE INTO canonical_facts(
                      security_id, metric_id, value, period_start_date, period_end_date, available_at,
                      accession_number, taxonomy, tag, unit, quality_flags, fact_hash, source_event_id, inserted_at
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        fact["security_id"], fact["metric_id"], fact["value"], fact["period_start_date"],
                        fact["period_end_date"], fact["available_at"], fact["accession_number"], fact["taxonomy"],
                        fact["tag"], fact["unit"], fact["quality_flags"], fact_identity_hash(fact), None, utc_now(),
                    ),
                )
                inserted += max(int(cur.rowcount), 0)
            event = append_event(conn, "companyfacts_imported", {
                "cik": cik10,
                "security_id": security_id,
                "raw_uri": raw_uri,
                "raw_sha256": raw_hash,
                "canonical_fact_candidates": inserted,
            })
            json_line(event)
            total += inserted
            time.sleep(args.sleep_s)
    eprint(f"canonical fact candidates processed={total}")
    return 0



def cmd_universe_build(args: argparse.Namespace) -> int:
    conn = open_db(args.db)
    ensure_db(conn)
    rows = conn.execute(
        """
        SELECT s.*,
               (SELECT COUNT(*) FROM canonical_facts cf WHERE cf.security_id = s.security_id) AS fact_count
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
    rows = conn.execute("SELECT * FROM canonical_facts ORDER BY security_id, metric_id, period_end_date").fetchall()
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
    rows = conn.execute(
        """
        SELECT rd.*, oi.side
        FROM risk_decisions rd
        JOIN order_intents oi ON oi.intent_id = rd.intent_id
        LEFT JOIN staged_orders so ON so.decision_id = rd.decision_id
        WHERE rd.approved = 1 AND so.decision_id IS NULL
        ORDER BY rd.created_at
        LIMIT ?
        """,
        (args.limit,),
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
    for table in ["events", "securities", "filings", "canonical_facts", "universe_snapshots", "universe_members", "model_runs", "order_intents", "risk_decisions", "staged_orders", "broker_events"]:
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
        f"  canonical_facts: {count('canonical_facts')}",
        "",
        "model/execution:",
        f"  model_runs: {count('model_runs')}",
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

    p = sub.add_parser("risk-check")
    add_common_db(p)
    add_core_args(p)
    p.add_argument("--run-id")
    p.set_defaults(func=cmd_risk_check)

    p = sub.add_parser("order-stage")
    add_common_db(p)
    p.add_argument("--limit", type=int, default=100)
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
