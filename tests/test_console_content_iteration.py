import pytest

from app.routers import ai, media, mp_rss
from app.services.llm_client import resolve_chat_targets


@pytest.mark.parametrize('title', ['赵丽颖生日快乐，粉丝为她庆生', '今天给赵丽颖过生日', '明星机场穿搭，市场热度第一'])
def test_gossip_is_not_rescued_by_financial_source_labels(title):
    item = {'title': title, 'channel_name': '投资研究', 'author': '上市公司', 'source_keyword': 'AI产业链'}
    assert media._is_low_value_media_noise(item)
    assert mp_rss._is_spam_life_mp_item(item)


def test_entertainment_sector_research_is_retained():
    item = {'title': '传媒上市公司财报：电影票房同比增长与营收分析', 'content': '综艺与电视剧业务'}
    assert not media._is_low_value_media_noise(item)
    assert not mp_rss._is_spam_life_mp_item(item)


def test_reply_uses_configured_light_pool_not_hardcoded_m3():
    conf = {'model': 'advanced', 'tool_model': 'MiniMax-M3', 'api_url': 'https://example.test/v1',
            'model_router': {'enabled': True, 'tool_channels': [
                {'id': 'local', 'enabled': True, 'model': 'local-small', 'weight': 1}],
                'tool_route_channels': {'reply': ['local']}}}
    targets = resolve_chat_targets(conf, route_kind='tool', route_key='reply', model_override='MiniMax-M3')
    assert [t['model'] for t in targets] == ['local-small']


def test_broadcast_script_review_does_not_synthesize(monkeypatch):
    monkeypatch.setattr(ai, 'load_ai_config', lambda: {})
    monkeypatch.setattr(ai, '_run_mmx_speech', lambda *a, **k: pytest.fail('must not synthesize'))
    result = ai.generate_onepage_audio({'script_only': True, 'script': '请先核对数据。'})
    assert result['status'] == 'ok'
    assert result['script'] == '请先核对数据。'


def test_broadcast_calls_supported_advanced_model_arguments(monkeypatch):
    def chat(messages, *, temperature, model_override, route_kind, route_key):
        assert route_kind == 'main'
        return '这是真正生成的口播稿'
    monkeypatch.setattr(ai, 'siliconflow_chat', chat)
    assert ai._build_onepage_audio_script({'sections': [{'title': '重点', 'text': '业绩增长'}]}, {}) == '这是真正生成的口播稿'


def test_broadcast_voice_failure_preserves_script(monkeypatch):
    monkeypatch.setattr(ai, 'load_ai_config', lambda: {})
    monkeypatch.setattr(ai, '_resolve_mmx_cli_key', lambda conf: '')
    def unavailable(*args, **kwargs):
        raise RuntimeError('private provider details')
    monkeypatch.setattr(ai, '_run_mmx_speech', unavailable)
    result = ai.generate_onepage_audio({'script': '保留文稿'})
    assert result['status'] == 'needs_voice'
    assert result['script'] == '保留文稿'
    assert 'private' not in result['error']


def test_ai_runtime_partial_save_keeps_other_engine_settings(tmp_path):
    from sqlalchemy import create_engine
    from sqlalchemy.orm import Session
    from app.db import Base
    from app.models import SyncState
    from app.routers.configs import set_ai_runtime, get_ai_runtime
    engine = create_engine(f'sqlite:///{tmp_path / "runtime.db"}')
    Base.metadata.create_all(engine, tables=[SyncState.__table__])
    with Session(engine) as db:
        set_ai_runtime({'enable_email_tool_overlay': False, 'default_concurrency': 7}, db)
        set_ai_runtime({'enable_msg_tool_overlay': False}, db)
        config = get_ai_runtime(db)
        assert config['enable_email_tool_overlay'] is False
        assert config['default_concurrency'] == 7
        assert config['enable_msg_tool_overlay'] is False
    engine.dispose()
