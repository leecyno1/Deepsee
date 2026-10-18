(() => {
    const root = document.getElementById('deepsee-developer');
    if (!root) return;
    const labels = {'messages:read':'微信消息', 'contacts:read':'联系人 / 标签', 'chats:read':'会话',
        'emails:read':'邮件', 'news:read':'新闻', 'media:read':'自媒体', 'mp:read':'公众号',
        'meetings:read':'会议', 'scores:read':'评分证据', 'attachments:read':'本地附件'};
    let projects = [], editing = null, loading = false, schema = null, logCursor = null, logsBusy = false;
    let testController = null, testVersion = 0;
    const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
    root.innerHTML = `
        <header class="dev-head"><div><div class="dev-kicker">DEEPSEE / DEVELOPER CONSOLE</div>
        <h3>连接数据，扩展你的应用</h3><p>从一枚项目密钥开始，让研究助手、内部应用与 Agent 安全读取本地数据。</p></div>
        <button id="dev-refresh" type="button">刷新状态 ↻</button></header>
        <div class="dev-address"><span class="dev-badge">只读 API · v1</span><code id="dev-base"></code><button id="dev-copy-base" type="button">复制地址</button><span>本机管理 · Bearer 认证</span></div>
        <div id="dev-metrics" class="dev-metrics" aria-label="接入概览"></div>
        <div id="dev-status" class="dev-status" role="status" aria-live="polite"></div>
        <nav class="dev-tabs" role="tablist" aria-label="开发者中心分组">
          <button id="dev-tab-projects" role="tab" aria-controls="dev-panel-projects" aria-selected="true" data-panel="projects">接入项目</button>
          <button id="dev-tab-explorer" role="tab" aria-controls="dev-panel-explorer" aria-selected="false" tabindex="-1" data-panel="explorer">接口与调试</button>
          <button id="dev-tab-logs" role="tab" aria-controls="dev-panel-logs" aria-selected="false" tabindex="-1" data-panel="logs">调用日志</button>
        </nav>
        <section id="dev-panel-projects" role="tabpanel" aria-labelledby="dev-tab-projects"><div class="dev-guide"><b>01 创建项目</b><span>选择最小数据权限</span><b>02 保存密钥</b><span>存入调用方环境变量</span><b>03 验证接入</b><span>在接口调试中读取能力清单</span></div>
        <div class="dev-layout"><div><form id="dev-form" class="dev-box">
        <h4 id="dev-form-title">创建接入项目</h4>
        <label class="dev-field">项目名称<input id="dev-name" required maxlength="80" placeholder="例如：研究助手"></label>
        <label class="dev-field">每分钟请求上限<input id="dev-rpm" type="number" min="1" max="10000" value="120" required></label>
        <fieldset class="dev-scope-fieldset"><legend>可读取的数据</legend><div id="dev-scopes" class="dev-scopes"></div></fieldset>
        <p>权限覆盖本实例对应类别的全部数据，暂不支持按群聊隔离。附件还需勾选微信消息。</p>
        <div class="dev-actions"><button id="dev-save" class="dev-primary" type="submit">创建项目与密钥</button><button id="dev-cancel" type="button">重置</button></div>
        </form></div><section class="dev-box"><div class="dev-section-head"><h4>接入项目</h4><span id="dev-project-count"></span></div><div id="dev-projects">尚未加载</div></section></div></section>
        <section id="dev-panel-explorer" role="tabpanel" aria-labelledby="dev-tab-explorer" hidden>
          <div class="dev-explorer-layout"><aside class="dev-box"><div class="dev-section-head"><h4>接口目录</h4><span class="dev-badge">GET</span></div><p>直接读取服务接口定义，不触发采集、AI 或微信发送。</p><label class="dev-field">查找接口<input id="dev-endpoint-search" type="search" placeholder="消息、联系人、search…"></label><div id="dev-endpoints" class="dev-endpoints"></div><button id="dev-schema" type="button">下载 OpenAPI 定义</button></aside>
          <div class="dev-box"><form id="dev-test-form"><div class="dev-section-head"><h4 id="dev-endpoint-title">选择接口</h4><span class="dev-badge">只读测试</span></div><code id="dev-endpoint-path"></code><p id="dev-endpoint-note"></p><div id="dev-params" class="dev-params"></div>
          <label class="dev-field">项目密钥<input id="dev-test-key" type="password" autocomplete="off" spellcheck="false" placeholder="粘贴已有密钥，仅本页临时使用"></label>
          <div class="dev-actions"><button id="dev-run" class="dev-primary" type="submit">发送只读请求</button><button id="dev-clear-key" type="button">清除密钥与结果</button></div><p class="dev-note">密钥不会写入浏览器存储或示例代码。请求直接访问当前服务，正常计入该项目额度。</p></form>
          <div class="dev-section-head dev-example-head"><h4>调用示例</h4><div class="dev-actions"><select id="dev-language" aria-label="示例语言"><option value="curl">cURL</option><option value="python">Python</option><option value="javascript">JavaScript / Node.js</option></select><button id="dev-copy-example" type="button">复制</button></div></div><pre id="dev-example" class="dev-code"></pre>
          <details class="dev-help"><summary>分页、增量同步与 CLI</summary><p>游标分页：保持筛选条件不变，将 meta.next_cursor 作为下一次 cursor，直到 has_more=false。消息和邮件可保存 meta.last_id，下一轮用 after_id 拉取新入库记录（不是修改追踪）。</p><p>文章、会议使用 offset 分页；聚合搜索按来源分组。只覆盖本地留存，不代表完整网络数据。附件需同时具备消息与附件权限，且媒体已留存；评分无验证样本时为空值。</p><pre class="dev-code"># 在 Deepsee 仓库目录运行；先安全设置 DEEPSEE_API_KEY
python scripts/deepsee.py capabilities
python scripts/deepsee.py search "关键词" --limit 5</pre><p>密钥放在服务端，不要嵌入 APP 安装包或网页。其他设备不能使用 127.0.0.1；需单独配置可达地址、HTTPS 和访问控制。不要代理开放 /api/developer。</p></details>
          <div class="dev-section-head"><h4>响应结果</h4><span id="dev-result-meta" role="status">尚未请求</span></div><p id="dev-result-hint" class="dev-note"></p><pre id="dev-result" class="dev-code dev-result">先用 /capabilities 验证密钥与可用权限。</pre></div></div>
        </section>
        <section id="dev-panel-logs" role="tabpanel" aria-labelledby="dev-tab-logs" hidden><div class="dev-box"><div class="dev-section-head"><h4>调用记录</h4><div class="dev-actions"><select id="dev-log-project" aria-label="筛选项目"><option value="">全部项目</option></select><select id="dev-log-outcome" aria-label="筛选调用结果"><option value="all">全部状态</option><option value="success">成功</option><option value="error">失败（含限流）</option><option value="limited">仅限流 429</option></select><button id="dev-log-refresh" type="button">刷新日志</button></div></div><p>最近记录保留 30 天，仅含已识别项目的请求；无效密钥请求不在此表。不会记录查询词、正文或密钥。</p>
        <div id="dev-logs" class="dev-log"></div><div class="dev-actions"><button id="dev-log-more" type="button" hidden>加载更早记录</button><span id="dev-log-state" role="status"></span></div></div></section>
        <dialog id="dev-key-dialog"><h4>请立即保存密钥</h4><p>完整密钥仅显示这一次。不要写进前端代码或提交到代码仓库。</p>
        <textarea id="dev-key" aria-label="新项目密钥" readonly spellcheck="false"></textarea><div class="dev-actions">
        <button id="dev-copy" type="button">复制密钥</button><button id="dev-use-key" type="button">带入接口调试</button><button id="dev-close" type="button" class="dev-primary">已保存，关闭</button></div></dialog>`;
    const $ = id => root.querySelector(`#${id}`);
    $('dev-base').textContent = `${location.origin}/api/v1`;
    for (const [scope, label] of Object.entries(labels)) {
        const node = document.createElement('label');
        const input = document.createElement('input'); input.type = 'checkbox'; input.value = scope;
        node.append(input, document.createTextNode(label)); $('dev-scopes').append(node);
    }
    const status = text => { $('dev-status').textContent = text; };
    async function request(path, method = 'GET', body) {
        const response = await fetch(`/api/developer/${path}`, {method, cache:'no-store',
            headers:{'X-Deepsee-Admin':'1', ...(body ? {'Content-Type':'application/json'} : {})},
            body: body ? JSON.stringify(body) : undefined});
        if (!response.ok) {
            if (response.status === 403) throw new Error('开发者中心仅支持本机同源访问，请在本机打开 http://127.0.0.1:8001/。');
            throw new Error(`操作失败（${response.status}），请检查权限、项目参数与服务状态。`);
        }
        return response.status === 204 ? null : response.json();
    }
    function reset() {
        editing = null; $('dev-form').reset(); $('dev-form-title').textContent = '创建接入项目'; $('dev-save').textContent = '创建项目与密钥';
    }
    function showKey(token) {
        $('dev-key').value = token; $('dev-key-dialog').showModal();
    }
    function button(text, fn) {
        const el = document.createElement('button'); el.type = 'button'; el.textContent = text;
        el.onclick = async () => {
            el.disabled = true;
            try { await fn(); } catch (error) { status(error.message); } finally { el.disabled = false; }
        }; return el;
    }
    function renderProjects() {
        const container = $('dev-projects'); container.replaceChildren();
        $('dev-project-count').textContent = `${projects.filter(p => p.enabled).length} 启用 / ${projects.length} 个项目`;
        const selection = $('dev-log-project').value;
        $('dev-log-project').innerHTML = '<option value="">全部项目</option>' + projects.map(p => `<option value="${esc(p.id)}">${esc(p.name)}</option>`).join('');
        $('dev-log-project').value = selection;
        if (!projects.length) { container.textContent = '还没有接入项目。先选择需要读取的数据，再创建密钥。'; return; }
        for (const project of projects) {
            const card = document.createElement('div'); card.className = 'dev-project';
            const title = document.createElement('strong'); title.textContent = project.name;
            const badge = document.createElement('span'); badge.className = 'dev-badge'; badge.dataset.enabled = project.enabled; badge.textContent = project.enabled ? '已启用' : '已停用';
            const info = document.createElement('small'); info.textContent = `${project.requests_per_minute} 次/分钟 · ${project.scopes.map(x => labels[x] || x).join('、')}`;
            const identity = document.createElement('small'); identity.textContent = project.source === 'environment' ? '由环境配置管理' : `密钥前缀 ${project.token_prefix}…`;
            card.append(title, badge, info, identity);
            if (project.created_at) { const date = document.createElement('small'); date.textContent = `创建于 ${new Date(project.created_at).toLocaleDateString()}${project.rotated_at ? ' · 最近轮换 ' + new Date(project.rotated_at).toLocaleString() : ''}`; card.append(date); }
            if (project.source === 'managed') {
                const actions = document.createElement('div'); actions.className = 'dev-actions';
                actions.append(button('编辑权限', () => {
                    editing = project; $('dev-form-title').textContent = '编辑项目'; $('dev-save').textContent = '保存权限与额度'; $('dev-name').value = project.name;
                    $('dev-rpm').value = project.requests_per_minute;
                    $('dev-scopes').querySelectorAll('input').forEach(el => { el.checked = project.scopes.includes(el.value); });
                    $('dev-name').focus();
                }), button('轮换密钥', async () => {
                    if (!window.confirm(`轮换 ${project.name} 的密钥？旧密钥将立即失效。`)) return;
                    const result = await request(`projects/${project.id}/rotate`, 'POST'); showKey(result.token); await load();
                }), button(project.enabled ? '停用' : '启用', async () => {
                    if (!window.confirm(`${project.enabled ? '停用' : '启用'} ${project.name}？`)) return;
                    await request(`projects/${project.id}`, project.enabled ? 'DELETE' : 'PUT', project.enabled ? undefined : {
                        name:project.name, scopes:project.scopes, requests_per_minute:project.requests_per_minute, enabled:true});
                    reset(); await load();
                })); card.append(actions);
            }
            container.append(card);
        }
    }
    function renderLogs(rows, append = false) {
        const container = $('dev-logs'); if (!append) container.replaceChildren();
        if (!rows.length) { container.textContent = '暂无已认证项目的调用记录。'; return; }
        const table = append ? container.querySelector('table') : document.createElement('table'); const head = document.createElement('tr');
        for (const label of ['时间', '项目', '接口', '状态', '请求编号']) { const th = document.createElement('th'); th.textContent = label; head.append(th); }
        if (!append) table.append(head);
        for (const row of rows) {
            const tr = document.createElement('tr');
            for (const value of [new Date(row.at).toLocaleString(), projects.find(p => p.id === row.project_id)?.name || row.project_id, row.route, row.status, row.request_id]) {
                const td = document.createElement('td'); td.textContent = String(value); if (value === row.status) td.dataset.error = row.status >= 400; tr.append(td);
            } table.append(tr);
        } container.append(table);
    }
    async function load() {
        if (loading) return; loading = true;
        try {
            const [result, overview, catalog] = await Promise.all([request('projects'), request('overview'), schema ? Promise.resolve(null) : request('catalog')]);
            projects = result.data; renderProjects();
            if (catalog) { schema = catalog.data; renderEndpoints(); selectEndpoint('/api/v1/capabilities'); }
            const stats = overview.data;
            $('dev-metrics').innerHTML = [[projects.filter(p=>p.enabled).length,'已启用项目'],[stats.requests,'24h 已识别请求'],[stats.success_rate === null ? '—' : `${stats.success_rate}%`,'24h 成功率'],[stats.failed,`24h 失败 · ${stats.rate_limited} 次限流`]].map(([value,label])=>`<div><b>${esc(value)}</b><span>${esc(label)}</span></div>`).join('');
            await loadLogs(); status('数据已刷新 · 修改配置需主动保存，已有密钥不会因刷新而改变');
        } catch (error) { status(error.message); } finally { loading = false; }
    }
    $('dev-form').onsubmit = async event => {
        event.preventDefault(); const submit = $('dev-form').querySelector('[type=submit]'); submit.disabled = true;
        try {
            const scopes = [...$('dev-scopes').querySelectorAll('input:checked')].map(el => el.value);
            if (!scopes.length) throw new Error('请至少选择一类可读取的数据。');
            if (scopes.includes('attachments:read') && !scopes.includes('messages:read')) throw new Error('读取附件还需要微信消息权限。');
            const body = {name:$('dev-name').value.trim(), requests_per_minute:Number($('dev-rpm').value), scopes};
            if (editing) body.enabled = editing.enabled;
            const result = await request(editing ? `projects/${editing.id}` : 'projects', editing ? 'PUT' : 'POST', body);
            if (result.token) showKey(result.token);
            reset(); await load();
        } catch (error) { status(error.message); } finally { submit.disabled = false; }
    };
    $('dev-cancel').onclick = reset; $('dev-refresh').onclick = load;
    $('dev-close').onclick = () => $('dev-key-dialog').close();
    $('dev-key-dialog').addEventListener('close', () => { $('dev-key').value = ''; $('dev-copy').textContent = '复制密钥'; });
    $('dev-copy').onclick = async () => {
        try { await navigator.clipboard.writeText($('dev-key').value); $('dev-copy').textContent = '已复制'; }
        catch { $('dev-key').select(); $('dev-copy').textContent = '请手动复制选中密钥'; }
    };
    function selectPanel(name) {
        root.querySelectorAll('[data-panel]').forEach(tab => {
            const selected = tab.dataset.panel === name; tab.setAttribute('aria-selected', String(selected)); tab.tabIndex = selected ? 0 : -1;
            $(`dev-panel-${tab.dataset.panel}`).hidden = !selected;
        });
    }
    root.querySelectorAll('[data-panel]').forEach((tab, index, tabs) => {
        tab.onclick = () => selectPanel(tab.dataset.panel);
        tab.onkeydown = event => {
            const offset = {ArrowRight:1, ArrowLeft:-1}[event.key];
            if (!offset && !['Home','End'].includes(event.key)) return;
            event.preventDefault(); const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + offset + tabs.length) % tabs.length;
            selectPanel(tabs[next].dataset.panel); tabs[next].focus();
        };
    });
    async function loadLogs(append = false) {
        if (logsBusy) return; logsBusy = true;
        ['dev-log-more','dev-log-refresh','dev-log-project','dev-log-outcome'].forEach(id => $(id).disabled = true);
        try {
            const query = new URLSearchParams({limit:'30', outcome:$('dev-log-outcome').value});
            if ($('dev-log-project').value) query.set('project_id',$('dev-log-project').value);
            if (append && logCursor) query.set('before_id', logCursor);
            const result = await request(`requests?${query}`); renderLogs(result.data, append);
            logCursor = result.meta.next_cursor; $('dev-log-more').hidden = !result.meta.has_more;
            $('dev-log-state').textContent = result.meta.has_more ? '可继续加载更早记录' : '已显示全部匹配记录';
        } catch (error) { $('dev-log-state').textContent = error.message; }
        finally { logsBusy = false; ['dev-log-more','dev-log-refresh','dev-log-project','dev-log-outcome'].forEach(id => $(id).disabled = false); }
    }
    const endpointTitles = {capabilities:'验证密钥与能力',messages:'微信消息',contacts:'联系人',labels:'通讯录标签',chats:'会话',emails:'邮件',articles:'新闻 / 自媒体 / 公众号',meetings:'会议',search:'跨来源搜索',scorecard:'联系人评分证据',attachment:'本地附件下载'};
    const parameterHints = {q:'关键词（字面匹配）',limit:'每页条数',cursor:'上一页 next_cursor',after_id:'上次同步 last_id',offset:'跳过条数',chat_id:'会话 ID',sender_id:'发送人 ID',since:'开始时间（ISO 8601）',until:'结束时间（不含）',source:'内容来源',sources:'来源，如 messages,news',contact_id:'联系人 ID',message_id:'消息 ID',email_id:'邮件 ID'};
    let endpoint = '/api/v1/capabilities';
    function titleFor(path) { const parts = path.split('/'); return (endpointTitles[parts.at(-1)] || endpointTitles[parts[3]] || path) + (path.endsWith('}') ? '详情' : ''); }
    function renderEndpoints() {
        if (!schema) return;
        const query = $('dev-endpoint-search').value.toLowerCase(); $('dev-endpoints').replaceChildren();
        for (const path of Object.keys(schema.paths)) {
            if (!(titleFor(path) + path).toLowerCase().includes(query)) continue;
            const el = button(titleFor(path), () => selectEndpoint(path)); el.dataset.path = path;
            el.setAttribute('aria-pressed', String(path === endpoint));
            const code = document.createElement('code'); code.textContent = path.replace('/api/v1',''); el.append(code); $('dev-endpoints').append(el);
        }
    }
    function selectEndpoint(path) {
        testController?.abort(); testVersion++;
        endpoint = path; const operation = schema.paths[path].get;
        $('dev-endpoint-title').textContent = titleFor(path); $('dev-endpoint-path').textContent = `GET ${path}`;
        const scopes = operation['x-deepsee-scopes'];
        $('dev-endpoint-note').textContent = path.endsWith('/articles') ? '按 source 使用对应权限：news / media / mp。仅读取本地留存。' : path.endsWith('/search') ? '不指定 sources 时，仅搜索该密钥已授权的来源；指定来源时须具备全部所选权限。' : `所需权限：${scopes.length ? scopes.map(s=>labels[s] || s).join(' + ') : '任意有效项目密钥'}。`;
        $('dev-params').innerHTML = (operation.parameters || []).map((param, index) => {
            const shape = param.schema.anyOf?.find(s=>s.type !== 'null') || param.schema;
            const value = param.name === 'limit' ? 5 : shape.default ?? '';
            const attrs = `id="dev-param-${index}" data-name="${esc(param.name)}" data-in="${param.in}" ${param.required ? 'required' : ''}`;
            const control = shape.enum ? `<select ${attrs}>${shape.enum.map(v=>`<option value="${esc(v)}">${esc(v)}</option>`).join('')}</select>` : `<input ${attrs} type="${shape.type === 'integer' ? 'number' : 'text'}" value="${esc(value)}" ${shape.minimum != null ? `min="${shape.minimum}"` : ''} ${shape.maximum != null ? `max="${shape.maximum}"` : ''} ${shape.maxLength != null ? `maxlength="${shape.maxLength}"` : ''} placeholder="${esc(parameterHints[param.name] || param.name)}">`;
            return `<label class="dev-field" for="dev-param-${index}">${esc(param.name)}${param.required ? ' · 必填' : ''}<small>${esc(parameterHints[param.name] || param.in)}</small>${control}</label>`;
        }).join('');
        $('dev-run').disabled = path.endsWith('/attachment');
        if (path.endsWith('/attachment')) $('dev-endpoint-note').textContent += ' 二进制附件请使用 CLI 下载，此处仅生成调用示例。';
        $('dev-result').textContent = '尚未请求此接口。'; $('dev-result-meta').textContent = '尚未请求'; $('dev-result-hint').textContent = '';
        renderEndpoints(); renderExample();
    }
    function requestPath() {
        let path = endpoint; const query = new URLSearchParams();
        $('dev-params').querySelectorAll('[data-name]').forEach(input => {
            const value = input.value.trim(); if (!value) return;
            if (input.dataset.in === 'path') path = path.replace(`{${input.dataset.name}}`, encodeURIComponent(value));
            else query.set(input.dataset.name, value);
        });
        return path + (query.size ? `?${query}` : '');
    }
    function renderExample() {
        const path = requestPath().replace('/api/v1','');
        const url = `${location.origin}/api/v1${path}`;
        const shell = value => "'" + value.replaceAll("'", "'\"'\"'") + "'";
        const examples = {
            curl: `# 先安全设置环境变量 DEEPSEE_API_KEY\ncurl --fail-with-body ${shell(url)} \\\n  -H "Authorization: Bearer $DEEPSEE_API_KEY"`,
            python: `import os, json\nfrom urllib.request import Request, urlopen\n\nrequest = Request(${JSON.stringify(url)}, headers={\n    "Authorization": "Bearer " + os.environ["DEEPSEE_API_KEY"]\n})\nwith urlopen(request, timeout=30) as response:\n    print(json.dumps(json.load(response), ensure_ascii=False, indent=2))`,
            javascript: `// Node.js 18+，不要在公开网页中嵌入密钥\nconst response = await fetch(${JSON.stringify(url)}, {\n  headers: { Authorization: \`Bearer \${process.env.DEEPSEE_API_KEY}\` },\n  signal: AbortSignal.timeout(30000)\n});\nif (!response.ok) throw new Error(\`HTTP \${response.status}\`);\nconsole.log(await response.json());`
        };
        if (endpoint.endsWith('/attachment')) {
            examples.python = examples.python.replace('print(json.dumps(json.load(response), ensure_ascii=False, indent=2))','with open("attachment.bin", "xb") as output:\n        output.write(response.read())');
            examples.javascript = examples.javascript.replace('console.log(await response.json());','const { writeFile } = await import("node:fs/promises");\nawait writeFile("attachment.bin", Buffer.from(await response.arrayBuffer()), { flag: "wx" });');
            examples.curl += ' --output attachment.bin --no-clobber';
        }
        $('dev-example').textContent = examples[$('dev-language').value];
    }
    async function copy(text, message) {
        try { await navigator.clipboard.writeText(text); status(message); } catch { status('浏览器未允许复制，请手动选择文本复制。'); }
    }
    $('dev-copy-base').onclick = () => copy($('dev-base').textContent,'API 地址已复制');
    $('dev-copy-example').onclick = () => copy($('dev-example').textContent,'调用示例已复制（不包含密钥）');
    $('dev-params').oninput = renderExample; $('dev-language').onchange = renderExample;
    $('dev-endpoint-search').oninput = renderEndpoints;
    $('dev-use-key').onclick = () => { $('dev-test-key').value = $('dev-key').value; $('dev-key').value = ''; $('dev-key-dialog').close(); selectPanel('explorer'); $('dev-test-key').focus(); };
    $('dev-clear-key').onclick = () => { testController?.abort(); testVersion++; $('dev-run').disabled = endpoint.endsWith('/attachment'); $('dev-test-key').value = ''; $('dev-result').textContent = '密钥与结果已清除。'; $('dev-result-meta').textContent = '尚未请求'; $('dev-result-hint').textContent = ''; };
    window.addEventListener('pagehide', () => { $('dev-test-key').value = ''; $('dev-key').value = ''; });
    $('dev-schema').onclick = () => {
        if (!schema) return;
        const url = URL.createObjectURL(new Blob([JSON.stringify(schema,null,2)],{type:'application/json'}));
        const link = document.createElement('a'); link.href = url; link.download = 'deepsee-openapi.json'; link.click(); setTimeout(()=>URL.revokeObjectURL(url),1000);
    };
    $('dev-log-project').onchange = () => loadLogs(); $('dev-log-outcome').onchange = () => loadLogs();
    $('dev-log-refresh').onclick = () => loadLogs(); $('dev-log-more').onclick = () => loadLogs(true);
    $('dev-test-form').onsubmit = async event => {
        event.preventDefault(); if ($('dev-run').disabled) return;
        const key = $('dev-test-key').value.trim(); if (!key) { $('dev-test-key').focus(); status('请粘贴项目密钥后再测试。'); return; }
        const path = requestPath(); const started = performance.now(); $('dev-run').disabled = true;
        const version = ++testVersion; const controller = new AbortController(); testController = controller;
        const timeout = setTimeout(() => controller.abort(),15000);
        $('dev-result').textContent = '正在请求…'; $('dev-result-meta').textContent = '请求中'; $('dev-result-hint').textContent = '';
        try {
            const response = await fetch(path,{headers:{Authorization:`Bearer ${key}`},cache:'no-store',redirect:'error',signal:controller.signal});
            const result = await response.json();
            if (version !== testVersion) return;
            $('dev-result-meta').textContent = `HTTP ${response.status} · ${Math.round(performance.now()-started)} ms · 剩余额度 ${response.headers.get('X-RateLimit-Remaining') ?? '—'}`;
            const hints = {401:'密钥无效或已停用，请核对密钥；轮换后旧密钥立即失效。',403:'密钥缺少此接口的数据权限，请在接入项目中调整。',404:'记录或本地附件不存在。',422:'参数无效，请检查必填字段、范围与时间格式。',429:`请求过于频繁，请 ${response.headers.get('Retry-After') || '稍后'} 秒后重试。`,503:'服务未配置或当前不可用，请检查服务配置。'};
            $('dev-result-hint').textContent = (response.ok ? '请求成功。空数组表示当前权限与筛选条件下没有匹配数据，不代表连接失败。' : hints[response.status] || '接口异常，可凭请求编号在日志中定位。') + ` 请求编号：${response.headers.get('X-Request-ID') || '—'}`;
            const rendered = JSON.stringify(result,null,2); $('dev-result').textContent = rendered.slice(0,24000) + (rendered.length > 24000 ? '\n…预览已截断，请缩小 limit 或用代码读取完整结果。' : '');
        } catch { if (version === testVersion) { $('dev-result').textContent = '连接失败、超时或响应格式不正确，请检查服务状态。'; $('dev-result-meta').textContent = '请求未完成'; } }
        finally { clearTimeout(timeout); if (version === testVersion) $('dev-run').disabled = endpoint.endsWith('/attachment'); }
    };
    window.DeepseeDeveloper = {load};
})();
