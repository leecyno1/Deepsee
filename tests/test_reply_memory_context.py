import json
from datetime import datetime, timedelta

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient
from sqlalchemy import create_engine
from sqlalchemy.orm import Session

from app.db import Base
from app.models import Message
from app.services import ai_execution, llm_client, reply_generation, wechat_gateway
from app.services.reply_memory import build_reply_memory, reset_memory


@pytest.fixture
def db(tmp_path):
    engine = create_engine(f'sqlite:///{tmp_path}/context.db')
    Base.metadata.create_all(engine)
    with Session(engine) as session:
        yield session
    engine.dispose()


def add(db, text, *, chat='alice', direction='in', when=None, sender='alice'):
    row = Message(chat_id=chat, sender_id=sender, content_text=text, direction=direction,
                  timestamp=when or datetime.now())
    db.add(row)
    db.flush()
    return row


def test_memory_recovers_backfilled_history_and_ignores_legacy_caps(db):
    now = datetime.now()
    current = add(db, '上次说什么', when=now)
    old = now - timedelta(days=10)
    add(db, '上次说什么', when=old)  # Repeated real text is not a duplicate by itself.
    add(db, '讨论了研究计划', direction='out', when=old+timedelta(minutes=1))
    add(db, 'other chat', chat='bob', when=old)
    add(db, 'future', when=now+timedelta(seconds=1))
    turns, info = build_reply_memory(db, conf={}, chat_id='alice', message_id=current.id,
        current_text='上次说什么', now=now, subsession={'id': 'old', 'history_max_messages': 0,
                                                      'history_max_tokens': 1})
    assert [t['content'] for t in turns] == ['上次说什么', '讨论了研究计划']
    assert info['messages'] == 2 and info['inbound'] == info['outbound'] == 1
    assert info['oldest_at'] == old.isoformat()
    turns, _ = build_reply_memory(db, conf={'reply_memory': {'max_age_hours': 72}},
                                 chat_id='alice', message_id=current.id)
    assert turns == []  # Explicit shorter windows still take effect.


def test_history_reaches_actual_agent_adapter_with_current_message_separated(db, monkeypatch):
    now = datetime.now()
    add(db, '下次讨论财报', when=now-timedelta(days=7))
    add(db, '好，下周讨论', direction='out', when=now-timedelta(days=7)+timedelta(seconds=1))
    current = add(db, '上次我们聊了什么', when=now)
    conf = {'ai_execution': {'mode': 'agent', 'provider': 'qoder', 'light_model': 'test-model',
                            'light_backend': 'agent'},
            'tool_prompts': {'reply_da': {'system': '简洁回复', 'user': '{{message_text}}'}}}
    monkeypatch.setattr(reply_generation, 'load_ai_config', lambda: conf)
    monkeypatch.setattr(llm_client, 'load_ai_config', lambda: conf)
    captured = []
    monkeypatch.setattr(ai_execution, '_run_cli',
                        lambda cfg, model, prompt, **kw: captured.append(json.loads(prompt)) or '上次约好讨论财报。')
    wechat_gateway.save_trigger_rules(db, {'prefixes': [], 'at_mention_enabled': False})
    result = reply_generation.generate_local_reply(db, {'message_id': current.id,
        'message_text': current.content_text, 'central_control': True})
    assert result['status'] == 'ok'
    assert result['execution']['memory']['messages'] == 2
    context = json.loads(captured[0][-2]['content'])
    assert [record['发言方'] for record in context['记录']] == ['对方', '我方']
    assert [record['内容'] for record in context['记录']] == ['下次讨论财报', '好，下周讨论']
    assert context['记录'][0]['时间'] == (now-timedelta(days=7)).isoformat()
    assert captured[0][-1]['content'] == '【当前消息 · 仅回复这一条】\n上次我们聊了什么'
    assert '不要声称未检索或没有历史' in captured[0][1]['content']


def test_memory_inspection_is_read_only_and_respects_reset(db, monkeypatch):
    from app.routers import wechat_gateway as routes
    monkeypatch.setattr(llm_client, 'load_ai_config', lambda: {})
    app = FastAPI()
    app.include_router(routes.router)
    app.dependency_overrides[routes.get_db] = lambda: db
    add(db, 'prior')
    db.commit()
    with TestClient(app) as client:
        response = client.get('/api/wechat-gateway/memory/inspect', params={'chat_id': 'alice'})
        assert response.status_code == 200
        assert response.json()['history'][0]['speaker'] == '对方'
        assert response.json()['history'][0]['text'] == 'prior'
        reset_memory(db, 'alice')
        assert client.get('/api/wechat-gateway/memory/inspect', params={'chat_id': 'alice'}).json()['history'] == []
        assert client.get('/api/wechat-gateway/memory/inspect', params={'chat_id': 'group@chatroom'}).status_code == 422
    assert db.query(Message).count() == 1


@pytest.mark.parametrize('text,reason', [
    ('😀', 'low_info_emoji'), ('[捂脸][偷笑]', 'low_info_emoji'),
    ('👍🏽', 'low_info_emoji'), ('👨‍👩‍👧‍👦', 'low_info_emoji'),
    ('哈哈哈哈哈哈', 'low_info_repetition'), ('哈哈哈哈！！', 'low_info_repetition'),
    ('hahahaha', 'low_info_repetition'), ('ai 哈哈哈哈', 'low_info_repetition'),
])
def test_obvious_low_information_messages_are_filtered(db, text, reason):
    result = wechat_gateway.evaluate_auto_reply_rules(db, chat_id='alice', sender_id='alice',
        text=text, is_group=False, rules_override={'prefixes': ['ai']})
    assert result['reason'] == reason and not result['allowed']


@pytest.mark.parametrize('text', ['好', '谢谢', '不是', '？', '哈哈？', '😀？', '哈哈，明天见',
                                  '😂帮我看下报告', '[PDF]', '哈哈', 'https://example.com',
                                  '123', '重要重要重要重要', '￥100', '++'])
def test_conservative_filter_preserves_meaningful_short_messages(db, text):
    result = wechat_gateway.evaluate_auto_reply_rules(db, chat_id='alice', sender_id='alice',
        text=text, is_group=False, rules_override={'prefixes': [], 'at_mention_enabled': False})
    assert result['allowed']


def test_low_information_rules_save_adjust_and_simulate(db):
    from app.routers.wechat_gateway import simulate_robot
    wechat_gateway.save_trigger_rules(db, {'prefixes': [], 'low_info_min_repeats': 6,
        'low_info_emoji_names': ['自定义笑'], 'low_info_allow_texts': ['👍']})
    for text in ['哈哈哈哈', '👍']:
        assert simulate_robot({'text': text}, db)['allowed']
    assert simulate_robot({'text': '[自定义笑]'}, db)['reason'] == 'low_info_emoji'
    assert simulate_robot({'text': '哈哈哈哈哈哈'}, db)['reason'] == 'low_info_repetition'
    assert simulate_robot({'text': '哈哈哈哈哈哈', 'rules': {'low_info_enabled': False}}, db)['allowed']
    assert wechat_gateway.load_trigger_rules(db)['low_info_enabled'] is True
