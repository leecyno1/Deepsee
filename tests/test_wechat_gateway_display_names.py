from datetime import datetime

import pytest
from sqlalchemy import create_engine
from sqlalchemy.orm import sessionmaker

from app.db import Base
from app.models import Chat, Contact, Message
from app.services.wechat_gateway import ingest_callback_event, record_outbound_message


@pytest.fixture
def SessionLocal(tmp_path):
    engine = create_engine(f"sqlite:///{tmp_path / 'names.db'}")
    Base.metadata.create_all(engine)
    return sessionmaker(bind=engine)


def test_ingest_callback_event_uses_contact_and_chat_display_names(SessionLocal):
    db = SessionLocal()
    try:
        existing = ingest_callback_event(db, {
            "TypeName": "AddMsg",
            "Appid": "wx_app_test",
            "Wxid": "leecyno1",
            "Data": {
                "NewMsgId": "900001",
                "MsgId": "800001",
                "FromUserName": "room_1@chatroom",
                "ToUserName": "leecyno1",
                "Content": "wxid_sender_1:\n你好",
                "MsgType": 1,
                "CreateTime": int(datetime(2026, 5, 9, 12, 0, 0).timestamp()),
            },
        })
        if existing.get("message_id"):
            db.query(Message).filter(Message.id == int(existing["message_id"])).delete()
        db.query(Contact).filter(Contact.id == "wxid_sender_1").delete()
        db.query(Chat).filter(Chat.id == "room_1@chatroom").delete()
        db.commit()

        db.add(Contact(id="wxid_sender_1", name="张三", alias="客户备注"))
        db.add(Chat(id="room_1@chatroom", title="柠檬工作室", is_chatroom=True))
        db.commit()

        payload = {
            "TypeName": "AddMsg",
            "Appid": "wx_app_test",
            "Wxid": "leecyno1",
            "Data": {
                "NewMsgId": "900001",
                "MsgId": "800001",
                "FromUserName": "room_1@chatroom",
                "ToUserName": "leecyno1",
                "Content": "wxid_sender_1:\n你好",
                "MsgType": 1,
                "CreateTime": int(datetime(2026, 5, 9, 12, 0, 0).timestamp()),
            },
        }

        result = ingest_callback_event(db, payload)
        assert result["stored"] is True

        message = db.get(Message, result["message_id"])
        assert message is not None
        assert message.sender_name == "客户备注"
        assert message.talker_name == "柠檬工作室"
    finally:
        db.query(Message).filter(Message.chat_id == "room_1@chatroom").delete()
        db.query(Contact).filter(Contact.id == "wxid_sender_1").delete()
        db.query(Chat).filter(Chat.id == "room_1@chatroom").delete()
        db.commit()
        db.close()


def test_record_outbound_message_uses_chat_display_name(SessionLocal):
    db = SessionLocal()
    try:
        db.query(Message).filter(Message.chat_id == "wxid_target_1").delete()
        db.query(Chat).filter(Chat.id == "wxid_target_1").delete()
        db.commit()

        db.add(Chat(id="wxid_target_1", title="李四", is_chatroom=False))
        db.commit()

        message = record_outbound_message(
            db,
            target="wxid_target_1",
            text="测试发送",
            provider_result={"data": {"msgId": 1, "newMsgId": 2}},
        )

        assert message.talker_name == "李四"
    finally:
        db.query(Message).filter(Message.chat_id == "wxid_target_1").delete()
        db.query(Chat).filter(Chat.id == "wxid_target_1").delete()
        db.commit()
        db.close()
