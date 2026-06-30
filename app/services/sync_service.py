from __future__ import annotations

from datetime import datetime, timedelta
from typing import Optional, List, Dict, Any
import time

from sqlalchemy.exc import OperationalError
from sqlalchemy.orm import Session
from sqlalchemy import select, text

from ..models import Message, Chat, Contact, SyncState, ExtAdapter, AdapterMessage
import json
from .chatlog_client import ChatlogClient
from .ext_adapter_service import ingest_adapter_logs
from ..config import settings
import re
import os
import requests
from urllib.parse import quote
from .wx_cli_client import WxCliClient


def _to_local_naive(dt: Optional[datetime]) -> Optional[datetime]:
    """Normalize a datetime to local naive time.

    - If `dt` is timezone-aware, convert to local timezone and drop tzinfo
    - If `dt` is already naive, return as-is
    - If `dt` is None, return None
    """
    if dt is None:
        return None
    try:
        if dt.tzinfo is not None:
            return dt.astimezone().replace(tzinfo=None)
    except Exception:
        pass
    return dt


def _get_last_sync(db: Session) -> Optional[datetime]:
    row = db.get(SyncState, "chatlog_last_sync")
    if row and row.value:
        try:
            return _to_local_naive(datetime.fromisoformat(row.value))
        except Exception:
            return None
    # fallback to newest message timestamp (already stored as naive local)
    latest: Optional[datetime] = db.execute(select(Message.timestamp).order_by(Message.timestamp.desc())).scalar()
    return _to_local_naive(latest)


def _set_last_sync(db: Session, ts: datetime):
    row = db.get(SyncState, "chatlog_last_sync")
    if not row:
        row = SyncState(key="chatlog_last_sync", value=ts.isoformat())
    else:
        row.value = ts.isoformat()
        row.updated_at = datetime.utcnow()
    db.add(row)


def _safe_commit(db: Session, retries: int = 5, base_delay: float = 0.2) -> None:
    """Commit with retry to tolerate sqlite locked errors."""
    last_exc: OperationalError | None = None
    for attempt in range(retries):
        try:
            db.commit()
            return
        except OperationalError as exc:
            db.rollback()
            last_exc = exc
            time.sleep(base_delay * (attempt + 1))
    if last_exc is not None:
        raise last_exc


def _parse_messages(payload: Any) -> List[Dict[str, Any]]:
    # chatlog json may be a list or an object with messages
    if isinstance(payload, dict) and "messages" in payload:
        return payload["messages"]
    if isinstance(payload, list):
        return payload
    # unknown format
    return []


def _normalize_http_base(base: str | None) -> str:
    value = (base or "").strip()
    if not value:
        value = settings.CHATLOG_HTTP_BASE or "http://127.0.0.1:5030"
    if not re.match(r"^https?://", value, flags=re.IGNORECASE):
        value = f"http://{value}"
    return value.rstrip("/")


def _encode_rel_path(path: str | None) -> str:
    text = str(path or "").strip().replace("\\", "/").lstrip("/")
    if not text:
        return ""
    return "/".join(quote(seg, safe="") for seg in text.split("/") if seg)


def _extract_contents_dict(raw: Any) -> dict[str, Any]:
    if isinstance(raw, dict):
        return raw
    if isinstance(raw, str):
        try:
            parsed = json.loads(raw)
            if isinstance(parsed, dict):
                return parsed
        except Exception:
            return {}
    return {}


def _build_chatlog_media_url(msg_type: Any, contents: dict[str, Any] | None, *, host: str | None = None) -> str | None:
    c = contents or {}
    base = _normalize_http_base(host or c.get("host"))
    mt = str(msg_type or "").strip().lower()
    mt_num = None
    try:
        mt_num = int(mt)
    except Exception:
        mt_num = None

    direct_image = str(c.get("cdnthumburl") or c.get("thumbUrl") or c.get("thumb_url") or c.get("image_url") or c.get("imageUrl") or "").strip()
    if direct_image:
        return direct_image

    md5 = str(c.get("md5") or c.get("imageId") or c.get("image_id") or c.get("mediaId") or c.get("id") or "").strip()
    path_raw = (
        c.get("path")
        or c.get("data")
        or c.get("relative")
        or c.get("image_path")
        or c.get("localPath")
        or c.get("video_path")
    )
    rel = _encode_rel_path(str(path_raw or ""))

    if mt_num == 3 or mt in {"image", "img", "图片"}:
        if md5 and rel:
            return f"{base}/image/{quote(md5, safe='')},{rel}"
        if md5:
            return f"{base}/image/{quote(md5, safe='')}"
        if rel:
            return f"{base}/data/{rel}"
        return None

    if mt_num in {43, 62} or mt in {"video", "视频"}:
        if md5 and rel:
            return f"{base}/video/{quote(md5, safe='')},{rel}"
        if md5:
            return f"{base}/video/{quote(md5, safe='')}"
        if rel:
            return f"{base}/data/{rel}"
        return None

    if mt_num == 34 or mt in {"voice", "audio", "语音"}:
        voice_id = str(c.get("voice") or c.get("voiceId") or c.get("id") or c.get("mediaId") or "").strip()
        if voice_id:
            return f"{base}/voice/{quote(voice_id, safe='')}"
        if rel:
            return f"{base}/data/{rel}"
        return None

    # chatlog uses type 49 for both links and files.
    if mt_num == 49 or mt in {"file", "document", "文件", "链接", "link"}:
        direct = str(c.get("url") or "").strip()
        if direct:
            return direct
        if md5:
            return f"{base}/file/{quote(md5, safe='')}"
        if rel:
            return f"{base}/data/{rel}"
        return None

    if rel:
        return f"{base}/data/{rel}"
    return None


def _build_meta_and_media(msg: dict[str, Any]) -> tuple[dict[str, Any], str | None]:
    meta_payload: dict[str, Any] = {}
    contents = _extract_contents_dict(msg.get("contents"))
    if contents:
        meta_payload["contents"] = contents
    media_url = _build_chatlog_media_url(msg.get("type"), contents)
    return meta_payload, media_url


def _load_wechat_id_filters(db: Session) -> tuple[set[str], set[str], set[str], set[str], bool]:
    def _load_list(key: str) -> set[str]:
        row = db.get(SyncState, key)
        if not row or not row.value:
            return set()
        try:
            data = json.loads(row.value)
            if isinstance(data, list):
                return {str(x) for x in data}
        except Exception:
            pass
        return set()

    bl_send = _load_list("blacklist_senders")
    bl_talk = _load_list("blacklist_talkers")
    wl_send = _load_list("whitelist_senders")
    wl_talk = _load_list("whitelist_talkers")
    return bl_send, bl_talk, wl_send, wl_talk, bool(wl_send or wl_talk)


def _passes_wechat_filters(
    talker: str | None,
    sender: str | None,
    filters: tuple[set[str], set[str], set[str], set[str], bool],
) -> bool:
    bl_send, bl_talk, wl_send, wl_talk, has_wl = filters
    if has_wl and not ((talker and talker in wl_talk) or (sender and sender in wl_send)):
        return False
    if (talker and talker in bl_talk) or (sender and sender in bl_send):
        return False
    return True


def _insert_wechat_local_message(
    db: Session,
    *,
    talker: str | None,
    talker_name: str | None,
    sender: str | None,
    sender_name: str | None,
    ts: datetime | None,
    content: str | None,
    msg_type: Any = None,
    direction: str | None = "in",
    is_chatroom: bool = False,
    media_url: str | None = None,
    meta: dict[str, Any] | None = None,
) -> bool:
    if not talker and not sender:
        return False
    if not content and not media_url:
        return False
    if ts and talker and content is not None:
        exists = db.execute(
            select(Message.id).where(
                Message.chat_id == talker,
                Message.sender_id == sender,
                Message.timestamp == ts,
                Message.content_text == content,
            )
        ).scalar()
        if exists:
            return False
    if talker:
        chat = db.get(Chat, talker)
        if not chat:
            chat = Chat(
                id=talker,
                title=talker_name or talker,
                type="group" if is_chatroom else "single",
                is_chatroom=is_chatroom,
            )
            db.add(chat)
        elif talker_name and (not chat.title or chat.title == talker):
            chat.title = talker_name
            db.add(chat)
    if sender:
        contact = db.get(Contact, sender)
        if not contact:
            contact = Contact(id=sender, name=sender_name or sender)
            db.add(contact)
        elif sender_name and (not contact.name or contact.name == sender):
            contact.name = sender_name
            db.add(contact)
    msg = Message(
        chat_id=talker,
        sender_id=sender,
        sender_name=sender_name,
        talker_name=talker_name,
        timestamp=ts,
        direction=direction,
        type=str(msg_type) if msg_type is not None else None,
        content_text=content,
        media_url=media_url,
        meta=meta or {},
    )
    db.add(msg)
    if talker and ts:
        chat = db.get(Chat, talker)
        if chat and (chat.last_message_at is None or ts > chat.last_message_at):
            chat.last_message_at = ts
            db.add(chat)
    return True


# --------- LangBot adapter backup source (optional) ---------

_LANGBOT_CURSOR_KEY = "langbot_backup_cursor"


def _get_json_obj(db: Session, key: str) -> dict:
    row = db.get(SyncState, key)
    if not row or not row.value:
        return {}
    try:
        data = json.loads(row.value)
        return data if isinstance(data, dict) else {}
    except Exception:
        return {}


def _set_json_obj(db: Session, key: str, obj: dict) -> None:
    payload = json.dumps(obj or {})
    row = db.get(SyncState, key)
    if not row:
        row = SyncState(key=key, value=payload)
    else:
        row.value = payload
        row.updated_at = datetime.utcnow()
    db.add(row)


def _get_extensions_log_dir(db: Session) -> str | None:
    cfg = _get_json_obj(db, "extensions_config")
    val = cfg.get("langbot_log_dir") if isinstance(cfg, dict) else None
    if isinstance(val, str) and val.strip():
        return val.strip()
    return None


def _looks_like_wechat_id(value: str | None) -> bool:
    if not value:
        return False
    s = str(value).strip()
    if not s or len(s) > 128:
        return False
    if any(ch.isspace() for ch in s):
        return False
    # Avoid storing Chinese names as contact IDs; keep likely IDs only.
    return bool(re.fullmatch(r"[A-Za-z0-9_@.\-]+", s))


def sync_from_langbot_adapters(
    db: Session,
    *,
    since: datetime | None = None,
    adapter_keys: list[str] | None = None,
    ingest: bool = True,
    force: bool = False,
) -> Dict[str, Any]:
    """Sync messages from LangBot adapter logs (ingested into adapter_messages) into main messages table.

    Data flow:
      1) (optional) ingest adapter logs -> AdapterMessage
      2) copy new AdapterMessage rows -> Message/Chat/Contact with dedupe

    Dedup is best-effort: (chat_id, sender_id, timestamp, content_text).
    """
    # Always compare in local naive domain to match messages table.
    now = _to_local_naive(datetime.now()) or datetime.now()
    since_dt = _to_local_naive(since) if since else None
    if since_dt is None:
        since_dt = now - timedelta(days=7)

    # Load filters once (same semantics as chatlog sync)
    def _load_list(key: str) -> set[str]:
        row = db.get(SyncState, key)
        if not row or not row.value:
            return set()
        try:
            data = json.loads(row.value)
            if isinstance(data, list):
                return set(str(x) for x in data)
        except Exception:
            pass
        return set()

    bl_send = _load_list("blacklist_senders")
    bl_talk = _load_list("blacklist_talkers")
    wl_send = _load_list("whitelist_senders")
    wl_talk = _load_list("whitelist_talkers")
    has_wl = bool(wl_send or wl_talk)

    base_log_dir = _get_extensions_log_dir(db) or settings.__dict__.get("LANGBOT_ADAPTER_LOG_DIR") or "./data/adapters"
    # Practical defaults: if user didn't configure, try co-located LangBot logs.
    if base_log_dir in {"./data/adapters", "data/adapters"}:
        for candidate in ("../LangBot/docker/data/logs", "../LangBot/data/logs"):
            if os.path.isdir(candidate):
                base_log_dir = candidate
                break

    # Determine which adapters to ingest from
    q = select(ExtAdapter).where(ExtAdapter.enabled == True)  # noqa
    if adapter_keys:
        q = q.where(ExtAdapter.key.in_(adapter_keys))
    adapters = db.execute(q.order_by(ExtAdapter.id.desc())).scalars().all()
    if not adapters:
        # Auto-seed a default adapter to make "手动合并" usable out of the box.
        if ingest and (not adapter_keys or "langbot" in adapter_keys):
            auto = ExtAdapter(
                key="langbot",
                name="LangBot日志",
                enabled=True,
                source_type="langbot",
                config={"log_dir": base_log_dir},
            )
            db.add(auto)
            _safe_commit(db)
            adapters = [auto]
        else:
            return {"status": "ok", "adapters": 0, "ingested": 0, "scanned": 0, "inserted": 0}

    cursor = _get_json_obj(db, _LANGBOT_CURSOR_KEY) if not force else {}

    total_ingested = 0
    total_scanned = 0
    total_inserted = 0
    per_adapter: list[dict] = []
    chat_cache: dict[str, Chat] = {}
    contact_cache: dict[str, Contact] = {}

    for ad in adapters:
        adapter_base = (ad.config or {}).get("log_dir") or base_log_dir
        ingested = 0
        if ingest:
            try:
                ingested = ingest_adapter_logs(db, ad, adapter_base, since=since_dt)
                # Make inserted AdapterMessage ids visible
                db.flush()
            except Exception:
                db.rollback()
                ingested = 0
        total_ingested += int(ingested or 0)

        last_id = 0
        try:
            last_id = int(cursor.get(ad.key) or 0)
        except Exception:
            last_id = 0

        query = select(AdapterMessage).where(AdapterMessage.adapter_key == ad.key)
        if force or last_id <= 0:
            query = query.where(AdapterMessage.timestamp >= since_dt)
        else:
            query = query.where(AdapterMessage.id > last_id)
        rows = db.execute(query.order_by(AdapterMessage.id.asc())).scalars().all()

        max_seen_id = last_id
        scanned = 0
        inserted = 0

        for m in rows:
            scanned += 1
            try:
                if m.id and m.id > max_seen_id:
                    max_seen_id = m.id
            except Exception:
                pass

            talker = (m.chat_id or "").strip() or None
            sender = (m.sender or "").strip() or None
            ts = _to_local_naive(m.timestamp)
            content = (m.content_text or "").strip()
            meta = m.meta or {}

            if not talker or not content:
                continue
            # Without a timestamp we can't reliably de-dup or sort; skip noisy rows.
            if ts is None:
                continue

            # Apply white/black list semantics
            if has_wl and not ((talker in wl_talk) or (sender and sender in wl_send)):
                continue
            if talker in bl_talk or (sender and sender in bl_send):
                continue

            if ts and ts < since_dt:
                continue

            # de-dup against existing messages table
            exists = None
            if ts:
                exists = db.execute(
                    select(Message.id).where(
                        Message.chat_id == talker,
                        Message.sender_id == sender,
                        Message.timestamp == ts,
                        Message.content_text == content,
                    )
                ).scalar()
            if exists:
                continue

            meta_dict = meta if isinstance(meta, dict) else {}
            meta_inner = meta_dict.get("meta") if isinstance(meta_dict.get("meta"), dict) else {}
            def _mget(*keys: str):
                for k in keys:
                    v = meta_dict.get(k)
                    if v is not None:
                        return v
                    v2 = meta_inner.get(k)
                    if v2 is not None:
                        return v2
                return None

            talker_name = _mget("talker_name", "talkerName", "chat_name")
            sender_name = _mget("sender_name", "senderName", "nickname") or sender
            msg_type = None
            msg_type = _mget("type", "msg_type", "message_type")

            # upsert chat/contact (best-effort)
            is_chatroom = talker.endswith("@chatroom")
            chat = chat_cache.get(talker) or db.get(Chat, talker)
            if not chat:
                chat = Chat(id=talker, title=talker_name or talker, is_chatroom=is_chatroom)
                db.add(chat)
            chat_cache[talker] = chat
            if sender and _looks_like_wechat_id(sender) and not sender.endswith("@chatroom"):
                c = contact_cache.get(sender) or db.get(Contact, sender)
                if not c:
                    c = Contact(id=sender, name=str(sender_name) if sender_name else None)
                    db.add(c)
                contact_cache[sender] = c

            meta_payload: dict[str, Any] = {}
            if isinstance(meta, dict):
                meta_payload.update(meta)
                # Flatten nested `meta` dict if present (legacy adapter ingest shape)
                inner = meta_payload.pop("meta", None)
                if isinstance(inner, dict):
                    for k, v in inner.items():
                        meta_payload.setdefault(k, v)
            meta_payload.setdefault("source", "langbot")
            meta_payload.setdefault("adapter_key", ad.key)
            if m.external_id is not None:
                meta_payload.setdefault("external_id", str(m.external_id))
            media_url = _build_chatlog_media_url(msg_type, _extract_contents_dict(meta_payload.get("contents")))

            msg = Message(
                chat_id=talker,
                sender_id=sender,
                sender_name=str(sender_name) if sender_name else None,
                talker_name=str(talker_name) if talker_name else None,
                timestamp=ts,
                direction=(m.direction or "in").lower(),
                type=str(msg_type) if msg_type is not None else "text",
                content_text=content,
                media_url=media_url,
                meta=meta_payload,
            )
            db.add(msg)
            inserted += 1
            total_inserted += 1

            if ts and (chat.last_message_at is None or ts > chat.last_message_at):
                chat.last_message_at = ts
                db.add(chat)

        total_scanned += scanned
        cursor[ad.key] = max_seen_id
        per_adapter.append(
            {
                "adapter_key": ad.key,
                "ingested": int(ingested or 0),
                "scanned": int(scanned),
                "inserted": int(inserted),
                "cursor": int(max_seen_id),
            }
        )
        _safe_commit(db)

    _set_json_obj(db, _LANGBOT_CURSOR_KEY, cursor)
    _safe_commit(db)
    return {
        "status": "ok",
        "since": since_dt.isoformat(),
        "until": now.isoformat(),
        "adapters": len(adapters),
        "ingested": total_ingested,
        "scanned": total_scanned,
        "inserted": total_inserted,
        "details": per_adapter,
    }


def sync_from_chatlog(db: Session, since: Optional[datetime] = None) -> Dict[str, Any]:
    """Incremental sync since a cutoff.

    Earlier implementation called the chatlog endpoint once per day without talker
    and without pagination, which can miss messages on some chatlog builds.
    This version mirrors the robust logic used in sync_full:
    - iterate day by day from since..now
    - fetch talkers from session list and walk each talker with pagination
    - fall back to non-talker queries if session list is unavailable
    """
    client = ChatlogClient()

    # Load filters once
    def _load_list(key: str) -> set[str]:
        row = db.get(SyncState, key)
        if not row or not row.value:
            return set()
        try:
            data = json.loads(row.value)
            if isinstance(data, list):
                return set(str(x) for x in data)
        except Exception:
            pass
        return set()

    bl_send = _load_list("blacklist_senders")
    bl_talk = _load_list("blacklist_talkers")
    wl_send = _load_list("whitelist_senders")
    wl_talk = _load_list("whitelist_talkers")
    has_wl = bool(wl_send or wl_talk)

    # Always use local naive clock for comparison with DB/chatlog timestamps
    now = _to_local_naive(datetime.now()) or datetime.now()
    # Normalize since against last_sync to avoid re-pulling very old data when the caller passes an early since
    last_seen = _get_last_sync(db)
    # Normalize caller-provided cutoff as well
    since = _to_local_naive(since)
    if since is None:
        since = last_seen
    # if caller provided a very early since, clamp to last_seen - 10min (safety window to avoid boundary misses)
    if last_seen is not None and since is not None:
        safety = timedelta(minutes=10)
        min_since = last_seen - safety
        # Compare in the same naive-local domain
        try:
            if since < min_since:
                since = min_since
        except TypeError:
            # In case any tz-aware sneaks in, normalize and compare again
            s1 = _to_local_naive(since)
            s2 = _to_local_naive(min_since)
            if s1 is not None and s2 is not None and s1 < s2:
                since = s2
    if since is None:
        since = now - timedelta(days=1)

    # Prepare time window
    start_date = since.date()
    end_date = now.date()

    try:
        session_payload = client.get_sessions()
        talkers = ChatlogClient.extract_talker_ids(session_payload)
    except requests.RequestException:
        raise
    except Exception:
        talkers = []

    total_fetched = 0
    inserted = 0
    max_ts: Optional[datetime] = since

    cur = start_date
    while cur <= end_date:
        day = cur.isoformat()
        if talkers:
            # Robust path: walk each talker with pagination
            for talker in talkers:
                offset = 0
                while True:
                    try:
                        raw = client.get_chatlog(time_range=day, talker=talker, limit=500, offset=offset)
                        part = _parse_messages(raw)
                        if not part:
                            break
                        total_fetched += len(part)
                        for m in part:
                            talkerName = m.get("talkerName")
                            sender = m.get("sender")
                            senderName = m.get("senderName")
                            isChatRoom = bool(m.get("isChatRoom"))
                            isSelf = bool(m.get("isSelf"))
                            type_ = m.get("type")
                            content = m.get("content") or m.get("text")
                            meta_payload, media_url = _build_meta_and_media(m)
                            time_str = m.get("time") or m.get("timestamp")
                            ts = None
                            if time_str:
                                try:
                                    ts = datetime.fromisoformat(time_str)
                                except Exception:
                                    ts = None
                            if ts and (max_ts is None or ts > max_ts):
                                max_ts = ts
                            # white/black list
                            if has_wl and not ((talker and talker in wl_talk) or (sender and sender in wl_send)):
                                continue
                            if (talker and talker in bl_talk) or (sender and sender in bl_send):
                                continue
                            # cutoff
                            if ts and ts < since:
                                continue
                            # de-dup
                            exists = None
                            if ts:
                                exists = db.execute(
                                    select(Message.id).where(
                                        Message.chat_id == talker,
                                        Message.sender_id == sender,
                                        Message.timestamp == ts,
                                        Message.content_text == content,
                                    )
                                ).scalar()
                                if exists:
                                    continue
                            # upsert chat/contact
                            chat = db.get(Chat, talker)
                            if not chat:
                                chat = Chat(id=talker, title=talkerName or talker, is_chatroom=isChatRoom)
                                db.add(chat)
                            if sender:
                                c = db.get(Contact, sender)
                                if not c:
                                    c = Contact(id=sender, name=senderName)
                                    db.add(c)
                            msg = Message(
                                chat_id=talker,
                                sender_id=sender,
                                sender_name=senderName,
                                talker_name=talkerName,
                                timestamp=ts,
                                direction="out" if isSelf else "in",
                                type=str(type_) if type_ is not None else None,
                                content_text=content,
                                media_url=media_url,
                                meta=meta_payload,
                            )
                            db.add(msg)
                            inserted += 1
                            if chat and ts and (chat.last_message_at is None or ts > chat.last_message_at):
                                chat.last_message_at = ts
                                db.add(chat)
            # Engine-level transaction already committed; avoid session commit to prevent unrelated flush
                        if len(part) < 500:
                            break
                        offset += 500
                    except Exception:
                        db.rollback()
                        break
        else:
            # Fallback path: no talkers available, try non-talker paginated fetch (if supported)
            offset = 0
            while True:
                try:
                    raw = client.get_chatlog(time_range=day, limit=500, offset=offset)
                    part = _parse_messages(raw)
                    if not part:
                        break
                    total_fetched += len(part)
                    for m in part:
                        talker = m.get("talker") or m.get("chat_id")
                        talkerName = m.get("talkerName")
                        sender = m.get("sender")
                        senderName = m.get("senderName")
                        isChatRoom = bool(m.get("isChatRoom"))
                        isSelf = bool(m.get("isSelf"))
                        type_ = m.get("type")
                        content = m.get("content") or m.get("text")
                        meta_payload, media_url = _build_meta_and_media(m)
                        time_str = m.get("time") or m.get("timestamp")
                        ts = None
                        if time_str:
                            try:
                                ts = _to_local_naive(datetime.fromisoformat(time_str))
                            except Exception:
                                ts = None
                        if ts and (max_ts is None or ts > max_ts):
                            max_ts = ts
                        if has_wl and not ((talker and talker in wl_talk) or (sender and sender in wl_send)):
                            continue
                        if (talker and talker in bl_talk) or (sender and sender in bl_send):
                            continue
                        if ts and ts < since:
                            continue
                        exists = None
                        if ts and talker and sender and content:
                            exists = db.execute(
                                select(Message.id).where(
                                    Message.chat_id == talker,
                                    Message.sender_id == sender,
                                    Message.timestamp == ts,
                                    Message.content_text == content,
                                )
                            ).scalar()
                        if exists:
                            continue
                        if talker:
                            chat = db.get(Chat, talker)
                            if not chat:
                                chat = Chat(id=talker, title=talkerName or talker, is_chatroom=isChatRoom)
                                db.add(chat)
                        if sender:
                            c = db.get(Contact, sender)
                            if not c:
                                c = Contact(id=sender, name=senderName)
                                db.add(c)
                        msg = Message(
                            chat_id=talker,
                            sender_id=sender,
                            sender_name=senderName,
                            talker_name=talkerName,
                            timestamp=ts,
                            direction="out" if isSelf else "in",
                            type=str(type_) if type_ is not None else None,
                            content_text=content,
                            media_url=media_url,
                            meta=meta_payload,
                        )
                        db.add(msg)
                        inserted += 1
                        if talker and ts:
                            chat = db.get(Chat, talker)
                            if chat and (chat.last_message_at is None or ts > chat.last_message_at):
                                chat.last_message_at = ts
                                db.add(chat)
                    _safe_commit(db)
                    if len(part) < 500:
                        break
                    offset += 500
                except Exception:
                    db.rollback()
                    break
        cur = cur + timedelta(days=1)

    if max_ts:
        _set_last_sync(db, max_ts)
        _safe_commit(db)

    return {
        "status": "ok",
        "fetched": total_fetched,
        "inserted": inserted,
        "since": since.isoformat(),
        "until": now.isoformat(),
        "talkers": len(talkers),
    }


def sync_full(db: Session, days: int = 30) -> Dict[str, Any]:
    client = ChatlogClient()
    now = datetime.now()
    start_date = (now - timedelta(days=max(1, days) - 1)).date()
    end_date = now.date()

    try:
        session_payload = client.get_sessions()
        talkers = ChatlogClient.extract_talker_ids(session_payload)
    except requests.RequestException:
        raise
    except Exception:
        talkers = []
    # Load filters once
    def _load_list(key: str) -> set[str]:
        row = db.get(SyncState, key)
        if not row or not row.value:
            return set()
        try:
            data = json.loads(row.value)
            if isinstance(data, list):
                return set(str(x) for x in data)
        except Exception:
            pass
        return set()
    bl_send = _load_list("blacklist_senders")
    bl_talk = _load_list("blacklist_talkers")
    wl_send = _load_list("whitelist_senders")
    wl_talk = _load_list("whitelist_talkers")
    has_wl = bool(wl_send or wl_talk)

    total_fetched = 0
    total_inserted = 0
    cur = start_date
    max_ts: Optional[datetime] = None
    unreachable = False
    while cur <= end_date:
        day = cur.isoformat()
        if talkers:
            # Preferred path: enumerate talkers and paginate each talker/day.
            for talker in talkers:
                offset = 0
                while True:
                    try:
                        raw = client.get_chatlog(time_range=day, talker=talker, limit=500, offset=offset)
                        part = _parse_messages(raw)
                        if not part:
                            break
                        total_fetched += len(part)
                        for m in part:
                            talkerName = m.get("talkerName")
                            sender = m.get("sender")
                            senderName = m.get("senderName")
                            isChatRoom = bool(m.get("isChatRoom"))
                            isSelf = bool(m.get("isSelf"))
                            type_ = m.get("type")
                            content = m.get("content") or m.get("text")
                            meta_payload, media_url = _build_meta_and_media(m)
                            time_str = m.get("time") or m.get("timestamp")
                            ts = None
                            if time_str:
                                try:
                                    ts = _to_local_naive(datetime.fromisoformat(time_str))
                                except Exception:
                                    ts = None
                            if ts and (max_ts is None or ts > max_ts):
                                max_ts = ts
                            if has_wl and not ((talker and talker in wl_talk) or (sender and sender in wl_send)):
                                continue
                            if (talker and talker in bl_talk) or (sender and sender in bl_send):
                                continue
                            if ts:
                                exists = db.execute(
                                    select(Message.id).where(
                                        Message.chat_id == talker,
                                        Message.sender_id == sender,
                                        Message.timestamp == ts,
                                        Message.content_text == content,
                                    )
                                ).scalar()
                                if exists:
                                    continue
                            chat = db.get(Chat, talker)
                            if not chat:
                                chat = Chat(id=talker, title=talkerName or talker, is_chatroom=isChatRoom)
                                db.add(chat)
                            if sender:
                                c = db.get(Contact, sender)
                                if not c:
                                    c = Contact(id=sender, name=senderName)
                                    db.add(c)
                            msg = Message(
                                chat_id=talker,
                                sender_id=sender,
                                sender_name=senderName,
                                talker_name=talkerName,
                                timestamp=ts,
                                direction="out" if isSelf else "in",
                                type=str(type_) if type_ is not None else None,
                                content_text=content,
                                media_url=media_url,
                                meta=meta_payload,
                            )
                            db.add(msg)
                            total_inserted += 1
                            if chat and ts and (chat.last_message_at is None or ts > chat.last_message_at):
                                chat.last_message_at = ts
                                db.add(chat)
                        _safe_commit(db)
                        if len(part) < 500:
                            break
                        offset += 500
                    except Exception:
                        db.rollback()
                        break
        else:
            # Fallback path: session list unavailable; try non-talker pagination for that day (if supported by chatlog build).
            offset = 0
            while True:
                try:
                    raw = client.get_chatlog(time_range=day, limit=500, offset=offset)
                    part = _parse_messages(raw)
                    if not part:
                        break
                    total_fetched += len(part)
                    for m in part:
                        talker = m.get("talker") or m.get("chat_id")
                        talkerName = m.get("talkerName")
                        sender = m.get("sender")
                        senderName = m.get("senderName")
                        isChatRoom = bool(m.get("isChatRoom"))
                        isSelf = bool(m.get("isSelf"))
                        type_ = m.get("type")
                        content = m.get("content") or m.get("text")
                        meta_payload, media_url = _build_meta_and_media(m)
                        time_str = m.get("time") or m.get("timestamp")
                        ts = None
                        if time_str:
                            try:
                                ts = _to_local_naive(datetime.fromisoformat(time_str))
                            except Exception:
                                ts = None
                        if ts and (max_ts is None or ts > max_ts):
                            max_ts = ts
                        if has_wl and not ((talker and talker in wl_talk) or (sender and sender in wl_send)):
                            continue
                        if (talker and talker in bl_talk) or (sender and sender in bl_send):
                            continue
                        if ts and talker and sender and content:
                            exists = db.execute(
                                select(Message.id).where(
                                    Message.chat_id == talker,
                                    Message.sender_id == sender,
                                    Message.timestamp == ts,
                                    Message.content_text == content,
                                )
                            ).scalar()
                            if exists:
                                continue
                        if talker:
                            chat = db.get(Chat, talker)
                            if not chat:
                                chat = Chat(id=talker, title=talkerName or talker, is_chatroom=isChatRoom)
                                db.add(chat)
                        if sender:
                            c = db.get(Contact, sender)
                            if not c:
                                c = Contact(id=sender, name=senderName)
                                db.add(c)
                        msg = Message(
                            chat_id=talker,
                            sender_id=sender,
                            sender_name=senderName,
                            talker_name=talkerName,
                            timestamp=ts,
                            direction="out" if isSelf else "in",
                            type=str(type_) if type_ is not None else None,
                            content_text=content,
                            media_url=media_url,
                            meta=meta_payload,
                        )
                        db.add(msg)
                        total_inserted += 1
                        if talker and ts:
                            chat = db.get(Chat, talker)
                            if chat and (chat.last_message_at is None or ts > chat.last_message_at):
                                chat.last_message_at = ts
                                db.add(chat)
                    _safe_commit(db)
                    if len(part) < 500:
                        break
                    offset += 500
                except Exception:
                    db.rollback()
                    # If chatlog is unreachable/hanging, don't keep retrying day-by-day (would block UI for minutes).
                    unreachable = True
                    break
            if unreachable:
                break
        cur = cur + timedelta(days=1)
        if unreachable:
            break

    if max_ts:
        # Persist as ISO (may include offset if source had it); downstream will normalize
        _set_last_sync(db, max_ts)
        _safe_commit(db)
    res: Dict[str, Any] = {
        "status": "ok",
        "fetched": total_fetched,
        "inserted": total_inserted,
        "from": start_date.isoformat(),
        "to": end_date.isoformat(),
        "talkers": len(talkers),
    }
    if unreachable and not talkers:
        res["reason"] = "chatlog_unreachable"
    return res


def sync_from_wx_cli(db: Session, days: int = 1) -> Dict[str, Any]:
    """Sync private/group WeChat messages from jackwener/wx-cli.

    wx-cli reads local WeChat data through its own CLI/daemon. It can be used on Windows or macOS
    after wx init succeeds; macOS may require extra process-debugging permission during init.
    Official-account and folded entries are intentionally skipped here so they do not pollute the
    WeChat message list; those should be routed to the 公众号 engine in a separate pass.
    """
    client = WxCliClient()
    now = datetime.now()
    requested_days = max(1, min(90, int(days or 1)))
    start_date = (now - timedelta(days=requested_days - 1)).date()
    end_date = now.date()
    filters = _load_wechat_id_filters(db)

    sessions = client.sessions(limit=settings.WX_CLI_SESSION_LIMIT)
    total_fetched = 0
    total_inserted = 0
    skipped_official = 0
    max_ts: datetime | None = None
    errors: list[dict[str, str]] = []

    for session in sessions:
        chat_type = str(session.get("chat_type") or "").strip()
        if chat_type in {"official_account", "folded"}:
            skipped_official += 1
            continue
        username = str(session.get("username") or session.get("chat") or session.get("name") or "").strip()
        display = str(session.get("display_name") or session.get("name") or session.get("chat") or username).strip()
        if not username and not display:
            continue
        chat_key = username or display
        offset = 0
        while True:
            try:
                payload = client.history(
                    display or chat_key,
                    since=start_date,
                    until=end_date,
                    limit=500,
                    offset=offset,
                )
            except Exception as exc:
                errors.append({"chat": display or chat_key, "error": str(exc)})
                break
            messages = payload.get("messages") or payload.get("results") or []
            if not isinstance(messages, list) or not messages:
                break
            total_fetched += len(messages)
            resolved_chat = str(payload.get("username") or username or chat_key).strip()
            resolved_display = str(payload.get("chat") or display or resolved_chat).strip()
            is_group = bool(payload.get("is_group")) or str(payload.get("chat_type") or chat_type) == "group"
            for item in messages:
                if not isinstance(item, dict):
                    continue
                ts = WxCliClient.parse_timestamp(item.get("timestamp") or item.get("time"))
                if ts and (max_ts is None or ts > max_ts):
                    max_ts = ts
                if ts and ts.date() < start_date:
                    continue
                content = item.get("content")
                if content is not None:
                    content = str(content)
                sender_id = str(item.get("sender_username") or "").strip()
                sender_name = str(
                    item.get("sender_group_nickname")
                    or item.get("sender_contact_display")
                    or item.get("sender")
                    or ""
                ).strip()
                if not sender_id:
                    sender_id = resolved_chat if not is_group else sender_name
                if not sender_name:
                    sender_name = sender_id
                if not _passes_wechat_filters(resolved_chat, sender_id, filters):
                    continue
                media_url = str(item.get("url") or "").strip() or None
                meta = {
                    "source": "wx_cli",
                    "chat_type": payload.get("chat_type") or chat_type,
                    "local_id": item.get("local_id"),
                    "raw": item,
                }
                if _insert_wechat_local_message(
                    db,
                    talker=resolved_chat,
                    talker_name=resolved_display,
                    sender=sender_id,
                    sender_name=sender_name,
                    ts=ts,
                    content=content,
                    msg_type=item.get("type"),
                    direction="in",
                    is_chatroom=is_group,
                    media_url=media_url,
                    meta=meta,
                ):
                    total_inserted += 1
            _safe_commit(db)
            if len(messages) < 500:
                break
            offset += 500

    if max_ts:
        _set_last_sync(db, max_ts)
        _safe_commit(db)
    return {
        "status": "ok" if not errors else "partial",
        "source": "wx_cli",
        "fetched": total_fetched,
        "inserted": total_inserted,
        "from": start_date.isoformat(),
        "to": end_date.isoformat(),
        "sessions": len(sessions),
        "skipped_official": skipped_official,
        "errors": errors[:20],
    }


def _normalize_chatlog_record(m: Dict[str, Any]) -> tuple[str | None, str | None, datetime | None, str]:
    """Return a comparison key (talker, sender, ts_local_naive, content_text)."""
    talker = m.get("talker") or m.get("chat_id")
    sender = m.get("sender") or m.get("sender_id")
    # time can be 'time' or 'timestamp' ISO string (with/without Z/offset)
    ts = None
    ts_str = m.get("time") or m.get("timestamp")
    if ts_str:
        try:
            ts = _to_local_naive(datetime.fromisoformat(ts_str))
        except Exception:
            ts = None
    content = m.get("content") or m.get("text") or ""
    return (talker or None, sender or None, ts, content or "")


def compare_with_chatlog(db: Session, *, days: int | None = None, date: str | None = None, fix: bool = False) -> Dict[str, Any]:
    """Compare messages in DB with chatlog for a date range and optionally repair.

    - If `date` (YYYY-MM-DD) is provided, compare that day only.
    - Else if `days` is provided, compare [now-days+1 .. now] inclusive.
    - If `fix=True`, insert missing_in_db items into DB.
    """
    if date:
        try:
            start_date = datetime.fromisoformat(date).date()
        except Exception as e:
            raise ValueError(f"invalid date: {date}")
        end_date = start_date
    else:
        d = max(1, int(days or 1))
        now = _to_local_naive(datetime.now()) or datetime.now()
        start_date = (now - timedelta(days=d - 1)).date()
        end_date = now.date()

    client = ChatlogClient()
    # Collect talkers once, but tolerate failures
    try:
        session_payload = client.get_sessions()
        talkers = ChatlogClient.extract_talker_ids(session_payload)
    except Exception:
        talkers = []

    summary: Dict[str, Any] = {"days": [], "totals": {"chatlog": 0, "db": 0, "missing_in_db": 0, "extra_in_db": 0}}
    details_sample: list[dict] = []

    cur = start_date
    while cur <= end_date:
        day = cur.isoformat()
        chatlog_keys: set[tuple[str | None, str | None, datetime | None, str]] = set()
        chatlog_records: list[Dict[str, Any]] = []

        if talkers:
            for t in talkers:
                offset = 0
                while True:
                    raw = None
                    try:
                        raw = client.get_chatlog(time_range=day, talker=t, limit=500, offset=offset)
                    except Exception:
                        break
                    part = _parse_messages(raw)
                    if not part:
                        break
                    for m in part:
                        key = _normalize_chatlog_record(m)
                        chatlog_keys.add(key)
                        chatlog_records.append(m)
                    if len(part) < 500:
                        break
                    offset += 500
        else:
            offset = 0
            while True:
                raw = None
                try:
                    raw = client.get_chatlog(time_range=day, limit=500, offset=offset)
                except Exception:
                    break
                part = _parse_messages(raw)
                if not part:
                    break
                for m in part:
                    key = _normalize_chatlog_record(m)
                    chatlog_keys.add(key)
                    chatlog_records.append(m)
                if len(part) < 500:
                    break
                offset += 500

        # DB keys in the same day window [day 00:00, day 23:59:59.999]
        start_dt = datetime.fromisoformat(day + "T00:00:00")
        end_dt = datetime.fromisoformat(day + "T23:59:59.999999")
        rows = db.execute(
            select(Message.chat_id, Message.sender_id, Message.timestamp, Message.content_text)
            .where(Message.timestamp >= start_dt, Message.timestamp <= end_dt)
        ).all()
        db_keys: set[tuple[str | None, str | None, datetime | None, str]] = set()
        for (chat_id, sender_id, ts, content_text) in rows:
            db_keys.add((chat_id, sender_id, _to_local_naive(ts), content_text or ""))

        missing = chatlog_keys - db_keys
        extra = db_keys - chatlog_keys

        # Sample up to 50 missing for UI display
        sample_count = 0
        if missing:
            # Fast lookup from key to raw chatlog record
            index = {}
            for m in chatlog_records:
                index[_normalize_chatlog_record(m)] = m
            for key in list(missing):
                m = index.get(key)
                if not m:
                    continue
                details_sample.append({
                    "day": day,
                    "chat_id": m.get("talker") or m.get("chat_id"),
                    "sender_id": m.get("sender") or m.get("sender_id"),
                    "timestamp": m.get("time") or m.get("timestamp"),
                    "content": m.get("content") or m.get("text") or "",
                })
                sample_count += 1
                if sample_count >= 50:
                    break

        # Optional repair: insert missing messages
        repaired = 0
        if fix and missing:
            index = { _normalize_chatlog_record(m): m for m in chatlog_records }
            created_chats: set[str] = set()
            created_contacts: set[str] = set()
            # Use engine-level transaction to bypass ORM pending flush
            from ..db import engine as _engine
            with _engine.begin() as conn:
                for key in missing:
                    m = index.get(key)
                    if not m:
                        continue
                    talker = m.get("talker") or m.get("chat_id")
                    if not talker:
                        continue
                    talkerName = m.get("talkerName")
                    sender = m.get("sender")
                    senderName = m.get("senderName")
                    isChatRoom = 1 if bool(m.get("isChatRoom")) else 0
                    isSelf = bool(m.get("isSelf"))
                    type_ = m.get("type")
                    content = m.get("content") or m.get("text") or ""
                    meta_payload, media_url = _build_meta_and_media(m)
                    ts = None
                    ts_str = m.get("time") or m.get("timestamp")
                    if ts_str:
                        try:
                            ts = _to_local_naive(datetime.fromisoformat(ts_str))
                        except Exception:
                            ts = None
                    if talker not in created_chats:
                        if not bool(conn.execute(text("SELECT 1 FROM chats WHERE id=:id"), {"id": talker}).first()):
                            conn.execute(
                                text("INSERT OR IGNORE INTO chats (id, title, type, is_chatroom, last_message_at) VALUES (:id, :title, NULL, :is_chatroom, NULL)"),
                                {"id": talker, "title": (talkerName or talker), "is_chatroom": isChatRoom},
                            )
                        created_chats.add(talker)
                    if sender and (sender not in created_contacts):
                        if not bool(conn.execute(text("SELECT 1 FROM contacts WHERE id=:id"), {"id": sender}).first()):
                            conn.execute(
                                text("INSERT OR IGNORE INTO contacts (id, name, alias, rating, labels, stats) VALUES (:id, :name, NULL, 50, NULL, NULL)"),
                                {"id": sender, "name": senderName},
                            )
                        created_contacts.add(sender)
                    if ts is not None:
                        conn.execute(
                            text("UPDATE chats SET last_message_at = :ts WHERE id = :id AND (last_message_at IS NULL OR last_message_at < :ts)"),
                            {"id": talker, "ts": ts},
                        )
                    conn.execute(
                        text(
                            """
                            INSERT INTO messages (
                                chat_id, sender_id, sender_name, talker_name,
                                timestamp, direction, type, content_text,
                                media_url, meta, tags, derived,
                                importance_score, upvotes, downvotes, ai_suggestions, send_status
                            ) VALUES (
                                :chat_id, :sender_id, :sender_name, :talker_name,
                                :timestamp, :direction, :type, :content_text,
                                :media_url, :meta, NULL, NULL,
                                50, 0, 0, NULL, NULL
                            )
                            """
                        ),
                        {
                            "chat_id": talker,
                            "sender_id": sender,
                            "sender_name": senderName,
                            "talker_name": talkerName,
                            "timestamp": ts,
                            "direction": ("out" if isSelf else "in"),
                            "type": (str(type_) if type_ is not None else None),
                            "content_text": content,
                            "media_url": media_url,
                            "meta": json.dumps(meta_payload, ensure_ascii=False),
                        },
                    )
                    repaired += 1

        day_summary = {
            "day": day,
            "chatlog": len(chatlog_keys),
            "db": len(db_keys),
            "missing_in_db": len(missing),
            "extra_in_db": len(extra),
            "repaired": repaired,
        }
        summary["days"].append(day_summary)
        summary["totals"]["chatlog"] += len(chatlog_keys)
        summary["totals"]["db"] += len(db_keys)
        summary["totals"]["missing_in_db"] += len(missing)
        summary["totals"]["extra_in_db"] += len(extra)

        cur = cur + timedelta(days=1)

    summary["sample_missing"] = details_sample
    return summary
