import json
from pathlib import Path

import pytest

from app.services import ai_execution as execution
from app.services import llm_client


def test_tiers_can_run_agent_and_models_in_parallel():
    conf = {"ai_execution": {"mode": "agent", "light_backend": "models"}}
    assert execution.agent_selected(conf, "main")
    assert not execution.agent_selected(conf, "tool")


def test_shared_entry_preserves_agent_metadata(monkeypatch):
    conf = {"ai_execution": {"mode": "agent", "advanced_model": "advanced-id", "light_model": "light-id"}}
    monkeypatch.setattr(llm_client, "load_ai_config", lambda: conf)
    calls = []
    monkeypatch.setattr(execution, "_run_cli", lambda cfg, model, prompt: calls.append((model, json.loads(prompt))) or '{"summary":"有效摘要"}')
    result = llm_client.siliconflow_chat([{"role": "user", "content": "测试"}], route_kind="tool", return_metadata=True)
    assert calls[0][0] == "light-id"
    assert result["execution"]["channel_id"] == "agent:codex:light"
    assert json.loads(result["text"])["summary"] == "有效摘要"


def test_empty_agent_output_is_failure(monkeypatch):
    monkeypatch.setattr(execution, "_run_cli", lambda *args: " ")
    with pytest.raises(RuntimeError, match="有效内容"):
        execution.agent_chat({}, [], route_kind="main", force_json=False)


def test_codex_home_does_not_inherit_tools(monkeypatch, tmp_path):
    source = tmp_path/"source"
    source.mkdir()
    (source/"config.toml").write_text('model="test"\n[mcp_servers.unsafe]\nurl="https://example.test"\n')
    monkeypatch.setenv("CODEX_HOME", str(source))
    target = Path(execution._codex_home(str(tmp_path)))
    assert "mcp_servers" not in (target/"config.toml").read_text()
    assert 'model = "test"' in (target/"config.toml").read_text()


def test_hermes_keeps_model_connection_without_user_customizations(monkeypatch, tmp_path):
    import yaml
    home = tmp_path / 'source'
    home.mkdir()
    (home / 'config.yaml').write_text(yaml.safe_dump({
        'model': {'provider': 'custom:test', 'default': 'test-model', 'api_mode': 'codex_responses'},
        'providers': {'test': {'base_url': 'https://example.test/v1', 'key_env': 'TEST_MODEL_KEY',
                               'command': 'must-not-copy'}},
        'mcp_servers': {'unsafe': {'command': 'must-not-run'}},
        'hooks': {'start': 'must-not-run'},
        'agent': {'system_prompt': 'must-not-copy'},
    }))
    (home / '.env').write_text('TEST_MODEL_KEY=test-secret\nAPI_SERVER_KEY=unrelated-secret\n')
    monkeypatch.setenv('HERMES_HOME', str(home))
    env = {'HERMES_IGNORE_USER_CONFIG': '1'}
    target = Path(execution._hermes_home(str(tmp_path/'profiles'/'test'), env))
    config = yaml.safe_load((target / 'config.yaml').read_text())
    assert config['model']['default'] == 'test-model'
    assert 'command' not in config['providers']['test']
    assert 'mcp_servers' not in config and 'hooks' not in config
    assert env['TEST_MODEL_KEY'] == 'test-secret'
    assert 'API_SERVER_KEY' not in env
    assert env['HERMES_SAFE_MODE'] == '1'
    assert 'HERMES_IGNORE_USER_CONFIG' not in env
    assert not (target / '.env').exists()
    monkeypatch.setattr(execution, 'find_cli', lambda provider: '/test/bin/hermes')
    command = execution._command(execution.AIExecutionConfig(provider='hermes'), 'light-model', tmp_path/'answer')
    assert '--safe-mode' not in command
    assert command[command.index('--toolsets') + 1] == 'none'
    assert command[command.index('--model') + 1] == 'light-model'


def test_hermes_preserves_cli_protocol_without_changing_user_config(monkeypatch, tmp_path):
    import yaml
    source = tmp_path / 'source'
    source.mkdir()
    original = 'model:\n  default: gpt-test\n  provider: custom:local\n  api_mode: codex_responses\n'
    (source / 'config.yaml').write_text(original)
    monkeypatch.setenv('HERMES_HOME', str(source))
    env = {}
    home = Path(execution._hermes_home(str(tmp_path/'isolated'), env))
    raw = (home/'config.yaml').read_text()
    clean = yaml.safe_load(raw)
    assert clean['model']['default'] == 'gpt-test'
    assert clean['model']['api_mode'] == 'codex_responses'
    assert clean['model']['provider'] == 'custom:local'
    assert 'deepsee' not in clean['providers']
    assert 'DEEPSEE_HERMES_MODEL_KEY' not in env
    assert (source/'config.yaml').read_text() == original


@pytest.mark.parametrize('provider', ['hermes', 'codex', 'claude', 'qoder'])
def test_cli_config_discards_legacy_connection_but_keeps_model_selection(provider):
    conf = {'ai_execution': {'provider': provider, 'api_url': 'https://old.test/v1',
                            'api_key': 'old-secret', 'advanced_model': 'chosen',
                            'light_model': 'small'}}
    cfg = execution.execution_config(conf)
    assert cfg.api_url == cfg.api_key == ''
    assert (cfg.advanced_model, cfg.light_model) == ('chosen', 'small')
    public = execution.public_execution_config(conf)
    assert public['configuration_notice'] and not public['has_key']
    assert 'old-secret' not in json.dumps(public)


def test_hermes_reuses_runtime_and_cleans_temporary_profile(monkeypatch, tmp_path):
    home = tmp_path / 'hermes'
    home.mkdir()
    monkeypatch.setenv('HERMES_HOME', str(home))
    monkeypatch.setattr(execution, 'find_cli', lambda provider: '/test/bin/hermes')
    profiles = []

    class Process:
        returncode = 0

        def __init__(self, command, **kwargs):
            profile = Path(kwargs['env']['HERMES_HOME'])
            assert profile.parent == home / 'profiles'
            assert (profile / 'config.yaml').is_file()
            assert kwargs['env']['HERMES_DISABLE_LAZY_INSTALLS'] == '1'
            profiles.append(profile)

        def __enter__(self):
            return self

        def __exit__(self, *args):
            pass

        def communicate(self, prompt, timeout):
            return '{"type":"result","exit_code":0,"text":"ok"}', ''

    monkeypatch.setattr(execution.subprocess, 'Popen', Process)
    assert execution._run_cli(execution.AIExecutionConfig(provider='hermes'), '', 'test') == 'ok'
    assert profiles and not profiles[0].exists()


def test_hermes_only_accepts_successful_final_result():
    output = '\n'.join(['startup notice', '{"type":"text","text":"partial"}',
                        '{"type":"result","exit_code":0,"text":"最终回答"}'])
    assert execution._hermes_answer(output) == '最终回答'
    with pytest.raises(RuntimeError):
        execution._hermes_answer('{"type":"result","exit_code":1,"text":"partial"}')
    with pytest.raises(RuntimeError):
        execution._hermes_answer('{"type":"text","text":"not finished"}')


def test_config_preserves_key_and_hides_it(monkeypatch, tmp_path):
    from app.routers import ai
    monkeypatch.chdir(tmp_path)
    llm_client.save_ai_config({"ai_execution": {"provider": "http", "api_key": "test-secret"}})
    result = ai.set_ai_execution({"mode": "agent", "light_backend": "models", "api_key": ""})
    assert result["has_key"]
    assert "test-secret" not in json.dumps(result)
    assert llm_client.load_ai_config()["ai_execution"]["api_key"] == "test-secret"
    assert not ai.set_ai_execution({"clear_key": True})["has_key"]


def test_analysis_uses_raw_evidence_and_reports_failure(monkeypatch, tmp_path):
    from app.routers import ai
    monkeypatch.chdir(tmp_path)
    monkeypatch.setattr(ai, "load_ai_config", lambda: {"ai_execution":{"mode":"agent"}})
    captured=[]
    monkeypatch.setattr(ai, "siliconflow_chat", lambda messages, **kw: captured.append(messages) or '# 市场观点总结\n订单增长有待持续验证 #1')
    payload={"messages":[{"id":1,"content_text":"公司新增订单增长百分之三十，盈利改善，当前估值仍然需要进一步验证。", "sender_id":"test", "derived":{}}],"modules":["market"],"snapshot_id":"raw-evidence-test"}
    result=ai._run_summary_local(payload)
    assert result["status"] == "ok"
    assert captured and "订单增长" in captured[0][1]["content"]
    ai.SUMMARY_CACHE.clear()
    payload["snapshot_id"]="failed-evidence-test"
    monkeypatch.setattr(ai, "siliconflow_chat", lambda *args, **kwargs: "")
    result=ai._run_summary_local(payload)
    assert result["status"] == "error"
    assert result["module_errors"]["market"]
    assert result["result"]["market_html"] == ""
    assert "失败" in result["result"]["market_markdown"]
