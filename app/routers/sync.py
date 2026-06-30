from __future__ import annotations

from fastapi import APIRouter, Depends
from sqlalchemy.orm import Session
from datetime import datetime, timedelta
import json
import uuid
from time import perf_counter
from ..db import SessionLocal
from ..services.sync_service import sync_from_chatlog, sync_full, sync_from_wx_cli, compare_with_chatlog
from ..services.snapshot_service import refresh_default_snapshots
from ..services import sync_runtime
from ..models import SyncState
from ..services.deployment_status import probe_chatlog_http, probe_wx_cli
from ..services.wechat_gateway import load_config as load_wechat_gateway_config
from ..services.wechatapi_client import WechatApiClient


router = APIRouter(prefix="/api/sync", tags=["sync"])
VALID_WECHAT_TRACKS = ("wechatapi", "chatlog", "wx_cli")


def get_db():
    db = SessionLocal()
    try:
        yield db
    finally:
        db.close()


def _load_chatlog_sync_policy(db: Session) -> dict[str, float | int]:
    return sync_runtime.get_chatlog_sync_policy(db, model_cls=SyncState)


def _wechatapi_track_state(db: Session) -> dict:
    cfg = load_wechat_gateway_config(db)
    configured = bool(
        str(cfg.get("base_url") or "").strip()
        and str(cfg.get("token") or "").strip()
        and str(cfg.get("app_id") or "").strip()
    )
    state = {
        "name": "wechatapi",
        "role": "实时轨道",
        "configured": configured,
        "enabled": bool(cfg.get("enabled")),
        "outbound_enabled": bool(cfg.get("outbound_enabled")),
        "callback_public_url": str(cfg.get("callback_public_url") or "").strip(),
        "healthy": False,
        "status": "not_configured",
        "message": "未配置 wechatapi 网关",
    }
    if not configured:
        return state
    try:
        result = WechatApiClient(
            base_url=str(cfg.get("base_url") or ""),
            token=str(cfg.get("token") or ""),
            header_name=str(cfg.get("header_name") or "VideosApi-token"),
            app_id=str(cfg.get("app_id") or ""),
        ).check_online()
        state.update(
            {
                "healthy": True,
                "status": "ok",
                "message": "wechatapi 在线，实时回调可作为主轨道",
                "result": result,
            }
        )
    except Exception as exc:
        state.update({"status": "error", "message": str(exc)})
    return state


def _chatlog_track_state() -> dict:
    state = {
        "name": "chatlog",
        "role": "本地兜底轨道",
        "configured": True,
        "healthy": False,
        "status": "unknown",
        "message": "",
    }
    try:
        probe = probe_chatlog_http()
        ok = bool(probe.get("ok"))
        state.update(
            {
                "healthy": ok,
                "status": "ok" if ok else "error",
                "message": "chatlog 本地服务可用" if ok else str(probe.get("error") or "chatlog unavailable"),
                "result": probe,
            }
        )
    except Exception as exc:
        state.update({"status": "error", "message": str(exc)})
    return state


def _wx_cli_track_state() -> dict:
    state = {
        "name": "wx_cli",
        "role": "本地 CLI 增强兜底",
        "configured": True,
        "healthy": False,
        "status": "unknown",
        "message": "",
    }
    try:
        probe = probe_wx_cli(timeout=3)
        ok = bool(probe.get("ok"))
        state.update(
            {
                "healthy": ok,
                "status": "ok" if ok else "error",
                "message": "wx-cli 本地服务可用" if ok else str(probe.get("error") or "wx-cli unavailable"),
                "result": probe,
            }
        )
    except Exception as exc:
        state.update({"status": "error", "message": str(exc)})
    return state


def _normalize_track_list(value, *, default: list[str]) -> list[str]:
    if isinstance(value, str):
        raw_items = [item.strip() for item in value.split(",")]
    elif isinstance(value, list):
        raw_items = [str(item or "").strip() for item in value]
    else:
        raw_items = []
    out: list[str] = []
    for item in raw_items:
        if item in VALID_WECHAT_TRACKS and item not in out:
            out.append(item)
    for item in default:
        if item in VALID_WECHAT_TRACKS and item not in out:
            out.append(item)
    return out


def _legacy_dual_track_selection(mode: str) -> tuple[list[str], list[str]]:
    if mode == "wechatapi_only":
        return ["wechatapi"], ["wechatapi", "chatlog", "wx_cli"]
    if mode == "chatlog_only":
        return ["chatlog", "wx_cli"], ["chatlog", "wx_cli", "wechatapi"]
    return ["wechatapi", "chatlog", "wx_cli"], ["wechatapi", "chatlog", "wx_cli"]


def _dual_track_policy(db: Session) -> dict:
    row = db.get(SyncState, "wechat_dual_track_policy")
    raw_policy: dict = {}
    policy = {
        "mode": "custom",
        "enabled_tracks": ["wechatapi", "chatlog", "wx_cli"],
        "track_order": ["wechatapi", "chatlog", "wx_cli"],
        "use_multiple_tracks": False,
        "chatlog_window_days": 1,
    }
    if row and row.value:
        try:
            raw = json.loads(row.value)
            if isinstance(raw, dict):
                raw_policy = raw
                policy.update(raw)
        except Exception:
            pass
    try:
        policy["chatlog_window_days"] = max(1, min(90, int(policy.get("chatlog_window_days") or 1)))
    except Exception:
        policy["chatlog_window_days"] = 1
    mode = str(policy.get("mode") or "").strip()
    legacy_enabled, legacy_order = _legacy_dual_track_selection(mode)
    has_new_selection = "enabled_tracks" in raw_policy or "track_order" in raw_policy
    if not has_new_selection:
        policy["enabled_tracks"] = legacy_enabled
        policy["track_order"] = legacy_order
    policy["track_order"] = _normalize_track_list(policy.get("track_order"), default=list(VALID_WECHAT_TRACKS))
    enabled = _normalize_track_list(policy.get("enabled_tracks"), default=[])
    if not enabled:
        enabled = ["wechatapi"]
    policy["enabled_tracks"] = [track for track in policy["track_order"] if track in enabled]
    for track in enabled:
        if track not in policy["enabled_tracks"]:
            policy["enabled_tracks"].append(track)
    policy["use_multiple_tracks"] = bool(policy.get("use_multiple_tracks"))
    policy["mode"] = "custom"
    return policy


def _save_dual_track_policy(db: Session, payload: dict | None) -> dict:
    policy = _dual_track_policy(db)
    if isinstance(payload, dict):
        policy.update(payload)
    policy = {
        "mode": "custom",
        "enabled_tracks": _normalize_track_list(policy.get("enabled_tracks"), default=[]),
        "track_order": _normalize_track_list(policy.get("track_order"), default=list(VALID_WECHAT_TRACKS)),
        "use_multiple_tracks": bool(policy.get("use_multiple_tracks")),
        "chatlog_window_days": max(1, min(90, int(policy.get("chatlog_window_days") or 1))),
    }
    policy["enabled_tracks"] = [track for track in policy["track_order"] if track in set(policy["enabled_tracks"])]
    if not policy["enabled_tracks"]:
        policy["enabled_tracks"] = [policy["track_order"][0] if policy["track_order"] else "wechatapi"]
    row = db.get(SyncState, "wechat_dual_track_policy")
    payload_text = json.dumps(policy, ensure_ascii=False)
    if not row:
        row = SyncState(key="wechat_dual_track_policy", value=payload_text)
    else:
        row.value = payload_text
    db.add(row)
    return policy


@router.post("/chatlog")
def sync_chatlog(since: str | None = None, db: Session = Depends(get_db)):
    # Accept ISO strings with/without timezone and trailing Z; normalize to naive local time
    parsed_since = None
    started_at = datetime.utcnow()
    run_id = f"chatlog-{uuid.uuid4().hex[:12]}"
    if since:
        try:
            s = since.replace("Z", "+00:00")
            dt = datetime.fromisoformat(s)
            if dt.tzinfo is not None:
                dt = dt.astimezone().replace(tzinfo=None)
            parsed_since = dt
        except Exception:
            parsed_since = None
    try:
        t0 = perf_counter()
        policy = _load_chatlog_sync_policy(db)
        res, attempts, sync_err = sync_runtime.run_with_retry(
            lambda: sync_from_chatlog(db, parsed_since),
            max_attempts=int(policy["max_attempts"]),
            sleep_seconds=float(policy["sleep_seconds"]),
            on_error=lambda _exc: db.rollback(),
        )
        if sync_err is not None:
            error_code, _ = sync_runtime.classify_sync_error(sync_err)
            payload = {
                "run_id": run_id,
                "status": "error",
                "started_at": started_at.isoformat(),
                "ended_at": datetime.utcnow().isoformat(),
                "duration_ms": int((perf_counter() - t0) * 1000),
                "attempts": int(attempts),
                "error_code": error_code,
                "error": str(sync_err),
                "fetched": 0,
                "inserted": 0,
                "since": parsed_since.isoformat() if parsed_since else None,
            }
            sync_runtime.persist_sync_run(db, SyncState, payload)
            db.commit()
            return {
                "status": "error",
                "run_id": run_id,
                "attempts": int(attempts),
                "error_code": error_code,
                "error": str(sync_err),
                "fetched": 0,
                "inserted": 0,
                "since": parsed_since.isoformat() if parsed_since else None,
                "until": datetime.now().isoformat(),
            }
        res = dict(res or {})
        res["run_id"] = run_id
        res["attempts"] = int(attempts)
        # After sync, immediately write fallback summaries so UI shows grey entries
        from sqlalchemy import select
        from ..models import Message
        from ..services.ai_tools import populate_fallback_derived, ensure_message_features
        # build a conservative window since parsed_since (or 3 days if None)
        cutoff = parsed_since or (datetime.utcnow() - timedelta(days=3))
        recent = db.execute(select(Message).where(Message.timestamp >= cutoff).order_by(Message.id.desc()).limit(5000)).scalars().all()
        try:
            populate_fallback_derived(db, recent, force=False)
        except Exception:
            pass
        # Fire-and-forget AI overlay on same window (does not block response)
        try:
            import threading
            from ..db import SessionLocal as _SessionLocal
            ids = [m.id for m in recent]
            def _overlay(ids: list[int]):
                sess = _SessionLocal()
                try:
                    # read ai-runtime switch; allow turning off overlay from settings
                    from ..models import SyncState
                    import json as _json
                    sw = sess.get(SyncState, 'ai_runtime')
                    cfg = {}
                    try:
                        if sw and sw.value:
                            cfg = _json.loads(sw.value) or {}
                    except Exception:
                        cfg = {}
                    if bool((cfg or {}).get('enable_msg_tool_overlay', True)):
                        rows = sess.execute(select(Message).where(Message.id.in_(ids))).scalars().all()
                        cc = int((cfg or {}).get('default_concurrency', 3) or 3)
                        # Keep a conservative concurrency to avoid 429 Too Many Requests
                        ensure_message_features(sess, rows, force=False, concurrency=max(1, min(16, cc)))
                except Exception:
                    pass
                finally:
                    sess.close()
            threading.Thread(target=_overlay, args=(ids,), daemon=True).start()
        except Exception:
            pass
        try:
            refresh_default_snapshots(db)
        except Exception as e:
            res["snapshot_error"] = str(e)
        payload = {
            "run_id": run_id,
            "status": "ok",
            "started_at": started_at.isoformat(),
            "ended_at": datetime.utcnow().isoformat(),
            "duration_ms": int((perf_counter() - t0) * 1000),
            "attempts": int(attempts),
            "error_code": None,
            "error": None,
            "fetched": int(res.get("fetched") or 0),
            "inserted": int(res.get("inserted") or 0),
            "since": res.get("since"),
            "until": res.get("until"),
        }
        sync_runtime.persist_sync_run(db, SyncState, payload)
        db.commit()
        return res
    except Exception as e:
        db.rollback()
        error_code, _ = sync_runtime.classify_sync_error(e)
        return {
            "status": "error",
            "run_id": run_id,
            "attempts": 1,
            "error_code": error_code,
            "error": str(e),
            "fetched": 0,
            "inserted": 0,
            "since": parsed_since.isoformat() if parsed_since else None,
            "until": datetime.now().isoformat(),
        }


@router.get("/state")
def sync_state(db: Session = Depends(get_db)):
    return sync_runtime.build_sync_state_payload(db, _model_cls=SyncState)


@router.get("/policy")
def get_sync_policy(db: Session = Depends(get_db)):
    return sync_runtime.get_chatlog_sync_policy(db, model_cls=SyncState)


@router.post("/policy")
def set_sync_policy(body: dict, db: Session = Depends(get_db)):
    policy = sync_runtime.save_chatlog_sync_policy(db, model_cls=SyncState, payload=body or {})
    db.commit()
    return {"status": "ok", "policy": policy}


@router.post("/chatlog/full")
def sync_chatlog_full(days: int = 30, db: Session = Depends(get_db)):
    try:
        res = sync_full(db, days=days)
        refresh_default_snapshots(db)
        db.commit()
        return res
    except Exception:
        db.rollback()
        raise


@router.post("/wx-cli/full")
def sync_wx_cli_full(days: int = 1, db: Session = Depends(get_db)):
    try:
        res = sync_from_wx_cli(db, days=days)
        refresh_default_snapshots(db)
        db.commit()
        return res
    except Exception:
        db.rollback()
        raise


@router.get("/wechat/dual-track/state")
def wechat_dual_track_state(db: Session = Depends(get_db)):
    policy = _dual_track_policy(db)
    wechatapi = _wechatapi_track_state(db)
    chatlog = _chatlog_track_state()
    wx_cli = _wx_cli_track_state()
    track_states = {"wechatapi": wechatapi, "chatlog": chatlog, "wx_cli": wx_cli}
    enabled_order = [track for track in policy["track_order"] if track in set(policy["enabled_tracks"])]
    active_track = enabled_order[0] if enabled_order else "none"
    healthy_active_track = next((track for track in enabled_order if track_states.get(track, {}).get("healthy")), None)
    return {
        "status": "ok",
        "policy": policy,
        "active_track": active_track,
        "healthy_active_track": healthy_active_track,
        "enabled_order": enabled_order,
        "available_tracks": list(VALID_WECHAT_TRACKS),
        "tracks": {"wechatapi": wechatapi, "chatlog": chatlog, "wx_cli": wx_cli},
    }


@router.post("/wechat/dual-track/policy")
def save_wechat_dual_track_policy(body: dict, db: Session = Depends(get_db)):
    policy = _save_dual_track_policy(db, body or {})
    db.commit()
    return {"status": "ok", "policy": policy}


@router.post("/wechat/dual-track")
def sync_wechat_dual_track(days: int | None = None, db: Session = Depends(get_db)):
    policy = _dual_track_policy(db)
    requested_days = max(1, min(90, int(days or policy.get("chatlog_window_days") or 1)))
    started_at = datetime.utcnow()
    run_id = f"wechat-dual-{uuid.uuid4().hex[:12]}"
    t0 = perf_counter()
    wechatapi = _wechatapi_track_state(db)
    chatlog = _chatlog_track_state()
    wx_cli = _wx_cli_track_state()
    track_states = {"wechatapi": wechatapi, "chatlog": chatlog, "wx_cli": wx_cli}
    enabled_order = [track for track in policy["track_order"] if track in set(policy["enabled_tracks"])]
    execution_order = enabled_order if bool(policy.get("use_multiple_tracks")) else enabled_order[:1]
    actions: list[dict] = []
    chatlog_result = None
    wx_cli_result = None

    for track in execution_order:
        state = track_states.get(track) or {}
        if track == "wechatapi":
            actions.append(
                {
                    "track": "wechatapi",
                    "status": "ok" if state.get("healthy") else "error",
                    "reason": state.get("message") or ("wechatapi 在线" if state.get("healthy") else "wechatapi 不可用"),
                }
            )
            continue
        if not state.get("healthy"):
            actions.append({"track": track, "status": "error", "reason": state.get("message") or f"{track} unavailable"})
            continue
        try:
            if track == "chatlog":
                chatlog_result = sync_full(db, days=requested_days)
                result = chatlog_result
                reason = "已按优先级使用 chatlog 补齐窗口数据"
            elif track == "wx_cli":
                wx_cli_result = sync_from_wx_cli(db, days=requested_days)
                result = wx_cli_result
                reason = "已按优先级使用 wx-cli 补齐窗口数据"
            else:
                continue
            refresh_default_snapshots(db)
            db.commit()
            actions.append(
                {
                    "track": track,
                    "status": "ok",
                    "reason": reason,
                    "fetched": int((result or {}).get("fetched") or 0),
                    "inserted": int((result or {}).get("inserted") or 0),
                }
            )
        except Exception as exc:
            db.rollback()
            actions.append({"track": track, "status": "error", "reason": str(exc)})

    ok = any(item.get("status") == "ok" for item in actions)
    payload = {
        "run_id": run_id,
        "status": "ok" if ok else "error",
        "started_at": started_at.isoformat(),
        "ended_at": datetime.utcnow().isoformat(),
        "duration_ms": int((perf_counter() - t0) * 1000),
        "policy": policy,
        "days": requested_days,
        "enabled_order": enabled_order,
        "execution_order": execution_order,
        "tracks": {"wechatapi": wechatapi, "chatlog": chatlog, "wx_cli": wx_cli},
        "actions": actions,
        "chatlog": chatlog_result,
        "wx_cli": wx_cli_result,
    }
    try:
        sync_runtime.persist_sync_run(
            db,
            SyncState,
            {
                "run_id": run_id,
                "status": payload["status"],
                "started_at": payload["started_at"],
                "ended_at": payload["ended_at"],
                "duration_ms": payload["duration_ms"],
                "attempts": 1,
                "error_code": None if ok else "SYNC-WECHAT-DUAL-TRACK-001",
                "error": None if ok else "; ".join(str(item.get("reason") or "") for item in actions),
                "fetched": int((chatlog_result or wx_cli_result or {}).get("fetched") or 0),
                "inserted": int((chatlog_result or wx_cli_result or {}).get("inserted") or 0),
                "since": (chatlog_result or wx_cli_result or {}).get("since") or (chatlog_result or wx_cli_result or {}).get("from"),
                "until": (chatlog_result or wx_cli_result or {}).get("until") or (chatlog_result or wx_cli_result or {}).get("to"),
            },
        )
        db.commit()
    except Exception:
        db.rollback()
    return payload


@router.get("/compare")
def sync_compare(days: int | None = 1, date: str | None = None, fix: bool | None = False, db: Session = Depends(get_db)):
    """Compare DB with chatlog for a date range or a specific day.

    - days: compare [now-days+1 .. now]; ignored if `date` is provided
    - date: YYYY-MM-DD for single day
    - fix: when true, insert missing chatlog messages into DB
    """
    # Run compare; when fix=True internal engine-level transaction is used, so no session commit here
    res = compare_with_chatlog(db, days=days, date=date, fix=bool(fix))
    return res
