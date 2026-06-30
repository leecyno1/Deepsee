from __future__ import annotations

import asyncio
import logging
from datetime import datetime, timedelta, timezone
from fastapi import FastAPI
from .config import settings
from .db import SessionLocal
from .services.sync_service import sync_from_chatlog
from .services.email_engine import imap_fetch, FetchOptions
from .models import EmailAccount, ExtAdapter
from .services.ext_adapter_service import ingest_adapter_logs
from .services import news_client
from .services.wechat8061_sync import wechat8061_sync_loop
from .services.aggregation_retention import prune_aggregation_data
from .services.media_collector_runner import run_media_collector_once
from .services.cache_cleanup import cleanup_application_cache

logger = logging.getLogger(__name__)
BACKGROUND_RUNTIME: dict[str, dict] = {}
def _utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _bg_state(name: str) -> dict:
    state = BACKGROUND_RUNTIME.get(name)
    if state is None:
        state = {
            "name": name,
            "enabled": False,
            "running": False,
            "runs": 0,
            "failures": 0,
            "last_started_at": None,
            "last_finished_at": None,
            "last_success_at": None,
            "last_error_at": None,
            "last_error": None,
        }
        BACKGROUND_RUNTIME[name] = state
    return state


def _bg_mark_enabled(name: str, enabled: bool) -> None:
    state = _bg_state(name)
    state["enabled"] = bool(enabled)


def _bg_mark_start(name: str) -> None:
    state = _bg_state(name)
    state["running"] = True
    state["runs"] = int(state.get("runs") or 0) + 1
    state["last_started_at"] = _utc_now()


def _bg_mark_success(name: str) -> None:
    state = _bg_state(name)
    now = _utc_now()
    state["running"] = False
    state["last_finished_at"] = now
    state["last_success_at"] = now
    state["last_error"] = None


def _bg_mark_error(name: str, exc: Exception) -> None:
    state = _bg_state(name)
    now = _utc_now()
    state["running"] = False
    state["failures"] = int(state.get("failures") or 0) + 1
    state["last_finished_at"] = now
    state["last_error_at"] = now
    state["last_error"] = str(exc)
    logger.exception("background loop failed: %s", name)


_BACKGROUND_RUNTIME_NAMES = [
    "chatlog_sync",
    "wechat8061_sync",
    "email_sync",
    "ext_adapter_sync",
    "news_refresh",
    "news_snapshot",
    "media_collector",
    "summary_overlay",
    "aggregation_retention",
    "media_cache_cleanup",
]


def _runtime_age_seconds(value: str | None) -> int | None:
    if not value:
        return None
    try:
        dt = datetime.fromisoformat(str(value).replace("Z", "+00:00"))
        return max(0, int((datetime.now(timezone.utc) - dt).total_seconds()))
    except Exception:
        return None


def _runtime_health(state: dict) -> str:
    if bool(state.get("running")):
        return "running"
    if bool(state.get("last_error")):
        return "error"
    if not bool(state.get("enabled")):
        return "off"
    if not state.get("last_success_at") and not state.get("last_finished_at"):
        return "waiting"
    return "ok"


def get_background_runtime_snapshot() -> dict[str, dict]:
    for name in _BACKGROUND_RUNTIME_NAMES:
        _bg_state(name)
    snapshot: dict[str, dict] = {}
    for key, value in BACKGROUND_RUNTIME.items():
        state = dict(value)
        last_at = state.get("last_started_at") if state.get("running") else (
            state.get("last_success_at") or state.get("last_error_at") or state.get("last_finished_at")
        )
        state["health"] = _runtime_health(state)
        state["last_activity_at"] = last_at
        state["last_activity_age_seconds"] = _runtime_age_seconds(last_at)
        snapshot[key] = state
    return snapshot


def _load_ai_runtime_config(db) -> dict:
    try:
        from .models import SyncState
        import json as _json

        state = db.get(SyncState, "ai_runtime")
        if state and state.value:
            data = _json.loads(state.value) or {}
            return data if isinstance(data, dict) else {}
    except Exception:
        pass
    return {}


def _message_visible_len(message) -> int:
    text = (getattr(message, "content_text", None) or "").strip()
    if not text:
        try:
            meta = getattr(message, "meta", None) or {}
            contents = meta.get("contents") if isinstance(meta, dict) else None
            parts: list[str] = []
            if isinstance(contents, dict):
                for key in ("content", "desc", "title", "url"):
                    value = contents.get(key)
                    if isinstance(value, str) and value.strip():
                        parts.append(value.strip())
            text = " ".join(parts).strip()
        except Exception:
            text = ""
    return len(text.replace("\n", " ").replace("\r", " ").replace("\t", " ").strip())


def run_summary_overlay_once(db, *, cfg: dict | None = None) -> dict[str, int]:
    from sqlalchemy import desc, select
    from .models import EmailMessage, Message
    from .services.ai_tools import ensure_message_features, populate_fallback_derived
    from .services.email_features import persist_email_features, persist_email_fallback

    cfg = cfg or _load_ai_runtime_config(db)
    result = {
        "wechat_fallback": 0,
        "wechat_tool": 0,
        "email_fallback": 0,
        "email_tool": 0,
    }

    if bool(cfg.get("enable_msg_tool_overlay", True)):
        cutoff = datetime.utcnow() - timedelta(days=3)
        recent_messages = db.execute(
            select(Message)
            .where(Message.timestamp >= cutoff)
            .order_by(desc(Message.timestamp), desc(Message.id))
            .limit(2000)
        ).scalars().all()
        result["wechat_fallback"] = int(populate_fallback_derived(db, recent_messages, force=False) or 0)
        pending_limit = max(1, int(cfg.get("msg_tool_overlay_limit", 60) or 60))
        pending_messages = []
        for msg in recent_messages:
            if _message_visible_len(msg) < 20:
                continue
            derived = msg.derived if isinstance(msg.derived, dict) else {}
            origin = str(derived.get("summary_origin") or "").lower()
            summary = str(derived.get("summary") or "").strip().lower()
            if origin == "tool" and summary.startswith("ai:"):
                continue
            pending_messages.append(msg)
            if len(pending_messages) >= pending_limit:
                break
        if pending_messages:
            info = ensure_message_features(
                db,
                pending_messages,
                force=False,
                concurrency=1,
                batch_size=1,
                temperature=0.1,
            )
            result["wechat_tool"] = int((info or {}).get("updated") or 0)

    if bool(cfg.get("enable_email_tool_overlay", True)):
        email_window = max(20, min(1000, int(cfg.get("email_overlay_window", 120) or 120)))
        email_cap = max(20, min(2000, int(cfg.get("email_overlay_cap", 160) or 160)))
        recent_emails = db.execute(
            select(EmailMessage)
            .order_by(desc(EmailMessage.sent_at), desc(EmailMessage.id))
            .limit(email_window)
        ).scalars().all()
        result["email_fallback"] = len(persist_email_fallback(db, recent_emails, force=False, commit=False))
        pending_emails = []
        for email in recent_emails:
            derived = email.derived if isinstance(email.derived, dict) else {}
            if str(derived.get("summary_origin") or "").lower() == "tool":
                continue
            pending_emails.append(email)
            if len(pending_emails) >= email_cap:
                break
        if pending_emails:
            result["email_tool"] = len(persist_email_features(db, pending_emails, force=False, commit=False))
    return result


def _run_chatlog_sync_job() -> None:
    db = SessionLocal()
    try:
        sync_from_chatlog(db)
        try:
            run_summary_overlay_once(db)
        except Exception as exc:
            logger.warning("background subtask failed: summary_overlay_once: %s", exc)
        db.commit()
    finally:
        db.close()


def _run_summary_overlay_job() -> dict[str, int]:
    db = SessionLocal()
    try:
        stats = run_summary_overlay_once(db)
        db.commit()
        return stats
    finally:
        db.close()


def _run_aggregation_retention_job() -> dict:
    db = SessionLocal()
    try:
        retention_days = int(settings.__dict__.get("AGGREGATION_RETENTION_DAYS", 90) or 90)
        result = prune_aggregation_data(db, retention_days=retention_days)
        db.commit()
        return result
    finally:
        db.close()


def _run_media_cache_cleanup_job() -> dict:
    db = SessionLocal()
    try:
        result = cleanup_application_cache(
            db,
            ttl_hours=int(settings.__dict__.get("MEDIA_CACHE_TTL_HOURS", 720) or 720),
            max_mb=int(settings.__dict__.get("MEDIA_CACHE_MAX_MB", 256) or 256),
            dry_run=False,
        )
        db.commit()
        return result
    finally:
        db.close()


async def _sync_loop():
    loop_name = "chatlog_sync"
    interval = int(settings.__dict__.get("SYNC_INTERVAL_SECONDS", 0) or 0)
    if interval <= 0:
        _bg_mark_enabled(loop_name, False)
        return
    _bg_mark_enabled(loop_name, True)
    while True:
        try:
            _bg_mark_start(loop_name)
            await asyncio.to_thread(_run_chatlog_sync_job)
            _bg_mark_success(loop_name)
        except Exception as exc:
            _bg_mark_error(loop_name, exc)
        await asyncio.sleep(interval)


async def _email_loop():
    loop_name = "email_sync"
    interval = int(settings.__dict__.get("EMAIL_SYNC_INTERVAL_SECONDS", 0) or 0)
    if interval <= 0:
        _bg_mark_enabled(loop_name, False)
        return
    _bg_mark_enabled(loop_name, True)
    while True:
        try:
            _bg_mark_start(loop_name)
            db = SessionLocal()
            try:
                accounts = db.query(EmailAccount).filter(EmailAccount.enabled == True).all()  # noqa
                for acc in accounts:
                    try:
                        imap_fetch(db, acc, FetchOptions(limit=50, unseen_only=True))
                    except Exception as exc:
                        logger.warning(
                            "background subtask failed: imap_fetch account_id=%s email=%s: %s",
                            getattr(acc, "id", None),
                            getattr(acc, "email_address", None),
                            exc,
                        )
                db.commit()
            finally:
                db.close()
            _bg_mark_success(loop_name)
        except Exception as exc:
            _bg_mark_error(loop_name, exc)
        await asyncio.sleep(interval)


async def _ext_adapter_loop():
    loop_name = "ext_adapter_sync"
    # poll every 30 seconds by default to ingest adapter logs if configured
    interval = 30
    base_dir = settings.__dict__.get("LANGBOT_ADAPTER_LOG_DIR") or "./data/adapters"
    if not base_dir:
        _bg_mark_enabled(loop_name, False)
        return
    _bg_mark_enabled(loop_name, True)
    while True:
        try:
            _bg_mark_start(loop_name)
            db = SessionLocal()
            try:
                adapters = db.query(ExtAdapter).filter(ExtAdapter.enabled == True).all()  # noqa
                for a in adapters:
                    try:
                        ingest_adapter_logs(db, a, a.config.get("log_dir") or base_dir, since=None)
                    except Exception as exc:
                        logger.warning(
                            "background subtask failed: ingest_adapter_logs adapter_id=%s name=%s: %s",
                            getattr(a, "id", None),
                            getattr(a, "name", None),
                            exc,
                        )
                db.commit()
            finally:
                db.close()
            _bg_mark_success(loop_name)
        except Exception as exc:
            _bg_mark_error(loop_name, exc)
        await asyncio.sleep(interval)


async def _news_loop():
    loop_name = "news_refresh"
    interval = int(settings.__dict__.get("NEWSNOW_REFRESH_INTERVAL_SECONDS", 0) or 0)
    if interval <= 0:
        _bg_mark_enabled(loop_name, False)
        return
    _bg_mark_enabled(loop_name, True)
    while True:
        try:
            _bg_mark_start(loop_name)
            # Trigger upstream refresh and warm local caches
            try:
                await asyncio.to_thread(news_client.newsnow_refresh)
            except Exception as exc:
                logger.warning("background subtask failed: newsnow_refresh: %s", exc)
            try:
                await asyncio.to_thread(news_client.newsnow_sources, force=True)
            except Exception as exc:
                logger.warning("background subtask failed: newsnow_sources: %s", exc)
            try:
                # warm a small slice
                await asyncio.to_thread(news_client.newsnow_news, limit=20, simple=True)
            except Exception as exc:
                logger.warning("background subtask failed: newsnow_news warmup: %s", exc)
            _bg_mark_success(loop_name)
        except Exception as exc:
            _bg_mark_error(loop_name, exc)
        await asyncio.sleep(max(30, interval))


async def _news_snapshot_loop():
    loop_name = "news_snapshot"
    """Periodic writer for news sentiment dataset snapshots.

    Writes compact JSON snapshots under data/datasets/ every
    settings.NEWS_SNAPSHOT_INTERVAL_SECONDS seconds using direct collectors.
    """
    interval = int(settings.__dict__.get("NEWS_SNAPSHOT_INTERVAL_SECONDS", 0) or 0)
    if interval <= 0:
        _bg_mark_enabled(loop_name, False)
        return
    _bg_mark_enabled(loop_name, True)
    # short initial delay to allow app startup
    await asyncio.sleep(3)
    while True:
        try:
            _bg_mark_start(loop_name)
            # Best-effort: collect and persist a fresh snapshot
            try:
                await asyncio.to_thread(news_client.write_news_snapshot, limit=200)
            except Exception as exc:
                logger.warning("background subtask failed: write_news_snapshot: %s", exc)
            # Optionally warm aggregation cache for UI consumption
            try:
                await asyncio.to_thread(news_client.direct_from_sources_json, limit=50)
            except Exception as exc:
                logger.warning("background subtask failed: direct_from_sources_json: %s", exc)
            _bg_mark_success(loop_name)
        except Exception as exc:
            _bg_mark_error(loop_name, exc)
        await asyncio.sleep(max(60, interval))


def _seconds_until_daily(hour: int, minute: int) -> int:
    now = datetime.now()
    target = now.replace(hour=max(0, min(23, hour)), minute=max(0, min(59, minute)), second=0, microsecond=0)
    if target <= now:
        target = target + timedelta(days=1)
    return max(1, int((target - now).total_seconds()))


async def _media_collector_loop():
    loop_name = "media_collector"
    enabled = bool(settings.__dict__.get("MEDIA_COLLECTOR_DAILY_ENABLED", True))
    if not enabled:
        _bg_mark_enabled(loop_name, False)
        return
    _bg_mark_enabled(loop_name, True)
    while True:
        hour = int(settings.__dict__.get("MEDIA_COLLECTOR_DAILY_HOUR", 5) or 5)
        minute = int(settings.__dict__.get("MEDIA_COLLECTOR_DAILY_MINUTE", 0) or 0)
        await asyncio.sleep(_seconds_until_daily(hour, minute))
        try:
            _bg_mark_start(loop_name)
            result = await asyncio.to_thread(run_media_collector_once, hot=True, search=True, authors=True)
            if not bool(result.get("ok")) and not bool(result.get("running")):
                raise RuntimeError("media collector failed")
            logger.info("media collector refreshed: %s", {
                "ok": result.get("ok"),
                "tasks": result.get("tasks"),
            })
            _bg_mark_success(loop_name)
        except Exception as exc:
            _bg_mark_error(loop_name, exc)


async def _aggregation_retention_loop():
    loop_name = "aggregation_retention"
    interval = int(settings.__dict__.get("AGGREGATION_RETENTION_INTERVAL_SECONDS", 86400) or 0)
    if interval <= 0:
        _bg_mark_enabled(loop_name, False)
        return
    _bg_mark_enabled(loop_name, True)
    await asyncio.sleep(5)
    while True:
        try:
            _bg_mark_start(loop_name)
            result = await asyncio.to_thread(_run_aggregation_retention_job)
            logger.info("aggregation retention pruned: %s", result)
            _bg_mark_success(loop_name)
        except Exception as exc:
            _bg_mark_error(loop_name, exc)
        await asyncio.sleep(max(3600, interval))


async def _media_cache_cleanup_loop():
    loop_name = "media_cache_cleanup"
    enabled = bool(settings.__dict__.get("MEDIA_CACHE_CLEANUP_ENABLED", True))
    interval = int(settings.__dict__.get("MEDIA_CACHE_CLEANUP_INTERVAL_SECONDS", 86400) or 0)
    if not enabled or interval <= 0:
        _bg_mark_enabled(loop_name, False)
        return
    _bg_mark_enabled(loop_name, True)
    await asyncio.sleep(10)
    while True:
        try:
            _bg_mark_start(loop_name)
            result = await asyncio.to_thread(_run_media_cache_cleanup_job)
            logger.info("media cache cleanup finished: %s", result)
            _bg_mark_success(loop_name)
        except Exception as exc:
            _bg_mark_error(loop_name, exc)
        await asyncio.sleep(max(3600, interval))


async def _summary_overlay_loop():
    loop_name = "summary_overlay"
    interval = int(settings.__dict__.get("SUMMARY_OVERLAY_INTERVAL_SECONDS", 3600) or 0)
    if interval <= 0:
        _bg_mark_enabled(loop_name, False)
        return
    _bg_mark_enabled(loop_name, True)
    await asyncio.sleep(5)
    while True:
        try:
            _bg_mark_start(loop_name)
            stats = await asyncio.to_thread(_run_summary_overlay_job)
            logger.info("summary overlay updated: %s", stats)
            _bg_mark_success(loop_name)
        except Exception as exc:
            _bg_mark_error(loop_name, exc)
        await asyncio.sleep(max(300, interval))


async def start_background_loops(app: FastAPI | None = None) -> None:
    interval = int(settings.__dict__.get("SYNC_INTERVAL_SECONDS", 0) or 0)
    _bg_mark_enabled("chatlog_sync", bool(interval and interval > 0))
    if interval and interval > 0:
        asyncio.create_task(_sync_loop())
    # Deprecated optional 8061 fallback; keep runtime entry but do not start unless explicitly enabled.
    try:
        from .services.llm_client import load_ai_config

        wechat8061_enabled = bool((load_ai_config() or {}).get("wechatpad_sync_enabled", False))
    except Exception:
        wechat8061_enabled = False
    _bg_mark_enabled("wechat8061_sync", wechat8061_enabled)
    if wechat8061_enabled:
        asyncio.create_task(wechat8061_sync_loop())
    # 邮件同步改为“仅手动触发”，不再定时自动拉取
    # 如需恢复定时，请显式改回并确保 EMAIL_SYNC_INTERVAL_SECONDS > 0
    # email_interval = int(settings.__dict__.get("EMAIL_SYNC_INTERVAL_SECONDS", 0) or 0)
    # if email_interval and email_interval > 0:
    #     asyncio.create_task(_email_loop())
    _bg_mark_enabled("email_sync", False)
    if settings.__dict__.get("LANGBOT_ADAPTER_LOG_DIR"):
        asyncio.create_task(_ext_adapter_loop())
    else:
        _bg_mark_enabled("ext_adapter_sync", False)
    news_interval = int(settings.__dict__.get("NEWSNOW_REFRESH_INTERVAL_SECONDS", 0) or 0)
    _bg_mark_enabled("news_refresh", bool(news_interval and news_interval > 0))
    if news_interval and news_interval > 0:
        asyncio.create_task(_news_loop())
    # Start snapshot loop for news sentiment datasets (every ~3h by default)
    snap_interval = int(settings.__dict__.get("NEWS_SNAPSHOT_INTERVAL_SECONDS", 0) or 0)
    _bg_mark_enabled("news_snapshot", bool(snap_interval and snap_interval > 0))
    if snap_interval and snap_interval > 0:
        asyncio.create_task(_news_snapshot_loop())
    media_collector_enabled = bool(settings.__dict__.get("MEDIA_COLLECTOR_DAILY_ENABLED", True))
    _bg_mark_enabled("media_collector", media_collector_enabled)
    if media_collector_enabled:
        asyncio.create_task(_media_collector_loop())
    summary_interval = int(settings.__dict__.get("SUMMARY_OVERLAY_INTERVAL_SECONDS", 3600) or 0)
    _bg_mark_enabled("summary_overlay", bool(summary_interval and summary_interval > 0))
    if summary_interval and summary_interval > 0:
        asyncio.create_task(_summary_overlay_loop())
    retention_interval = int(settings.__dict__.get("AGGREGATION_RETENTION_INTERVAL_SECONDS", 86400) or 0)
    _bg_mark_enabled("aggregation_retention", bool(retention_interval and retention_interval > 0))
    if retention_interval and retention_interval > 0:
        asyncio.create_task(_aggregation_retention_loop())
    cache_cleanup_enabled = bool(settings.__dict__.get("MEDIA_CACHE_CLEANUP_ENABLED", True))
    cache_cleanup_interval = int(settings.__dict__.get("MEDIA_CACHE_CLEANUP_INTERVAL_SECONDS", 86400) or 0)
    _bg_mark_enabled("media_cache_cleanup", bool(cache_cleanup_enabled and cache_cleanup_interval > 0))
    if cache_cleanup_enabled and cache_cleanup_interval > 0:
        asyncio.create_task(_media_cache_cleanup_loop())
