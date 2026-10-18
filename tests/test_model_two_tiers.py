import json
from pathlib import Path
from types import SimpleNamespace

import pytest

from app.services import ai_execution, llm_client, model_catalog


def test_legacy_mid_routes_to_advanced_pool_and_fallback():
    conf = {'model': 'advanced-model', 'tool_model': 'light-model', 'model_router': {
        'enabled': True, 'prefer_router': True,
        'main_channels': [{'id': 'advanced', 'model': 'advanced-route', 'enabled': True}],
        'mid_channels': [{'id': 'obsolete', 'model': 'obsolete-model', 'enabled': True}],
        'main_module_channels': {'default': ['advanced']},
        'mid_route_channels': {'onepage': ['obsolete']}}}
    targets = llm_client.resolve_chat_targets(conf, route_kind='mid', route_key='onepage', model_override=None)
    assert targets[0]['model'] == 'advanced-route'
    assert all(t['model'] not in ('obsolete-model', 'light-model') for t in targets)
    conf['model_router']['enabled'] = False
    assert llm_client.resolve_chat_target(conf, route_kind='mid', route_key='onepage', model_override=None)['model'] == 'advanced-model'


def test_normalized_onepage_inherits_advanced_default_and_preserves_override():
    router = {'main_channels': [{'id': 'a', 'model': 'advanced'}, {'id': 'b', 'model': 'other'}],
              'main_module_channels': {'default': ['a']}}
    assert llm_client._normalize_model_router(router, {})['main_module_channels']['onepage'] == ['a']
    router['main_module_channels']['onepage'] = ['b']
    assert llm_client._normalize_model_router(router, {})['main_module_channels']['onepage'] == ['b']


@pytest.mark.parametrize('tier,expected', [('main', 'large'), ('mid', 'large'), ('tool', 'small')])
def test_agent_two_independent_models(monkeypatch, tier, expected):
    conf = {'ai_execution': {'mode': 'agent', 'advanced_model': 'large', 'light_model': 'small', 'onepage_model': 'old'}}
    monkeypatch.setattr(ai_execution, '_run_cli', lambda cfg, model, prompt: model)
    assert ai_execution.agent_chat(conf, [], route_kind=tier, force_json=False) == expected
    assert 'onepage_model' not in ai_execution.public_execution_config(conf)


def test_codex_catalog_reports_cache_and_skips_other_provider_cache(monkeypatch, tmp_path):
    monkeypatch.setenv('CODEX_HOME', str(tmp_path))
    monkeypatch.setattr(model_catalog, 'find_cli', lambda _: '/test/codex')
    (tmp_path/'config.toml').write_text('model="chosen"\n')
    (tmp_path/'models_cache.json').write_text(json.dumps({'models': [{'slug': 'cached'}, {'slug': 'hidden', 'visibility': 'hide'}]}))
    payload = model_catalog.CatalogRequest(source='agent', provider='codex')
    result = model_catalog.model_catalog(payload, {})
    assert result['source'] == 'local_cache' and result['models'] == ['cached', 'chosen']
    (tmp_path/'config.toml').write_text('model="custom"\nmodel_provider="private"\n')
    assert model_catalog.model_catalog(payload, {})['models'] == ['custom']


def test_claude_aliases_are_not_claimed_as_account_catalog(monkeypatch):
    monkeypatch.setattr(model_catalog, 'find_cli', lambda _: '/test/claude')
    monkeypatch.setattr(model_catalog.subprocess, 'run', lambda *a, **k: SimpleNamespace(stdout="--model <model> alias (e.g. 'opus', 'sonnet')\n  --other 'not-a-model'", returncode=0))
    result = model_catalog.model_catalog(model_catalog.CatalogRequest(source='agent', provider='claude'), {})
    assert result['source'] == 'cli_aliases'
    assert result['models'] == ['opus', 'sonnet']


def test_hermes_catalog_uses_selected_local_connection(monkeypatch, tmp_path):
    monkeypatch.setenv('HERMES_HOME', str(tmp_path))
    monkeypatch.setattr(model_catalog, 'find_cli', lambda _: '/test/hermes')
    (tmp_path/'config.yaml').write_text('model:\n  provider: custom:test\n  default: small\nproviders:\n  test:\n    base_url: https://agent.test/v1\n    key_env: TEST_HERMES_KEY\n')
    (tmp_path/'.env').write_text('TEST_HERMES_KEY=local-test-key\n')
    calls = []
    def get(url, **kw):
        calls.append((url, kw['headers']))
        return SimpleNamespace(status_code=200, raise_for_status=lambda: None, json=lambda: {'data': [{'id': 'small'}, {'id': 'large'}]})
    monkeypatch.setattr(model_catalog.requests, 'get', get)
    conf = {'api_url': 'https://unrelated.test/v1', 'api_key': 'unrelated',
            'ai_execution': {'provider': 'http', 'api_url': 'https://other-agent.test/v1', 'api_key': 'other'}}
    result = model_catalog.model_catalog(model_catalog.CatalogRequest(source='agent', provider='hermes'), conf)
    assert result['models'] == ['large', 'small']
    assert calls == [('https://agent.test/v1/models', {'Authorization': 'Bearer local-test-key'})]


def test_agent_catalog_never_inherits_builtin_provider(monkeypatch):
    monkeypatch.setattr(model_catalog.requests, 'get', lambda *a, **kw: pytest.fail('must not call unrelated provider'))
    with pytest.raises(ValueError, match='API Base'):
        model_catalog.model_catalog(model_catalog.CatalogRequest(source='agent', provider='http'),
                                    {'api_url': 'https://unrelated.test/v1', 'api_key': 'secret'})


@pytest.mark.parametrize('override', ['', 'https://stale.test/v1'])
def test_hermes_catalog_ignores_saved_and_requested_connection_overrides(monkeypatch, override):
    monkeypatch.setattr(model_catalog, 'find_cli', lambda _: '/test/hermes')
    monkeypatch.setattr(model_catalog, '_hermes_connection', lambda: {'api_url': 'http://localhost:11434/v1'})
    calls = []
    monkeypatch.setattr(model_catalog.requests, 'get', lambda url, **kw:
        calls.append((url, kw['headers'])) or SimpleNamespace(status_code=200, raise_for_status=lambda: None,
            json=lambda: {'data': [{'id': 'local-model'}]}))
    result = model_catalog.model_catalog(model_catalog.CatalogRequest(source='agent', provider='hermes',
        api_url=override, api_key='stale-secret'),
        {'ai_execution': {'provider': 'hermes', 'api_url': 'https://old.test/v1', 'api_key': 'old'}})
    assert result['models'] == ['local-model']
    assert calls == [('http://localhost:11434/v1/models', {})]


def test_saving_cli_config_cleans_old_connection_without_resetting_other_settings(monkeypatch):
    from app.routers import ai
    conf = {'model': 'builtin-model', 'api_key': 'builtin-secret',
            'ai_execution': {'provider': 'hermes', 'api_url': 'https://old.test/v1',
                             'api_key': 'old-key', 'advanced_model': 'chosen'}}
    monkeypatch.setattr(ai, 'load_ai_config', lambda: conf)
    monkeypatch.setattr(ai, 'save_ai_config', lambda _: None)
    result = ai.set_ai_execution({'timeout_seconds': 120})
    assert conf['ai_execution']['api_url'] == conf['ai_execution']['api_key'] == ''
    assert conf['ai_execution']['advanced_model'] == 'chosen'
    assert conf['model'] == 'builtin-model' and conf['api_key'] == 'builtin-secret'
    assert not result['configuration_notice'] and not result['has_key']


def test_switching_agent_or_url_does_not_preserve_old_key(monkeypatch):
    from app.routers import ai
    conf = {'ai_execution': {'provider': 'hermes', 'api_url': 'https://first.test/v1', 'api_key': 'old-key'}}
    monkeypatch.setattr(ai, 'load_ai_config', lambda: conf)
    monkeypatch.setattr(ai, 'save_ai_config', lambda _: None)
    ai.set_ai_execution({'provider': 'http', 'api_url': 'https://new.test/v1'})
    assert not conf['ai_execution']['api_key']


def test_settings_have_two_tiers_and_visible_model_discovery():
    base = Path(__file__).resolve().parents[1]
    html = (base/'static/index.html').read_text()
    script = (base/'static/modules/ai-execution.js').read_text()
    assert 'data-router-lane="mid"' not in html
    assert 'execOnepage' not in script
    assert "['Advanced','高级处理'" in script and "['Light','轻量处理'" in script
    assert 'id="execCatalog"' in script and '自定义模型 ID' in script
    assert "['main', 'onepage', '一页通']" in html
