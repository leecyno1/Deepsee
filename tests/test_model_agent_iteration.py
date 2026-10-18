import json
from datetime import datetime, timedelta
from types import SimpleNamespace

import pytest
from sqlalchemy import create_engine
from sqlalchemy.orm import Session

from app.db import Base
from app.models import Chat, Message, SyncState, WechatSubsession, WechatSubsessionTurn
from app.services import ai_execution, model_catalog, social_sources
from app.services.reply_memory import build_reply_memory, reset_memory


@pytest.fixture
def db(tmp_path):
    engine = create_engine(f'sqlite:///{tmp_path}/memory.db')
    Base.metadata.create_all(engine, tables=[x.__table__ for x in
        (Chat, Message, SyncState, WechatSubsession, WechatSubsessionTurn)])
    with Session(engine) as session:
        yield session
    engine.dispose()


def message(db, text, *, chat='alice', sender='alice', direction='in', timestamp=None, meta=None):
    row = Message(chat_id=chat, sender_id=sender, direction=direction,
                  timestamp=timestamp or datetime.now(), content_text=text, meta=meta)
    db.add(row)
    db.flush()
    return row


def test_memory_is_latest_chronological_window(db):
    for index in range(60):
        message(db, f'turn {index}')
    message(db, 'other chat', chat='bob')
    history, info = build_reply_memory(db, conf={}, chat_id='alice')
    assert len(history) == 50
    assert history[0]['content'] == 'turn 10'
    assert history[-1]['content'] == 'turn 59'
    assert info['messages'] == 50


def test_memory_excludes_future_old_current_and_respects_reset(db):
    now = datetime.now()
    message(db, 'expired', timestamp=now-timedelta(days=31))
    message(db, 'future', timestamp=now+timedelta(days=1))
    message(db, 'valid', timestamp=now-timedelta(minutes=1))
    current = message(db, 'current', timestamp=now)
    history, _ = build_reply_memory(db, conf={}, chat_id='alice', message_id=current.id, now=now)
    assert [m['content'] for m in history] == ['valid']
    reset_memory(db, 'alice')
    message(db, 'after reset')
    history, _ = build_reply_memory(db, conf={}, chat_id='alice')
    assert [m['content'] for m in history] == ['after reset']


def test_group_memory_keeps_only_sender_and_their_replies(db):
    room = 'group@chatroom'
    a = message(db, 'alice question', chat=room)
    b = message(db, 'bob question', chat=room, sender='bob')
    message(db, 'answer alice', chat=room, direction='out', meta={'auto_reply': {'trigger_message_id': a.id}})
    message(db, 'answer bob', chat=room, direction='out', meta={'auto_reply': {'trigger_message_id': b.id}})
    for index in range(310):
        message(db, f'busy group noise {index}', chat=room, sender='bob')
    history, info = build_reply_memory(db, conf={}, chat_id=room, sender_id='alice')
    assert [m['content'] for m in history] == ['alice question', 'answer alice']
    assert info['scope'] == 'chat_and_sender'


def test_memory_budget_and_disable(db):
    message(db, '中'*1000)
    history, info = build_reply_memory(db, conf={'reply_memory': {'max_tokens': 256}}, chat_id='alice')
    assert len(history[0]['content']) == info['estimated_tokens'] == 256
    history, _ = build_reply_memory(db, conf={'reply_memory': {'enabled': False}}, chat_id='alice')
    assert not history


def test_model_catalog_never_reuses_saved_key_for_changed_host(monkeypatch):
    calls = []
    def get(url, **kw):
        calls.append((url, kw))
        return SimpleNamespace(status_code=200, raise_for_status=lambda: None,
                               json=lambda: {'data': [{'id': 'model-b'}, {'id': 'model-a'}]})
    monkeypatch.setattr(model_catalog.requests, 'get', get)
    conf = {'api_url': 'https://original.test/v1', 'api_key': 'saved-test-secret'}
    assert model_catalog.list_provider_models(model_catalog.CatalogRequest(api_url='https://new.test/v1'), conf) == ['model-a', 'model-b']
    assert calls[-1][1]['headers'] == {}
    model_catalog.list_provider_models(model_catalog.CatalogRequest(), conf)
    assert calls[-1][1]['headers']['Authorization'] == 'Bearer saved-test-secret'
    assert calls[-1][1]['allow_redirects'] is False


def test_qoder_catalog_and_command(monkeypatch, tmp_path):
    monkeypatch.setattr(model_catalog, 'find_cli', lambda _: '/test/qodercli')
    monkeypatch.setattr(model_catalog.subprocess, 'run', lambda *a, **k: SimpleNamespace(returncode=0, stdout='MiniMax-M3\nAuto\n'))
    assert model_catalog.list_provider_models(model_catalog.CatalogRequest(source='agent', provider='qoder'), {}) == ['Auto', 'MiniMax-M3']
    monkeypatch.setattr(ai_execution, 'find_cli', lambda _: '/test/qodercli')
    command = ai_execution._command(ai_execution.AIExecutionConfig(provider='qoder'), 'MiniMax-M3', tmp_path/'out')
    assert command[command.index('--tools')+1] == ''
    assert '--no-session-persistence' in command
    assert command[command.index('--model')+1] == 'MiniMax-M3'


def test_keyless_local_model_and_bounded_probe(monkeypatch):
    from app.routers import ai
    assert ai_execution.ai_available({'api_url': 'http://127.0.0.1:11434/v1'})
    assert not ai_execution.ai_available({'api_url': 'https://provider.test/v1'})
    captured = []
    def post(url, **kw):
        captured.append(kw)
        return SimpleNamespace(raise_for_status=lambda: None,
            json=lambda: {'choices': [{'message': {'content': '连接成功'}}]})
    monkeypatch.setattr(ai.requests, 'post', post)
    result = ai._test_router_channel_connectivity(
        {'id': 'local', 'model': 'local-model', 'api_url': 'http://127.0.0.1:11434/v1'}, 'tool', {})
    assert result['ok']
    assert captured[0]['timeout'] == (5, 20)
    assert 'Authorization' not in captured[0]['headers']
    assert 'response_format' not in captured[0]['json']
    monkeypatch.setattr(ai, 'load_ai_config', lambda: {
        'api_url': 'http://127.0.0.1:11434/v1', 'tool_model': 'local-model'})
    assert ai.test_ai_execution({'tier': 'light'})['ok']


def test_agent_preserves_task_prompt_history_and_merges_onepage_into_advanced(monkeypatch):
    conf = {'ai_execution': {'mode': 'agent', 'provider': 'qoder', 'advanced_model': 'report-model',
                            'onepage_model': 'obsolete-model', 'onepage_backend': 'models', 'light_backend': 'models'}}
    messages = [{'role': 'system', 'content': 'reply in JSON array'},
                {'role': 'user', 'content': 'prior question'},
                {'role': 'assistant', 'content': 'prior answer'},
                {'role': 'user', 'content': 'current task'}]
    captured = []
    monkeypatch.setattr(ai_execution, '_run_cli', lambda cfg, model, prompt: captured.append((model, json.loads(prompt))) or '[]')
    result = ai_execution.agent_chat(conf, messages, route_kind='mid', route_key='onepage', force_json=True, return_metadata=True)
    assert captured[0][0] == 'report-model'
    assert captured[0][1][1:] == messages
    assert result['execution']['route_key'] == 'onepage'
    assert result['execution']['channel_id'] == 'agent:qoder:advanced'
    assert result['execution']['route_kind'] == 'main'
    assert ai_execution.agent_selected(conf, 'mid')
    assert not ai_execution.agent_selected(conf, 'tool')


@pytest.mark.parametrize('xml', [
    b'<rss><channel><item><title>Story</title><link>https://example.test/a</link></item></channel></rss>',
    b'<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>Story</title><link href="https://example.test/a"/></entry></feed>',
])
def test_rss_and_atom(xml):
    assert social_sources._rss_items(xml)[0]['url'] == 'https://example.test/a'


def test_collection_preserves_success_on_error_and_removes_disabled_sources(monkeypatch, tmp_path):
    monkeypatch.setattr(social_sources, '_collector_data_dir', lambda: tmp_path)
    config = social_sources.SocialSourcesConfig(sources=[social_sources.SocialSource(platform='v2ex')])
    monkeypatch.setattr(social_sources, 'load_sources', lambda: config)
    monkeypatch.setattr(social_sources, 'fetch_source', lambda *a: [{'title': 'Real result', 'url': 'https://example.test'}])
    assert social_sources.collect_sources()['count'] == 1
    path = next(tmp_path.glob('search/*/agent-reach/*.json'))
    original = path.read_bytes()
    def fail(*args):
        raise ValueError('渠道未就绪')
    monkeypatch.setattr(social_sources, 'fetch_source', fail)
    assert not social_sources.collect_sources()['ok']
    assert path.read_bytes() == original
    config.sources = []
    social_sources.collect_sources()
    assert not path.exists()
