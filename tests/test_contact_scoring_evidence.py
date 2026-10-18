from datetime import datetime
from types import SimpleNamespace

from app.services import contact_scoring as scoring


def event(**kwargs):
    return {"id": 1, "source_time": datetime(2026, 4, 1, 16), "asset_type": "stock",
            "asset_code": "601899", "benchmark_code": "sh000300", "direction": "bullish",
            "horizon_flags": {"1m": True}, **kwargs}


def test_entry_after_close_and_same_day_benchmark():
    asset = [{"date": "2026-04-01", "close": 100}, {"date": "2026-04-02", "close": 110},
             {"date": "2026-05-04", "close": 121}]
    index = [{"date": "2026-04-02", "close": 1000}, {"date": "2026-05-04", "close": 1050}]
    fetch = lambda kind, code, *args: index if kind == "index" else asset
    rows = scoring.evaluate_prediction_event(event(), as_of=datetime(2026, 5, 5), price_fetcher=fetch)
    assert rows[0]["entry_price"] == 110
    assert rows[0]["absolute_return"] == 0.1
    assert rows[0]["excess_return"] == 0.05
    assert rows[0]["meta"]["entry_date"] == "2026-04-02"
    index[1]["date"] = "2026-05-05"
    assert scoring.evaluate_prediction_event(event(), as_of=datetime(2026, 5, 5), price_fetcher=fetch) == []


def test_no_stale_or_future_price_used_as_matured_return():
    assert scoring._price_on_or_after([{"date": "2026-04-02", "close": 110}], datetime(2026, 5, 1).date()) is None
    fetch = lambda *args: [{"date": "2026-04-02", "close": 110}, {"date": "2026-06-01", "close": 200}]
    assert scoring.evaluate_prediction_event(event(benchmark_code=None), as_of=datetime(2026, 5, 5), price_fetcher=fetch) == []
    assert scoring.evaluate_prediction_event(event(benchmark_code=None), as_of=datetime(2026, 6, 5), price_fetcher=fetch) == []


def test_dedupe_uses_first_signal_not_best_return():
    rows = [{"cluster_id": "a", "horizon_code": "1m", "source_time": datetime(2026, 4, 1), "event_score": 0},
            {"cluster_id": "a", "horizon_code": "1m", "source_time": datetime(2026, 4, 2), "event_score": 100}]
    assert scoring._dedupe_evaluation_rows(rows)[0]["event_score"] == 0


def test_missing_evidence_is_not_a_failure_or_rating():
    assert scoring.resolve_auto_rating(SimpleNamespace(stats={"auto_rating": 50, "sample_size": 0})) is None
    assert scoring._contact_evidence([], datetime(2026, 10, 16))["status"] == "unrated"
    ev = SimpleNamespace(id=1, event_cluster_id="a", is_actionable=True, direction="bullish", event_kind="price_call",
                         raw_text="推荐紫金矿业", normalized_text="", source_time=datetime(2026, 10, 15),
                         horizon_flags={"1m": True}, evaluations=[])
    state = scoring._contact_evidence([ev], datetime(2026, 10, 16))
    assert state["waiting_samples"] == 1 and state["missing_samples"] == 0
    assert state["coverage"] is None
    state = scoring._contact_evidence([ev], datetime(2026, 12, 16))
    assert state["missing_samples"] == 1 and state["verified_samples"] == 0


def test_recaps_dates_and_disclaimers_are_not_predictions():
    assert scoring._find_direction("昨天紫金矿业上涨3%，收盘创了新高。") is None
    assert scoring._find_direction("建议减仓紫金矿业，不推荐继续加仓") == "bearish"
    assert scoring._find_direction("紫金矿业，风险提示：股价下跌") is None
    assert scoring._detect_horizon_flags("3月业绩公布，继续看好公司")["1m"] is True
    assert scoring._select_benchmark_code("index", "sh000300", "看好沪深300") is None


def test_bearish_excess_and_sample_size_are_not_penalized():
    rows = [{"event_id": i, "cluster_id": str(i), "direction": "bearish", "event_kind": "price_call",
             "is_actionable": True, "horizon_code": "1m", "excess_return": -0.1,
             "direction_hit": True, "event_score": 90} for i in range(40)]
    result = scoring.summarize_contact_score(contact_id="a", evaluations=rows, manual_rating=50)
    assert result["score_breakdown"]["excess_return_score"] == 100
    assert result["frequency_penalty"] == 0


def test_research_broker_is_not_recommended_security(monkeypatch):
    from app.services import market_data as market
    monkeypatch.setattr(market, "load_asset_lookup_entries", lambda config: [
        market._build_lookup_entry("stock", "601211", "国泰海通"),
        market._build_lookup_entry("stock", "605098", "行动教育"),
        market._build_lookup_entry("stock", "300024", "机器人"),
    ])
    resolve = market.build_asset_text_resolver({})
    assert resolve("【国泰海通商社】继续推荐行动教育，扩张提速")["asset_code"] == "605098"
    assert resolve("国泰海通策略观点：看好机器人板块") is None
    assert resolve("继续看好国泰海通")["asset_code"] == "601211"
    assert resolve("推荐机器人300024")["asset_code"] == "300024"


def test_tushare_fund_split_is_adjusted_not_scored_as_crash(monkeypatch):
    import pandas as pd
    from app.services import market_data as market
    monkeypatch.setattr(market, "_get_tushare_client", lambda token: SimpleNamespace(fund_daily=lambda **k: None))
    prices = pd.DataFrame([{"trade_date": "20260401", "close": 2.0}, {"trade_date": "20260501", "close": 1.1}])
    factors = pd.DataFrame([{"trade_date": "20260401", "adj_factor": 1.0}, {"trade_date": "20260501", "adj_factor": 2.0}])
    monkeypatch.setattr(market, "_tushare_call", lambda client, name, **kwargs: factors if name == "fund_adj" else prices)
    result = market._fetch_tushare_series(market.normalize_asset_identity("etf", "512480"), datetime(2026, 4, 1), datetime(2026, 5, 1), {"tushare_token": "test"})
    assert result[0]["close"] == 1.0
    assert result[1]["close"] == 1.1
    assert result.provenance["adjustment"] == "qfq"
    factors.drop(index=0, inplace=True)
    assert market._fetch_tushare_series(market.normalize_asset_identity("etf", "512480"), datetime(2026, 4, 1), datetime(2026, 5, 1), {"tushare_token": "test"}) == []
