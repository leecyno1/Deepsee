"""Bounded module execution with independently published results."""
from concurrent.futures import ThreadPoolExecutor, as_completed
from copy import deepcopy
from threading import Lock
from time import time
import json


MARKET_RESEARCH_GUIDANCE = """
投资研究要求（在用户设定的章节内组织，不重复铺陈）：
先列出最值得核验的 3–5 条研究线索；没有可靠线索可少列或不列。
每条区分【消息事实／来源观点／分析推断】，说明影响路径、相关资产、时间跨度；
给出支持证据与反证/分歧、下一催化及时间（未知写待确认）、失效条件和可观测验证指标。
将这些内容浓缩为“研究线索 | 依据与分歧 | 催化/期限 | 失效条件 | 下一步核验”表格。
优先级用“优先核验/持续跟踪/证据不足”，依据证据完整度和催化明确性，不给虚假的精确概率或收益率。
聊天中的业绩、估值、价格和收益率均为来源说法，未经独立行情数据核验不得称为市场事实。
不得编造现价、目标价、涨跌幅、指数表现、财务数据、仓位或买卖点；缺少行情数据时列为数据缺口。
明确区分“值得研究”与“可以买入”，不给没有风险预算支撑的仓位建议。
引用尽量写明来源和时间，并附 #<id>；邮件等非微信来源注明渠道，不能把不同渠道的同号 ID 合并。
不要把会议邀请、祝福、文件后缀或热度高当作投资机会；注意来源之间可能是同一研报转发。
输出保持紧凑，建议不超过 1800 字。摘要截取、样本限制或旧资料需说明，不声称检索了未提供的数据。
""".strip()


MODULE_FIELDS = {
    "market": "market_markdown", "meetings": "meetings_markdown",
    "counter": "counter_markdown", "contacts": "top_contacts_markdown",
    "newswatch": "newswatch_markdown", "socialwatch": "socialwatch_markdown",
    "mediawatch": "mediawatch_markdown", "mpwatch": "mpwatch_markdown",
    "minuteswatch": "minuteswatch_markdown",
}


def select_evidence(rows, *, max_chars=32000, excerpt_chars=900):
    """Deduplicate forwards, then interleave sources instead of one chat dominating."""
    groups = {}
    seen = set()
    duplicate_count = 0
    for row in rows:
        text = str(row.get("summary") or row.get("content") or row.get("title") or "").strip()
        fingerprint = "".join(text.split()).casefold()
        if not fingerprint:
            continue
        if fingerprint in seen:
            duplicate_count += 1
            continue
        seen.add(fingerprint)
        item = {k: row.get(k) for k in ("id", "time", "sender", "source", "rating", "talker", "channel", "url", "evidence_basis") if row.get(k) is not None}
        item["summary" if row.get("summary") else "content"] = text[:excerpt_chars]
        item["excerpted"] = len(text) > excerpt_chars
        source = str(row.get("sender") or row.get("source") or row.get("talker") or "unknown")
        groups.setdefault(source, []).append(item)
    selected, used = [], 0
    # Rows retain recency order within each source.
    from collections import deque
    queues = deque(deque(items) for items in groups.values())
    while queues:
        group = queues.popleft()
        item = group.popleft()
        cost = len(json.dumps(item, ensure_ascii=False))
        if used + cost <= max_chars:
            selected.append(item)
            used += cost
        if group:
            queues.append(group)
    return selected, {
        "candidate_count": len(rows), "unique_count": len(seen),
        "duplicate_count": duplicate_count, "selected_count": len(selected),
        "omitted_count": max(0, len(seen) - len(selected)),
        "excerpted_count": sum(bool(item["excerpted"]) for item in selected),
        "input_chars": used,
    }


def run_modules(payload, run_one, publish, *, concurrency=2):
    modules = list(dict.fromkeys(payload["modules"]))
    concurrency = max(1, min(2, int(concurrency or 2)))
    started = time()
    state = {
        "status": "running", "modules": modules, "report": {}, "module_errors": {},
        "progress": {"phase": "analyzing", "total": len(modules), "finished": 0,
                     "succeeded": 0, "failed": 0, "empty": 0, "concurrency": concurrency,
                     "started_at": started,
                     "modules": {m: {"state": "queued"} for m in modules}},
    }
    lock = Lock()

    def emit():
        state["progress"]["elapsed_seconds"] = round(time() - started, 1)
        publish(deepcopy(state))

    def work(module):
        with lock:
            state["progress"]["modules"][module] = {"state": "running", "started_at": time()}
            emit()

        def evidence_ready(coverage):
            with lock:
                state["progress"]["modules"][module]["coverage"] = coverage
                emit()

        return run_one({**payload, "modules": [module], "_evidence_ready": evidence_ready})

    emit()
    with ThreadPoolExecutor(max_workers=concurrency, thread_name_prefix="analysis") as pool:
        futures = {pool.submit(work, m): m for m in modules}
        for future in as_completed(futures):
            module = futures[future]
            try:
                output = future.result()
            except Exception:
                output = {"status": "error", "module_errors": {module: "模块执行失败，请检查执行通道"}}
            error = (output.get("module_errors") or {}).get(module)
            if output.get("status") == "error" and not error:
                error = "模块执行失败，请检查执行通道"
            with lock:
                entry = state["progress"]["modules"][module]
                empty = not error and entry.get("coverage", {}).get("selected_count") == 0
                entry.update(state="failed" if error else "empty" if empty else "done", elapsed_seconds=round(time() - entry["started_at"], 1))
                report = output.get("result") or {}
                field = MODULE_FIELDS[module]
                state["report"][field] = report.get(field) or ("本模块分析失败，可单独重试。" if error else "当前范围暂无可分析资料。")
                html_field = field.replace("_markdown", "_html")
                if report.get(html_field):
                    state["report"][html_field] = report[html_field]
                chart = (report.get("analysis_data") or {}).get(module)
                if chart and not error:
                    state["report"].setdefault("analysis_data", {})[module] = chart
                if error:
                    state["module_errors"][module] = error
                progress = state["progress"]
                progress["finished"] += 1
                progress["failed" if error else "empty" if empty else "succeeded"] += 1
                emit()
    failed = state["progress"]["failed"]
    state["status"] = "error" if failed == len(modules) else "partial" if failed else "ok"
    state["progress"]["phase"] = "complete"
    emit()
    return {**state, "result": state["report"]}
