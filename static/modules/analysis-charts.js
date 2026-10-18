/* Data-backed research views; shared by standalone and embedded AI analysis. */
(() => {
    const names = {market:'市场观点', newswatch:'新闻舆情', counter:'分歧观点', meetings:'会议路演',
        contacts:'联系人', mediawatch:'自媒体', mpwatch:'公众号', minuteswatch:'会议纪要'};
    const tones = [['bullish', '看好'], ['bearish', '看空'], ['neutral', '中性']];
    const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
    const count = value => Math.max(0, Math.floor(Number(value) || 0));
    let current = null;
    let selected = 'market';
    let sort = 'volume';
    try { selected = sessionStorage.getItem('deepsee.charts.module') || selected; } catch (_) {}

    function render(data) {
        current = data;
        const host = document.getElementById('analysisResearchBoard');
        if (!host) return;
        const charts = data?.analysis_data || {};
        const modules = Object.keys(names).filter(key => charts[key]);
        host.hidden = !data;
        if (!data) { host.replaceChildren(); return; }
        if (!modules.length) {
            host.innerHTML = '<div class="research-empty"><strong>研究数据视图</strong><span>这份报告暂无结构化数据。重新生成后，将展示主题分布、观点分歧与证据覆盖；原报告仍可阅读。</span></div>';
            return;
        }
        if (!modules.includes(selected)) selected = modules[0];
        const expanded = new Set(host.dataset.chartModule === selected
            ? [...host.querySelectorAll('.research-topic[open]')].map(el => el.dataset.topic) : []);
        host.dataset.chartModule = selected;
        const chart = charts[selected];
        const c = chart.coverage || {};
        const topics = (chart.topics || []).filter(t => count(t.counts?.total) > 0);
        const disputed = topics.filter(t => count(t.counts.bullish) && count(t.counts.bearish));
        const sorted = [...topics].sort((a,b) => sort === 'division'
            ? Math.min(count(b.counts.bullish),count(b.counts.bearish)) - Math.min(count(a.counts.bullish),count(a.counts.bearish)) || b.counts.total-a.counts.total
            : b.counts.total-a.counts.total);
        const max = Math.max(1, ...topics.map(t => count(t.counts.total)));
        const rows = sorted.slice(0, 12).map((t, i) => {
            const n = count(t.counts.total);
            const split = tones.map(([key, label]) => `${label} ${count(t.counts[key])}`).join(' · ');
            const bars = tones.map(([key]) => `<span class="research-segment ${key}" style="width:${count(t.counts[key])/max*100}%"></span>`).join('');
            const refs = tones.map(([key, label]) => {
                const ids = t[`${key}_ids`] || [];
                const badges = ids.map(id => {
                    // Route identifiers by their actual module; never open email IDs as WeChat.
                    const attr = selected === 'newswatch' ? 'data-news-id' : /^\d+$/.test(String(id)) ? 'data-msg-id' : '';
                    return attr ? `<button type="button" class="msg-badge" ${attr}="${esc(id)}" aria-label="查看${esc(label)}证据 ${esc(id)}">#${esc(id)}</button>` : `<span class="research-ref">${esc(id)}</span>`;
                }).join(' ');
                return `<div class="research-refs"><b>${label} ${ids.length}</b><span>${badges || '无'}</span></div>`;
            }).join('');
            return `<details class="research-topic" data-topic="${esc(t.topic)}" ${expanded.has(t.topic)?'open':''}><summary><span class="research-rank">${String(i+1).padStart(2,'0')}</span><span class="research-topic-main"><span class="research-topic-label"><strong>${esc(t.topic)}</strong><b>${n}<small> 条</small></b></span><span class="research-track" aria-hidden="true">${bars}</span><span class="research-split">${split}</span></span><span class="research-expand" aria-hidden="true">＋</span></summary><div class="research-topic-detail">${refs}</div></details>`;
        }).join('');
        const coverageRows = [['采用资料',c.selected_count,'selected'], ['合并重复',c.duplicate_count,'duplicate'], ['预算未纳入',c.omitted_count,'omitted']];
        const base = Math.max(1, count(c.candidate_count));
        const coverage = coverageRows.map(([label,n,cls]) => `<div class="research-coverage-row"><span>${label}</span><div class="research-small-track"><i class="${cls}" style="width:${Math.min(100,count(n)/base*100)}%"></i></div><b>${count(n)}</b></div>`).join('');
        const channels = {wechat:'微信', email:'邮件', news:'新闻', media:'自媒体', mp:'公众号'};
        const sources = (chart.sources || []).map(s => `<div class="research-source"><span>${esc(channels[s.name] || s.name)}</span><span class="research-source-track"><i style="width:${Math.min(100,count(s.count)/Math.max(1,count(c.selected_count))*100)}%"></i></span><b>${count(s.count)}</b></div>`).join('');
        const date = new Date(chart.generated_at || NaN);
        const stamp = Number.isNaN(date.getTime()) ? '生成时间未记录' : `生成于 ${date.toLocaleString('zh-CN', {month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'})}`;
        host.innerHTML = `<header class="research-heading"><div><span class="research-eyebrow">RESEARCH LENS / 研究透视</span><h2>从信息，到研究线索</h2></div><span class="research-stamp">${esc(stamp)} · 已保存结果</span></header>
            <div class="research-toolbar"><div class="research-tabs" role="group" aria-label="图表模块">${modules.map(m => `<button type="button" data-chart-module="${m}" aria-pressed="${m===selected}">${names[m]}</button>`).join('')}</div><span>当前图表：${names[selected]}</span></div>
            <div class="research-metrics"><div><span>采用证据</span><strong>${count(c.selected_count)}<small> / ${count(c.unique_count)}</small></strong><em>去重后资料</em></div><div><span>识别主题</span><strong>${topics.length || '—'}</strong><em>${topics.length ? '依据可追溯样本' : '本模块暂无主题数据'}</em></div><div><span>多空并存</span><strong>${topics.length ? disputed.length : '—'}</strong><em>存在相反观点的主题</em></div><div><span>长文截取</span><strong>${count(c.excerpted_count)}</strong><em>采用资料中的截取条数</em></div></div>
            <div class="research-layout"><section class="research-topic-panel"><div class="research-panel-heading"><div><h3>主题与观点分布</h3><p>条长表示样本量 · 展开查看引用</p></div><label class="research-sort">排序 <select aria-label="主题排序"><option value="volume" ${sort==='volume'?'selected':''}>样本量</option><option value="division" ${sort==='division'?'selected':''}>双向证据数</option></select></label></div>
            <div class="research-legend"><span><i class="bullish"></i>看好</span><span><i class="bearish"></i>看空</span><span><i class="neutral"></i>中性</span></div>
            ${rows || '<div class="research-no-topics"><strong>暂未返回可核验的主题分类</strong><p>仍可阅读下方文字分析。图表需要模型返回带证据 ID 的分类，不会从正文猜测多空比例。</p></div>'}
            <p class="research-footnote">${topics.length>12?`展示前 12 / ${topics.length} 个主题。`:''}同一证据可能涉及多个主题，不能跨主题相加。观点由 AI 分类，样本分布不代表涨跌概率或投资胜率。</p></section>
            <aside class="research-evidence-panel"><div class="research-panel-heading"><div><h3>证据覆盖</h3><p>本模块候选 ${count(c.candidate_count)} 条</p></div><span class="research-section-number">02</span></div>${coverage}<p class="research-footnote">${count(c.excerpted_count)} 条长文经过截取；预算未纳入不等于无价值。空内容不计入去重资料。</p><div class="research-source-heading">采用资料的渠道</div>${sources || '<p class="research-footnote">暂无渠道记录</p>'}<div class="research-reading-note"><b>研究提示</b><p>先看双向证据，再核验催化与失效条件。热度反映讨论，不等于确定性。</p></div></aside></div>`;
        host.querySelectorAll('[data-chart-module]').forEach(button => button.addEventListener('click', () => {
            selected = button.dataset.chartModule;
            try { sessionStorage.setItem('deepsee.charts.module', selected); } catch (_) {}
            render(current);
            host.querySelector(`[data-chart-module="${selected}"]`)?.focus({preventScroll:true});
        }));
        host.querySelector('select').addEventListener('change', event => {
            sort = event.target.value; render(current); host.querySelector('select').focus({preventScroll:true});
        });
    }
    window.DeepseeAnalysisCharts = {render};
})();
