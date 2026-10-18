/* Progressive module results, shared by standalone and embedded pages. */
(() => {
    const titles = { market: '市场观点', meetings: '会议路演', counter: '分歧观点', contacts: '联系人',
        newswatch: '新闻', mediawatch: '自媒体', mpwatch: '公众号', minuteswatch: '会议纪要', socialwatch: '社交' };
    const states = { queued: '等待', running: '分析中', done: '已完成', empty: '无资料', failed: '失败' };
    const fields = { contacts: 'top_contacts_markdown' };
    const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

    function show(task, onRetry) {
        const host = document.getElementById('analysisJobProgress');
        if (!host) return;
        host.hidden = false;
        host.replaceChildren();
        const result = task.result || {};
        const p = result.progress || {};
        const top = document.createElement('div');
        top.className = 'analysis-job-heading';
        const label = document.createElement('strong');
        const finished = p.finished || 0;
        const total = p.total || (result.modules || []).length;
        const elapsed = task.status === 'pending' && p.started_at
            ? Math.max(0, Math.floor(Date.now() / 1000 - p.started_at)) : Math.floor(p.elapsed_seconds || 0);
        label.textContent = p.phase === 'preparing' ? '整理证据…' : `已处理 ${finished}/${total} · 完成 ${p.succeeded || 0} · 无资料 ${p.empty || 0} · 失败 ${p.failed || 0}`;
        const time = document.createElement('span');
        time.textContent = `${Math.floor(elapsed / 60)}分${String(elapsed % 60).padStart(2, '0')}秒 · ${p.concurrency || 2} 路并行`;
        top.append(label, time);
        const bar = document.createElement('progress');
        bar.max = total || 1;
        bar.value = finished;
        bar.setAttribute('aria-label', '模块完成进度');
        const list = document.createElement('div');
        list.className = 'analysis-job-modules';
        for (const module of result.modules || []) {
            const entry = (p.modules || {})[module] || { state: 'queued' };
            const card = document.createElement('div');
            card.className = 'analysis-job-module';
            card.dataset.state = entry.state;
            const name = document.createElement('span');
            name.textContent = titles[module] || module;
            const badge = document.createElement('b');
            badge.textContent = states[entry.state] || entry.state;
            const detail = document.createElement('small');
            const c = entry.coverage;
            detail.textContent = c
                ? `采用 ${c.selected_count}/${c.unique_count} 条去重资料 · 合并 ${c.duplicate_count} 条重复${c.omitted_count ? ` · 未纳入 ${c.omitted_count} 条` : ''}${c.excerpted_count ? ` · 截取 ${c.excerpted_count} 条长文` : ''}`
                : entry.state === 'queued' ? '等待执行名额' : entry.state === 'running' ? '准备资料 / 等待模型返回' : '';
            card.append(name, badge, detail);
            if (entry.state === 'failed') {
                detail.textContent = (result.module_errors || {})[module] || '执行失败';
                const retry = document.createElement('button');
                retry.type = 'button';
                retry.textContent = '单独重试';
                retry.disabled = task.status === 'pending';
                retry.addEventListener('click', () => onRetry(module));
                card.append(retry);
            }
            list.append(card);
        }
        host.append(top, bar, list);
        const note = document.createElement('small');
        note.className = 'analysis-job-note';
        note.textContent = task.status === 'pending'
            ? '已完成模块立即显示。其余卡片暂保留上次结果；长文本按证据预算截取，不代表全部历史。'
            : result.error || '分析范围以各模块实际采用的资料为准；失败模块可单独重试。';
        host.append(note);
    }

    async function watch(task, { onSections, onStatus, onRetry }) {
        localStorage.setItem('deepsee.analysis.activeTask', String(task.id));
        const rendered = new Map();
        let failures = 0;
        while (true) {
            const result = task.result || {};
            const entries = result.progress?.modules || {};
            const ready = (result.modules || []).filter(module => {
                const text = result.report?.[fields[module] || `${module}_markdown`];
                return text && (task.status !== 'pending' || ['done', 'empty', 'failed'].includes(entries[module]?.state))
                    && rendered.get(module) !== text;
            });
            if (ready.length) {
                onSections(result, ready);
                ready.forEach(module => rendered.set(module, result.report[fields[module] || `${module}_markdown`]));
            }
            show(task, onRetry);
            const p = result.progress;
            onStatus(task.status === 'pending'
                ? p ? `后台分析 · 已处理 ${p.finished || 0}/${p.total}，完成模块已展示` : '后台分析运行中…'
                : result.error || (p?.failed ? `完成 · ${p.failed} 个模块失败，可单独重试` : '分析完成'));
            if (task.status !== 'pending') {
                localStorage.removeItem('deepsee.analysis.activeTask');
                return result;
            }
            await sleep(2000);
            try {
                const response = await fetch(`/api/tasks/${task.id}`, { signal: AbortSignal.timeout(15000) });
                if (!response.ok) throw new Error(`状态读取失败 (${response.status})`);
                task = await response.json();
                failures = 0;
            } catch (error) {
                onStatus('状态连接暂时中断，正在重连；已完成的内容仍保留');
                if (++failures >= 5) throw new Error('进度连接中断。任务可能仍在后台运行，刷新页面可重新连接。');
            }
        }
    }
    window.DeepseeAnalysisJobs = { watch };
})();
