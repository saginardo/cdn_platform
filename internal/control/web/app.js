let csrf = '';
let nodes = [];
let sites = [];
let tlsStatuses = new Map();
let publishStatuses = new Map();
let certificatePollTimer = null;
let publishPollTimer = null;
let uninstallPollTimer = null;
let uninstallNodeID = '';
let uninstallNodeName = '';
let uninstallCommand = '';
let uninstallActionPending = false;

const nodeStatusLabels = {
  pending: '待激活',
  active: '运行中',
  draining: '暂停中',
  revoked: '已撤销授权',
  uninstalling: '卸载中',
  uninstalled: '已卸载',
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
const defaultClientMaxBodySizeMB = 128;
const numberFormatter = new Intl.NumberFormat('zh-CN');
const consoleViews = new Set(['overview', 'nodes', 'sites']);
const viewLabels = { overview: '概览', nodes: '节点', sites: '站点' };
const mobileSidebarQuery = window.matchMedia('(max-width: 800px)');

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
  if (!response.ok) {
    const error = new Error(data.error || `请求失败（HTTP ${response.status}）`);
    error.status = response.status;
    error.data = data;
    throw error;
  }
  return data;
}

function notice(message, success = false) {
  const target = byId('app').classList.contains('hidden') ? 'auth-notice' : 'notice';
  const box = byId(target);
  box.textContent = message;
  box.className = success ? (target === 'auth-notice' ? 'auth-notice success' : 'success') : (target === 'auth-notice' ? 'auth-notice' : '');
}
function show(id) { byId(id).classList.remove('hidden'); }
function hide(id) { byId(id).classList.add('hidden'); }

async function refresh() {
  [nodes, sites] = await Promise.all([request('/api/nodes'), request('/api/sites')]);
  byId('node-count').textContent = numberFormatter.format(nodes.length);
  byId('active-node-count').textContent = numberFormatter.format(nodes.filter((node) => node.status === 'active').length);
  byId('site-count').textContent = numberFormatter.format(sites.length);
  byId('site-list-meta').textContent = `${numberFormatter.format(sites.length)} 个站点 · ${numberFormatter.format(sites.filter((site) => site.enabled && site.published).length)} 个已发布`;
  byId('node-table').innerHTML = nodes.map(renderNodeRow).join('');
  renderSites();
  renderNodeSelector(selectedNodeIDs());
  void refreshTraffic();
  await Promise.all([refreshTLSStatuses(), refreshPublishStatuses()]);
}

function renderNodeRow(node) {
  return `<tr><td>${escapeHTML(node.name)}</td><td><code>${escapeHTML(node.id)}</code></td><td><code>${escapeHTML(node.public_ipv4)}</code></td><td><span class="status ${escapeHTML(node.status)}">${escapeHTML(nodeStatusLabel(node.status))}</span></td><td>${node.last_heartbeat_at ? formatDateTime(node.last_heartbeat_at) : '从未上报'}</td><td class="actions">${renderNodeActions(node)}</td></tr>`;
}

function renderNodeActions(node) {
  const actions = [];
  if (['pending', 'active', 'draining'].includes(node.status)) {
    actions.push(`<button class="small enroll" data-id="${node.id}">获取部署/升级命令</button>`);
  }
  if (node.status === 'active') {
    actions.push(`<button class="small secondary node-status" data-id="${node.id}" data-status="draining">暂停调度</button>`);
  }
  if (node.status === 'draining') {
    actions.push(`<button class="small secondary node-status" data-id="${node.id}" data-status="active">启用调度</button>`);
  }
  if (node.status === 'revoked') {
    actions.push(`<button class="small secondary node-status" data-id="${node.id}" data-status="active">恢复并启用调度</button>`);
  }
  if (['pending', 'active', 'draining'].includes(node.status)) {
    actions.push(`<button class="small danger node-status" data-id="${node.id}" data-status="revoked">撤销授权</button>`);
  }
  if (['active', 'draining', 'revoked', 'uninstalling'].includes(node.status)) {
    actions.push(`<button class="small secondary node-uninstall" data-id="${node.id}">${node.status === 'uninstalling' ? '查看卸载' : '卸载节点'}</button>`);
  }
  if (node.status === 'pending' || node.status === 'uninstalled') {
    actions.push(`<button class="small ${node.status === 'uninstalled' ? 'danger' : 'secondary'} node-delete" data-id="${node.id}">删除记录</button>`);
  }
  return actions.join(' ');
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
        <div><dt>请求体</dt><dd>${numberFormatter.format(site.client_max_body_size_mb ?? defaultClientMaxBodySizeMB)} MiB</dd></div>
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
  byId('site-node-selector').innerHTML = nodes.filter((node) => !['revoked', 'uninstalling', 'uninstalled'].includes(node.status)).map((node) => `<label class="node-option"><input type="checkbox" value="${node.id}" ${selectedSet.has(node.id) ? 'checked' : ''}>${escapeHTML(node.name)} <code>${escapeHTML(node.public_ipv4)}</code></label>`).join('') || '<span class="muted">请先创建待激活或运行中的节点。</span>';
}

function uninstallBlockerText(blocker) {
  const site = blocker.site_name ? `站点「${blocker.site_name}」` : '站点';
  if (blocker.code === 'still_assigned') return `${site}仍分配了此节点，请先移除并保存。`;
  if (blocker.code === 'site_not_published') return `${site}移除节点后尚未重新发布。`;
  if (blocker.code === 'no_active_node') return `${site}没有其他运行中节点，请先分配并发布替代节点，或停用站点。`;
  return blocker.detail || '卸载前置条件尚未满足。';
}

function uninstallStateText(status) {
  const node = status.node;
  const job = status.job;
  if (node.status === 'uninstalled') {
    return job?.forced ? '已强制标记为卸载完成，边缘节点清理未经过回调验证。' : '边缘节点已回调确认卸载完成。';
  }
  if (!job || job.status === 'canceled') {
    if (node.status === 'active') return '请先暂停调度或撤销授权，再开始卸载准备。';
    if (node.status === 'pending') return '只有从未注册过的待激活节点可以直接删除记录。';
    return job?.status === 'canceled' ? '卸载流程已取消；节点保持暂停或撤销，托管 DNS 会在重新启用调度后由健康检查恢复。' : '准备后会移除该节点的托管 DNS 记录，并检查站点迁移状态。';
  }
  if (job.status === 'preparing' && status.can_generate_command) return '前置条件已满足，可生成一次性卸载命令，或在节点失联时强制完成。';
  if (job.status === 'preparing') return '正在等待 DNS 缓存过期，并检查站点迁移状态。';
  if (job.status === 'ready') return '前置条件已满足，可生成 30 分钟有效的一次性卸载命令。';
  if (job.status === 'running') return '边缘节点正在执行清理，完成后会自动回调控制面。';
  if (job.status === 'failed') return `边缘节点卸载失败：${job.detail || '未提供错误详情'}`;
  if (job.status === 'succeeded') return '边缘节点已回调确认卸载完成。';
  if (job.status === 'forced') return '已强制标记为卸载完成，边缘节点清理未经过回调验证。';
  return job.status;
}

function resetUninstallControls() {
  ['pause-node-for-uninstall', 'prepare-node-uninstall', 'generate-node-uninstall-command', 'cancel-node-uninstall', 'force-node-uninstall', 'delete-node-record', 'node-uninstall-confirm-wrap'].forEach(hide);
  hide('node-uninstall-blockers');
  hide('node-uninstall-countdown');
  hide('node-uninstall-scope');
  hide('node-uninstall-token-expiry');
}

function setUninstallError(message = '') {
  byId('node-uninstall-error').textContent = message;
  if (message) show('node-uninstall-error'); else hide('node-uninstall-error');
}

function setUninstallBusy(busy) {
  uninstallActionPending = busy;
  ['pause-node-for-uninstall', 'prepare-node-uninstall', 'generate-node-uninstall-command', 'cancel-node-uninstall', 'force-node-uninstall', 'delete-node-record'].forEach((id) => { byId(id).disabled = busy; });
}

function renderNodeUninstall(status) {
  resetUninstallControls();
  setUninstallError();
  uninstallNodeName = status.node.name;
  byId('node-uninstall-meta').textContent = `${status.node.name} · ${status.node.public_ipv4} · ${nodeStatusLabel(status.node.status)}`;
  byId('node-uninstall-confirm-label').textContent = `输入「${status.node.name}」以确认`;
  byId('node-uninstall-state').textContent = uninstallStateText(status);
  if (status.node.status !== 'pending') show('node-uninstall-scope');

  const blockers = status.blockers || [];
  if (blockers.length) {
    byId('node-uninstall-blockers').innerHTML = blockers.map((blocker) => `<li>${escapeHTML(uninstallBlockerText(blocker))}</li>`).join('');
    show('node-uninstall-blockers');
  }
  if (status.ready_in_seconds > 0) {
    byId('node-uninstall-countdown').textContent = `DNS 安全等待剩余 ${numberFormatter.format(status.ready_in_seconds)} 秒`;
    show('node-uninstall-countdown');
  }
  if (uninstallCommand) {
    byId('node-uninstall-command').textContent = uninstallCommand;
    show('node-uninstall-command');
    if (status.job?.token_expires_at) {
      byId('node-uninstall-token-expiry').textContent = `命令有效期至 ${formatDateTime(status.job.token_expires_at)}`;
      show('node-uninstall-token-expiry');
    }
  } else {
    hide('node-uninstall-command');
  }

  const jobStatus = status.job?.status;
  byId('generate-node-uninstall-command').textContent = uninstallCommand ? '重新生成卸载命令' : '生成卸载命令';
  if (!status.job && status.node.status === 'active') show('pause-node-for-uninstall');
  if ((!status.job || jobStatus === 'canceled') && ['draining', 'revoked'].includes(status.node.status)) show('prepare-node-uninstall');
  if (status.can_generate_command) show('generate-node-uninstall-command');
  if (['preparing', 'ready', 'failed'].includes(jobStatus)) show('cancel-node-uninstall');
  if (['preparing', 'ready', 'running', 'failed'].includes(jobStatus) && blockers.length === 0 && status.ready_in_seconds === 0) {
    show('force-node-uninstall');
    show('node-uninstall-confirm-wrap');
  }
  if (status.node.status === 'uninstalled' || status.node.status === 'pending') {
    show('delete-node-record');
    show('node-uninstall-confirm-wrap');
  }

  window.clearTimeout(uninstallPollTimer);
  uninstallPollTimer = null;
  if (jobStatus === 'running' || status.ready_in_seconds > 0) {
    uninstallPollTimer = window.setTimeout(() => loadNodeUninstallStatus().catch((error) => setUninstallError(error.message)), 2000);
  }
}

async function loadNodeUninstallStatus() {
  if (!uninstallNodeID) return;
  const nodeID = uninstallNodeID;
  const status = await request(`/api/nodes/${nodeID}/uninstall`);
  if (nodeID !== uninstallNodeID) return;
  renderNodeUninstall(status);
}

async function openNodeUninstall(nodeID) {
  uninstallNodeID = nodeID;
  uninstallNodeName = '';
  uninstallCommand = '';
  byId('node-uninstall-confirm').value = '';
  byId('node-uninstall-state').textContent = '正在读取卸载状态…';
  resetUninstallControls();
  setUninstallError();
  hide('node-uninstall-command');
  const dialog = byId('node-uninstall-dialog');
  if (!dialog.open) dialog.showModal();
  try {
    await loadNodeUninstallStatus();
  } catch (error) {
    closeNodeUninstall();
    notice(error.message);
  }
}

function closeNodeUninstall() {
  window.clearTimeout(uninstallPollTimer);
  uninstallPollTimer = null;
  uninstallNodeID = '';
  uninstallNodeName = '';
  uninstallCommand = '';
  const dialog = byId('node-uninstall-dialog');
  if (dialog.open) dialog.close();
}

async function runNodeUninstallAction(path, options, successMessage) {
  if (uninstallActionPending || !uninstallNodeID) return;
  const nodeID = uninstallNodeID;
  window.clearTimeout(uninstallPollTimer);
  uninstallPollTimer = null;
  setUninstallBusy(true);
  setUninstallError();
  try {
    const status = await request(path, options);
    if (nodeID === uninstallNodeID) {
      if (status.uninstall_command) uninstallCommand = status.uninstall_command;
      renderNodeUninstall(status);
    }
    await refresh();
    if (nodeID === uninstallNodeID) notice(successMessage, true);
  } catch (error) {
    if (nodeID === uninstallNodeID) {
      if (error.data?.uninstall) renderNodeUninstall(error.data.uninstall);
      setUninstallError(error.message);
    }
  } finally {
    setUninstallBusy(false);
  }
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

function viewFromLocation() {
  const view = window.location.hash.replace(/^#\/?/, '');
  return consoleViews.has(view) ? view : 'overview';
}

function sidebarOpen() { return document.body.classList.contains('sidebar-open'); }

function syncSidebarMode() {
  const open = mobileSidebarQuery.matches && sidebarOpen() && !byId('app').classList.contains('hidden');
  document.body.classList.toggle('sidebar-open', open);
  byId('sidebar-toggle').setAttribute('aria-expanded', String(open));
  byId('sidebar-toggle').setAttribute('aria-label', open ? '关闭导航' : '打开导航');
  if (mobileSidebarQuery.matches) byId('sidebar').setAttribute('aria-hidden', String(!open));
  else byId('sidebar').removeAttribute('aria-hidden');
}

function setSidebarOpen(open, restoreFocus = false) {
  const shouldOpen = Boolean(open && mobileSidebarQuery.matches && !byId('app').classList.contains('hidden'));
  document.body.classList.toggle('sidebar-open', shouldOpen);
  syncSidebarMode();
  if (shouldOpen) {
    window.requestAnimationFrame(() => (document.querySelector('.nav.active') || document.querySelector('.nav'))?.focus());
  } else if (restoreFocus && !byId('app').classList.contains('hidden')) {
    byId('sidebar-toggle').focus();
  }
}

function activateView(view) {
  const restoreSidebarFocus = sidebarOpen();
  document.querySelectorAll('.nav').forEach((button) => {
    const active = button.dataset.view === view;
    button.classList.toggle('active', active);
    if (active) button.setAttribute('aria-current', 'page'); else button.removeAttribute('aria-current');
  });
  document.querySelectorAll('.view').forEach((section) => section.classList.toggle('hidden', section.id !== view));
  byId('mobile-page-title').textContent = viewLabels[view] || viewLabels.overview;
  setSidebarOpen(false, restoreSidebarFocus);
}

function syncViewFromLocation() { activateView(viewFromLocation()); }

function showApp() {
  hide('setup-panel');
  hide('login-panel');
  hide('auth-shell');
  show('app');
  show('logout');
  byId('auth-notice').textContent = '';
  syncViewFromLocation();
  syncSidebarMode();
  refresh().catch((error) => notice(error.message));
}

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

byId('logout').addEventListener('click', async () => { try { await request('/api/logout', { method: 'POST' }); } finally { window.clearTimeout(certificatePollTimer); window.clearTimeout(publishPollTimer); closeNodeUninstall(); certificatePollTimer = null; publishPollTimer = null; tlsStatuses = new Map(); publishStatuses = new Map(); csrf = ''; setSidebarOpen(false); byId('notice').textContent = ''; hide('app'); hide('logout'); show('auth-shell'); show('login-panel'); byId('login-password').focus(); } });

byId('sidebar-toggle').addEventListener('click', () => setSidebarOpen(!sidebarOpen()));
byId('sidebar-close').addEventListener('click', () => setSidebarOpen(false, true));
byId('sidebar-backdrop').addEventListener('click', () => setSidebarOpen(false, true));
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && sidebarOpen()) setSidebarOpen(false, true);
});
mobileSidebarQuery.addEventListener('change', syncSidebarMode);

document.querySelectorAll('.nav').forEach((button) => button.addEventListener('click', () => {
  const hash = `#/${button.dataset.view}`;
  if (window.location.hash === hash) activateView(button.dataset.view);
  else window.location.hash = hash;
}));
window.addEventListener('hashchange', syncViewFromLocation);

byId('show-node-form').addEventListener('click', () => show('node-form'));
byId('show-site-form').addEventListener('click', () => resetSiteForm());
document.querySelectorAll('.cancel').forEach((button) => button.addEventListener('click', () => { button.closest('form').classList.add('hidden'); if (button.closest('form').id === 'site-form') resetSiteForm(false); }));

byId('close-node-uninstall').addEventListener('click', closeNodeUninstall);
byId('node-uninstall-dialog').addEventListener('close', () => {
  window.clearTimeout(uninstallPollTimer);
  uninstallPollTimer = null;
  uninstallNodeID = '';
  uninstallNodeName = '';
  uninstallCommand = '';
});
byId('prepare-node-uninstall').addEventListener('click', async () => {
  uninstallCommand = '';
  await runNodeUninstallAction(`/api/nodes/${uninstallNodeID}/uninstall`, { method: 'POST' }, '卸载准备已开始');
});
byId('pause-node-for-uninstall').addEventListener('click', async () => {
  if (uninstallActionPending || !uninstallNodeID) return;
  const nodeID = uninstallNodeID;
  setUninstallBusy(true);
  setUninstallError();
  try {
    await request(`/api/nodes/${nodeID}/status`, { method: 'POST', body: JSON.stringify({ status: 'draining' }) });
    await refresh();
    if (nodeID === uninstallNodeID) {
      await loadNodeUninstallStatus();
      notice('节点已暂停调度', true);
    }
  } catch (error) {
    if (nodeID === uninstallNodeID) setUninstallError(error.message);
  } finally {
    setUninstallBusy(false);
  }
});
byId('generate-node-uninstall-command').addEventListener('click', async () => {
  await runNodeUninstallAction(`/api/nodes/${uninstallNodeID}/uninstall/command`, { method: 'POST' }, '一次性卸载命令已生成');
});
byId('cancel-node-uninstall').addEventListener('click', async () => {
  uninstallCommand = '';
  await runNodeUninstallAction(`/api/nodes/${uninstallNodeID}/uninstall`, { method: 'DELETE' }, '卸载流程已取消');
});
byId('force-node-uninstall').addEventListener('click', async () => {
  if (byId('node-uninstall-confirm').value !== uninstallNodeName) return setUninstallError('请输入完整且完全一致的节点名称。');
  await runNodeUninstallAction(`/api/nodes/${uninstallNodeID}/uninstall/force-complete`, { method: 'POST', body: JSON.stringify({ confirmation: byId('node-uninstall-confirm').value }) }, '节点已强制标记为卸载完成');
});
byId('delete-node-record').addEventListener('click', async () => {
  if (byId('node-uninstall-confirm').value !== uninstallNodeName) return setUninstallError('请输入完整且完全一致的节点名称。');
  if (uninstallActionPending || !uninstallNodeID) return;
  const nodeID = uninstallNodeID;
  setUninstallBusy(true);
  setUninstallError();
  try {
    await request(`/api/nodes/${nodeID}`, { method: 'DELETE', body: JSON.stringify({ confirmation: byId('node-uninstall-confirm').value }) });
    if (nodeID === uninstallNodeID) closeNodeUninstall();
    await refresh();
    notice('节点记录已删除', true);
  } catch (error) {
    if (nodeID === uninstallNodeID) setUninstallError(error.message);
  } finally {
    setUninstallBusy(false);
  }
});

byId('node-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  try { await request('/api/nodes', { method: 'POST', body: JSON.stringify({ name: byId('node-name').value, public_ipv4: byId('node-ip').value }) }); event.target.reset(); hide('node-form'); await refresh(); notice('节点已创建', true); } catch (error) { notice(error.message); }
});

byId('site-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const backup = byId('site-backup-url').value.trim();
  const payload = { name: byId('site-name').value, zone_id: byId('site-zone').value, domains: split(byId('site-domains').value), node_ids: selectedNodeIDs(), primary_origin: { url: byId('site-primary-url').value, host_header: byId('site-primary-host').value, enabled: true }, stream_paths: split(byId('site-stream-paths').value), passthrough: byId('site-passthrough').checked, client_max_body_size_mb: Number(byId('site-client-max-body-size').value), enabled: byId('site-enabled').checked };
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
  byId('site-client-max-body-size').value = String(defaultClientMaxBodySizeMB);
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
  byId('site-client-max-body-size').value = String(site.client_max_body_size_mb ?? defaultClientMaxBodySizeMB);
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
    if (button.classList.contains('node-uninstall') || button.classList.contains('node-delete')) await openNodeUninstall(button.dataset.id);
    if (button.classList.contains('publish')) { const task = await request(`/api/sites/${button.dataset.id}/publish`, { method: 'POST' }); await refresh(); notice(`发布任务 ${task.id}：${taskStatusLabel(task.status)}`, true); }
    if (button.classList.contains('invalidate')) { const task = await request(`/api/sites/${button.dataset.id}/invalidate-cache`, { method: 'POST' }); await refresh(); notice(`缓存已刷新，任务 ${task.id}`, true); }
    if (button.classList.contains('certificate')) { const task = await request(`/api/sites/${button.dataset.id}/certificate`, { method: 'POST' }); tlsStatuses.set(button.dataset.id, { certificate_task: task, published_after_certificate: false }); renderSites(); scheduleCertificatePoll(); notice(`TLS 任务 ${task.id}：${taskStatusLabel(task.status)}`, true); }
    if (button.classList.contains('edit-site')) { const site = sites.find((item) => item.id === button.dataset.id); if (site) editSite(site); }
    if (button.classList.contains('allowlist')) { const result = await request(`/api/sites/${button.dataset.id}/origin-allowlist`); byId('site-allowlist').textContent = `源站防火墙或安全组应只允许以下边缘节点 IPv4 CIDR。添加、移除或撤销节点后请同步更新。\n\n${result.ipv4_cidrs.join('\n')}`; show('site-allowlist'); }
  } catch (error) { notice(error.message); }
});

syncSidebarMode();
boot();
