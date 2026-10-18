import json
import threading
from datetime import datetime

import pytest
from fastapi import BackgroundTasks
from sqlalchemy import create_engine, select
from sqlalchemy.orm import sessionmaker

from app.db import Base
from app.models import Message, Report, SyncState, Task
from app.routers import ai
from app.services.analysis_jobs import run_modules, select_evidence


def test_evidence_budget_deduplicates_and_keeps_other_sources():
    rows = [{"id": n, "sender": "A", "summary": f"行业观点{n}" + "依据" * 600} for n in range(20)]
    rows += [{"id": 100, "sender": "B", "summary": "公司利润增长但库存积压"}, dict(rows[0])]
    selected, coverage = select_evidence(rows, max_chars=2300, excerpt_chars=500)
    assert coverage["duplicate_count"] == 1
    assert coverage["input_chars"] <= 2300
    assert coverage["omitted_count"] > 0 and coverage["excerpted_count"] > 0
    assert any(r["id"] == 100 for r in selected)
    assert all(not ("summary" in r and "content" in r) for r in selected)


def test_quant_counts_only_supplied_evidence():
    from app.services.quant_analysis import normalize_quant
    result = normalize_quant({"topics": [{"topic": "订单", "bullish_ids": ["1", "invented"],
                                         "bearish_ids": ["1", "2"]}]}, allowed_ids={"1", "2"})
    assert result["topics"][0]["counts"] == {"total": 2, "bullish": 1, "bearish": 1, "neutral": 0}


def test_structured_charts_follow_partial_reports_and_exclude_failed_modules():
    def run_one(payload):
        module = payload["modules"][0]
        return {"status": "error" if module == "meetings" else "ok",
                "result": {"market_markdown": "研究线索", "analysis_data": {module: {"topics": [], "version": 1}}}}
    states = []
    output = run_modules({"modules": ["market", "meetings"]}, run_one, states.append)
    assert output["result"]["analysis_data"] == {"market": {"topics": [], "version": 1}}
    assert any(s["report"].get("analysis_data", {}).get("market") for s in states)


def test_quant_and_coverage_survive_persistent_cache(database, monkeypatch):
    calls = []
    def chat(*args, **kwargs):
        calls.append(True)
        return json.dumps({"markdown": "# 市场观点总结\n行业订单增长，但需核实风险 #1",
                           "quant": {"topics": [{"topic": "行业订单", "bullish_ids": ["1", "fake"]}]}}, ensure_ascii=False)
    monkeypatch.setattr(ai, "siliconflow_chat", chat)
    payload = {"modules": ["market"], "snapshot_id": "chart-cache", "messages": [{
        "id": 1, "time": datetime.utcnow().isoformat(), "channel": "wechat",
        "sender_name": "test", "content": "行业订单增长，公司利润改善，估值和需求持续性仍需进一步核验。",
        "derived": {"summary": "行业订单增长，公司利润改善，估值和需求持续性仍需进一步核验。"}}]}
    with database() as db:
        first = ai._run_summary_local({**payload, "_cache_db": db})
        db.commit()
    with database() as db:
        second = ai._run_summary_local({**payload, "_cache_db": db})
    assert first["status"] == second["status"] == "ok"
    a = first["result"]["analysis_data"]["market"]
    assert a == second["result"]["analysis_data"]["market"]
    assert a["coverage"]["selected_count"] == 1
    assert a["topics"][0]["counts"]["total"] == 1
    assert a["topics"][0]["bullish_ids"] == ["1"]
    assert a["sources"] == [{"name": "wechat", "count": 1}]
    assert len(calls) == 1


def test_agent_timeout_includes_queue_wait(monkeypatch):
    from app.services import ai_execution
    values = iter([100, 101, 110])
    monkeypatch.setattr(ai_execution.time, "monotonic", lambda: next(values))
    observed = []
    class Slot:
        def acquire(self, timeout):
            observed.append(timeout)
            return True
        def release(self):
            pass
    monkeypatch.setattr(ai_execution, "_slots", Slot())
    monkeypatch.setattr(ai_execution, "_run_cli", lambda cfg, *args: observed.append(cfg.timeout_seconds) or "ok")
    assert ai_execution.agent_chat({}, [], route_kind="main", force_json=False, request_timeout=30) == "ok"
    assert observed == [29, 20]


def test_analysis_agent_does_not_wait_for_lightweight_slots(monkeypatch):
    from app.services import ai_execution
    class Busy:
        def acquire(self, **kwargs):
            pytest.fail("高级分析不应等待轻量任务名额")
    monkeypatch.setattr(ai_execution, "_slots", Busy())
    monkeypatch.setattr(ai_execution, "_analysis_slots", threading.BoundedSemaphore(2))
    monkeypatch.setattr(ai_execution, "_run_cli", lambda *args: "done")
    assert ai_execution.agent_chat({}, [], route_kind="main", route_key="market", force_json=False) == "done"


def test_empty_module_is_not_counted_as_generated_analysis():
    def run_one(payload):
        payload["_evidence_ready"]({"selected_count": 0})
        return {"status": "ok", "result": {"market_markdown": "没有资料"}}
    output = run_modules({"modules": ["market"]}, run_one, lambda state: None)
    assert output["progress"]["empty"] == 1
    assert output["progress"]["succeeded"] == 0
    assert output["progress"]["finished"] == 1


def test_parallel_modules_publish_before_slowest_finishes_and_preserve_failure():
    both_started = threading.Barrier(2)
    first_visible = threading.Event()
    updates = []

    def run_one(payload):
        module = payload["modules"][0]
        both_started.wait(timeout=3)
        payload["_evidence_ready"]({"selected_count": 2})
        if module == "market":
            return {"status": "ok", "result": {"market_markdown": "有来源的市场线索 #1"}}
        assert first_visible.wait(timeout=3)
        return {"status": "error", "module_errors": {"meetings": "模型调用超时"}}

    def publish(state):
        updates.append(state)
        if state["report"].get("market_markdown"):
            first_visible.set()

    output = run_modules({"modules": ["market", "meetings"]}, run_one, publish, concurrency=32)
    partial = next(s for s in updates if s["progress"]["finished"] == 1)
    assert partial["report"]["market_markdown"]
    assert partial["progress"]["modules"]["meetings"]["state"] == "running"
    assert output["status"] == "partial"
    assert output["progress"]["concurrency"] == 2
    assert output["progress"]["finished"] == 2
    assert output["progress"]["succeeded"] == output["progress"]["failed"] == 1
    assert output["module_errors"]["meetings"] == "模型调用超时"


@pytest.fixture
def database(tmp_path, monkeypatch):
    engine = create_engine(f"sqlite:///{tmp_path / 'test.db'}", connect_args={"check_same_thread": False, "timeout": 1})
    Base.metadata.create_all(engine)
    factory = sessionmaker(bind=engine, expire_on_commit=False)
    monkeypatch.setattr(ai, "SessionLocal", factory)
    monkeypatch.setattr(ai, "SUMMARY_RUN_LOCK", threading.Lock())
    monkeypatch.setattr(ai, "SUMMARY_RUN_STATE", {"task_id": None, "started_at": 0})
    monkeypatch.chdir(tmp_path)
    yield factory
    engine.dispose()


def test_job_returns_before_ai_and_commits_snapshot_before_model(database, monkeypatch):
    with database() as db:
        db.add(Message(id=1, chat_id="test", sender_id="test", direction="in", type="text",
                       timestamp=datetime.utcnow(), content_text="行业订单持续增长，公司利润改善，估值需要进一步核实。"))
        db.commit()
    calls = []

    def fake_module(payload):
        # Another connection can write while the AI module is running.
        with database() as independent:
            independent.add(SyncState(key="sync-can-write", value="yes"))
            independent.commit()
        calls.append(payload["modules"])
        return {"status": "ok", "result": {"market_markdown": "# 市场观点总结\n订单增长待核实 #1"}}

    monkeypatch.setattr(ai, "_run_summary_local", fake_module)
    background = BackgroundTasks()
    with database() as db:
        task = ai.start_summary_job({"modules": ["market"], "filters": {"period": "1day"}}, background, db)
        assert task.status == "pending" and not calls
        duplicate = ai.start_summary_job({"modules": ["market"]}, BackgroundTasks(), db)
        assert duplicate.id == task.id
    queued = background.tasks[0]
    queued.func(*queued.args, **queued.kwargs)
    with database() as db:
        saved = db.get(Task, task.id)
        assert saved.status == "done"
        assert saved.result["progress"]["finished"] == 1
        assert "订单增长" in saved.result["report"]["market_markdown"]
        assert db.scalars(select(Report)).first()
    assert not ai.SUMMARY_RUN_LOCK.locked()


def test_partial_report_is_saved_and_failed_module_does_not_erase_success(database, monkeypatch):
    def fake_module(payload):
        if payload["modules"] == ["mediawatch"]:
            return {"status": "ok", "result": {"mediawatch_markdown": "有用资料"}}
        return {"status": "error", "module_errors": {"mpwatch": "模型调用超时"}}

    monkeypatch.setattr(ai, "_run_summary_local", fake_module)
    with database() as db:
        result = ai.summary({"modules": ["mediawatch", "mpwatch"]}, db)
        assert result.status == "done"
        assert result.result["status"] == "partial"
        assert result.result["report"]["mediawatch_markdown"] == "有用资料"
        assert db.scalars(select(Report)).first()


def test_market_one_bounded_call_and_research_constraints(database, monkeypatch):
    captured = []
    monkeypatch.setattr(ai, "load_ai_config", lambda: {})
    monkeypatch.setattr(ai, "siliconflow_chat", lambda messages, **kw: captured.append((messages, kw)) or "# 市场观点总结\n公司观点待核验 #1")
    with database() as db:
        result = ai._run_summary_local({
            "modules": ["market"], "snapshot_id": "test-budget", "_cache_db": db,
            "messages": [{"id": i, "sender_id": f"person-{i % 8}", "content_text": f"公司{i}估值分析：" + "订单增长" * 500} for i in range(400)],
        })
        assert result["status"] == "ok" and len(captured) == 1
        messages, kwargs = captured[0]
        assert len(messages[1]["content"]) < 40000
        assert "失效条件" in messages[0]["content"] and "未经独立行情数据核验" in messages[0]["content"]
        assert kwargs["request_timeout"] == 180


def test_external_analysis_does_not_generate_item_summaries(database, monkeypatch):
    from app.services import external_content_summaries, media_store
    monkeypatch.setattr(ai, "load_ai_config", lambda: {})
    monkeypatch.setattr(ai, "_list_collector_items", lambda **kw: {"items": [{"id": "test", "title": "行业订单增长，相关公司业绩改善的研究线索", "description": "行业订单增长，相关公司业绩改善的研究线索"}]})
    monkeypatch.setattr(media_store, "list_media_items", lambda **kw: {"items": []})
    def unexpected(*args, **kwargs):
        pytest.fail("分析不应隐式补跑轻量摘要")
    monkeypatch.setattr(external_content_summaries, "summarize_external_items", unexpected)
    monkeypatch.setattr(ai, "siliconflow_chat", lambda *a, **kw: "# 自媒体观察\n有待核验的行业线索")
    with database() as db:
        result = ai._run_summary_local({"modules": ["mediawatch"], "_cache_db": db, "snapshot_id": "test-external"})
        assert result["status"] == "ok"
