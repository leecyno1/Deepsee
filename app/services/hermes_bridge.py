"""Hermes API Server bridge — 0913 → Hermes 智能回复

通过 Hermes API Server (/v1/chat/completions) 调用完整 agent loop，
获得 wiki 知识库、记忆、工具、技能等全部智能能力。

Hermes 作为"脑子"，0913 作为"缰绳"（收发 + 规则 UI）。
"""

from __future__ import annotations

import hashlib
import json
import logging
import os
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

import requests

from ..db import SessionLocal
from ..models import WechatSubsession

logger = logging.getLogger(__name__)

# ── 会话自动刷新：超过此空闲时间（小时）则启动新 Hermes session ────
CST = timezone(timedelta(hours=8))
_IDLE_RESET_HOURS = float(os.getenv("HERMES_SESSION_IDLE_RESET_HOURS", "4"))


def _read_env_file_value(path: Path, key: str) -> str:
    try:
        if not path.exists():
            return ""
        for raw in path.read_text(encoding="utf-8").splitlines():
            line = raw.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            current_key, value = line.split("=", 1)
            if current_key.strip() == key:
                return value.strip().strip('"').strip("'")
    except Exception:
        return ""
    return ""


def _resolve_hermes_api_key() -> str:
    explicit = str(os.getenv("HERMES_API_KEY", "") or "").strip()
    if explicit:
        return explicit

    process_api_server_key = str(os.getenv("API_SERVER_KEY", "") or "").strip()
    if process_api_server_key:
        return process_api_server_key

    hermes_home = Path(os.getenv("HERMES_HOME") or (Path.home() / ".hermes"))
    env_key = _read_env_file_value(hermes_home / ".env", "API_SERVER_KEY")
    if env_key:
        return env_key
    return ""


# ── Hermes API Server 配置 ──────────────────────────────────────────
HERMES_API_BASE = os.getenv("HERMES_API_BASE", "http://127.0.0.1:8642")
HERMES_SESSION_ID = "wechat_gateway_default"  # fallback when chat_id is empty
HERMES_CHAT_URL = f"{HERMES_API_BASE.rstrip('/')}/v1/chat/completions"
TIMEOUT = 180  # agent loop 可能较慢（tool calls, wiki 搜索等）

# ── WeChat 兜底模型（MiniMax 直连，绕过 Hermes 主模型，极省 token）──
_WECHAT_FALLBACK_ENABLED = os.getenv("WECHAT_FALLBACK_API_KEY", "").strip() != ""
_WECHAT_FALLBACK_API_KEY = os.getenv("WECHAT_FALLBACK_API_KEY", "").strip()
_WECHAT_FALLBACK_API_BASE = os.getenv(
    "WECHAT_FALLBACK_API_BASE", "https://api.minimaxi.com/v1"
).rstrip("/")
_WECHAT_FALLBACK_MODEL = os.getenv(
    "WECHAT_FALLBACK_MODEL", "MiniMax-M3"
).strip()
_WECHAT_FALLBACK_TIMEOUT = int(os.getenv("WECHAT_FALLBACK_TIMEOUT", "30"))


# ── 降级：Hermes 不可用时回退到 0913 直调 LLM ──────────────────────
_FALLBACK_ENABLED = os.getenv("HERMES_FALLBACK_ENABLED", "false").lower() in (
    "true", "1", "yes"
)


def _sanitize_session_key_part(value: str) -> str:
    safe = "".join(c for c in str(value or "") if c.isalnum() or c in "@._-")
    return safe or "default"


def _chat_last_message_hours(chat_id: str) -> float | None:
    """返回 chat_id 最后一次消息距今的小时数，没有历史则返回 None。"""
    if not chat_id:
        return None
    db = SessionLocal()
    try:
        from sqlalchemy import text

        row = db.execute(
            text("SELECT max(timestamp) FROM messages WHERE chat_id = :cid"),
            {"cid": chat_id},
        ).scalar()
        if not row:
            return None
        last_ts = datetime.fromisoformat(str(row))
        if last_ts.tzinfo is None:
            last_ts = last_ts.replace(tzinfo=CST)
        delta = datetime.now(CST) - last_ts
        return delta.total_seconds() / 3600
    except Exception:
        return None
    finally:
        db.close()


def _session_freshness_suffix(chat_id: str) -> str:
    """如果 chat 空闲超过阈值，返回时间窗口后缀，否则返回空字符串。

    用 round-to-4h 窗口防抖动：同个 4h 窗口内始终同一个 session。
    """
    hours = _chat_last_message_hours(chat_id)
    if hours is None or hours < _IDLE_RESET_HOURS:
        return ""
    now = datetime.now(CST)
    # 向下取整到 4 小时窗口
    window = int(now.timestamp() / (_IDLE_RESET_HOURS * 3600))
    return f":fresh{window}"


def _bridge_session_id(
    *,
    channel: str,
    subsession_id: str | None = None,
    chat_id: str = "",
    sender_id: str = "",
) -> str:
    """Bridge-owned Hermes session key with explicit channel namespacing.

    0913 already persists WeChat contact/chat membership and turns in its own tables.
    Hermes session continuity should therefore align to the resolved subsession,
    not explode into one session per contact. This also prevents collisions with
    Feishu/DingTalk/API-server-native sessions because the bridge namespace and
    channel are encoded in the key.

    CRITICAL: chat_id is ALWAYS embedded in the session key regardless of subsession.
    Without per-chat isolation, all contacts share one Hermes session, causing
    cross-contact context pollution and multi-conversation summaries leaked to
    individual contacts.
    """
    normalized_channel = _sanitize_session_key_part(channel)
    normalized_subsession = _sanitize_session_key_part(subsession_id or "")
    chat_key = _sanitize_session_key_part(chat_id or sender_id or HERMES_SESSION_ID)
    if normalized_subsession != "default" or str(subsession_id or "").strip():
        return f"agent:bridge:{normalized_channel}:subsession:{normalized_subsession}:chat:{chat_key}"

    return f"agent:bridge:{normalized_channel}:chat:{chat_key}"


def _load_subsession_prompt(subsession_id: str | None) -> str | None:
    sid = str(subsession_id or "").strip()
    if not sid:
        return None
    db = SessionLocal()
    try:
        row = db.get(WechatSubsession, sid)
        prompt = str((row.system_prompt if row else "") or "").strip()
        return prompt or None
    finally:
        db.close()


def _prompt_hash(prompt: str | None) -> str | None:
    text = str(prompt or "").strip()
    if not text:
        return None
    return hashlib.sha256(text.encode("utf-8")).hexdigest()[:12]


def _default_system_prompt(
    sender_name: str = "",
    talker_name: str = "",
    is_group: bool = False,
) -> str:
    """默认 system prompt — 仅当 0913 回调未传入 subsession prompt 时使用。"""
    return (
        "你是程胤的微信助手，帮他处理工作消息。"
        "说话跟他本人风格一致：直接、自然、不讲究。像同事回微信，不像写邮件。"
        "需要查专业资料时先查再答，用自己的话说。日常闲聊就正常聊。"
        "有人问你是谁 → 「程胤团队的」。路演/会议邀约 → 只回「已知晓」。"
        "回微信三条铁律:"
        "① 不懂/没数据就直说不懂,不要硬编(不要编具体行情数据、电话、地址、内部信息);"
        "② 对方发非实质内容(表情包/单字/标点)或附件文件名时,简短自然回应即可,不要硬塞分析、不要主动切话题;"
        "③ 始终是「程胤团队的」助理,不要扮演销售、客服、官方账号等任何其他身份。"
    )


def _build_execution_context(
    *,
    chat_id: str = "",
    sender_id: str = "",
    sender_name: str = "",
    talker_name: str = "",
    is_group: bool = False,
    subsession_id: str | None = None,
    system_prompt: str | None = None,
) -> dict[str, Any]:
    explicit_prompt = str(system_prompt or "").strip() or None
    resolved_prompt = explicit_prompt
    prompt_source = "explicit" if explicit_prompt else "subsession"
    if not resolved_prompt:
        resolved_prompt = _load_subsession_prompt(subsession_id)
    if not resolved_prompt:
        prompt_source = "default"
        resolved_prompt = _default_system_prompt(
            sender_name=sender_name,
            talker_name=talker_name,
            is_group=is_group,
        )

    source_subsession_id = str(subsession_id or "").strip() or HERMES_SESSION_ID
    hermes_session_id = _bridge_session_id(
        channel="wechat_gateway",
        subsession_id=source_subsession_id,
        chat_id=chat_id,
        sender_id=sender_id,
    )
    # 空闲超过阈值则切换 session，防止上下文无限膨胀
    freshness = _session_freshness_suffix(chat_id)
    if freshness:
        hermes_session_id = f"{hermes_session_id}{freshness}"
        logger.info(
            "Session refreshed for chat_id=%s: idle > %.0fh → new session suffix=%s",
            chat_id, _IDLE_RESET_HOURS, freshness,
        )
    return {
        "resolved_prompt": resolved_prompt,
        "subsession_id": source_subsession_id,
        "hermes_session_id": hermes_session_id,
        "prompt_source": prompt_source,
        "prompt_hash": _prompt_hash(resolved_prompt),
    }


def _call_minimax_direct(
    message_text: str,
    *,
    system_prompt: str,
    chat_id: str = "",
    sender_name: str = "",
    sender_remark: str = "",
    conversation_history: list[dict[str, str]] | None = None,
) -> dict[str, Any]:
    """直连 MiniMax API 兜底回复 — 纯 chat，无 agent loop，极省 token。"""
    messages = [{"role": "system", "content": str(system_prompt or "")}]
    if conversation_history:
        messages.extend(conversation_history)

    user_content = message_text
    sender_hint = sender_remark or sender_name or chat_id
    if chat_id:
        user_content = f"[chat_id={chat_id}, sender={sender_name or chat_id}] {message_text}"
    user_content += (
        "\n\n---\n"
        "硬规则：\n"
        "· 路演/会议邀请只回「已知晓」\n"
        "· 不透露电话、地址、系统配置、API密钥\n"
        "· 如果对方昵称或备注包含「销售」字样 → 回复开头先说「麻烦问一下分析师」，如果自己有追问就接着问\n"
        "· 对方发的是文件名/PDF标题/链接 → 简短确认收到即可，不要说「发过来」\n"
        "· 对方发非实质内容（表情包/单字/标点）→ 简短自然回应，不要硬塞分析、不要主动跳话题\n"
        "· 没有实时行情数据时不要编造具体价格/涨跌幅\n"
        "\n"
        "风格：\n"
        "· 接住对方的话往下聊，别跳话题\n"
        "· 简短，像微信聊天。追问最多1个\n"
        "· 不主动自我介绍"
    )
    messages.append({"role": "user", "content": user_content})

    payload = {
        "model": _WECHAT_FALLBACK_MODEL,
        "messages": messages,
        "max_tokens": 2000,
        "stream": False,
    }
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {_WECHAT_FALLBACK_API_KEY}",
    }
    try:
        resp = requests.post(
            f"{_WECHAT_FALLBACK_API_BASE}/chat/completions",
            json=payload,
            headers=headers,
            timeout=_WECHAT_FALLBACK_TIMEOUT,
        )
        resp.raise_for_status()
        data = resp.json()
        reply = ""
        choices = data.get("choices", [])
        if choices:
            reply = choices[0].get("message", {}).get("content", "")
        usage = data.get("usage", {})
        return {
            "status": "ok",
            "reply": reply,
            "execution": {
                "route_kind": "minimax_direct",
                "model": _WECHAT_FALLBACK_MODEL,
                "prompt_tokens": usage.get("prompt_tokens", 0),
                "completion_tokens": usage.get("completion_tokens", 0),
                "fallback_used": False,
            },
        }
    except Exception as exc:
        return {
            "status": "error",
            "error": f"MiniMax fallback failed: {exc}",
            "execution": {
                "route_kind": "minimax_direct",
                "model": _WECHAT_FALLBACK_MODEL,
                "fallback_used": False,
            },
        }


def call_hermes_for_reply(
    message_text: str,
    *,
    subsession_id: str | None = None,
    chat_id: str = "",
    sender_id: str = "",
    sender_name: str = "",
    sender_remark: str = "",
    talker_name: str = "",
    is_group: bool = False,
    system_prompt: str | None = None,
    conversation_history: list[dict[str, str]] | None = None,
) -> dict[str, Any]:
    """获取微信自动回复 — MiniMax 兜底直连优先，Hermes 为主。

    优先级：
    1. MiniMax 兜底（已配置 API key 时）— 直连，纯 chat，极省 token
    2. Hermes API（主）— wiki / 记忆 / 工具 / 技能全链路
    3. 0913 降级（Hermes 不可用时）— 旧 SiliconFlow 路径
    """
    # MiniMax 兜底优先：已配置 API key 则直连，完全绕过 Hermes
    if _WECHAT_FALLBACK_ENABLED:
        return _call_minimax_direct(
            message_text,
            system_prompt=str(system_prompt or _default_system_prompt(
                sender_name=sender_name,
                talker_name=talker_name,
                is_group=is_group,
            )),
            chat_id=chat_id,
            sender_name=sender_name,
            sender_remark=sender_remark,
            conversation_history=conversation_history,
        )
    execution_context = _build_execution_context(
        chat_id=chat_id,
        sender_id=sender_id,
        sender_name=sender_name,
        talker_name=talker_name,
        is_group=is_group,
        subsession_id=subsession_id,
        system_prompt=system_prompt,
    )
    try:
        return _call_hermes_api(
            message_text,
            subsession_id=subsession_id,
            chat_id=chat_id,
            sender_id=sender_id,
            sender_name=sender_name,
            sender_remark=sender_remark,
            talker_name=talker_name,
            is_group=is_group,
            system_prompt=system_prompt,
            conversation_history=conversation_history,
            execution_context=execution_context,
        )
    except Exception as exc:
        logger.warning("Hermes API failed: %s", exc)
        if _FALLBACK_ENABLED:
            logger.info("Falling back to 0913 direct LLM path")
            return _fallback_direct_llm(
                message_text,
                subsession_id=str(execution_context.get("subsession_id") or "").strip() or None,
                chat_id=chat_id,
                sender_id=sender_id,
                sender_name=sender_name,
                talker_name=talker_name,
                is_group=is_group,
                system_prompt=str(execution_context.get("resolved_prompt") or "").strip() or None,
            )
        return {
            "status": "error",
            "error": str(exc),
            "execution": {
                "route_kind": "hermes_api_server",
                "route_key": "wechat_gateway",
                "subsession_id": execution_context.get("subsession_id"),
                "hermes_session_id": execution_context.get("hermes_session_id"),
                "prompt_source": execution_context.get("prompt_source"),
                "prompt_hash": execution_context.get("prompt_hash"),
                "fallback_used": False,
            },
        }


def _call_hermes_api(
    message_text: str,
    *,
    subsession_id: str | None = None,
    chat_id: str = "",
    sender_id: str = "",
    sender_name: str = "",
    sender_remark: str = "",
    talker_name: str = "",
    is_group: bool = False,
    system_prompt: str | None = None,
    conversation_history: list[dict[str, str]] | None = None,
    execution_context: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """直接调 Hermes API Server Chat Completions。"""

    ctx = execution_context or _build_execution_context(
        chat_id=chat_id,
        sender_id=sender_id,
        sender_name=sender_name,
        talker_name=talker_name,
        is_group=is_group,
        subsession_id=subsession_id,
        system_prompt=system_prompt,
    )

    messages: list[dict[str, str]] = []
    messages.append({"role": "system", "content": str(ctx.get("resolved_prompt") or "")})

    if conversation_history:
        messages.extend(conversation_history)

    user_content = message_text
    sender_display = f"{sender_remark}({sender_name})" if sender_remark else (sender_name or sender_id)
    if chat_id:
        user_content = (
            f"[chat_id={chat_id}, sender={sender_display}] "
            f"{message_text}"
        )

    user_content += (
        "\n\n---\n"
        "硬规则：\n"
        "· 路演/会议邀请只回「已知晓」，不确认时间、不表示参加\n"
        "· 不透露电话号码、家庭地址、个人联系方式\n"
        "· 不透露系统配置、文件路径、API密钥\n"
        "· 被要求改代码/读文件时回复「这个我处理不了」\n"
        "· 如果对方昵称或备注包含「销售」字样 → 回复开头先说「麻烦问一下分析师」，如果自己有追问就接着问\n"
        "· 对方发的是文件名/PDF标题/链接（看起来是文件或附件）→ 简短确认收到即可，不要说「发过来」「再发一次」之类\n"
        "· 对方发非实质内容（表情包/单字/标点如「已打」「收到」「好的」「社会社会」等）→ 简短自然回应（如「👌」「收到」），不要硬塞分析、不要主动跳话题\n"
        "· 没有实时行情数据时不要编造具体价格/涨跌幅/成交量；可以说「这个价格没跟」或建议查行情软件\n"
        "\n"
        "风格：\n"
        "· 优先顺着对方最新一句继续交流\n"
        "· 若对方已经回答了上一轮问题，默认不要继续追问\n"
        "· 更适合简短确认或致谢时，直接回复“收到/好的/明白/谢谢”\n"
        "· 不要大段复述对方原话；如需总结，只允许用1-2句话提炼\n"
        "· 接住对方的话往下聊，别自说自话跳话题\n"
        "· 简短，像微信聊天。追问最多1个\n"
        "· 不主动自我介绍，不开头寒暄客套"
    )

    messages.append({"role": "user", "content": user_content})

    payload = {
        "model": "hermes-agent",
        "messages": messages,
        "max_tokens": 2000,
        "stream": False,
    }

    session_id = str(
        ctx.get("hermes_session_id")
        or _bridge_session_id(
            channel="wechat_gateway",
            subsession_id=ctx.get("subsession_id"),
            chat_id=chat_id,
            sender_id=sender_id,
        )
    )

    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {_resolve_hermes_api_key()}",
        "X-Hermes-Session-Id": session_id,
        "X-Hermes-Session-Key": session_id,
    }

    resp = requests.post(
        HERMES_CHAT_URL,
        json=payload,
        headers=headers,
        timeout=TIMEOUT,
    )
    resp.raise_for_status()
    data = resp.json()

    choices = data.get("choices", [])
    reply_text = ""
    if choices:
        reply_text = choices[0].get("message", {}).get("content", "")

    usage = data.get("usage", {})

    return {
        "status": "ok",
        "reply": reply_text,
        "execution": {
            "route_kind": "hermes_api_server",
            "route_key": "wechat_gateway",
            "subsession_id": ctx.get("subsession_id"),
            "hermes_session_id": session_id,
            "prompt_source": ctx.get("prompt_source"),
            "prompt_hash": ctx.get("prompt_hash"),
            "model": data.get("model", "unknown"),
            "prompt_tokens": usage.get("prompt_tokens", 0),
            "completion_tokens": usage.get("completion_tokens", 0),
            "fallback_used": False,
        },
    }


def _fallback_direct_llm(
    message_text: str,
    *,
    subsession_id: str | None = None,
    chat_id: str = "",
    sender_id: str = "",
    sender_name: str = "",
    talker_name: str = "",
    is_group: bool = False,
    system_prompt: str | None = None,
) -> dict[str, Any]:
    """降级：Hermes 不可用时回退到原有 siliconflow_chat 路径。"""
    from .reply_generation import generate_local_reply

    db = SessionLocal()
    try:
        result = generate_local_reply(
            db,
            {
                "message_text": message_text,
                "chat_id": chat_id,
                "sender_id": sender_id,
                "sender_name": sender_name,
                "talker_name": talker_name or chat_id,
                "is_group": is_group,
                "wait_for_human_reply_suppression": False,
                "subsession_id": str(subsession_id or "").strip() or None,
                "system_prompt": str(system_prompt or "").strip() or None,
            },
        )
        execution = dict(result.get("execution") or {})
        execution.setdefault("route_kind", "direct_llm_fallback")
        execution.setdefault("route_key", "wechat_gateway")
        execution["subsession_id"] = str(subsession_id or "").strip() or execution.get("subsession_id")
        execution["hermes_session_id"] = _bridge_session_id(
            channel="wechat_gateway",
            subsession_id=subsession_id,
            chat_id=chat_id,
            sender_id=sender_id,
        )
        execution["prompt_hash"] = _prompt_hash(system_prompt)
        execution["prompt_source"] = "fallback_passthrough" if system_prompt else execution.get("prompt_source")
        execution["fallback_used"] = True
        result["execution"] = execution
        return result
    except Exception as exc:
        return {
            "status": "error",
            "error": f"fallback failed: {exc}",
            "execution": {
                "route_kind": "direct_llm_fallback",
                "route_key": "wechat_gateway",
                "subsession_id": str(subsession_id or "").strip() or None,
                "hermes_session_id": _bridge_session_id(
                    channel="wechat_gateway",
                    subsession_id=subsession_id,
                    chat_id=chat_id,
                    sender_id=sender_id,
                ),
                "prompt_hash": _prompt_hash(system_prompt),
                "prompt_source": "fallback_passthrough" if system_prompt else None,
                "fallback_used": True,
                "error": f"fallback failed: {exc}",
            },
        }
    finally:
        db.close()
