from __future__ import annotations

import os
import shutil
import time
from dataclasses import dataclass
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

import requests

from sqlalchemy import text
from sqlalchemy.orm import Session

from ..config import settings
from ..models import (
    AnalysisSnapshot,
    ContactScoreSnapshot,
    ContactValueMetricSnapshot,
    Report,
    SyncState,
    Task,
)
from .aggregation_retention import DEFAULT_RETENTION_DAYS
from .llm_client import load_ai_config
from ..background import get_background_runtime_snapshot
from .media_collector_runner import get_media_collector_run_state
from .mp_rss_store import DEFAULT_MP_UPSTREAM_URL


def _enabled_router_channels(router: dict[str, Any]) -> list[dict[str, Any]]:
    channels: list[dict[str, Any]] = []
    for lane in ("main_channels", "mid_channels", "tool_channels"):
        lane_channels = router.get(lane) if isinstance(router.get(lane), list) else []
        channels.extend([c for c in lane_channels if isinstance(c, dict) and bool(c.get("enabled", True))])
    return channels


def _has_usable_llm_key(conf: dict[str, Any], channels: list[dict[str, Any]]) -> bool:
    base_key = str(conf.get("api_key") or conf.get("siliconflow_api_key") or "").strip()
    if base_key:
        return True
    return any(str(c.get("api_key") or "").strip() for c in channels)


def _summarize_ai_config(conf: dict[str, Any]) -> dict[str, Any]:
    router = conf.get("model_router") if isinstance(conf.get("model_router"), dict) else {}
    channels = _enabled_router_channels(router)
    return {
        "llm_api_key_configured": _has_usable_llm_key(conf, channels),
        "model_router_enabled": bool(router.get("enabled", True)) if router else False,
        "enabled_channels": len(channels),
    }


def _estimate_old_aggregation_rows(
    db: Session | None,
    *,
    now: datetime | None = None,
    retention_days: int = DEFAULT_RETENTION_DAYS,
) -> dict[str, int | None]:
    cutoff = (now or datetime.utcnow()) - timedelta(days=retention_days)
    targets = (
        ("analysis_snapshots", AnalysisSnapshot, AnalysisSnapshot.created_at),
        ("tasks", Task, Task.created_at),
        ("reports", Report, Report.created_at),
        ("contact_score_snapshots", ContactScoreSnapshot, ContactScoreSnapshot.as_of),
        ("contact_value_metric_snapshots", ContactValueMetricSnapshot, ContactValueMetricSnapshot.as_of),
    )
    estimates: dict[str, int | None] = {}
    if db is None:
        return {name: None for name, _model, _column in targets}
    for name, model, column in targets:
        try:
            estimates[name] = int(db.query(model).filter(column < cutoff).count())
        except Exception:
            estimates[name] = None
    return estimates


def _aggregation_retention_diagnostics(db: Session | None) -> dict[str, Any]:
    return {
        "retention_days": DEFAULT_RETENTION_DAYS,
        "protected_raw_tables": ["messages", "email_messages", "contacts"],
        "prune_endpoint": "/api/admin/aggregation-retention/prune",
        "estimated_old_rows": _estimate_old_aggregation_rows(db),
    }


def _load_sync_json(db: Session | None, key: str) -> dict[str, Any]:
    if db is None:
        return {}
    try:
        import json as _json

        row = db.get(SyncState, key)
        if not row or not row.value:
            return {}
        data = _json.loads(row.value)
        return data if isinstance(data, dict) else {}
    except Exception:
        return {}


def _cloud_agent_runtime_diagnostics() -> dict[str, Any]:
    hermes_home = os.environ.get("HERMES_HOME") or str(Path.home() / ".hermes")
    openclaw_home = (
        os.environ.get("OPENCLAW_HOME")
        or os.environ.get("LOBSTER_HOME")
        or str(Path.home() / ".openclaw")
    )
    return {
        "hermes": {
            "home": hermes_home,
            "available": Path(hermes_home).exists(),
        },
        "openclaw": {
            "home": openclaw_home,
            "available": Path(openclaw_home).exists(),
        },
        "agent_api": {
            "enabled": bool(
                str(getattr(settings, "AGENT_API_TOKEN", "") or "").strip()
                or str(getattr(settings, "AGENT_API_TOKENS", "") or "").strip()
            ),
            "allowlist_configured": bool(str(getattr(settings, "AGENT_API_ALLOWLIST", "") or "").strip()),
            "blocklist_configured": bool(str(getattr(settings, "AGENT_API_BLOCKLIST", "") or "").strip()),
            "base_path": "/api/agent",
        },
    }


def probe_chatlog_http(base_url: str | None = None, timeout: float | None = None) -> dict[str, Any]:
    base = str(base_url or settings.CHATLOG_HTTP_BASE or "").strip().rstrip("/")
    if not base:
        return {"ok": False, "error": "CHATLOG_HTTP_BASE empty"}
    effective_timeout = float(timeout or getattr(settings, "CHATLOG_HTTP_SESSION_TIMEOUT_SECONDS", 5) or 5)
    url = f"{base}/api/v1/session"
    try:
        resp = requests.get(url, timeout=effective_timeout)
        return {
            "ok": resp.status_code < 500,
            "status_code": resp.status_code,
            "url": url,
        }
    except Exception as exc:
        return {"ok": False, "url": url, "error": str(exc)}


def probe_mp_upstream(db: Session | None = None, timeout: float = 3.0) -> dict[str, Any]:
    cfg = _load_sync_json(db, "mp_config")
    base = str(cfg.get("upstream_base_url") or cfg.get("base_url") or DEFAULT_MP_UPSTREAM_URL or "").strip().rstrip("/")
    if not base:
        return {"ok": False, "error": "公众号外部源未配置"}
    if not base.startswith(("http://", "https://")):
        base = "http://" + base
    url = f"{base}/api/v1/wx/public/channels"
    started = time.monotonic()
    try:
        resp = requests.get(url, params={"limit": 1, "offset": 0}, timeout=timeout)
        latency = int((time.monotonic() - started) * 1000)
        return {
            "ok": resp.status_code < 500,
            "status_code": resp.status_code,
            "url": url,
            "latency_ms": latency,
        }
    except Exception as exc:
        return {"ok": False, "url": url, "error": str(exc)}


def collector_installation_status() -> dict[str, Any]:
    root = Path.cwd()
    collector_dir = root / "media-collector"
    scripts = {
        "hot": collector_dir / "collect.sh",
        "search": collector_dir / "batch_search.sh",
        "authors": collector_dir / "batch_author_search.sh",
    }
    script_status = {name: path.exists() for name, path in scripts.items()}
    run_state = get_media_collector_run_state()
    status = run_state.get("status") if isinstance(run_state.get("status"), dict) else {}
    return {
        "collector_dir": str(collector_dir),
        "scripts": script_status,
        "scripts_ready": all(script_status.values()),
        "running": bool(run_state.get("running")),
        "last_run": run_state.get("last_run"),
        "hot_latest_day": (status.get("hot") or {}).get("latest_day") if isinstance(status.get("hot"), dict) else None,
        "search_latest_day": (status.get("search") or {}).get("latest_day") if isinstance(status.get("search"), dict) else None,
        "authors_latest_day": (status.get("authors") or {}).get("latest_day") if isinstance(status.get("authors"), dict) else None,
    }


DEFAULT_WRITABLE_PATHS = (
    Path("data"),
    Path("data") / "datasets",
    Path("backups"),
)


@dataclass
class ReadinessCheck:
    name: str
    status: str
    error_code: str | None = None
    message: str | None = None
    latency_ms: int | None = None

    def as_dict(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "status": self.status,
            "error_code": self.error_code,
            "message": self.message,
            "latency_ms": self.latency_ms,
        }


def _disk_info(path: str | os.PathLike[str] = ".") -> dict[str, Any]:
    usage = shutil.disk_usage(path)
    return {
        "path": os.fspath(path),
        "total_bytes": usage.total,
        "used_bytes": usage.used,
        "free_bytes": usage.free,
        "free_mb": round(usage.free / 1024 / 1024, 1),
    }


def _check_writable_paths(paths: tuple[Path, ...] = DEFAULT_WRITABLE_PATHS) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    for path in paths:
        current = Path(path)
        try:
            current.mkdir(parents=True, exist_ok=True)
            probe = current / ".write_probe"
            probe.write_text("ok", encoding="utf-8")
            probe.unlink(missing_ok=True)
            out.append({"path": str(current), "writable": True})
        except Exception as exc:
            out.append({"path": str(current), "writable": False, "error": str(exc)})
    return out


def build_readiness_checks(db: Session) -> list[ReadinessCheck]:
    checks: list[ReadinessCheck] = []

    # database
    try:
        db.execute(text("SELECT 1"))
        checks.append(ReadinessCheck(name="database", status="ok"))
    except Exception as exc:
        checks.append(ReadinessCheck(name="database", status="fail", error_code="DB-UNAVAILABLE-001", message=str(exc)))

    # sqlite fts
    try:
        if settings.DATABASE_URL.startswith("sqlite"):
            row = db.execute(text("SELECT name FROM sqlite_master WHERE type='table' AND name='messages_fts' LIMIT 1")).scalar()
            if not row:
                raise RuntimeError("messages_fts missing")
        checks.append(ReadinessCheck(name="sqlite_fts", status="ok"))
    except Exception as exc:
        checks.append(ReadinessCheck(name="sqlite_fts", status="fail", error_code="DB-FTS-001", message=str(exc)))

    # disk space
    try:
        info = _disk_info(Path.cwd())
        if info["free_bytes"] < 1_000_000_000:
            raise RuntimeError(f"low disk space: {info['free_mb']} MB free")
        checks.append(ReadinessCheck(name="disk_space", status="ok", message=f"free_mb={info['free_mb']}"))
    except Exception as exc:
        checks.append(ReadinessCheck(name="disk_space", status="fail", error_code="SYS-DISK-001", message=str(exc)))

    # writable paths
    try:
        results = _check_writable_paths()
        failures = [item for item in results if not item.get("writable")]
        if failures:
            raise RuntimeError(
                ", ".join(f"{item['path']}: {item.get('error', 'unwritable')}" for item in failures)
            )
        checks.append(ReadinessCheck(name="writable_paths", status="ok", message=f"checked={len(results)}"))
    except Exception as exc:
        checks.append(ReadinessCheck(name="writable_paths", status="fail", error_code="SYS-PATH-001", message=str(exc)))

    # external config
    try:
        conf = load_ai_config()
        router = conf.get("model_router") if isinstance(conf.get("model_router"), dict) else {}
        if not router or not bool(router.get("enabled", True)):
            raise RuntimeError("model_router disabled")
        channels = _enabled_router_channels(router)
        if not channels:
            raise RuntimeError("no enabled route channels")
        checks.append(ReadinessCheck(name="external_config", status="ok", message=f"enabled_channels={len(channels)}"))

        if not _has_usable_llm_key(conf, channels):
            raise RuntimeError("missing llm api key")
        checks.append(ReadinessCheck(name="llm_config", status="ok", message="api_key=configured"))
    except Exception as exc:
        checks.append(ReadinessCheck(name="external_config", status="fail", error_code="CFG-STATE-002", message=str(exc)))
        if "llm_config" not in {c.name for c in checks}:
            checks.append(ReadinessCheck(name="llm_config", status="fail", error_code="LLM-CFG-001", message=str(exc)))

    # chatlog reachability
    try:
        probe = probe_chatlog_http()
        if not probe.get("ok"):
            raise RuntimeError(str(probe.get("error") or f"http_status={probe.get('status_code')}"))
        checks.append(ReadinessCheck(name="chatlog_http", status="ok", message=f"status={probe.get('status_code')}"))
    except Exception as exc:
        checks.append(ReadinessCheck(name="chatlog_http", status="fail", error_code="CHATLOG-HTTP-001", message=str(exc)))

    # media collector installation
    try:
        collector = collector_installation_status()
        if not collector.get("scripts_ready"):
            raise RuntimeError(f"missing collector scripts: {collector.get('scripts')}")
        latest = collector.get("hot_latest_day") or collector.get("search_latest_day") or collector.get("authors_latest_day")
        checks.append(ReadinessCheck(name="media_collector", status="ok", message=f"latest_day={latest or 'none'}"))
    except Exception as exc:
        checks.append(ReadinessCheck(name="media_collector", status="fail", error_code="MEDIA-COLLECTOR-001", message=str(exc)))

    # mp upstream health
    try:
        probe = probe_mp_upstream(db)
        if not probe.get("ok"):
            raise RuntimeError(str(probe.get("error") or f"http_status={probe.get('status_code')}"))
        checks.append(ReadinessCheck(
            name="mp_upstream",
            status="ok",
            message=f"status={probe.get('status_code')}",
            latency_ms=probe.get("latency_ms"),
        ))
    except Exception as exc:
        checks.append(ReadinessCheck(name="mp_upstream", status="fail", error_code="MP-UPSTREAM-001", message=str(exc)))

    # background state
    try:
        runtime = get_background_runtime_snapshot()
        enabled = {k: v for k, v in runtime.items() if bool(v.get("enabled"))}
        failing = [name for name, state in enabled.items() if state.get("last_error")]
        if failing:
            raise RuntimeError("background failures: " + ", ".join(sorted(failing)))
        checks.append(ReadinessCheck(name="background_loops", status="ok", message=f"enabled={len(enabled)}"))
    except Exception as exc:
        checks.append(ReadinessCheck(name="background_loops", status="fail", error_code="BG-STATE-001", message=str(exc)))

    # queue depth
    try:
        pending_summary = db.query(Task).filter(Task.type == "summary", Task.status == "pending").count()
        if pending_summary > 0:
            raise RuntimeError(f"pending_summary={pending_summary}")
        checks.append(ReadinessCheck(name="summary_queue", status="ok", message="pending_summary=0"))
    except Exception as exc:
        checks.append(ReadinessCheck(name="summary_queue", status="fail", error_code="SUM-QUEUE-001", message=str(exc)))

    return checks


def summarize_diagnostics(db: Session | None = None) -> dict[str, Any]:
    db_path = Path(str(settings.DATABASE_URL.replace("sqlite:///", ""))) if settings.DATABASE_URL.startswith("sqlite:///") else None
    try:
        ai_config = load_ai_config()
    except Exception:
        ai_config = {}
    diagnostics: dict[str, Any] = {
        "timestamp": datetime.utcnow().isoformat(),
        "database_url": settings.DATABASE_URL,
        "chatlog_http_base": settings.CHATLOG_HTTP_BASE,
        "chatlog_dir": settings.CHATLOG_DIR,
        "disk": _disk_info(Path.cwd()),
        "paths": _check_writable_paths(),
        "api_keys": _summarize_ai_config(ai_config),
        "external_services": {"chatlog_http": probe_chatlog_http()},
        "content_engines": {
            "media_collector": collector_installation_status(),
            "mp_upstream": probe_mp_upstream(db),
        },
        "cloud_agent_runtime": _cloud_agent_runtime_diagnostics(),
        "background_runtime": get_background_runtime_snapshot(),
        "aggregation_retention": _aggregation_retention_diagnostics(db),
    }
    if db_path and db_path.exists():
        diagnostics["database_file"] = {"path": str(db_path), "size_bytes": db_path.stat().st_size}
    if db is not None:
        try:
            diagnostics["pending_summary"] = db.query(Task).filter(Task.type == "summary", Task.status == "pending").count()
        except Exception:
            diagnostics["pending_summary"] = None
        try:
            diagnostics["sqlite_fts"] = bool(db.execute(text("SELECT name FROM sqlite_master WHERE type='table' AND name='messages_fts' LIMIT 1")).scalar())
        except Exception:
            diagnostics["sqlite_fts"] = False
    return diagnostics
