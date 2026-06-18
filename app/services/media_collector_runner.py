from __future__ import annotations

import json
import os
import subprocess
import threading
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from ..config import settings
from .media_collector_store import get_collector_status


_RUN_LOCK = threading.Lock()
_LAST_RUN: dict[str, Any] | None = None


def _project_root() -> Path:
    return Path(__file__).resolve().parent.parent.parent


def _collector_dir() -> Path:
    return _project_root() / "media-collector"


def _runs_dir() -> Path:
    path = _project_root() / "data" / "collector_runs"
    path.mkdir(parents=True, exist_ok=True)
    return path


def _last_run_path() -> Path:
    return _runs_dir() / "last.json"


def _utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _truncate(text: str, limit: int = 4000) -> str:
    value = str(text or "")
    if len(value) <= limit:
        return value
    return value[-limit:]


def _script_timeout(timeout_seconds: int | None = None) -> int:
    raw = timeout_seconds if timeout_seconds is not None else settings.__dict__.get("MEDIA_COLLECTOR_TIMEOUT_SECONDS", 240)
    try:
        return max(30, min(1800, int(raw or 240)))
    except Exception:
        return 240


def _run_script(name: str, script: str, *, timeout_seconds: int, pretty: bool = False) -> dict[str, Any]:
    collector_dir = _collector_dir()
    script_path = collector_dir / script
    if not script_path.exists():
        return {
            "name": name,
            "ok": False,
            "returncode": 127,
            "error": f"脚本不存在: {script_path}",
            "started_at": _utc_now(),
            "finished_at": _utc_now(),
        }

    started_at = _utc_now()
    env = os.environ.copy()
    env.setdefault("PYTHONUNBUFFERED", "1")
    args = ["bash", str(script_path)]
    if pretty:
        args.append("--pretty")

    try:
        completed = subprocess.run(
            args,
            cwd=str(collector_dir),
            env=env,
            text=True,
            capture_output=True,
            timeout=timeout_seconds,
            check=False,
        )
        return {
            "name": name,
            "ok": completed.returncode == 0,
            "returncode": completed.returncode,
            "started_at": started_at,
            "finished_at": _utc_now(),
            "stdout": _truncate(completed.stdout),
            "stderr": _truncate(completed.stderr),
        }
    except subprocess.TimeoutExpired as exc:
        return {
            "name": name,
            "ok": False,
            "returncode": 124,
            "started_at": started_at,
            "finished_at": _utc_now(),
            "error": f"采集超时（{timeout_seconds}s）",
            "stdout": _truncate(exc.stdout or ""),
            "stderr": _truncate(exc.stderr or ""),
        }
    except Exception as exc:
        return {
            "name": name,
            "ok": False,
            "returncode": 1,
            "started_at": started_at,
            "finished_at": _utc_now(),
            "error": str(exc),
        }


def _load_last_run() -> dict[str, Any] | None:
    global _LAST_RUN
    if _LAST_RUN:
        return dict(_LAST_RUN)
    path = _last_run_path()
    if not path.exists():
        return None
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
        if isinstance(data, dict):
            _LAST_RUN = data
            return dict(data)
    except Exception:
        return None
    return None


def get_media_collector_run_state() -> dict[str, Any]:
    last = _load_last_run()
    return {
        "running": _RUN_LOCK.locked(),
        "last_run": last,
        "status": get_collector_status(),
    }


def run_media_collector_once(
    *,
    hot: bool = True,
    search: bool = True,
    authors: bool = True,
    timeout_seconds: int | None = None,
    pretty: bool = False,
) -> dict[str, Any]:
    global _LAST_RUN
    if not _RUN_LOCK.acquire(blocking=False):
        return {
            "ok": False,
            "running": True,
            "message": "自媒体采集正在运行，请稍后查看结果",
            "last_run": _load_last_run(),
            "status": get_collector_status(),
        }

    started_at = _utc_now()
    timeout = _script_timeout(timeout_seconds)
    try:
        tasks: list[tuple[str, str]] = []
        if hot:
            tasks.append(("hot", "collect.sh"))
        if search:
            tasks.append(("search", "batch_search.sh"))
        if authors:
            tasks.append(("authors", "batch_author_search.sh"))

        results = [
            _run_script(name, script, timeout_seconds=timeout, pretty=pretty)
            for name, script in tasks
        ]
        ok = all(bool(item.get("ok")) for item in results) if results else True
        payload: dict[str, Any] = {
            "ok": ok,
            "running": False,
            "started_at": started_at,
            "finished_at": _utc_now(),
            "tasks": [name for name, _ in tasks],
            "results": results,
            "status": get_collector_status(),
        }
        _LAST_RUN = payload
        _last_run_path().write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
        return payload
    finally:
        _RUN_LOCK.release()
