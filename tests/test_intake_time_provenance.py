import json

from app.services import media_collector_store, news_engine


def test_hot_items_keep_stale_and_observation_time(tmp_path, monkeypatch):
    day = tmp_path / "hot" / "2026-10-17"
    day.mkdir(parents=True)
    (day / "newsnow_cls.json").write_text(json.dumps({
        "fetched_at": "2026-10-17T12:00:00+08:00", "observed_at": "2026-10-15T12:00:00+08:00",
        "_stale": True, "items": [{"rank": 1, "title": "市场新闻", "url": "https://example.com/a"}]
    }))
    monkeypatch.setenv("COLLECTOR_DATA_DIR", str(tmp_path))
    row = media_collector_store.list_hot_items()["items"][0]
    assert row["_stale"] is True
    assert row["cache_status"] == "stale"
    assert row["observed_at"] == "2026-10-15T12:00:00+08:00"
    assert row["published_at"] == ""


def test_rss_without_date_never_invents_publication_time(monkeypatch):
    class Response:
        text = '<rss><channel><item><title>市场新闻</title><link>https://example.com/a</link></item></channel></rss>'
    monkeypatch.setattr(news_engine.requests, "get", lambda *a, **k: Response())
    rows = news_engine._fetch_source(news_engine.NewsSource("test-rss", "test", "https://example.com/rss"))
    assert rows[0]["pub_ts"] == 0
