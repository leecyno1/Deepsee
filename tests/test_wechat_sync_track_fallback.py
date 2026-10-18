class _Db:
    def commit(self):
        pass

    def rollback(self):
        pass


def test_wechatapi_offline_response_is_not_healthy(monkeypatch):
    from app.services import sync_runtime

    monkeypatch.setattr(
        "app.services.wechat_gateway.load_config",
        lambda _db: {
            "base_url": "http://wechat.example/api",
            "token": "token",
            "app_id": "app-id",
            "enabled": True,
        },
    )

    class _Client:
        def __init__(self, **_kwargs):
            pass

        def check_online(self):
            return {"ret": 200, "msg": "操作成功", "data": False}

    monkeypatch.setattr("app.services.wechatapi_client.WechatApiClient", _Client)

    state = sync_runtime.get_wechatapi_track_state(_Db())

    assert state["healthy"] is False
    assert state["status"] == "offline"


def test_single_track_sync_falls_back_to_first_healthy_track():
    from app.services import sync_runtime

    calls = []
    adapters = sync_runtime.DualTrackSyncAdapters(
        load_policy=lambda _db: {
            "enabled_tracks": ["wechatapi", "chatlog", "wx_cli"],
            "track_order": ["wechatapi", "chatlog", "wx_cli"],
            "use_multiple_tracks": False,
            "chatlog_window_days": 1,
        },
        get_wechatapi_state=lambda _db: {"healthy": False, "message": "offline"},
        get_chatlog_state=lambda _db: {"healthy": True, "message": "ready"},
        get_wx_cli_state=lambda _db: {"healthy": True, "message": "ready"},
        sync_full=lambda _db, _days: calls.append("chatlog") or {"fetched": 3, "inserted": 2},
        sync_from_wx_cli=lambda _db, _days: calls.append("wx_cli") or {},
        refresh_snapshots=lambda _db: None,
        persist_run=lambda _db, _payload: None,
    )

    result = sync_runtime.execute_dual_track_sync_run(
        _Db(), adapters=adapters, model_cls=object
    )

    assert result["execution_order"] == ["chatlog"]
    assert calls == ["chatlog"]
    assert result["status"] == "ok"
