from __future__ import annotations

import os
import sys
from pathlib import Path

from sqlalchemy import create_engine
from sqlalchemy.orm import sessionmaker

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)

from app.db import Base
from app.models import Chat, Contact, Message, SyncState
from app.services import send_dispatcher
from app.services.wechat_gateway import ingest_callback_event, load_config, save_config


def _session_factory(tmp_path: Path):
    db_path = tmp_path / "wechat-gateway-test.db"
    engine = create_engine(f"sqlite:///{db_path}", future=True)
    TestingSession = sessionmaker(bind=engine, autocommit=False, autoflush=False, expire_on_commit=False)
    Base.metadata.create_all(
        engine,
        tables=[
            Chat.__table__,
            Contact.__table__,
            Message.__table__,
            SyncState.__table__,
        ],
    )
    return TestingSession


def _sample_callback() -> dict:
    return {
        "TypeName": "AddMsg",
        "Appid": "wx_app_test",
        "Wxid": "self_wxid",
        "Data": {
            "MsgId": 101,
            "NewMsgId": 202,
            "MsgType": 1,
            "CreateTime": 1778036763,
            "PushContent": "群消息预览",
            "FromUserName": {"string": "room_1@chatroom"},
            "ToUserName": {"string": "self_wxid"},
            "Content": {"string": "wxid_sender:\n你好，网关"},
            "MsgSource": "<msgsource><membercount>3</membercount></msgsource>",
        },
    }


def test_wechat_gateway_config_roundtrip(tmp_path):
    Session = _session_factory(tmp_path)
    db = Session()
    try:
        saved = save_config(
            db,
            {
                "enabled": True,
                "outbound_enabled": True,
                "block_chat_ids": ["filehelper", "filehelper"],
                "rate_limit_per_chat_per_minute": 999,
            },
        )
        loaded = load_config(db)

        assert saved["enabled"] is True
        assert loaded["enabled"] is True
        assert loaded["block_chat_ids"] == ["filehelper"]
        assert loaded["rate_limit_per_chat_per_minute"] == 999
        assert loaded["base_url"].startswith("http://api.wechatapi.net/")
    finally:
        db.close()


def test_ingest_callback_event_ack_and_dedupe(tmp_path):
    Session = _session_factory(tmp_path)
    db = Session()
    try:
        payload = _sample_callback()

        first = ingest_callback_event(db, payload)
        assert first["ok"] is True
        assert first["duplicate"] is False
        assert first["stored"] is True
        assert first["message_id"]
        assert db.query(Message).count() == 1
        assert db.query(Chat).count() == 1
        assert db.query(Contact).count() == 1

        message = db.query(Message).one()
        assert message.chat_id == "room_1@chatroom"
        assert message.sender_id == "wxid_sender"
        assert message.direction == "in"
        assert message.type == "text"
        assert message.content_text == "你好，网关"
        assert (message.meta or {}).get("source") == "wechat_gateway"
        assert (message.meta or {}).get("pipeline", {}).get("action") == "allow"

        outbound_payload = {
            "TypeName": "AddMsg",
            "Appid": "wx_app_test",
            "Wxid": "self_wxid",
            "Data": {
                "MsgId": 303,
                "NewMsgId": 404,
                "MsgType": 1,
                "CreateTime": 1778036770,
                "FromUserName": {"string": "self_wxid"},
                "ToUserName": {"string": "wxid_friend"},
                "Content": {"string": "这是人工回复"},
                "MsgSource": "<msgsource></msgsource>",
            },
        }
        outbound_result = ingest_callback_event(db, outbound_payload)
        assert outbound_result["ok"] is True
        assert outbound_result["stored"] is True
        outbound_message = db.get(Message, int(outbound_result["message_id"]))
        assert outbound_message is not None
        assert outbound_message.direction == "out"
        assert (outbound_message.meta or {}).get("manual") is True
        assert (outbound_message.meta or {}).get("human_manual") is True

        second = ingest_callback_event(db, payload)
        assert second["ok"] is True
        assert second["duplicate"] is True
        assert db.query(Message).count() == 2
    finally:
        db.close()


def test_wechat_gateway_rules_only_apply_to_wechat_provider(tmp_path, monkeypatch):
    Session = _session_factory(tmp_path)
    db = Session()
    try:
        save_config(
            db,
            {
                "enabled": True,
                "outbound_enabled": True,
                "block_chat_ids_enabled": True,
                "block_chat_ids": ["filehelper"],
                "token": "***",
                "app_id": "wx_app_test",
            },
        )
    finally:
        db.close()

    monkeypatch.setattr(send_dispatcher, "SessionLocal", Session, raising=False)

    class _DummyWechatApiClient:
        def __init__(self, *args, **kwargs):
            self.calls = []

        def configured(self):
            return True

        def send_text(self, to_wxid: str, text: str):
            self.calls.append((to_wxid, text))
            return {"ret": 200, "data": {"newMsgId": 9001, "msgId": 1, "toWxid": to_wxid, "type": 1}}

    class _DummyPadClient:
        def configured(self):
            return True

        def send_text(self, to_user: str, text: str):
            return {"status": "ok", "target": to_user, "text": text}

    monkeypatch.setattr(send_dispatcher, "WechatApiClient", _DummyWechatApiClient, raising=False)
    monkeypatch.setattr(send_dispatcher, "WeChatPadClient", _DummyPadClient)

    blocked = send_dispatcher.dispatch_send_item(
        {
            "target": "filehelper",
            "text": "hello blocked",
            "provider_override": "wechatapi_gateway",
        }
    )
    assert blocked["ok"] is False
    assert blocked["provider"] == "wechatapi_gateway"
    assert blocked["blocked"] is True

    other_provider = send_dispatcher.dispatch_send_item(
        {
            "target": "filehelper",
            "text": "hello pad",
            "provider_override": "wechatpad_direct",
        }
    )
    assert other_provider["ok"] is True
    assert other_provider["provider"] == "wechatpad_direct"


def test_wechat_gateway_send_logs_outbound_message(tmp_path, monkeypatch):
    Session = _session_factory(tmp_path)
    db = Session()
    try:
        save_config(
            db,
            {
                "enabled": True,
                "outbound_enabled": True,
                "token": "token-x",
                "app_id": "wx_app_test",
            },
        )
    finally:
        db.close()

    monkeypatch.setattr(send_dispatcher, "SessionLocal", Session, raising=False)

    class _DummyWechatApiClient:
        def configured(self):
            return True

        def send_text(self, to_wxid: str, text: str):
            return {
                "ret": 200,
                "msg": "操作成功",
                "data": {
                    "toWxid": to_wxid,
                    "msgId": 123,
                    "newMsgId": 456,
                    "type": 1,
                },
            }

    monkeypatch.setattr(send_dispatcher, "WechatApiClient", _DummyWechatApiClient, raising=False)

    result = send_dispatcher.dispatch_send_item(
        {
            "target": "wxid_friend",
            "text": "你好，发送链路",
            "provider_override": "wechatapi_gateway",
        }
    )

    assert result["ok"] is True
    assert result["provider"] == "wechatapi_gateway"

    verify = Session()
    try:
        rows = verify.query(Message).order_by(Message.id.asc()).all()
        assert len(rows) == 1
        msg = rows[0]
        assert msg.chat_id == "wxid_friend"
        assert msg.direction == "out"
        assert msg.content_text == "你好，发送链路"
        assert (msg.meta or {}).get("source") == "wechat_gateway"
        assert (msg.meta or {}).get("external_new_msg_id") == 456
    finally:
        verify.close()
