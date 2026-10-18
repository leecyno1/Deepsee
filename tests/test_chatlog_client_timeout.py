import os
import sys

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
if PROJECT_ROOT not in sys.path:
    sys.path.insert(0, PROJECT_ROOT)


def test_chatlog_client_uses_configured_timeouts(monkeypatch):
    from app.services.chatlog_client import ChatlogClient
    from app.config import settings

    monkeypatch.setattr(settings, "CHATLOG_HTTP_SESSION_TIMEOUT_SECONDS", 3, raising=False)
    monkeypatch.setattr(settings, "CHATLOG_HTTP_TIMEOUT_SECONDS", 7, raising=False)

    calls = []

    class _Resp:
        status_code = 200
        headers = {"content-type": "application/json"}

        def raise_for_status(self):
            return None

        def json(self):
            return []

        @property
        def text(self):
            return ""

    def _fake_get(url, params=None, timeout=None):
        calls.append({"url": url, "params": params, "timeout": timeout})
        return _Resp()

    monkeypatch.setattr("app.services.chatlog_client.requests.get", _fake_get)

    c = ChatlogClient(base="http://127.0.0.1:5030")
    c.get_sessions()
    c.get_chatlog("2026-03-07", talker="wxid_1", limit=10, offset=0)

    assert calls[0]["timeout"] == 3
    assert calls[1]["timeout"] == 7
    assert calls[0]["url"].endswith("/api/v1/sessions")
    assert calls[0]["params"]["limit"] == "500"
    assert calls[1]["url"].endswith("/api/v1/history")
    assert calls[1]["params"]["chat"] == "wxid_1"


def test_chatlog_client_normalizes_current_upstream_history():
    from app.services.chatlog_client import ChatlogClient

    payload = ChatlogClient._normalize_history(
        {
            "chat": "测试群",
            "username": "123@chatroom",
            "is_group": True,
            "messages": [
                {
                    "timestamp": 1787846400,
                    "time": "2026-08-28 00:00",
                    "sender": "张三",
                    "sender_id": "wxid_sender",
                    "is_self": False,
                    "type": "text",
                    "content": "hello",
                }
            ],
        },
        talker="123@chatroom",
    )

    row = payload["messages"][0]
    assert row["talker"] == "123@chatroom"
    assert row["talkerName"] == "测试群"
    assert row["sender"] == "wxid_sender"
    assert row["senderName"] == "张三"
    assert row["isChatRoom"] is True


def test_chatlog_health_probe_uses_current_sessions_endpoint(monkeypatch):
    from app.services import deployment_status

    calls = []

    class _Resp:
        status_code = 200

    def _fake_get(url, timeout=None):
        calls.append({"url": url, "timeout": timeout})
        return _Resp()

    monkeypatch.setattr(deployment_status.requests, "get", _fake_get)
    result = deployment_status.probe_chatlog_http("http://127.0.0.1:5031")

    assert result["ok"] is True
    assert calls[0]["url"] == "http://127.0.0.1:5031/api/v1/sessions?limit=1"
