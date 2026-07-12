let csrf = '';
let nodes = [];
let sites = [];
let tlsStatuses = new Map();
let publishStatuses = new Map();
let certificatePollTimer = null;
let publishPollTimer = null;

const nodeStatusLabels = {
  pending: '待激活',
  active: '运行中',
  draining: '排空中',
  revoked: '已撤销',
};
const taskStatusLabels = {
  queued: '排队中',
  dispatching: '分发中',
  applying: '应用中',
  succeeded: '成功',
  partial: '部分成功',
  failed: '失败',
  rolled_back: '已回滚',
};
const numberFormatter = new Intl.NumberFormat('zh-CN');

const byId = (id) => document.getElementById(id);
const split = (value) => value.split(',').map((item) => item.trim()).filter(Boolean);
const certificateTaskActive = (task) => task && ['queued', 'dispatching', 'applying'].includes(task.status);
const publishTaskActive = (task) => task && ['queued', 'dispatching', 'applying'].includes(task.status);
const nodeStatusLabel = (status) => nodeStatusLabels[status] || status;
const taskStatusLabel = (status) => taskStatusLabels[status] || status;

async function request(path, options = {}) {
  const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) };
  if (csrf && options.method && options.method !== 'GET') headers['X-CSRF-Token'] = csrf;
  const response = await fetch(path, { ...options, headers });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `请求失败（HTTP ${response.status}）`);
  return data;
}

function notice(message, success = false) { const box = byId('notice'); box.textContent = message; box.className = success ? 'success' : ''; }
function show(id) { byId(id).classList.remove('hidden'); }
function hide(id) { byId(id).classList.add('hidden'); }

async function refresh() {
  [nodes, sites] = await Promise.all([request('/api/nodes'), request('/api/sites')]);
  byId('node-count').textContent = numberFormatter.format(nodes.length);
  byId('active-node-count').textContent = numberFormatter.format(nodes.filter((node) => node.status === 'active').length);
  byId('site-count').textContent = numberFormatter.format(sites.length);
  byId('site-list-meta').textContent = `${numberFormatter.format(sites.length)} 个站点 · ${numberFormatter.format(sites.filter((site) => site.enabled && site.published).length)} 个已发布`;
  byId('node-table').innerHTML = nodes.map((node) => `<tr><td>${escapeHTML(node.name)}</td><td><code>${escapeHTML(node.id)}</code></td><td><code>${escapeHTML(node.public_ipv4)}</code></td><td><span class="status ${node.status}">${escapeHTML(nodeStatusLabel(node.status))}</span></td><td>${node.last_heartbeat_at ? formatDateTime(node.last_heartbeat_at) : '从未上报'}</td><td class="actions">${node.status !== 'revoked' ? `<button class="small enroll" data-id="${node.id}">获取部署命令</button>` : ''} ${node.status === 'active' ? `<button class="small secondary node-status" data-id="${node.id}" data-status="draining">排空</button>` : ''} <button class="small ${node.status === 'revoked' ? 'secondary' : 'danger'} node-status" data-id="${node.id}" data-status="${node.status === 'revoked' || node.status === 'draining' ? 'active' : 'revoked'}">${node.status === 'revoked' || node.status === 'draining' ? '启用' : '撤销'}</button></td></tr>`).join('');
  renderSites();
  renderNodeSelector(selectedNodeIDs());
  void refreshTraffic();
  await Promise.all([refreshTLSStatuses(), refreshPublishStatuses()]);
}

function renderSites() {
  byId('site-table').innerHTML = sites.map((site) => {
    const tlsStatus = tlsStatuses.get(site.id);
    const publishStatus = publishStatuses.get(site.id);
    const task = tlsStatus?.certificate_task || null;
    const active = certificateTaskActive(task);
    return `<article class="site-row">
      <div class="site-identity">
        <div class="site-name-line"><h2>${escapeHTML(site.name)}</h2>${siteStateMarkup(site)}</div>
        <div class="site-domain-list">${site.domains.map((domain) => `<span>${escapeHTML(domain)}</span>`).join('')}</div>
      </div>
      <dl class="site-facts">
        <div><dt>协议</dt><dd>${escapeHTML(siteProtocol(site))}</dd></div>
        <div><dt>节点</dt><dd>${numberFormatter.format(site.node_ids.length)} 个</dd></div>
        <div><dt>缓存</dt><dd>${siteCacheMarkup(site)}</dd></div>
        <div><dt>TLS</dt><dd>${tlsStatusMarkup(tlsStatus)}</dd></div>
		<div><dt>发布</dt><dd>${publishStatusMarkup(publishStatus)}</dd></div>
      </dl>
		${publishDetailMarkup(publishStatus)}
      <div class="site-actions">
        <button class="small publish" data-id="${site.id}" ${publishTaskActive(publishStatus?.task) ? 'disabled' : ''}>${site.published ? (publishStatus?.task?.status === 'failed' || publishStatus?.task?.status === 'partial' ? '重新发布' : '发布') : '发布'}</button>
        <button class="small secondary edit-site" data-id="${site.id}">编辑</button>
        <button class="small secondary certificate" data-id="${site.id}" ${active ? 'disabled title="TLS 证书签发正在进行中"' : ''}>申请 TLS</button>
        <details class="action-menu"><summary>更多</summary><div class="action-menu-panel">${siteCacheable(site) ? `<button class="menu-action invalidate" data-id="${site.id}">刷新缓存</button>` : ''}<button class="menu-action allowlist" data-id="${site.id}">源站 CIDR</button></div></details>
      </div>
    </article>`;
  }).join('') || '<div class="site-empty">暂无站点</div>';
}

function publishStatusMarkup(publishStatus) {
  const task = publishStatus?.task;
  if (!task) return '<span class="fact-muted">未发布</span>';
  const label = taskStatusLabel(task.status);
  return `<span class="status ${escapeHTML(task.status)}"${task.detail ? ` title="${escapeHTML(task.detail)}"` : ''}>${escapeHTML(label)}</span>`;
}

function publishDetailMarkup(publishStatus) {
  const task = publishStatus?.task;
  if (!task || !['failed', 'partial'].includes(task.status) || !(publishStatus.nodes || []).length) return '';
  const entries = publishStatus.nodes.filter((node) => node.status !== 'succeeded').map((node) => {
    const conflict = (node.port_conflicts || []).map((item) => `端口 ${item.port} 被 ${item.process}${item.pid ? `（PID ${item.pid}）` : ''} 占用`).join('；');
    const detail = conflict || node.detail || '节点未确认目标配置';
    return `<li><strong>${escapeHTML(node.node_name)}</strong>：${escapeHTML(detail)}</li>`;
  });
  if (!entries.length) return '';
  return `<details class="publish-detail"><summary>查看发布详情</summary><ul>${entries.join('')}</ul></details>`;
}

function siteStateMarkup(site) {
  if (!site.enabled) return '<span class="status disabled">已停用</span>';
  if (!site.published) return '<span class="status pending">待发布</span>';
  return '<span class="status active">已发布</span>';
}

function siteCacheMarkup(site) {
  if (site.passthrough) return '<span class="fact-muted">透传</span>';
  if (!siteCacheable(site)) return '<span class="fact-muted">不缓存</span>';
  const streamSuffix = site.stream_paths?.length ? '<small>流式路径除外</small>' : '';
  return `<span>v${numberFormatter.format(site.cache_generation)}</span>${streamSuffix}`;
}

function tlsStatusMarkup(tlsStatus) {
	const task = tlsStatus?.certificate_task;
  if (!task) return '<span class="fact-muted">未申请</span>';
  if (certificateTaskActive(task)) return `<span class="status ${escapeHTML(task.status)}">${escapeHTML(taskStatusLabel(task.status))}</span>`;
  if (task.status === 'succeeded') {
    return `<span class="status succeeded">${tlsStatus.published_after_certificate ? '已签发' : '已签发，待发布'}</span>`;
  }
  if (task.status === 'failed') return `<span class="status failed" title="${escapeHTML(task.detail || '')}">签发失败</span>`;
  return `<span class="status ${escapeHTML(task.status)}">${escapeHTML(taskStatusLabel(task.status))}</span>`;
}

function originScheme(site) {
  try { return new URL(site.primary_origin.url).protocol.replace(':', ''); } catch (_) { return ''; }
}

function siteProtocol(site) {
  const scheme = originScheme(site);
  if (scheme === 'grpc' || scheme === 'grpcs') return 'gRPC';
  if (scheme === 'ws' || scheme === 'wss') return 'WebSocket';
  if (site.stream_paths?.length) return 'HTTP + WebSocket / SSE';
  return 'HTTP';
}

function siteCacheable(site) {
  const scheme = originScheme(site);
  return !site.passthrough && (scheme === 'http' || scheme === 'https') && !(site.stream_paths || []).includes('/');
}

async function refreshTLSStatuses() {
  const results = await Promise.all(sites.map(async (site) => {
    try { return [site.id, await request(`/api/sites/${site.id}/tls-status`)]; } catch (_) { return [site.id, null]; }
  }));
  tlsStatuses = new Map(results.filter(([, status]) => status));
  renderSites();
  scheduleCertificatePoll();
}

async function refreshPublishStatuses() {
  const previous = publishStatuses;
  const results = await Promise.all(sites.map(async (site) => {
    try { return [site.id, await request(`/api/sites/${site.id}/publish-status`)]; } catch (_) { return [site.id, null]; }
  }));
  publishStatuses = new Map(results.filter(([, status]) => status));
  for (const [siteID, status] of publishStatuses) {
    const priorStatus = previous.get(siteID)?.task?.status;
    const currentTask = status?.task;
    if (publishTaskActive({ status: priorStatus }) && ['failed', 'partial'].includes(currentTask?.status)) {
      const site = sites.find((item) => item.id === siteID);
      notice(`${site?.name || '站点'} 发布${taskStatusLabel(currentTask.status)}，请查看节点详情。`);
    }
  }
  renderSites();
  schedulePublishPoll();
}

function scheduleCertificatePoll() {
  window.clearTimeout(certificatePollTimer);
  certificatePollTimer = null;
  if ([...tlsStatuses.values()].some((status) => certificateTaskActive(status?.certificate_task))) {
    certificatePollTimer = window.setTimeout(() => {
      refreshTLSStatuses().catch((error) => notice(error.message));
    }, 2000);
  }
}

function schedulePublishPoll() {
  window.clearTimeout(publishPollTimer);
  publishPollTimer = null;
  if ([...publishStatuses.values()].some((status) => publishTaskActive(status?.task))) {
    publishPollTimer = window.setTimeout(() => {
      refreshPublishStatuses().catch((error) => notice(error.message));
    }, 2000);
  }
}

function selectedNodeIDs() {
  return [...document.querySelectorAll('#site-node-selector input:checked')].map((input) => input.value);
}

function renderNodeSelector(selected = []) {
  const selectedSet = new Set(selected);
  byId('site-node-selector').innerHTML = nodes.filter((node) => node.status !== 'revoked').map((node) => `<label class="node-option"><input type="checkbox" value="${node.id}" ${selectedSet.has(node.id) ? 'checked' : ''}>${escapeHTML(node.name)} <code>${escapeHTML(node.public_ipv4)}</code></label>`).join('') || '<span class="muted">请先创建待激活或运行中的节点。</span>';
}

async function refreshTraffic() {
  const summaries = await Promise.all(sites.map(async (site) => {
    try {
      const points = await request(`/api/sites/${site.id}/metrics`);
      return { site, points };
    } catch (_) {
      return { site, points: [] };
    }
  }));
  byId('traffic-table').innerHTML = summaries.map(({ site, points }) => {
    const total = points.reduce((result, point) => ({ requests: result.requests + Number(point.requests || 0), bytes: result.bytes + Number(point.bytes || 0), errors: result.errors + Number(point.errors || 0), hits: result.hits + Number(point.cache_hits || 0) }), { requests: 0, bytes: 0, errors: 0, hits: 0 });
    const hitRate = total.requests ? `${((total.hits / total.requests) * 100).toFixed(1)}%` : '-';
    return `<tr><td>${escapeHTML(site.name)}</td><td>${numberFormatter.format(total.requests)}</td><td>${formatBytes(total.bytes)}</td><td>${numberFormatter.format(total.errors)}</td><td>${hitRate}</td></tr>`;
  }).join('') || '<tr><td colspan="5" class="muted">暂无站点。</td></tr>';
}

function formatBytes(value) {
  if (!value) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  const digits = index ? 1 : 0;
  const formatted = Number(value / (1024 ** index)).toLocaleString('zh-CN', { minimumFractionDigits: digits, maximumFractionDigits: digits });
  return `${formatted} ${units[index]}`;
}

function formatDateTime(value) { return new Date(value).toLocaleString('zh-CN', { hour12: false }); }
function escapeHTML(value) { const element = document.createElement('div'); element.textContent = value || ''; return element.innerHTML; }

async function boot() {
  try {
    const session = await fetch('/api/session');
    if (session.ok) {
      const data = await session.json();
      csrf = data.csrf_token || '';
      showApp();
      return;
    }
    const status = await request('/api/setup/status');
    if (status.initialized) show('login-panel'); else show('setup-panel');
  } catch (error) { show('setup-panel'); }
}

function showApp() { hide('setup-panel'); hide('login-panel'); show('app'); show('logout'); refresh().catch((error) => notice(error.message)); }

byId('setup-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const password = byId('setup-password').value;
  if (password !== byId('setup-password-confirm').value) return notice('两次输入的密码不一致');
  try {
    const result = await request('/api/setup', { method: 'POST', body: JSON.stringify({ password }) });
    byId('setup-result').textContent = `TOTP 密钥：${result.totp_secret}\n\n恢复代码（请离线保存，每个代码只能使用一次）：\n${(result.recovery_codes || []).join('\n')}\n\n请将 TOTP 密钥添加到身份验证器应用，然后登录。`;
    show('setup-result'); hide('setup-form');
  } catch (error) { notice(error.message); }
});

byId('login-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  try {
    const result = await request('/api/login', { method: 'POST', body: JSON.stringify({ password: byId('login-password').value, totp: byId('login-totp').value, recovery_code: byId('login-recovery').value }) });
    csrf = result.csrf_token; showApp();
  } catch (error) { notice(error.message); }
});

byId('logout').addEventListener('click', async () => { try { await request('/api/logout', { method: 'POST' }); } finally { window.clearTimeout(certificatePollTimer); window.clearTimeout(publishPollTimer); certificatePollTimer = null; publishPollTimer = null; tlsStatuses = new Map(); publishStatuses = new Map(); csrf = ''; hide('app'); hide('logout'); show('login-panel'); } });

document.querySelectorAll('.nav').forEach((button) => button.addEventListener('click', () => {
  document.querySelectorAll('.nav').forEach((item) => item.classList.remove('active')); button.classList.add('active');
  document.querySelectorAll('.view').forEach((item) => item.classList.add('hidden')); show(button.dataset.view);
}));

byId('show-node-form').addEventListener('click', () => show('node-form'));
byId('show-site-form').addEventListener('click', () => resetSiteForm());
document.querySelectorAll('.cancel').forEach((button) => button.addEventListener('click', () => { button.closest('form').classList.add('hidden'); if (button.closest('form').id === 'site-form') resetSiteForm(false); }));

byId('node-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  try { await request('/api/nodes', { method: 'POST', body: JSON.stringify({ name: byId('node-name').value, public_ipv4: byId('node-ip').value }) }); event.target.reset(); hide('node-form'); await refresh(); notice('节点已创建', true); } catch (error) { notice(error.message); }
});

byId('site-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const backup = byId('site-backup-url').value.trim();
  const payload = { name: byId('site-name').value, zone_id: byId('site-zone').value, domains: split(byId('site-domains').value), node_ids: selectedNodeIDs(), primary_origin: { url: byId('site-primary-url').value, host_header: byId('site-primary-host').value, enabled: true }, stream_paths: split(byId('site-stream-paths').value), passthrough: byId('site-passthrough').checked, enabled: byId('site-enabled').checked };
  if (backup) payload.backup_origin = { url: backup, host_header: byId('site-backup-host').value, enabled: true };
  const siteID = byId('site-id').value;
  try {
    await request(siteID ? `/api/sites/${siteID}` : '/api/sites', { method: siteID ? 'PUT' : 'POST', body: JSON.stringify(payload) });
    resetSiteForm(false); hide('site-form'); await refresh();
    notice(siteID ? '站点已更新，请发布以应用新配置。' : '站点已创建，请在发布前申请 TLS 证书。', true);
  } catch (error) { notice(error.message); }
});

function resetSiteForm(showForm = true) {
  byId('site-form').reset();
  byId('site-id').value = '';
  byId('site-zone').disabled = false;
  byId('site-passthrough').checked = false;
  byId('site-enabled').checked = true;
  byId('site-submit').textContent = '创建站点';
  renderNodeSelector();
  if (showForm) show('site-form');
}

function editSite(site) {
  byId('site-id').value = site.id;
  byId('site-name').value = site.name;
  byId('site-zone').value = site.zone_id;
  byId('site-zone').disabled = true;
  byId('site-domains').value = site.domains.join(', ');
  byId('site-primary-url').value = site.primary_origin.url;
  byId('site-primary-host').value = site.primary_origin.host_header || '';
  byId('site-backup-url').value = site.backup_origin?.url || '';
  byId('site-backup-host').value = site.backup_origin?.host_header || '';
  byId('site-stream-paths').value = (site.stream_paths || []).join(', ');
  byId('site-passthrough').checked = Boolean(site.passthrough);
  byId('site-enabled').checked = site.enabled;
  byId('site-submit').textContent = '保存更改';
  renderNodeSelector(site.node_ids);
  show('site-form');
}

document.addEventListener('click', async (event) => {
  const button = event.target.closest('button'); if (!button || !button.dataset.id) return;
  try {
    if (button.classList.contains('enroll')) { const result = await request(`/api/nodes/${button.dataset.id}/enrollment-token`, { method: 'POST' }); byId('node-command').textContent = result.install_command; show('node-command'); }
    if (button.classList.contains('node-status')) { await request(`/api/nodes/${button.dataset.id}/status`, { method: 'POST', body: JSON.stringify({ status: button.dataset.status }) }); await refresh(); }
    if (button.classList.contains('publish')) { const task = await request(`/api/sites/${button.dataset.id}/publish`, { method: 'POST' }); await refresh(); notice(`发布任务 ${task.id}：${taskStatusLabel(task.status)}`, true); }
    if (button.classList.contains('invalidate')) { const task = await request(`/api/sites/${button.dataset.id}/invalidate-cache`, { method: 'POST' }); await refresh(); notice(`缓存已刷新，任务 ${task.id}`, true); }
    if (button.classList.contains('certificate')) { const task = await request(`/api/sites/${button.dataset.id}/certificate`, { method: 'POST' }); tlsStatuses.set(button.dataset.id, { certificate_task: task, published_after_certificate: false }); renderSites(); scheduleCertificatePoll(); notice(`TLS 任务 ${task.id}：${taskStatusLabel(task.status)}`, true); }
    if (button.classList.contains('edit-site')) { const site = sites.find((item) => item.id === button.dataset.id); if (site) editSite(site); }
    if (button.classList.contains('allowlist')) { const result = await request(`/api/sites/${button.dataset.id}/origin-allowlist`); byId('site-allowlist').textContent = `源站防火墙或安全组应只允许以下边缘节点 IPv4 CIDR。添加、移除或撤销节点后请同步更新。\n\n${result.ipv4_cidrs.join('\n')}`; show('site-allowlist'); }
  } catch (error) { notice(error.message); }
});

boot();
