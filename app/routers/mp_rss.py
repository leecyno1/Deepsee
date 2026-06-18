from __future__ import annotations

import json

from fastapi import APIRouter, Depends, HTTPException, Query
from sqlalchemy.orm import Session

from ..db import SessionLocal
from ..models import Message, SyncState

from ..services.mp_rss_store import DEFAULT_MP_UPSTREAM_URL, get_mp_article, list_mp_articles


router = APIRouter(prefix="/api/mp", tags=["mp-rss"])
def _extract_appmsg_from_content(content_text):
    """Parse WeChat appmsg XML to get title/desc/url."""
    text = str(content_text or "").strip()
    if not text or "<appmsg" not in text.lower():
        return {}
    try:
        import xml.etree.ElementTree as ET
        root = ET.fromstring(text)
    except Exception:
        return {}

    def find_text(*paths):
        for p in paths:
            try:
                value = root.findtext(p)
            except Exception:
                value = None
            value = str(value or "").strip()
            if value:
                return value
        return ""

    return {
        "title": find_text(".//appmsg/title", ".//title"),
        "desc": find_text(".//appmsg/des", ".//appmsg/description", ".//des"),
        "url": find_text(".//appmsg/url", ".//url"),
        "sourceusername": find_text(".//appmsg/sourceusername", ".//sourceusername"),
        "sourcedisplayname": find_text(".//appmsg/sourcedisplayname", ".//sourcedisplayname"),
        "thumburl": find_text(".//appmsg/thumburl", ".//thumburl"),
    }


def _clean_local_summary(*values: object, max_len: int = 180) -> str:
    import re
    for value in values:
        text = str(value or "").strip()
        if not text:
            continue
        text = re.sub(r"<[^>]+>", " ", text)
        text = re.sub(r"&[a-zA-Z#0-9]+;", " ", text)
        text = re.sub(r"\s+", " ", text).strip()
        if text:
            return text[:max_len]
    return ""


def _message_summary(message: Message) -> str:
    """Return the best available generated summary for a Message row."""
    derived = message.derived if isinstance(message.derived, dict) else {}
    meta = message.meta if isinstance(message.meta, dict) else {}
    meta_derived = meta.get("derived") if isinstance(meta.get("derived"), dict) else {}
    return _clean_local_summary(
        derived.get("summary"),
        derived.get("summary_full"),
        derived.get("key_info"),
        meta_derived.get("summary"),
        meta_derived.get("summary_full"),
        meta_derived.get("key_info"),
    )


def _list_local_wechat_mp_articles(db, *, limit=100, offset=0, q=None):
    """Query local wechat gateway gh_* messages as 公众号 articles."""
    from sqlalchemy import or_, and_

    query = db.query(Message).filter(
        Message.meta.isnot(None),
        or_(
            Message.sender_id.like("gh_%"),
            Message.chat_id.like("gh_%"),
        ),
        or_(Message.direction == "in", Message.direction == None, Message.direction == ""),
    )

    if q:
        query = query.filter(
            or_(
                Message.content_text.like(f"%{q}%"),
                Message.sender_name.like(f"%{q}%"),
                Message.talker_name.like(f"%{q}%"),
            )
        )

    rows = query.order_by(Message.timestamp.desc()).limit(limit).offset(offset).all()
    items = []
    for m in rows:
        appmsg = _extract_appmsg_from_content(m.content_text)
        meta = m.meta if isinstance(m.meta, dict) else {}
        existing = meta.get("contents") if isinstance(meta.get("contents"), dict) else {}
        derived = meta.get("derived") if isinstance(meta.get("derived"), dict) else {}
        title = appmsg.get("title") or existing.get("title") or str(m.content_text or "")[:120]
        desc = _clean_local_summary(
            appmsg.get("desc"),
            existing.get("desc"),
            derived.get("key_info"),
            derived.get("summary"),
            _message_summary(m),
            m.content_text,
        )
        url = appmsg.get("url") or existing.get("url") or ""
        channel_name = str(m.sender_name or m.talker_name or appmsg.get("sourcedisplayname") or "").strip()
        heat_score = 0
        if m.timestamp:
            try:
                from datetime import datetime
                age_hours = max(0.0, (datetime.now() - m.timestamp).total_seconds() / 3600)
                heat_score = max(1, int(1000 / (1 + age_hours / 6)))
            except Exception:
                heat_score = 1

        items.append({
            "id": f"local-gh-{m.id}",
            "channel_name": channel_name,
            "publish_time": m.timestamp.isoformat() if m.timestamp else "",
            "title": title,
            "summary": desc,
            "url": url,
            "read_count": 0,
            "share_count": 0,
            "like_count": 0,
            "recommend_count": 0,
            "heat": heat_score,
            "source": "wechat_gateway_local",
            "message_id": m.id,
            "content": str(m.content_text or ""),
        })

    return items


def get_db():
    db = SessionLocal()
    try:
        yield db
    finally:
        db.close()


def _get_mp_config(db: Session) -> dict:
    row = db.get(SyncState, "mp_config")
    if not row or not row.value:
        return {"upstream_base_url": DEFAULT_MP_UPSTREAM_URL}
    try:
        data = json.loads(row.value)
        if not isinstance(data, dict):
            data = {}
        if not str(data.get("upstream_base_url") or "").strip():
            data["upstream_base_url"] = DEFAULT_MP_UPSTREAM_URL
        return data
    except Exception:
        return {"upstream_base_url": DEFAULT_MP_UPSTREAM_URL}


@router.get("/articles")
def api_list_articles(
    limit: int = Query(100, ge=1, le=300),
    offset: int = Query(0, ge=0),
    q: str | None = None,
    db: Session = Depends(get_db),
):
    cfg = _get_mp_config(db)
    db_path = str(cfg.get("db_path") or "").strip() or None
    upstream_base_url = str(cfg.get("upstream_base_url") or cfg.get("base_url") or "").strip() or None
    upstream_auth_token = str(cfg.get("upstream_auth_token") or cfg.get("auth_token") or "").strip() or None
    result = list_mp_articles(
        limit=limit,
        offset=offset,
        q=q,
        db_path=db_path,
        upstream_base_url=upstream_base_url,
        upstream_auth_token=upstream_auth_token,
    )
    local_items = _list_local_wechat_mp_articles(db, limit=limit, offset=0, q=q)
    if local_items:
        merged = [*local_items, *list(result.get("items") or [])]
        seen: set[str] = set()
        deduped = []
        for item in merged:
            key = str(item.get("url") or item.get("id") or "").strip()
            if not key or key in seen:
                continue
            seen.add(key)
            deduped.append(item)
        deduped.sort(key=lambda it: str(it.get("publish_time") or ""), reverse=True)
        result["items"] = deduped[int(offset): int(offset) + int(limit)]
        result["total"] = max(int(result.get("total") or 0), len(deduped))
        source = result.get("source") if isinstance(result.get("source"), dict) else {}
        source["local_wechat_mp"] = len(local_items)
        result["source"] = source
    return result


@router.get("/articles/{article_id:path}")
def api_get_article(article_id: str, include_content: bool = False, db: Session = Depends(get_db)):
    cfg = _get_mp_config(db)
    db_path = str(cfg.get("db_path") or "").strip() or None
    upstream_base_url = str(cfg.get("upstream_base_url") or cfg.get("base_url") or "").strip() or None
    upstream_auth_token = str(cfg.get("upstream_auth_token") or cfg.get("auth_token") or "").strip() or None
    if article_id.startswith("local-gh-"):
        try:
            mid = int(article_id.replace("local-gh-", "", 1))
        except Exception:
            mid = 0
        msg = db.get(Message, mid) if mid else None
        if not msg:
            raise HTTPException(404, "article not found")
        meta = msg.meta if isinstance(msg.meta, dict) else {}
        appmsg = _extract_appmsg_from_content(msg.content_text)
        existing = meta.get("contents") if isinstance(meta.get("contents"), dict) else {}
        title = appmsg.get("title") or existing.get("title") or str(msg.content_text or "")[:120]
        summary = _clean_local_summary(appmsg.get("desc"), existing.get("desc"), _message_summary(msg), msg.content_text)
        return {
            "id": article_id,
            "channel_name": str(msg.sender_name or msg.talker_name or "").strip(),
            "publish_time": msg.timestamp.isoformat() if msg.timestamp else "",
            "title": title,
            "summary": summary,
            "content": str(msg.content_text or "") if include_content else summary,
            "url": appmsg.get("url") or existing.get("url") or "",
            "source": "wechat_gateway_local",
        }
    try:
        item = get_mp_article(
            article_id,
            include_content=include_content,
            db_path=db_path,
            upstream_base_url=upstream_base_url,
            upstream_auth_token=upstream_auth_token,
        )
    except FileNotFoundError as exc:
        raise HTTPException(400, str(exc))
    if not item:
        raise HTTPException(404, "article not found")
    return item
