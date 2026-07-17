let csrf = '';
let nodes = [];
let sites = [];
let tlsStatuses = new Map();
let publishStatuses = new Map();
let deletionStatuses = new Map();
let certificatePollTimer = null;
let publishPollTimer = null;
let siteDeletePollTimer = null;
let deletingSiteID = '';
let deletingSiteName = '';
let siteDeletePending = false;
let uninstallPollTimer = null;
let uninstallNodeID = '';
let uninstallNodeName = '';
let uninstallCommand = '';
let uninstallActionPending = false;
let upgradePollTimer = null;
let upgradeNodeID = '';
let upgradeActionPending = false;
let overviewLoading = false;
let overviewLoaded = false;
let overviewData = null;
let overviewSiteMetric = 'requests';
let logLoading = false;
let logLoaded = false;
let logSearchInitialized = false;
let logPageOffset = 0;
let logPageHasMore = false;
let logQueryState = null;
let logRequestController = null;
let routeDataReady = false;
let activeRoute = { view: 'overview', page: 'main', siteID: '' };
let acceptedHash = '#/overview';
let pendingApprovedHash = '';
let siteFormBaseline = '';
let siteFormReady = false;
let settingsData = null;
let settingsFormBaseline = '';
let settingsFormReady = false;
let settingsLoading = false;
let securityData = null;
let securityLoading = false;
let securityPollTimer = null;
let securityActionPending = false;
let securityDataGeneration = 0;

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
const defaultReadWriteTimeoutSeconds = 360;
const defaultTCPConnectTimeoutSeconds = 10;
const defaultTCPIdleTimeoutSeconds = 300;
const defaultDNSTTLSeconds = 60;
const defaultSecurityPolicyPattern = String.raw`(?i)^/+(?:\.env(?:[._~-]?[A-Za-z0-9-]*)?(?:\.php)?|(?:[^/]+/)*\.env(?:[._~-]?[A-Za-z0-9-]*)?(?:\.php)?|\.git(?:/|$|-)|\.aws(?:/|$)|\.docker/(?:config\.json|)|\.svn(?:/|$)|\.hg(?:/|$)|\.ht(?:access|passwd)|\.DS_Store$)`;
const numberFormatter = new Intl.NumberFormat('zh-CN');
const compactNumberFormatter = new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 2 });
const consoleViews = new Set(['overview', 'logs', 'security', 'nodes', 'sites', 'settings']);
const viewLabels = { overview: '概览', logs: '日志', security: '安全', nodes: '节点', sites: '站点', settings: '设置' };
const mobileSidebarQuery = window.matchMedia('(max-width: 800px)');
const overviewStatusColors = ['#3274d9', '#168a7a', '#6d5bc5', '#d29224', '#c44f4f', '#2b8fa3', '#8b99a2'];

const byId = (id) => document.getElementById(id);
const split = (value) => value.split(',').map((item) => item.trim()).filter(Boolean);
const certificateTaskActive = (task) => task && ['queued', 'dispatching', 'applying'].includes(task.status);
const publishTaskActive = (task) => task && ['queued', 'dispatching', 'applying'].includes(task.status);
const deletionTaskActive = (task) => task && ['queued', 'dispatching', 'applying'].includes(task.status);
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

function settingsSourceLabel(source) {
  if (source === 'database') return '当前来源：控制台设置';
  if (source === 'environment') return '当前来源：环境变量';
  return '当前未配置';
}

function applySettingsData(data, { populate = false } = {}) {
  settingsData = data;
  if (populate || !settingsFormReady) populateSettingsForms();
  if (siteFormReady && byId('site-dns-ttl-inherit').checked) {
    byId('site-dns-ttl').value = String(settingsData?.dns?.default_ttl_seconds ?? defaultDNSTTLSeconds);
    updateSiteFormPreview(sites.find((site) => site.id === byId('site-id').value) || null);
  }
}

async function refreshSettings({ force = false, preserveDirtySections = [] } = {}) {
  const draft = settingsFormReady ? settingsFormState() : null;
  const preserved = new Set(preserveDirtySections.filter((section) => settingsSectionDirty(section)));
  settingsLoading = true;
  try {
    const data = await request('/api/settings');
    applySettingsData(data, { populate: force || !settingsFormsDirty() });
    if (draft && preserved.size) restoreSettingsDraft(draft, preserved);
  } finally {
    settingsLoading = false;
  }
}

function populateSettingsForms() {
  if (!settingsData) return;
  byId('settings-dns-ttl').value = String(settingsData.dns?.default_ttl_seconds ?? defaultDNSTTLSeconds);
  byId('dns-settings-state').textContent = `有效范围 60–300 秒`;
  byId('cloudflare-settings-source').textContent = settingsSourceLabel(settingsData.cloudflare?.source);
  byId('settings-cloudflare-token').value = '';
  byId('settings-cloudflare-token').placeholder = settingsData.cloudflare?.configured ? '已配置，输入新 Token 以替换' : '输入 API Token';
  byId('reset-cloudflare-settings').disabled = !settingsData.cloudflare?.override_configured;
  byId('test-cloudflare-settings').disabled = !settingsData.cloudflare?.configured;

  const smtp = settingsData.smtp || {};
  byId('smtp-settings-source').textContent = settingsSourceLabel(smtp.source);
  byId('settings-smtp-enabled').checked = Boolean(smtp.enabled);
  byId('settings-smtp-host').value = smtp.host || '';
  byId('settings-smtp-port').value = String(smtp.port || (smtp.security === 'tls' ? 465 : 587));
  byId('settings-smtp-security').value = smtp.security || 'starttls';
  byId('settings-smtp-username').value = smtp.username || '';
  byId('settings-smtp-password').value = '';
  byId('settings-smtp-password').placeholder = smtp.password_configured ? '已保存，留空保持不变' : '';
  byId('settings-smtp-from').value = smtp.from_address || '';
  byId('settings-smtp-recipients').value = (smtp.recipients || []).join(', ');
  byId('reset-smtp-settings').disabled = !smtp.override_configured;
  syncSMTPControls();
  settingsFormReady = true;
  markSettingsFormClean();
}

function smtpSettingsPayload() {
  const payload = {
    enabled: byId('settings-smtp-enabled').checked,
    host: byId('settings-smtp-host').value,
    port: Number(byId('settings-smtp-port').value),
    security: byId('settings-smtp-security').value,
    username: byId('settings-smtp-username').value,
    from_address: byId('settings-smtp-from').value,
    recipients: split(byId('settings-smtp-recipients').value),
  };
  const password = byId('settings-smtp-password').value;
  if (password) payload.password = password;
  return payload;
}

function settingsFormState() {
  return {
    dns: { default_ttl_seconds: Number(byId('settings-dns-ttl').value) },
    cloudflare: { token: byId('settings-cloudflare-token').value },
    smtp: smtpSettingsPayload(),
  };
}

function settingsFormSnapshot() {
  return JSON.stringify(settingsFormState());
}

function settingsSectionDirty(section) {
  if (!settingsFormReady || !settingsFormBaseline) return false;
  const baseline = JSON.parse(settingsFormBaseline);
  return JSON.stringify(settingsFormState()[section]) !== JSON.stringify(baseline[section]);
}

function restoreSettingsDraft(draft, sections) {
  if (sections.has('dns')) byId('settings-dns-ttl').value = String(draft.dns.default_ttl_seconds);
  if (sections.has('cloudflare')) byId('settings-cloudflare-token').value = draft.cloudflare.token;
  if (sections.has('smtp')) {
    const smtp = draft.smtp;
    byId('settings-smtp-enabled').checked = smtp.enabled;
    byId('settings-smtp-host').value = smtp.host;
    byId('settings-smtp-port').value = String(smtp.port);
    byId('settings-smtp-security').value = smtp.security;
    byId('settings-smtp-username').value = smtp.username;
    byId('settings-smtp-password').value = smtp.password || '';
    byId('settings-smtp-from').value = smtp.from_address;
    byId('settings-smtp-recipients').value = smtp.recipients.join(', ');
    syncSMTPControls();
  }
}

function settingsFormsDirty() {
  return settingsFormReady && activeRoute.view === 'settings' && settingsFormSnapshot() !== settingsFormBaseline;
}

function markSettingsFormClean() {
  settingsFormBaseline = settingsFormReady ? settingsFormSnapshot() : '';
}

function setSettingsBusy(formID, busy) {
  const form = byId(formID);
  form.classList.toggle('is-busy', busy);
  form.querySelectorAll('button, input, select').forEach((control) => { control.disabled = busy; });
  if (!busy && settingsData) {
    byId('reset-cloudflare-settings').disabled = !settingsData.cloudflare?.override_configured;
    byId('test-cloudflare-settings').disabled = !settingsData.cloudflare?.configured;
    byId('reset-smtp-settings').disabled = !settingsData.smtp?.override_configured;
    syncSMTPControls();
  }
}

function syncSMTPControls() {
  byId('test-smtp-settings').disabled = byId('smtp-settings-form').classList.contains('is-busy') || !byId('settings-smtp-enabled').checked;
}

function securityActionLabel(action) {
  return action === 'ban' ? 'IP 封禁' : '仅拦截';
}

function securityDurationLabel(seconds) {
  return ({ 3600: '1 小时', 21600: '6 小时', 43200: '12 小时', 86400: '24 小时' })[Number(seconds)] || '--';
}

function securityNodeName(nodeID) {
  return securityData?.nodes?.find((node) => node.id === nodeID)?.name || nodeID || '--';
}

function securityRequestCell(item) {
  const authority = item.host || '--';
  const method = item.method || '--';
  return `<span class="security-request"><span>${escapeHTML(method)} · ${escapeHTML(authority)}</span><code>${escapeHTML(item.path || '--')}</code></span>`;
}

function renderSecurity() {
  if (!securityData) return;
  const policies = securityData.policies || [];
  const bans = securityData.bans || [];
  const activeBanCount = Number(securityData.active_ban_count ?? bans.length);
  const events = securityData.events || [];
  const eligibleNodes = (securityData.nodes || []).filter((node) => ['active', 'draining'].includes(node.status));
  const capableNodes = eligibleNodes.filter((node) => node.capable);
  const appliedNodes = capableNodes.filter((node) => node.configured && node.desired_version > 0 && node.applied_version >= node.desired_version);
  byId('security-policy-count').textContent = numberFormatter.format(policies.filter((policy) => policy.enabled).length);
  byId('security-ban-count').textContent = numberFormatter.format(activeBanCount);
  byId('security-node-coverage').textContent = `${capableNodes.length} / ${eligibleNodes.length}`;
  byId('security-node-applied').textContent = `${appliedNodes.length} / ${capableNodes.length}`;
  byId('security-meta').textContent = `${policies.length} 条策略 · ${numberFormatter.format(activeBanCount)} 个活动封禁${activeBanCount > bans.length ? ` · 显示前 ${numberFormatter.format(bans.length)} 条` : ''}`;
  byId('security-deployment-error').textContent = securityData.deployment_error || '';
  byId('security-deployment-error').classList.toggle('hidden', !securityData.deployment_error);

  byId('security-node-table').innerHTML = (securityData.nodes || []).length ? securityData.nodes.map((node) => {
    let result = '<span class="status pending">需升级</span>';
    if (node.capable && node.last_error) result = `<span class="status failed" title="${escapeHTML(node.last_error)}">节点错误</span>`;
    else if (node.capable && !node.configured) result = '<span class="status pending">待部署</span>';
    else if (node.capable && node.desired_version > 0 && node.applied_version >= node.desired_version) result = '<span class="status succeeded">已应用</span>';
    else if (node.capable) result = '<span class="status applying">等待应用</span>';
    return `<tr><td>${escapeHTML(node.name)}</td><td>${escapeHTML(nodeStatusLabel(node.status))}</td><td>${node.capable ? '已就绪' : '不支持'}</td><td>${numberFormatter.format(node.desired_version)}</td><td>${numberFormatter.format(node.applied_version)}</td><td>${result}</td></tr>`;
  }).join('') : '<tr><td colspan="6" class="muted">暂无节点</td></tr>';

  byId('security-policy-table').innerHTML = policies.length ? policies.map((policy) => `<tr>
    <td><strong>${escapeHTML(policy.name)}</strong></td>
    <td><code class="security-pattern">${escapeHTML(policy.pattern)}</code></td>
    <td>${escapeHTML(securityActionLabel(policy.action))}${policy.action === 'ban' ? `<br><span class="muted">${escapeHTML(securityDurationLabel(policy.ban_duration_seconds))}</span>` : ''}</td>
    <td>${numberFormatter.format(policy.priority)}</td>
    <td><span class="status ${policy.enabled ? 'succeeded' : 'pending'}">${policy.enabled ? '已启用' : '已停用'}</span></td>
    <td class="actions"><button class="small secondary edit-security-policy" data-id="${escapeHTML(policy.id)}">编辑</button>${policy.builtin ? '' : `<button class="small danger delete-security-policy" data-id="${escapeHTML(policy.id)}">删除</button>`}</td>
  </tr>`).join('') : '<tr><td colspan="6" class="muted">暂无访问策略</td></tr>';

  byId('security-ban-table').innerHTML = bans.length ? bans.map((ban) => `<tr>
    <td><code>${escapeHTML(ban.ip)}</code></td><td>${escapeHTML(ban.policy_name || '--')}</td><td>${escapeHTML(securityNodeName(ban.trigger_node_id))}</td>
    <td>${securityRequestCell(ban)}</td><td>${formatDateTime(ban.expires_at)}</td>
    <td><button class="small danger unban-security-ip" data-ip="${escapeHTML(ban.ip)}">解除封禁</button></td>
  </tr>`).join('') : '<tr><td colspan="6" class="muted">暂无活动封禁</td></tr>';

  byId('security-event-table').innerHTML = events.length ? events.map((event) => `<tr>
    <td>${formatDateTime(event.observed_at)}</td><td><code>${escapeHTML(event.client_ip)}</code></td><td>${escapeHTML(event.policy_name || '--')}</td>
    <td>${escapeHTML(securityNodeName(event.node_id))}</td><td>${securityRequestCell(event)}</td><td>${escapeHTML(securityActionLabel(event.action))}</td>
  </tr>`).join('') : '<tr><td colspan="6" class="muted">暂无策略命中</td></tr>';
  hide('security-state');
  show('security-content');
}

function scheduleSecurityRefresh() {
  window.clearTimeout(securityPollTimer);
  securityPollTimer = null;
  if (activeRoute.view === 'security') {
    securityPollTimer = window.setTimeout(() => refreshSecurity().catch((error) => setSecurityState(error.message)), 15000);
  }
}

function setSecurityState(message) {
  byId('security-state').textContent = message;
  show('security-state');
}

async function refreshSecurity() {
  if (securityLoading) return;
  securityLoading = true;
  const generation = securityDataGeneration;
  try {
    const data = await request('/api/security');
    if (generation === securityDataGeneration) {
      securityData = data;
      renderSecurity();
    }
  } finally {
    securityLoading = false;
    scheduleSecurityRefresh();
  }
}

function renderSecurityRoute({ routeChanged = false } = {}) {
  if (securityData) renderSecurity();
  if ((routeChanged || !securityData) && !securityLoading) {
    setSecurityState('正在加载安全状态…');
    void refreshSecurity().catch((error) => setSecurityState(error.message));
  } else {
    scheduleSecurityRefresh();
  }
}

function syncSecurityPolicyDuration() {
  const banned = byId('security-policy-action').value === 'ban';
  byId('security-policy-duration-wrap').classList.toggle('hidden', !banned);
  byId('security-policy-duration').disabled = !banned;
}

function setSecurityPolicyError(message = '') {
  byId('security-policy-error').textContent = message;
  byId('security-policy-error').classList.toggle('hidden', !message);
}

function openSecurityPolicy(policy = null) {
  byId('security-policy-form').reset();
  byId('security-policy-id').value = policy?.id || '';
  byId('security-policy-dialog-title').textContent = policy ? '编辑访问策略' : '新增访问策略';
  byId('security-policy-name').value = policy?.name || '';
  byId('security-policy-priority').value = String(policy?.priority || Math.min(10000, Math.max(100, ...(securityData?.policies || []).map((item) => item.priority + 10))));
  byId('security-policy-action').value = policy?.action || 'ban';
  byId('security-policy-duration').value = String(policy?.ban_duration_seconds || 21600);
  byId('security-policy-enabled').checked = policy ? Boolean(policy.enabled) : true;
  byId('security-policy-pattern').value = policy?.pattern || defaultSecurityPolicyPattern;
  setSecurityPolicyError();
  syncSecurityPolicyDuration();
  byId('security-policy-dialog').showModal();
  byId('security-policy-name').focus();
}

function closeSecurityPolicy() {
  if (byId('security-policy-dialog').open) byId('security-policy-dialog').close();
}

function securityPolicyPayload() {
  const action = byId('security-policy-action').value;
  return {
    name: byId('security-policy-name').value,
    enabled: byId('security-policy-enabled').checked,
    pattern: byId('security-policy-pattern').value,
    action,
    ban_duration_seconds: action === 'ban' ? Number(byId('security-policy-duration').value) : 0,
    priority: Number(byId('security-policy-priority').value),
  };
}

async function refresh() {
  const [loadedNodes, loadedSites, loadedSettings] = await Promise.all([request('/api/nodes'), request('/api/sites'), request('/api/settings')]);
  nodes = loadedNodes;
  sites = loadedSites;
  applySettingsData(loadedSettings, { populate: !settingsFormsDirty() });
  routeDataReady = true;
  byId('site-list-meta').textContent = `${numberFormatter.format(sites.length)} 个站点 · ${numberFormatter.format(sites.filter((site) => site.enabled && site.published).length)} 个已发布`;
  byId('node-table').innerHTML = nodes.map(renderNodeRow).join('');
  renderSites();
  renderLogFilterOptions();
  syncRouteFromLocation({ forceForm: !siteFormDirty(), focus: false });
  void refreshOverview();
  await Promise.all([refreshTLSStatuses(), refreshPublishStatuses(), refreshDeletionStatuses()]);
}

function renderNodeRow(node) {
  return `<tr><td>${escapeHTML(node.name)}</td><td><code>${escapeHTML(node.id)}</code></td><td><code>${escapeHTML(node.public_ipv4)}</code></td><td><span class="status ${escapeHTML(node.status)}">${escapeHTML(nodeStatusLabel(node.status))}</span></td><td>${renderAgentVersion(node)}</td><td>${node.last_heartbeat_at ? formatDateTime(node.last_heartbeat_at) : '从未上报'}</td><td class="actions">${renderNodeActions(node)}</td></tr>`;
}

function shortDigest(value) {
  return value ? String(value).slice(0, 12) : '--';
}

function nodeUpgradeLabel(node) {
  const task = node.upgrade_task;
  if (task && ['queued', 'applying'].includes(task.status)) return task.status === 'queued' ? '等待升级' : '升级中';
  if (node.upgrade_up_to_date) return '主控当前版本';
  if (task?.status === 'failed') return '升级失败';
  if (!node.upgrade_capable) return '需手动启用';
  if (node.can_upgrade) return '可升级';
  return '暂不可升级';
}

function renderAgentVersion(node) {
  const label = nodeUpgradeLabel(node);
  const state = node.upgrade_up_to_date ? 'succeeded' : (node.upgrade_task?.status || 'pending');
  const digest = node.agent_sha256 || '';
  return `<div class="agent-version"><code title="${escapeHTML(digest)}">${escapeHTML(shortDigest(digest))}</code><span class="status ${escapeHTML(state)}">${escapeHTML(label)}</span></div>`;
}

function renderNodeActions(node) {
  const actions = [];
  if (['pending', 'active', 'draining'].includes(node.status)) {
    actions.push(`<button class="small enroll" data-id="${node.id}">获取部署/升级命令</button>`);
  }
  const upgradeActive = node.upgrade_task && ['queued', 'applying'].includes(node.upgrade_task.status);
  const upgradeFailed = node.upgrade_task?.status === 'failed' && !node.upgrade_up_to_date;
  const upgradeVisible = node.upgrade_capable && !node.upgrade_up_to_date;
  if (['active', 'draining'].includes(node.status) && (upgradeVisible || upgradeActive || upgradeFailed)) {
    actions.push(`<button class="small ${upgradeActive ? '' : 'secondary'} node-upgrade" data-id="${node.id}">${node.can_upgrade && !upgradeFailed ? '升级' : '查看升级'}</button>`);
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
    const deletionStatus = deletionStatuses.get(site.id);
    const siteHash = `#/sites/${encodeURIComponent(site.id)}`;
    return `<article class="site-row">
      <div class="site-identity">
        <div class="site-name-line"><h2><a class="site-name-link" href="${siteHash}">${escapeHTML(site.name)}</a></h2>${siteStateMarkup(site)}</div>
        <div class="site-domain-list">${site.domains.map((domain) => `<span>${escapeHTML(domain)}</span>`).join('')}</div>
      </div>
      <dl class="site-facts">
        <div><dt>节点</dt><dd>${numberFormatter.format(site.node_ids.length)} 个</dd></div>
		<div><dt>TLS</dt><dd>${siteNeedsTLS(site) ? tlsStatusMarkup(tlsStatus) : '<span class="fact-muted">无需证书</span>'}</dd></div>
		<div><dt>发布</dt><dd>${site.deleting ? deletionStatusMarkup(deletionStatus) : publishStatusMarkup(publishStatus)}</dd></div>
      </dl>
      <div class="site-actions">
        ${site.deleting ? `<button class="small danger open-site-delete" data-id="${site.id}">查看删除</button>` : `<button class="small publish" data-id="${site.id}" ${publishTaskActive(publishStatus?.task) ? 'disabled' : ''}>${site.published ? (publishStatus?.task?.status === 'failed' || publishStatus?.task?.status === 'partial' ? '重新发布' : '发布') : '发布'}</button>`}
        <button class="small secondary manage-site" data-id="${site.id}">管理</button>
      </div>
      ${publishDetailMarkup(publishStatus)}
    </article>`;
  }).join('') || '<div class="site-empty">暂无站点</div>';
}

function renderSiteViews() {
  renderSites();
  renderSiteDetailStatus();
}

function publishStatusMarkup(publishStatus) {
  const task = publishStatus?.task;
  if (!task) return '<span class="fact-muted">未发布</span>';
  const label = taskStatusLabel(task.status);
  return `<span class="status ${escapeHTML(task.status)}"${task.detail ? ` title="${escapeHTML(task.detail)}"` : ''}>${escapeHTML(label)}</span>`;
}

function deletionStatusMarkup(deletionStatus) {
  const task = deletionStatus?.task;
  if (!task) return '<span class="status applying">删除中</span>';
  const label = task.status === 'succeeded' ? '已删除' : (task.status === 'failed' || task.status === 'partial' ? '删除受阻' : '删除中');
  return `<span class="status ${escapeHTML(task.status)}"${task.detail ? ` title="${escapeHTML(task.detail)}"` : ''}>${label}</span>`;
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
  if (site.deleting) return '<span class="status applying">删除中</span>';
  if (!site.published) return '<span class="status pending">待发布</span>';
  if (!site.enabled) return '<span class="status disabled">已停用</span>';
  return '<span class="status active">已发布</span>';
}

function siteCacheMarkup(site) {
	if (site.tcp_only) return '<span class="fact-muted">不适用</span>';
  if (site.passthrough) return '<span class="fact-muted">透传</span>';
  if (!siteCacheable(site)) return '<span class="fact-muted">不缓存</span>';
  return `<span>v${numberFormatter.format(site.cache_generation)}</span>`;
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

function originURLScheme(value) {
  try { return new URL(value).protocol.replace(':', '').toLowerCase(); } catch (_) { return ''; }
}

function originURLUsesTLS(value) { return ['https', 'wss', 'grpcs'].includes(originURLScheme(value)); }

function originScheme(site) { return originURLScheme(site.primary_origin?.url || ''); }

function siteNeedsTLS(site) {
  return !site.tcp_only || (site.tcp_forwards || []).some((forward) => forward.listen_tls);
}

function updateOriginTLSFields() {
	if (byId('site-tcp-only').checked) return;
  const primaryUsesTLS = originURLUsesTLS(byId('site-primary-url').value);
  const backupUsesTLS = originURLUsesTLS(byId('site-backup-url').value);
  byId('site-primary-tls-name-wrap').classList.toggle('hidden', !primaryUsesTLS);
  byId('site-backup-tls-name-wrap').classList.toggle('hidden', !backupUsesTLS);
  byId('site-primary-tls-name').disabled = !primaryUsesTLS;
  byId('site-backup-tls-name').disabled = !backupUsesTLS;
}

function tcpForwardPayload() {
	return [...byId('site-tcp-forward-list').querySelectorAll('.tcp-forward-row')].map((row) => {
		const upstreamTLS = row.querySelector('.tcp-forward-upstream-tls').checked;
		return {
			name: row.querySelector('.tcp-forward-name').value,
			listen_port: Number(row.querySelector('.tcp-forward-listen-port').value),
			listen_tls: row.querySelector('.tcp-forward-listen-tls').checked,
			upstream_host: row.querySelector('.tcp-forward-upstream-host').value,
			upstream_port: Number(row.querySelector('.tcp-forward-upstream-port').value),
			upstream_tls: upstreamTLS,
			upstream_tls_server_name: upstreamTLS ? row.querySelector('.tcp-forward-upstream-sni').value : '',
			connect_timeout_seconds: Number(row.querySelector('.tcp-forward-connect-timeout').value),
			idle_timeout_seconds: Number(row.querySelector('.tcp-forward-idle-timeout').value),
		};
	});
}

function syncTCPForwardRow(row) {
	const upstreamTLS = row.querySelector('.tcp-forward-upstream-tls').checked;
	const sniWrap = row.querySelector('.tcp-forward-upstream-sni-wrap');
	sniWrap.classList.toggle('hidden', !upstreamTLS);
	sniWrap.querySelector('input').disabled = !upstreamTLS || byId('site-name').disabled;
}

function addTCPForwardRow(forward = {}) {
	const row = document.createElement('div');
	row.className = 'tcp-forward-row';
	row.innerHTML = `<div class="tcp-forward-fields">
		<label>名称 <input class="tcp-forward-name" required maxlength="100" placeholder="IMAPS"></label>
		<label>监听端口 <input class="tcp-forward-listen-port" type="number" min="1" max="65535" step="1" required inputmode="numeric" placeholder="9993"></label>
		<label>上游主机 <input class="tcp-forward-upstream-host" required placeholder="mail.example.com" autocomplete="off"></label>
		<label>上游端口 <input class="tcp-forward-upstream-port" type="number" min="1" max="65535" step="1" required inputmode="numeric" placeholder="993"></label>
		<label>连接超时
		  <select class="tcp-forward-connect-timeout"><option value="5">5 秒</option><option value="10">10 秒</option><option value="30">30 秒</option><option value="60">60 秒</option></select>
		</label>
		<label>会话空闲超时
		  <select class="tcp-forward-idle-timeout"><option value="300">5 分钟</option><option value="900">15 分钟</option><option value="1800">30 分钟</option><option value="3600">60 分钟</option></select>
		</label>
		<label class="checkbox-label"><input class="tcp-forward-listen-tls" type="checkbox"> 入口 TLS</label>
		<label class="checkbox-label"><input class="tcp-forward-upstream-tls" type="checkbox"> 上游 TLS</label>
		<label class="tcp-forward-upstream-sni-wrap site-field-wide">上游 TLS SNI <input class="tcp-forward-upstream-sni" placeholder="mail.example.com" autocomplete="off"></label>
	  </div>
	  <button class="remove-tcp-forward secondary" type="button" title="删除此端口" aria-label="删除此 TCP 转发"><span aria-hidden="true">&times;</span></button>`;
	row.querySelector('.tcp-forward-name').value = forward.name || '';
	row.querySelector('.tcp-forward-listen-port').value = forward.listen_port || '';
	row.querySelector('.tcp-forward-listen-tls').checked = forward.listen_tls ?? true;
	row.querySelector('.tcp-forward-upstream-host').value = forward.upstream_host || '';
	row.querySelector('.tcp-forward-upstream-port').value = forward.upstream_port || '';
	row.querySelector('.tcp-forward-upstream-tls').checked = forward.upstream_tls ?? true;
	row.querySelector('.tcp-forward-upstream-sni').value = forward.upstream_tls_server_name || '';
	row.querySelector('.tcp-forward-connect-timeout').value = String(forward.connect_timeout_seconds || defaultTCPConnectTimeoutSeconds);
	row.querySelector('.tcp-forward-idle-timeout').value = String(forward.idle_timeout_seconds || defaultTCPIdleTimeoutSeconds);
	byId('site-tcp-forward-list').append(row);
	syncTCPForwardRow(row);
	return row;
}

function syncSiteTrafficMode() {
	const tcpOnly = byId('site-tcp-only').checked;
	const locked = byId('site-name').disabled;
	byId('site-origin-section').classList.toggle('hidden', tcpOnly);
	for (const id of ['site-body-policy', 'site-timeout-policy', 'site-passthrough-policy']) byId(id).classList.toggle('hidden', tcpOnly);
	byId('site-primary-url').required = !tcpOnly;
	byId('site-origin-section').querySelectorAll('input, select').forEach((field) => { field.disabled = locked || tcpOnly; });
	for (const id of ['site-client-max-body-size', 'site-read-write-timeout', 'site-passthrough']) byId(id).disabled = locked || tcpOnly;
	if (!tcpOnly) updateOriginTLSFields();
}

function siteProtocol(site) {
	if (site.tcp_only) return 'TCP / TLS';
	if ((site.tcp_forwards || []).length) return 'HTTP + TCP';
  const scheme = originScheme(site);
  if (scheme === 'grpc' || scheme === 'grpcs') return 'gRPC';
  if (scheme === 'ws' || scheme === 'wss') return 'WebSocket';
  return 'HTTP / WS / SSE';
}

function siteCacheable(site) {
	if (site.tcp_only) return false;
  const scheme = originScheme(site);
  return !site.passthrough && (scheme === 'http' || scheme === 'https');
}

function updateSiteFormPreview(savedSite = null) {
  if (!siteFormReady || activeRoute.view !== 'sites' || activeRoute.page === 'list') return;
  updateOriginTLSFields();
  const payload = siteFormPayload();
	const draft = {
    ...(savedSite || {}),
    primary_origin: payload.primary_origin,
    passthrough: payload.passthrough,
    cache_generation: savedSite?.cache_generation ?? 0,
    client_max_body_size_mb: payload.client_max_body_size_mb,
    read_write_timeout_seconds: payload.read_write_timeout_seconds,
    dns_ttl_seconds: payload.dns_ttl_seconds,
		node_ids: payload.node_ids,
		tcp_only: payload.tcp_only,
		tcp_forwards: payload.tcp_forwards,
  };
  const scheme = originScheme(draft);
  byId('site-summary-protocol').textContent = siteProtocol(draft);
  byId('site-summary-cache').innerHTML = siteCacheMarkup(draft);
  byId('site-summary-body').textContent = `${numberFormatter.format(payload.client_max_body_size_mb)} MiB`;
	byId('site-summary-timeout').textContent = scheme === 'grpc' || scheme === 'grpcs' ? '不适用于 gRPC' : `${numberFormatter.format(payload.read_write_timeout_seconds / 60)} 分钟`;
	if (payload.tcp_only) {
		byId('site-summary-body').textContent = '不适用';
		byId('site-summary-timeout').textContent = '按 TCP 端口配置';
	}
  const effectiveDNSTTL = payload.dns_ttl_seconds ?? settingsData?.dns?.default_ttl_seconds ?? defaultDNSTTLSeconds;
  byId('site-summary-dns-ttl').textContent = payload.dns_ttl_seconds == null ? `${numberFormatter.format(effectiveDNSTTL)} 秒（全局）` : `${numberFormatter.format(effectiveDNSTTL)} 秒`;
	byId('site-summary-nodes').textContent = `${numberFormatter.format(payload.node_ids.length)} 个`;
	byId('site-summary-tcp').textContent = payload.tcp_forwards.length ? payload.tcp_forwards.map((forward) => forward.listen_port).join(', ') : '未配置';
}

function renderSiteDetailStatus() {
  if (activeRoute.view !== 'sites' || activeRoute.page !== 'detail' || !routeDataReady) return;
  const site = sites.find((item) => item.id === activeRoute.siteID);
  if (!site) return;
  const tlsStatus = tlsStatuses.get(site.id);
  const publishStatus = publishStatuses.get(site.id);
  const deletionStatus = deletionStatuses.get(site.id);
  const certificateTask = tlsStatus?.certificate_task || null;
  const publishButton = byId('site-detail-publish');
  const certificateButton = byId('site-detail-certificate');
  const deleteButton = byId('site-detail-delete');

  byId('site-detail-state').innerHTML = siteStateMarkup(site);
  byId('site-detail-meta').textContent = `${site.domains.join(', ') || '未配置域名'} · ${site.id}`;
	byId('site-summary-tls').innerHTML = siteNeedsTLS(site) ? tlsStatusMarkup(tlsStatus) : '<span class="fact-muted">无需证书</span>';
  byId('site-summary-publish').innerHTML = site.deleting ? deletionStatusMarkup(deletionStatus) : publishStatusMarkup(publishStatus);
	byId('site-operation-tls').innerHTML = siteNeedsTLS(site) ? tlsStatusMarkup(tlsStatus) : '<span class="fact-muted">无 TLS 监听</span>';
  byId('site-operation-cache').innerHTML = siteCacheMarkup(site);
  byId('site-operation-delete').innerHTML = site.deleting ? deletionStatusMarkup(deletionStatus) : '撤销托管 DNS，并从边缘节点移除配置和证书';
	byId('site-cache-operation').classList.toggle('hidden', !siteCacheable(site));
	byId('site-tls-operation').classList.toggle('hidden', !siteNeedsTLS(site));
  byId('site-publish-detail').innerHTML = publishDetailMarkup(publishStatus);

  [publishButton, certificateButton, byId('site-detail-invalidate'), byId('site-detail-allowlist'), deleteButton].forEach((button) => { button.dataset.id = site.id; });
  publishButton.disabled = site.deleting || publishTaskActive(publishStatus?.task);
  publishButton.textContent = site.published && ['failed', 'partial'].includes(publishStatus?.task?.status) ? '重新发布' : '发布';
  certificateButton.disabled = site.deleting || certificateTaskActive(certificateTask);
  certificateButton.title = certificateTaskActive(certificateTask) ? 'TLS 证书签发正在进行中' : '';
  byId('site-detail-invalidate').disabled = site.deleting;
  byId('site-detail-allowlist').disabled = site.deleting;
  deleteButton.textContent = site.deleting ? '查看删除' : '删除站点';
  deleteButton.disabled = !site.deleting && (certificateTaskActive(certificateTask) || publishTaskActive(publishStatus?.task));
  deleteButton.title = deleteButton.disabled ? '请等待当前 TLS 或发布任务完成' : '';
  updateSiteFormPreview(site);
  setSiteEditorLocked(site.deleting);
}

function setSiteEditorLocked(locked) {
  document.querySelectorAll('#site-form input, #site-form select, #site-form textarea').forEach((field) => { field.disabled = locked; });
	if (!locked) updateOriginTLSFields();
	byId('add-tcp-forward').disabled = locked;
	document.querySelectorAll('.remove-tcp-forward').forEach((button) => { button.disabled = locked; });
	syncSiteTrafficMode();
	document.querySelectorAll('.tcp-forward-row').forEach(syncTCPForwardRow);
  syncSiteDNSTTLControl();
  if (byId('site-id').value) byId('site-zone').disabled = true;
  byId('site-submit').disabled = locked;
}

async function refreshTLSStatuses() {
  const results = await Promise.all(sites.map(async (site) => {
    try { return [site.id, await request(`/api/sites/${site.id}/tls-status`)]; } catch (_) { return [site.id, null]; }
  }));
  tlsStatuses = new Map(results.filter(([, status]) => status));
  renderSiteViews();
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
  renderSiteViews();
  schedulePublishPoll();
}

async function refreshDeletionStatuses() {
  const previous = deletionStatuses;
  const deletingSites = sites.filter((site) => site.deleting);
  const results = await Promise.all(deletingSites.map(async (site) => {
    try { return [site.id, await request(`/api/sites/${site.id}/delete-status`)]; } catch (_) { return [site.id, null]; }
  }));
  deletionStatuses = new Map(results.filter(([, status]) => status?.task));
  let completedSite = null;
  for (const [siteID, status] of deletionStatuses) {
    const priorStatus = previous.get(siteID)?.task?.status;
    const currentStatus = status.task?.status;
    if (deletionTaskActive({ status: priorStatus }) && ['failed', 'partial'].includes(currentStatus)) {
      const site = sites.find((item) => item.id === siteID);
      notice(`${site?.name || '站点'} 删除受阻，请查看节点详情。`);
    }
    if (currentStatus === 'succeeded') completedSite = sites.find((item) => item.id === siteID) || null;
  }
  renderSiteViews();
  scheduleSiteDeletePoll();
  if (deletingSiteID && deletionStatuses.has(deletingSiteID)) renderSiteDeleteDialog(deletionStatuses.get(deletingSiteID));
  if (completedSite) {
    window.setTimeout(() => {
      if (deletingSiteID === completedSite.id) closeSiteDelete();
      if (activeRoute.view === 'sites' && activeRoute.siteID === completedSite.id) navigateTo('#/sites');
      refresh().then(() => notice(`站点「${completedSite.name}」已删除`, true)).catch((error) => notice(error.message));
    }, 0);
  }
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

function scheduleSiteDeletePoll() {
  window.clearTimeout(siteDeletePollTimer);
  siteDeletePollTimer = null;
  if ([...deletionStatuses.values()].some((status) => deletionTaskActive(status?.task))) {
    siteDeletePollTimer = window.setTimeout(() => {
      refreshDeletionStatuses().catch((error) => notice(error.message));
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

function siteDeletionStateText(status) {
  const task = status?.task;
  if (!task) return '删除尚未开始。确认后会先撤销托管 DNS，再等待边缘节点完成下线。';
  if (task.status === 'queued') return '删除任务已排队。';
  if (task.status === 'dispatching') return '正在撤销托管 DNS 并生成边缘配置。';
  if (task.status === 'applying') return task.detail || '正在等待边缘节点确认移除站点。';
  if (task.status === 'succeeded') return '站点 DNS、边缘配置和证书材料均已清理。';
  if (task.status === 'partial') return '部分边缘节点已完成移除，其余节点未确认；修复节点后重试删除。';
  if (task.status === 'failed') return task.detail || '删除受阻；修复问题后重试。';
  return task.detail || taskStatusLabel(task.status);
}

function siteDeletionBlockerText(node) {
  const conflict = (node.port_conflicts || []).map((item) => `端口 ${item.port} 被 ${item.process}${item.pid ? `（PID ${item.pid}）` : ''} 占用`).join('；');
  return `${node.node_name || node.node_id}：${conflict || node.detail || '未确认站点已移除'}`;
}

function setSiteDeleteError(message = '') {
  byId('site-delete-error').textContent = message;
  if (message) show('site-delete-error'); else hide('site-delete-error');
}

function setSiteDeleteBusy(busy) {
  siteDeletePending = busy;
  byId('confirm-site-delete').disabled = busy;
  byId('close-site-delete').disabled = busy;
}

function renderSiteDeleteDialog(status = null) {
  const site = sites.find((item) => item.id === deletingSiteID);
  if (!site) return;
  const task = status?.task || null;
  const active = deletionTaskActive(task);
  const retryable = task && ['failed', 'partial'].includes(task.status);
  byId('site-delete-meta').textContent = `${site.name} · ${site.domains.join(', ')}`;
  byId('site-delete-confirm-label').textContent = `输入「${site.name}」以确认`;
  byId('site-delete-state').textContent = siteDeletionStateText(status);
  const blockers = (status?.nodes || []).filter((node) => node.status !== 'succeeded');
  if (blockers.length) {
    byId('site-delete-blockers').innerHTML = blockers.map((node) => `<li>${escapeHTML(siteDeletionBlockerText(node))}</li>`).join('');
    show('site-delete-blockers');
  } else {
    hide('site-delete-blockers');
  }
  byId('site-delete-confirm-wrap').classList.toggle('hidden', active || task?.status === 'succeeded');
  byId('confirm-site-delete').classList.toggle('hidden', active || task?.status === 'succeeded');
  byId('confirm-site-delete').textContent = retryable ? '重试删除' : '开始安全删除';
}

async function openSiteDelete(siteID) {
  const site = sites.find((item) => item.id === siteID);
  if (!site) return;
  deletingSiteID = site.id;
  deletingSiteName = site.name;
  byId('site-delete-confirm').value = '';
  setSiteDeleteError();
  renderSiteDeleteDialog(deletionStatuses.get(site.id) || null);
  const dialog = byId('site-delete-dialog');
  if (!dialog.open) dialog.showModal();
  if (site.deleting) {
    try {
      const status = await request(`/api/sites/${site.id}/delete-status`);
      if (site.id !== deletingSiteID) return;
      deletionStatuses.set(site.id, status);
      renderSiteDeleteDialog(status);
      scheduleSiteDeletePoll();
    } catch (error) {
      if (site.id === deletingSiteID) setSiteDeleteError(error.message);
    }
  }
}

function closeSiteDelete() {
  deletingSiteID = '';
  deletingSiteName = '';
  const dialog = byId('site-delete-dialog');
  if (dialog.open) dialog.close();
}

function nodeUpgradeStateText(status) {
  const task = status.upgrade_task;
  if (task?.status === 'queued') return '等待边缘代理领取升级任务。';
  if (task?.status === 'applying') return task.detail || '边缘节点正在校验制品并执行升级。';
  if (task?.status === 'succeeded') return task.detail || '边缘节点已升级并完成心跳确认。';
  if (status.upgrade_up_to_date) return '节点已运行主控当前边缘制品。';
  if (task?.status === 'failed') return '在线升级未完成，节点已保留或恢复升级前状态。';
  if (status.can_upgrade) return '节点可同步到主控当前边缘制品。';
  return status.upgrade_blocker || '当前无法执行在线升级。';
}

function setUpgradeError(message = '') {
  byId('node-upgrade-error').textContent = message;
  if (message) show('node-upgrade-error'); else hide('node-upgrade-error');
}

function setUpgradeBusy(busy) {
  upgradeActionPending = busy;
  byId('start-node-upgrade').disabled = busy;
}

function renderNodeUpgrade(status) {
  byId('node-upgrade-meta').textContent = `${status.name} · ${status.public_ipv4} · ${nodeStatusLabel(status.status)}`;
  byId('node-upgrade-current').textContent = status.agent_sha256 || '尚未上报';
  byId('node-upgrade-target').textContent = status.target_agent_sha256 || '主控未配置';
  byId('node-upgrade-state').textContent = nodeUpgradeStateText(status);
  setUpgradeError(status.upgrade_task?.status === 'failed' && !status.upgrade_up_to_date ? (status.upgrade_task.detail || status.upgrade_blocker || '升级失败') : '');
  if (status.can_upgrade) {
    byId('start-node-upgrade').textContent = status.upgrade_task?.status === 'failed' ? '重试升级' : '开始升级';
    show('start-node-upgrade');
  } else {
    hide('start-node-upgrade');
  }

  nodes = nodes.map((node) => node.id === status.id ? status : node);
  byId('node-table').innerHTML = nodes.map(renderNodeRow).join('');
  window.clearTimeout(upgradePollTimer);
  upgradePollTimer = null;
  if (status.upgrade_task && ['queued', 'applying'].includes(status.upgrade_task.status)) {
    upgradePollTimer = window.setTimeout(() => loadNodeUpgradeStatus().catch((error) => setUpgradeError(error.message)), 2000);
  }
}

async function loadNodeUpgradeStatus() {
  if (!upgradeNodeID) return;
  const nodeID = upgradeNodeID;
  const status = await request(`/api/nodes/${nodeID}/upgrade`);
  if (nodeID !== upgradeNodeID) return;
  renderNodeUpgrade(status);
}

async function openNodeUpgrade(nodeID) {
  upgradeNodeID = nodeID;
  byId('node-upgrade-state').textContent = '正在读取升级状态…';
  byId('node-upgrade-current').textContent = '--';
  byId('node-upgrade-target').textContent = '--';
  hide('start-node-upgrade');
  setUpgradeError();
  const dialog = byId('node-upgrade-dialog');
  if (!dialog.open) dialog.showModal();
  try {
    await loadNodeUpgradeStatus();
  } catch (error) {
    setUpgradeError(error.message);
  }
}

function closeNodeUpgrade() {
  window.clearTimeout(upgradePollTimer);
  upgradePollTimer = null;
  upgradeNodeID = '';
  const dialog = byId('node-upgrade-dialog');
  if (dialog.open) dialog.close();
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

async function refreshOverview() {
  if (overviewLoading) return;
  overviewLoading = true;
  const section = byId('overview');
  const buttons = [byId('refresh-overview'), byId('refresh-site-analytics')];
  const state = byId('overview-state');
  section.setAttribute('aria-busy', 'true');
  buttons.forEach((button) => {
    button.disabled = true;
    button.classList.add('is-loading');
  });
  if (!overviewLoaded) {
    state.className = 'overview-state';
    state.textContent = '正在加载概览数据…';
    if (activeRoute.view === 'overview' && activeRoute.page === 'site-analytics') {
      byId('overview-site-detail-state').className = 'overview-state';
      byId('overview-site-detail-state').textContent = '正在加载站点请求数据…';
      hide('overview-site-detail-content');
    }
  }
  try {
    const data = await request('/api/overview');
    renderOverview(data);
    overviewLoaded = true;
    const requests = Number(data.totals?.requests || 0);
    state.textContent = requests ? '' : '最近 24 小时暂无请求数据。';
    state.className = requests ? 'overview-state hidden' : 'overview-state empty';
    byId('overview-updated').textContent = `更新于 ${new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })}`;
    if (byId('notice').textContent.startsWith('概览数据加载失败')) byId('notice').textContent = '';
  } catch (error) {
    if (!overviewLoaded) {
      state.className = 'overview-state error';
      state.textContent = '概览数据暂时不可用，请稍后刷新。';
      if (activeRoute.view === 'overview' && activeRoute.page === 'site-analytics') {
        byId('overview-site-detail-state').className = 'overview-state error';
        byId('overview-site-detail-state').textContent = '站点请求数据暂时不可用，请稍后刷新。';
        hide('overview-site-detail-content');
      }
    }
    notice(`概览数据加载失败：${error.message}`);
  } finally {
    overviewLoading = false;
    section.setAttribute('aria-busy', 'false');
    buttons.forEach((button) => {
      button.disabled = false;
      button.classList.remove('is-loading');
    });
  }
}

function renderLogFilterOptions() {
  const siteSelect = byId('log-site');
  const nodeSelect = byId('log-node');
  const selectedSite = siteSelect.value;
  const selectedNode = nodeSelect.value;
  const siteOptions = [...sites].sort((left, right) => String(left.name || left.id).localeCompare(String(right.name || right.id), 'zh-CN'));
  const nodeOptions = [...nodes].sort((left, right) => String(left.name || left.id).localeCompare(String(right.name || right.id), 'zh-CN'));
  siteSelect.innerHTML = `<option value="">全部站点</option>${siteOptions.map((site) => `<option value="${escapeHTML(site.id)}">${escapeHTML(site.name || site.id)}</option>`).join('')}`;
  nodeSelect.innerHTML = `<option value="">全部节点</option>${nodeOptions.map((node) => `<option value="${escapeHTML(node.id)}">${escapeHTML(node.name || node.id)}</option>`).join('')}`;
  if (siteOptions.some((site) => site.id === selectedSite)) siteSelect.value = selectedSite;
  if (nodeOptions.some((node) => node.id === selectedNode)) nodeSelect.value = selectedNode;
}

function initializeLogSearch() {
  if (logSearchInitialized) return;
  logSearchInitialized = true;
  resetLogSearchForm();
  if (routeDataReady) void runLogSearch();
}

function resetLogSearchForm() {
  byId('log-time-range').value = '1h';
  byId('log-status').value = '';
  byId('log-path').value = '';
  byId('log-client-ip').value = '';
  byId('log-site').value = '';
  byId('log-node').value = '';
  byId('log-method').value = '';
  byId('log-cache-status').value = '';
  byId('log-from').value = '';
  byId('log-to').value = '';
  toggleLogCustomRange();
  logLoaded = false;
  logQueryState = null;
  logPageOffset = 0;
  logPageHasMore = false;
  byId('log-table').innerHTML = '';
  hide('log-results-content');
  renderLogPagination();
}

function toggleLogCustomRange() {
  const custom = byId('log-time-range').value === 'custom';
  byId('log-from-wrap').classList.toggle('hidden', !custom);
  byId('log-to-wrap').classList.toggle('hidden', !custom);
  if (custom) {
    const now = new Date();
    if (!byId('log-to').value) byId('log-to').value = formatDateTimeLocal(now);
    if (!byId('log-from').value) byId('log-from').value = formatDateTimeLocal(new Date(now.getTime() - 60 * 60 * 1000));
  }
}

function formatDateTimeLocal(value) {
  const date = value instanceof Date ? value : new Date(value);
  const pad = (number) => String(number).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

function logSearchWindow(keepWindow) {
  if (keepWindow && logQueryState) return { from: new Date(logQueryState.from), to: new Date(logQueryState.to) };
  const range = byId('log-time-range').value;
  const to = new Date();
  if (range === 'custom') {
    const from = new Date(byId('log-from').value);
    const customTo = new Date(byId('log-to').value);
    if (Number.isNaN(from.getTime()) || Number.isNaN(customTo.getTime())) throw new Error('请填写有效的自定义时间范围');
    return { from, to: customTo };
  }
  const durations = { '15m': 15 * 60 * 1000, '1h': 60 * 60 * 1000, '6h': 6 * 60 * 60 * 1000, '24h': 24 * 60 * 60 * 1000, '7d': 7 * 24 * 60 * 60 * 1000 };
  return { from: new Date(to.getTime() - (durations[range] || durations['1h'])), to };
}

function collectLogFilters() {
  return {
    site_id: byId('log-site').value,
    node_id: byId('log-node').value,
    method: byId('log-method').value,
    status: byId('log-status').value.trim(),
    path: byId('log-path').value.trim(),
    client_ip: byId('log-client-ip').value.trim(),
    cache_status: byId('log-cache-status').value,
  };
}

async function runLogSearch({ offset = 0, keepWindow = false } = {}) {
  if (logLoading) return;
  let rangeWindow;
  try {
    rangeWindow = logSearchWindow(keepWindow);
    if (!(rangeWindow.from < rangeWindow.to)) throw new Error('开始时间必须早于结束时间');
    if (rangeWindow.to - rangeWindow.from > 7 * 24 * 60 * 60 * 1000) throw new Error('日志检索范围不能超过 7 天');
  } catch (error) {
    setLogSearchState(error.message, 'error');
    hide('log-results-content');
    return;
  }

  if (logRequestController) logRequestController.abort();
  const controller = new AbortController();
  logRequestController = controller;
  logLoading = true;
  logLoaded = false;
  const section = byId('logs');
  section.setAttribute('aria-busy', 'true');
  setLogSearchState('正在检索日志…', 'loading');
  hide('log-results-content');
  ['log-search-submit', 'log-search-reset', 'refresh-logs', 'log-prev', 'log-next'].forEach((id) => { byId(id).disabled = true; });
  byId('log-search-submit').classList.add('is-loading');
  byId('refresh-logs').classList.add('is-loading');

  const filters = keepWindow && logQueryState ? {
    site_id: logQueryState.site_id,
    node_id: logQueryState.node_id,
    method: logQueryState.method,
    status: logQueryState.status,
    path: logQueryState.path,
    client_ip: logQueryState.client_ip,
    cache_status: logQueryState.cache_status,
  } : collectLogFilters();
  const params = new URLSearchParams({ from: rangeWindow.from.toISOString(), to: rangeWindow.to.toISOString(), offset: String(Math.max(0, offset)) });
  Object.entries(filters).forEach(([key, value]) => { if (value) params.set(key, value); });
  try {
    const data = await request(`/api/logs?${params.toString()}`, { signal: controller.signal });
    if (logRequestController !== controller) return;
    const logs = Array.isArray(data.logs) ? data.logs : [];
    logQueryState = { ...filters, from: data.from || rangeWindow.from.toISOString(), to: data.to || rangeWindow.to.toISOString() };
    logPageOffset = Number(data.offset ?? offset) || 0;
    logPageHasMore = Boolean(data.has_more);
    logLoaded = true;
    renderLogRows(logs);
    renderLogPagination();
    if (!logs.length) {
      setLogSearchState('当前时间和筛选条件下没有匹配的日志。', 'empty');
      byId('log-results-meta').textContent = `${formatDateTime(data.from || rangeWindow.from)} 至 ${formatDateTime(data.to || rangeWindow.to)} · 本页无结果`;
      if (logPageOffset > 0) show('log-results-content'); else hide('log-results-content');
    } else {
      hide('log-search-state');
      byId('log-results-meta').textContent = `${numberFormatter.format(logs.length)} 条结果 · ${formatDateTime(data.from || rangeWindow.from)} 至 ${formatDateTime(data.to || rangeWindow.to)}${data.has_more ? ' · 还有更多结果' : ''}`;
      show('log-results-content');
    }
  } catch (error) {
    if (error.name === 'AbortError') return;
    logLoaded = false;
    setLogSearchState(`日志检索失败：${error.message}`, 'error');
    hide('log-results-content');
  } finally {
    if (logRequestController === controller) {
      logRequestController = null;
      logLoading = false;
      section.setAttribute('aria-busy', 'false');
      ['log-search-submit', 'log-search-reset', 'refresh-logs'].forEach((id) => { byId(id).disabled = false; });
      renderLogPagination();
      byId('log-search-submit').classList.remove('is-loading');
      byId('refresh-logs').classList.remove('is-loading');
    }
  }
}

function cancelLogSearch() {
  if (logRequestController) logRequestController.abort();
  logRequestController = null;
  logLoading = false;
  byId('logs').setAttribute('aria-busy', 'false');
  ['log-search-submit', 'log-search-reset', 'refresh-logs'].forEach((id) => { byId(id).disabled = false; });
  byId('log-search-submit').classList.remove('is-loading');
  byId('refresh-logs').classList.remove('is-loading');
  renderLogPagination();
}

function setLogSearchState(message, kind) {
  const state = byId('log-search-state');
  state.className = `overview-state${kind === 'empty' ? ' empty' : ''}${kind === 'error' ? ' error' : ''}`;
  state.textContent = message;
  show('log-search-state');
}

function renderLogRows(logs) {
  if (!logs.length) {
    byId('log-table').innerHTML = '<tr><td colspan="8" class="muted">本页没有匹配的日志。</td></tr>';
    return;
  }
  byId('log-table').innerHTML = logs.map((event) => {
    const site = sites.find((item) => item.id === event.site_id);
    const node = nodes.find((item) => item.id === event.node_id);
    const status = Number(event.status || 0);
    const statusClass = status >= 500 ? 'status-code-5xx' : status >= 400 ? 'status-code-4xx' : status >= 300 ? 'status-code-3xx' : status >= 200 ? 'status-code-2xx' : 'status-code-other';
    const path = event.path || '/';
    return `<tr><td><time datetime="${escapeHTML(event.timestamp || '')}" title="${escapeHTML(event.timestamp || '')}">${escapeHTML(formatDateTime(event.timestamp))}</time></td><td><strong class="log-primary-text" title="${escapeHTML(site?.name || event.site_id || '未知站点')}">${escapeHTML(site?.name || event.site_id || '未知站点')}</strong><span class="log-secondary-text" title="${escapeHTML(node?.name || event.node_id || '')}">${escapeHTML(node?.name || event.node_id || '未知节点')}</span></td><td><span class="log-method">${escapeHTML(event.method || '--')}</span><code class="log-path" title="${escapeHTML(path)}">${escapeHTML(path)}</code></td><td><span class="log-status-code ${statusClass}">${escapeHTML(String(status || '--'))}</span></td><td><code>${escapeHTML(event.client_ip || '--')}</code></td><td><strong>${formatBytes(Number(event.bytes || 0))}</strong><span class="log-secondary-text">${numberFormatter.format(Math.max(0, Number(event.duration_ms || 0)))} ms</span></td><td><span class="log-cache-status">${escapeHTML(formatLogCacheStatus(event.cache_status))}</span></td><td><code class="log-upstream" title="${escapeHTML(event.upstream || '')}">${escapeHTML(event.upstream || '--')}</code></td></tr>`;
  }).join('');
}

function formatLogCacheStatus(value) {
  const labels = { HIT: '命中', MISS: '未命中', BYPASS: '绕过', EXPIRED: '已过期', STALE: '陈旧', UPDATING: '更新中', REVALIDATED: '重新验证' };
  const normalized = String(value || '').toUpperCase();
  return normalized ? `${labels[normalized] || normalized}（${normalized}）` : '无缓存';
}

function renderLogPagination() {
  const page = Math.floor(logPageOffset / logSearchPageSize()) + 1;
  byId('log-page-label').textContent = `第 ${numberFormatter.format(page)} 页`;
  byId('log-prev').disabled = logLoading || logPageOffset <= 0;
  byId('log-next').disabled = logLoading || !logPageHasMore;
}

function logSearchPageSize() { return 100; }

function renderOverview(data) {
  overviewData = data;
  const totals = data.totals || {};
  const series = Array.isArray(data.series) ? data.series : [];
  const requests = Number(totals.requests || 0);
  const errorRequests = Number(totals.error_requests || 0);
  byId('overview-requests').textContent = formatCompactNumber(requests);
  byId('overview-traffic').textContent = formatBytes(Number(totals.bytes || 0));
  byId('overview-error-rate').textContent = formatPercent(requests ? errorRequests / requests : 0);

  renderSparkline(byId('overview-requests-chart'), series.map((point) => Number(point.requests || 0)), '最近 24 小时请求趋势');
  renderSparkline(byId('overview-traffic-chart'), series.map((point) => Number(point.bytes || 0)), '最近 24 小时流量趋势');
  renderSparkline(byId('overview-errors-chart'), series.map((point) => {
    const pointRequests = Number(point.requests || 0);
    return pointRequests ? Number(point.error_requests || 0) / pointRequests : 0;
  }), '最近 24 小时错误率趋势');
  renderStatusCodes(Array.isArray(data.status_codes) ? data.status_codes : [], requests);
  renderOverviewSites(Array.isArray(data.sites) ? data.sites : []);
  renderOverviewSiteDetail();
}

function renderSparkline(target, values, label) {
  target.innerHTML = sparklineSVG(values, label);
}

function sparklineSVG(values, label) {
  const data = values.length ? values.map((value) => Math.max(0, Number(value) || 0)) : [0, 0];
  const width = 300;
  const height = 80;
  const padding = 4;
  const bottom = height - padding;
  const maximum = Math.max(...data, 0);
  const points = data.map((value, index) => {
    const x = data.length === 1 ? width / 2 : padding + (index / (data.length - 1)) * (width - padding * 2);
    const y = maximum ? bottom - (value / maximum) * (height - padding * 2) : bottom;
    return [x, y];
  });
  const line = points.map(([x, y], index) => `${index ? 'L' : 'M'}${x.toFixed(2)} ${y.toFixed(2)}`).join(' ');
  const area = `${line} L${points[points.length - 1][0].toFixed(2)} ${bottom} L${points[0][0].toFixed(2)} ${bottom} Z`;
  const [endX, endY] = points[points.length - 1];
  const safeLabel = escapeHTML(label);
  return `<svg class="sparkline" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" role="img" aria-label="${safeLabel}"><title>${safeLabel}</title><path class="spark-area" d="${area}"></path><path class="spark-line" d="${line}"></path><circle class="spark-end" cx="${endX.toFixed(2)}" cy="${endY.toFixed(2)}" r="2.8"></circle></svg>`;
}

function renderStatusCodes(statusCodes, totalRequests) {
  const entries = statusCodes
    .map((item) => ({ code: String(item.code), requests: Number(item.requests || 0) }))
    .filter((item) => item.requests > 0);
  const displayed = entries.slice(0, 6);
  const otherRequests = entries.slice(6).reduce((sum, item) => sum + item.requests, 0);
  if (otherRequests) displayed.push({ code: '其他', requests: otherRequests });

  let offset = 0;
  const segments = displayed.map((item, index) => {
    const percentage = totalRequests ? (item.requests / totalRequests) * 100 : 0;
    const segment = `<circle class="donut-segment" cx="60" cy="60" r="43" pathLength="100" stroke="${overviewStatusColors[index]}" stroke-dasharray="${percentage.toFixed(4)} ${(100 - percentage).toFixed(4)}" stroke-dashoffset="${(-offset).toFixed(4)}" transform="rotate(-90 60 60)"></circle>`;
    offset += percentage;
    return segment;
  }).join('');
  const totalLabel = formatCompactNumber(totalRequests);
  const lengthAttribute = totalLabel.length > 6 ? ' textLength="48" lengthAdjust="spacingAndGlyphs"' : '';
  byId('overview-status-chart').innerHTML = `<svg viewBox="0 0 120 120" aria-hidden="true"><circle class="donut-track" cx="60" cy="60" r="43"></circle>${segments}<text class="donut-total" x="60" y="58"${lengthAttribute}>${escapeHTML(totalLabel)}</text><text class="donut-caption" x="60" y="72">请求</text></svg>`;
  byId('overview-status-legend').innerHTML = displayed.length ? displayed.map((item, index) => `<li><span class="legend-swatch swatch-${index}" aria-hidden="true"></span><span class="legend-code">${escapeHTML(item.code)}</span><span class="legend-count">${formatCompactNumber(item.requests)}</span></li>`).join('') : '<li class="legend-empty">暂无状态码数据</li>';
}

function renderOverviewSites(overviewSites) {
  byId('overview-site-count').textContent = `${numberFormatter.format(overviewSites.length)} 个站点`;
  byId('overview-site-table').innerHTML = overviewSites.map((site) => {
    const name = site.name || site.id || '未命名站点';
    const domains = Array.isArray(site.domains) ? site.domains.filter(Boolean) : [];
    const domain = domains.length ? `${domains[0]}${domains.length > 1 ? ` 等 ${domains.length} 个域名` : ''}` : '未配置域名';
    const requests = Number(site.requests || 0);
    const bytes = Number(site.bytes || 0);
    const values = Array.isArray(site.series) ? site.series.map((point) => Number(point.requests || 0)) : [];
    const fullRequests = numberFormatter.format(requests);
    return `<tr class="overview-site-row" data-id="${escapeHTML(site.id)}" tabindex="0" role="link" aria-label="查看 ${escapeHTML(name)} 的请求详情"><td><div class="site-identity"><strong title="${escapeHTML(name)}">${escapeHTML(name)}</strong><span class="site-domain" title="${escapeHTML(domains.join(', ') || domain)}">${escapeHTML(domain)}</span></div></td><td><span class="site-request-total" title="${fullRequests} 次请求">${formatCompactNumber(requests)}</span></td><td><span class="site-transfer-total">${formatBytes(bytes)}</span></td><td><div class="site-sparkline">${sparklineSVG(values, `${name} 最近 24 小时请求趋势`)}</div></td></tr>`;
  }).join('') || '<tr><td colspan="4" class="muted">暂无站点。</td></tr>';
}

function renderOverviewSiteDetail() {
  if (activeRoute.view !== 'overview' || activeRoute.page !== 'site-analytics') return;
  const state = byId('overview-site-detail-state');
  const configuredSite = sites.find((site) => site.id === activeRoute.siteID);
  if (!routeDataReady) {
    byId('overview-site-title').textContent = '加载站点…';
    byId('overview-site-meta').textContent = activeRoute.siteID;
    hide('overview-site-manage');
    hide('overview-site-detail-content');
    state.className = 'overview-state';
    state.textContent = '正在加载站点请求数据…';
    return;
  }
  if (!configuredSite) {
    byId('overview-site-title').textContent = '未找到站点';
    byId('overview-site-meta').textContent = activeRoute.siteID;
    hide('overview-site-manage');
    hide('overview-site-detail-content');
    state.className = 'overview-state error';
    state.textContent = '该站点可能已被删除，或链接中的站点 ID 无效。';
    return;
  }

  byId('overview-site-title').textContent = configuredSite.name;
  byId('overview-site-meta').textContent = `${configuredSite.domains.join(', ') || '未配置域名'} · 最近 24 小时`;
  byId('overview-site-manage').dataset.id = configuredSite.id;
  show('overview-site-manage');
  if (!overviewData) {
    hide('overview-site-detail-content');
    state.className = 'overview-state';
    state.textContent = '正在加载站点请求数据…';
    return;
  }

  const analytics = (overviewData.sites || []).find((site) => site.id === configuredSite.id);
  if (!analytics) {
    hide('overview-site-detail-content');
    state.className = 'overview-state error';
    state.textContent = '站点请求数据暂时不可用，请稍后刷新。';
    return;
  }

  const requests = Number(analytics.requests || 0);
  const bytes = Number(analytics.bytes || 0);
  const errors = Number(analytics.error_requests || 0);
  byId('overview-site-requests').textContent = numberFormatter.format(requests);
  byId('overview-site-bytes').textContent = formatBytes(bytes);
  byId('overview-site-errors').textContent = numberFormatter.format(errors);
  byId('overview-site-error-rate').textContent = formatPercent(requests ? errors / requests : 0);
  renderOverviewSiteStatusCodes(Array.isArray(analytics.status_codes) ? analytics.status_codes : [], requests);
  renderOverviewSiteSeries(analytics);
  show('overview-site-detail-content');
  if (requests) {
    hide('overview-site-detail-state');
  } else {
    state.className = 'overview-state empty';
    state.textContent = '最近 24 小时暂无请求数据。';
    show('overview-site-detail-state');
  }
}

function renderOverviewSiteStatusCodes(statusCodes, totalRequests) {
  const entries = statusCodes
    .map((item) => ({ code: String(item.code), requests: Number(item.requests || 0) }))
    .filter((item) => item.requests > 0);
  const displayed = entries.slice(0, 6);
  const otherRequests = entries.slice(6).reduce((sum, item) => sum + item.requests, 0);
  if (otherRequests) displayed.push({ code: '其他', requests: otherRequests });

  let offset = 0;
  const segments = displayed.map((item, index) => {
    const percentage = totalRequests ? (item.requests / totalRequests) * 100 : 0;
    const segment = `<circle class="donut-segment" cx="60" cy="60" r="43" pathLength="100" stroke="${overviewStatusColors[index]}" stroke-dasharray="${percentage.toFixed(4)} ${(100 - percentage).toFixed(4)}" stroke-dashoffset="${(-offset).toFixed(4)}" transform="rotate(-90 60 60)"></circle>`;
    offset += percentage;
    return segment;
  }).join('');
  const totalLabel = formatCompactNumber(totalRequests);
  const lengthAttribute = totalLabel.length > 6 ? ' textLength="48" lengthAdjust="spacingAndGlyphs"' : '';
  byId('overview-site-status-chart').innerHTML = `<svg viewBox="0 0 120 120" aria-hidden="true"><circle class="donut-track" cx="60" cy="60" r="43"></circle>${segments}<text class="donut-total" x="60" y="58"${lengthAttribute}>${escapeHTML(totalLabel)}</text><text class="donut-caption" x="60" y="72">请求</text></svg>`;
  byId('overview-site-status-list').innerHTML = entries.length ? entries.map((item, index) => `<div class="analytics-status-row"><span class="legend-swatch swatch-${Math.min(index, 6)}" aria-hidden="true"></span><strong>${escapeHTML(item.code)}</strong><span>${numberFormatter.format(item.requests)}</span><span>${formatPercent(totalRequests ? item.requests / totalRequests : 0)}</span></div>`).join('') : '<div class="analytics-status-empty">暂无状态码数据</div>';
}

function renderOverviewSiteSeries(analytics) {
  const series = Array.isArray(analytics.series) ? analytics.series : [];
  document.querySelectorAll('.analytics-metric').forEach((button) => {
    const active = button.dataset.metric === overviewSiteMetric;
    button.classList.toggle('active', active);
    button.setAttribute('aria-pressed', String(active));
  });
  const chart = byId('overview-site-series-chart');
  chart.classList.toggle('traffic', overviewSiteMetric === 'bytes');
  chart.innerHTML = analyticsSeriesSVG(series, overviewSiteMetric);
}

function analyticsSeriesSVG(series, metric) {
  if (!series.length) return '<div class="analytics-chart-empty">暂无分时数据</div>';
  const values = series.map((point) => Math.max(0, Number(point[metric] || 0)));
  const width = 900;
  const height = 250;
  const left = 70;
  const right = 18;
  const top = 18;
  const bottom = 38;
  const chartWidth = width - left - right;
  const chartHeight = height - top - bottom;
  const maximum = Math.max(...values, 0);
  const scaleMaximum = maximum || 1;
  const points = values.map((value, index) => {
    const x = values.length === 1 ? left + chartWidth / 2 : left + (index / (values.length - 1)) * chartWidth;
    const y = top + chartHeight - (value / scaleMaximum) * chartHeight;
    return [x, y];
  });
  const line = points.map(([x, y], index) => `${index ? 'L' : 'M'}${x.toFixed(2)} ${y.toFixed(2)}`).join(' ');
  const baseY = top + chartHeight;
  const area = `${line} L${points[points.length - 1][0].toFixed(2)} ${baseY} L${points[0][0].toFixed(2)} ${baseY} Z`;
  const formatValue = (value) => metric === 'bytes' ? formatBytes(value) : formatCompactNumber(value);
  const grid = [1, 0.5, 0].map((ratio) => {
    const y = top + (1 - ratio) * chartHeight;
    return `<line class="analytics-chart-grid" x1="${left}" y1="${y}" x2="${width - right}" y2="${y}"></line><text class="analytics-chart-y-label" x="${left - 10}" y="${y + 4}">${escapeHTML(formatValue(maximum * ratio))}</text>`;
  }).join('');
  const labelIndexes = [...new Set([0, Math.floor((series.length - 1) / 2), series.length - 1])];
  const xLabels = labelIndexes.map((index) => {
    const anchor = index === 0 ? 'start' : (index === series.length - 1 ? 'end' : 'middle');
    return `<text class="analytics-chart-x-label" x="${points[index][0]}" y="${height - 10}" text-anchor="${anchor}">${escapeHTML(formatAnalyticsHour(series[index].time))}</text>`;
  }).join('');
  const dots = points.map(([x, y], index) => `<circle class="analytics-chart-dot" cx="${x.toFixed(2)}" cy="${y.toFixed(2)}" r="3"><title>${escapeHTML(`${formatAnalyticsHour(series[index].time)}：${formatValue(values[index])}`)}</title></circle>`).join('');
  const label = metric === 'bytes' ? '最近 24 小时传输量趋势' : '最近 24 小时请求量趋势';
  return `<svg class="analytics-line-chart" viewBox="0 0 ${width} ${height}" role="img" aria-label="${label}"><title>${label}</title>${grid}<path class="analytics-chart-area" d="${area}"></path><path class="analytics-chart-line" d="${line}"></path>${dots}${xLabels}</svg>`;
}

function formatAnalyticsHour(value) {
  return new Date(value).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false });
}

function formatCompactNumber(value) {
  return compactNumberFormatter.format(Math.max(0, Number(value) || 0));
}

function formatPercent(value) {
  return Math.max(0, Number(value) || 0).toLocaleString('zh-CN', { style: 'percent', minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function formatBytes(value) {
  value = Number(value) || 0;
  if (value <= 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  const digits = index ? 1 : 0;
  const formatted = Number(value / (1024 ** index)).toLocaleString('zh-CN', { minimumFractionDigits: digits, maximumFractionDigits: digits });
  return `${formatted} ${units[index]}`;
}

function formatDateTime(value) { return new Date(value).toLocaleString('zh-CN', { hour12: false }); }
function escapeHTML(value) { const element = document.createElement('div'); element.textContent = value ?? ''; return element.innerHTML; }

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
    showAuthPanel(status.initialized ? 'login-panel' : 'setup-panel');
  } catch (error) { showAuthPanel('setup-panel'); }
}

function showAuthPanel(panelID) {
  hide('boot-shell');
  show('auth-shell');
  show(panelID);
}

function parseRouteHash(hash) {
  const path = hash.replace(/^#\/?/, '').replace(/\/+$/, '');
  const segments = path.split('/').filter(Boolean);
  if (segments[0] === 'overview' && segments.length === 3 && segments[1] === 'sites') {
    try { return { view: 'overview', page: 'site-analytics', siteID: decodeURIComponent(segments[2]) }; } catch (_) { return { view: 'overview', page: 'site-analytics', siteID: segments[2] }; }
  }
  if (segments[0] === 'sites') {
    if (segments.length === 1) return { view: 'sites', page: 'list', siteID: '' };
    if (segments.length === 2 && segments[1] === 'new') return { view: 'sites', page: 'new', siteID: '' };
    if (segments.length === 2) {
      try { return { view: 'sites', page: 'detail', siteID: decodeURIComponent(segments[1]) }; } catch (_) { return { view: 'sites', page: 'missing', siteID: segments[1] }; }
    }
  }
  const view = segments.length === 1 && consoleViews.has(segments[0]) ? segments[0] : 'overview';
  return { view, page: 'main', siteID: '' };
}

function routeFromLocation() { return parseRouteHash(window.location.hash); }

function routeHash(route) {
  if (route.view === 'overview' && route.page === 'site-analytics') return `#/overview/sites/${encodeURIComponent(route.siteID)}`;
  if (route.view !== 'sites') return `#/${route.view}`;
  if (route.page === 'new') return '#/sites/new';
  if (route.page === 'detail' || route.page === 'missing') return `#/sites/${encodeURIComponent(route.siteID)}`;
  return '#/sites';
}

function routeKey(route) { return `${route.view}:${route.page}:${route.siteID}`; }

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

function renderLogsRoute({ routeChanged = false } = {}) {
  if (!logSearchInitialized) initializeLogSearch();
  if (!routeDataReady) {
    setLogSearchState('正在加载站点和节点列表…', 'loading');
    return;
  }
  if (routeChanged && !logLoaded && !logLoading) void runLogSearch();
  if (!logLoaded && !logLoading) void runLogSearch();
  renderLogPagination();
}

function renderOverviewRoute(route, { routeChanged = false } = {}) {
  const detailPage = route.page === 'site-analytics';
  byId('overview-main-page').classList.toggle('hidden', detailPage);
  byId('overview-site-detail-page').classList.toggle('hidden', !detailPage);
  if (!detailPage) return;
  if (routeChanged) overviewSiteMetric = 'requests';
  renderOverviewSiteDetail();
}

function renderSiteRoute(route, { populateForm = false, routeChanged = false } = {}) {
  const detailPage = route.page !== 'list';
  byId('site-list-page').classList.toggle('hidden', detailPage);
  byId('site-detail-page').classList.toggle('hidden', !detailPage);
  if (!detailPage) {
    siteFormReady = false;
    hide('site-allowlist');
    return;
  }

  if (route.page === 'new') {
    show('site-detail-editor');
    hide('site-detail-missing');
    hide('site-summary-section');
    hide('site-operations');
    hide('site-detail-publish');
    byId('site-detail-title').textContent = '添加站点';
    byId('site-detail-meta').textContent = '创建新的站点配置';
    byId('site-detail-state').textContent = '';
    if (populateForm || !siteFormReady || byId('site-id').value) prepareNewSiteForm();
    return;
  }

  if (!routeDataReady) {
    hide('site-detail-editor');
    hide('site-detail-missing');
    hide('site-detail-publish');
    byId('site-detail-title').textContent = '加载站点…';
    byId('site-detail-meta').textContent = route.siteID;
    byId('site-detail-state').textContent = '';
    return;
  }

  const site = sites.find((item) => item.id === route.siteID);
  if (!site) {
    siteFormReady = false;
    hide('site-detail-editor');
    show('site-detail-missing');
    hide('site-detail-publish');
    byId('site-detail-title').textContent = '未找到站点';
    byId('site-detail-meta').textContent = route.siteID;
    byId('site-detail-state').textContent = '';
    return;
  }

  show('site-detail-editor');
  hide('site-detail-missing');
  show('site-summary-section');
  show('site-operations');
  show('site-detail-publish');
  byId('site-detail-title').textContent = site.name;
  if (routeChanged) {
    hide('site-allowlist');
    byId('site-detail-allowlist').setAttribute('aria-expanded', 'false');
  }
  if (populateForm || !siteFormReady || byId('site-id').value !== site.id) populateSiteForm(site);
  renderSiteDetailStatus();
}

function renderSettingsRoute({ routeChanged = false } = {}) {
  if (routeChanged && settingsData) populateSettingsForms();
  if (!settingsData && !settingsLoading) void refreshSettings({ force: true }).catch((error) => notice(error.message));
}

function activateRoute(route, { forceForm = false, focus = true } = {}) {
  const previousRoute = activeRoute;
  const routeChanged = routeKey(previousRoute) !== routeKey(route);
  if (previousRoute.view === 'logs' && route.view !== 'logs' && logRequestController) cancelLogSearch();
  if (previousRoute.view === 'security' && route.view !== 'security') {
    window.clearTimeout(securityPollTimer);
    securityPollTimer = null;
  }
  activeRoute = route;
  const restoreSidebarFocus = sidebarOpen();
  document.querySelectorAll('.nav').forEach((button) => {
    const active = button.dataset.view === route.view;
    button.classList.toggle('active', active);
    if (active) button.setAttribute('aria-current', 'page'); else button.removeAttribute('aria-current');
  });
  document.querySelectorAll('.view').forEach((section) => section.classList.toggle('hidden', section.id !== route.view));
  if (route.view === 'overview') renderOverviewRoute(route, { routeChanged });
  if (route.view === 'logs') renderLogsRoute({ routeChanged });
  if (route.view === 'security') renderSecurityRoute({ routeChanged });
  if (route.view === 'sites') renderSiteRoute(route, { populateForm: forceForm || routeChanged, routeChanged });
  if (route.view === 'settings') renderSettingsRoute({ routeChanged });
  const site = route.view === 'sites' && route.page === 'detail' ? sites.find((item) => item.id === route.siteID) : null;
  const analyticsSite = route.view === 'overview' && route.page === 'site-analytics' ? sites.find((item) => item.id === route.siteID) : null;
  const mobileTitle = route.page === 'new' ? '添加站点' : (site?.name || analyticsSite?.name || viewLabels[route.view] || viewLabels.overview);
  byId('mobile-page-title').textContent = mobileTitle;
  setSidebarOpen(false, restoreSidebarFocus);
  if (focus && routeChanged) {
    if (route.view === 'sites' && route.page !== 'list') window.requestAnimationFrame(() => byId('site-detail-title').focus());
    if (route.view === 'overview' && route.page === 'site-analytics') window.requestAnimationFrame(() => byId('overview-site-title').focus());
    if (route.view === 'logs') window.requestAnimationFrame(() => byId('logs-title').focus());
    if (route.view === 'security') window.requestAnimationFrame(() => byId('security-title').focus());
    if (route.view === 'settings') window.requestAnimationFrame(() => byId('settings-title').focus());
  }
}

function syncRouteFromLocation(options = {}) {
  const route = routeFromLocation();
  activateRoute(route, options);
  acceptedHash = routeHash(route);
}

function confirmDiscardChanges() {
  return (!siteFormDirty() && !settingsFormsDirty()) || window.confirm('有未保存的更改，确定离开吗？');
}

function navigateTo(hash) {
  const destination = routeHash(parseRouteHash(hash));
  if (destination === acceptedHash) {
    activateRoute(parseRouteHash(destination));
    return true;
  }
  if (!confirmDiscardChanges()) return false;
  markSiteFormClean();
  markSettingsFormClean();
  pendingApprovedHash = destination;
  if (window.location.hash === destination) {
    pendingApprovedHash = '';
    syncRouteFromLocation();
  } else {
    window.location.hash = destination;
  }
  return true;
}

function handleHashChange() {
  const destination = routeHash(routeFromLocation());
  if (pendingApprovedHash === destination) {
    pendingApprovedHash = '';
    syncRouteFromLocation();
    return;
  }
  const routeChanged = destination !== acceptedHash;
  if (routeChanged && !confirmDiscardChanges()) {
    pendingApprovedHash = '';
    window.history.pushState(null, '', acceptedHash);
    activateRoute(parseRouteHash(acceptedHash), { focus: false });
    return;
  }
  if (routeChanged) {
    markSiteFormClean();
    markSettingsFormClean();
  }
  pendingApprovedHash = '';
  syncRouteFromLocation();
}

function showApp() {
  hide('boot-shell');
  hide('setup-panel');
  hide('login-panel');
  hide('auth-shell');
  show('app');
  show('logout');
  byId('auth-notice').textContent = '';
  syncRouteFromLocation();
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

byId('logout').addEventListener('click', async () => {
  if (!confirmDiscardChanges()) return;
  markSiteFormClean();
  markSettingsFormClean();
  try {
    await request('/api/logout', { method: 'POST' });
  } finally {
    cancelLogSearch();
    logLoaded = false;
    logSearchInitialized = false;
    logQueryState = null;
    logPageOffset = 0;
    logPageHasMore = false;
    resetLogSearchForm();
    window.clearTimeout(certificatePollTimer);
    window.clearTimeout(publishPollTimer);
    window.clearTimeout(siteDeletePollTimer);
    window.clearTimeout(securityPollTimer);
    closeNodeUninstall();
    closeSiteDelete();
    certificatePollTimer = null;
    publishPollTimer = null;
    siteDeletePollTimer = null;
    securityPollTimer = null;
    tlsStatuses = new Map();
    publishStatuses = new Map();
    deletionStatuses = new Map();
    sites = [];
    routeDataReady = false;
    siteFormReady = false;
    settingsData = null;
    settingsFormReady = false;
    settingsFormBaseline = '';
    securityData = null;
    securityDataGeneration += 1;
    overviewLoaded = false;
    overviewData = null;
    csrf = '';
    setSidebarOpen(false);
    byId('notice').textContent = '';
    hide('app');
    hide('logout');
    show('auth-shell');
    show('login-panel');
    byId('login-password').focus();
  }
});

byId('sidebar-toggle').addEventListener('click', () => setSidebarOpen(!sidebarOpen()));
byId('sidebar-close').addEventListener('click', () => setSidebarOpen(false, true));
byId('sidebar-backdrop').addEventListener('click', () => setSidebarOpen(false, true));
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && sidebarOpen()) setSidebarOpen(false, true);
});
mobileSidebarQuery.addEventListener('change', syncSidebarMode);
byId('refresh-overview').addEventListener('click', refreshOverview);
byId('refresh-site-analytics').addEventListener('click', refreshOverview);
byId('refresh-security').addEventListener('click', () => {
  setSecurityState('正在刷新安全状态…');
  refreshSecurity().catch((error) => setSecurityState(error.message));
});
byId('deploy-security').addEventListener('click', async () => {
  if (securityActionPending) return;
  securityActionPending = true;
  securityDataGeneration += 1;
  byId('deploy-security').disabled = true;
  try {
    securityData = await request('/api/security/deploy', { method: 'POST', body: '{}' });
    renderSecurity();
    notice('安全策略已重新部署', true);
  } catch (error) {
    notice(error.message);
  } finally {
    securityActionPending = false;
    byId('deploy-security').disabled = false;
  }
});
byId('add-security-policy').addEventListener('click', () => openSecurityPolicy());
byId('close-security-policy').addEventListener('click', closeSecurityPolicy);
byId('security-policy-action').addEventListener('change', syncSecurityPolicyDuration);
byId('security-policy-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (securityActionPending || !event.currentTarget.reportValidity()) return;
  securityActionPending = true;
  securityDataGeneration += 1;
  byId('save-security-policy').disabled = true;
  setSecurityPolicyError();
  const id = byId('security-policy-id').value;
  try {
    securityData = await request(id ? `/api/security/policies/${encodeURIComponent(id)}` : '/api/security/policies', {
      method: id ? 'PUT' : 'POST', body: JSON.stringify(securityPolicyPayload()),
    });
    renderSecurity();
    closeSecurityPolicy();
    notice('安全策略已保存并进入边缘部署', true);
  } catch (error) {
    setSecurityPolicyError(error.message);
  } finally {
    securityActionPending = false;
    byId('save-security-policy').disabled = false;
  }
});
byId('security-policy-table').addEventListener('click', async (event) => {
  const button = event.target.closest('button');
  if (!button?.dataset.id || securityActionPending) return;
  const policy = securityData?.policies?.find((item) => item.id === button.dataset.id);
  if (!policy) return;
  if (button.classList.contains('edit-security-policy')) return openSecurityPolicy(policy);
  if (!button.classList.contains('delete-security-policy') || !window.confirm(`确定删除策略「${policy.name}」并重新部署吗？`)) return;
  securityActionPending = true;
  securityDataGeneration += 1;
  try {
    securityData = await request(`/api/security/policies/${encodeURIComponent(policy.id)}`, { method: 'DELETE' });
    renderSecurity();
    notice('安全策略已删除', true);
  } catch (error) {
    notice(error.message);
  } finally {
    securityActionPending = false;
  }
});
byId('security-ban-table').addEventListener('click', async (event) => {
  const button = event.target.closest('.unban-security-ip');
  if (!button?.dataset.ip || securityActionPending || !window.confirm(`确定解除 ${button.dataset.ip} 的封禁吗？`)) return;
  securityActionPending = true;
  securityDataGeneration += 1;
  try {
    securityData = await request(`/api/security/bans/${encodeURIComponent(button.dataset.ip)}`, { method: 'DELETE' });
    renderSecurity();
    notice('IP 封禁已解除，边缘节点将在下一轮同步', true);
  } catch (error) {
    notice(error.message);
  } finally {
    securityActionPending = false;
  }
});
byId('log-time-range').addEventListener('change', toggleLogCustomRange);
byId('log-search-form').addEventListener('submit', (event) => {
  event.preventDefault();
  void runLogSearch();
});
byId('log-search-reset').addEventListener('click', () => {
  if (logLoading) return;
  resetLogSearchForm();
  void runLogSearch();
});
byId('refresh-logs').addEventListener('click', () => { void runLogSearch(); });
byId('log-prev').addEventListener('click', () => {
  if (logPageOffset > 0) void runLogSearch({ offset: Math.max(0, logPageOffset - logSearchPageSize()), keepWindow: true });
});
byId('log-next').addEventListener('click', () => {
  if (logPageHasMore) void runLogSearch({ offset: logPageOffset + logSearchPageSize(), keepWindow: true });
});
byId('overview-site-back').addEventListener('click', () => navigateTo('#/overview'));
byId('overview-site-manage').addEventListener('click', () => navigateTo(`#/sites/${encodeURIComponent(byId('overview-site-manage').dataset.id)}`));
byId('overview-site-table').addEventListener('click', (event) => {
  const row = event.target.closest('.overview-site-row');
  if (row) navigateTo(`#/overview/sites/${encodeURIComponent(row.dataset.id)}`);
});
byId('overview-site-table').addEventListener('keydown', (event) => {
  if (event.key !== 'Enter') return;
  const row = event.target.closest('.overview-site-row');
  if (!row) return;
  event.preventDefault();
  navigateTo(`#/overview/sites/${encodeURIComponent(row.dataset.id)}`);
});
document.querySelectorAll('.analytics-metric').forEach((button) => button.addEventListener('click', () => {
  overviewSiteMetric = button.dataset.metric;
  renderOverviewSiteDetail();
}));

document.querySelectorAll('.nav').forEach((button) => button.addEventListener('click', () => {
  const hash = `#/${button.dataset.view}`;
  if (acceptedHash === hash) activateRoute(parseRouteHash(hash));
  else navigateTo(hash);
}));

byId('dns-settings-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (!event.currentTarget.reportValidity()) return;
  setSettingsBusy('dns-settings-form', true);
  try {
    await request('/api/settings/dns', { method: 'PUT', body: JSON.stringify({ default_ttl_seconds: Number(byId('settings-dns-ttl').value) }) });
    await refreshSettings({ force: true, preserveDirtySections: ['cloudflare', 'smtp'] });
    notice('DNS 设置已保存', true);
  } catch (error) {
    notice(error.message);
  } finally {
    setSettingsBusy('dns-settings-form', false);
  }
});

byId('cloudflare-settings-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const token = byId('settings-cloudflare-token').value.trim();
  if (!token) return notice('请输入新的 Cloudflare API Token');
  setSettingsBusy('cloudflare-settings-form', true);
  try {
    await request('/api/settings/cloudflare', { method: 'PUT', body: JSON.stringify({ token }) });
    await refreshSettings({ force: true, preserveDirtySections: ['dns', 'smtp'] });
    notice('Cloudflare API Token 已验证并保存', true);
  } catch (error) {
    notice(error.message);
  } finally {
    setSettingsBusy('cloudflare-settings-form', false);
  }
});

byId('test-cloudflare-settings').addEventListener('click', async () => {
  setSettingsBusy('cloudflare-settings-form', true);
  try {
    await request('/api/settings/cloudflare/test', { method: 'POST', body: '{}' });
    notice('Cloudflare 当前配置验证通过', true);
  } catch (error) {
    notice(error.message);
  } finally {
    setSettingsBusy('cloudflare-settings-form', false);
  }
});

byId('reset-cloudflare-settings').addEventListener('click', async () => {
  if (!window.confirm('确定删除控制台 Token，并恢复使用环境变量吗？')) return;
  setSettingsBusy('cloudflare-settings-form', true);
  try {
    await request('/api/settings/cloudflare', { method: 'DELETE' });
    await refreshSettings({ force: true, preserveDirtySections: ['dns', 'smtp'] });
    notice('Cloudflare 已恢复环境变量配置', true);
  } catch (error) {
    notice(error.message);
  } finally {
    setSettingsBusy('cloudflare-settings-form', false);
  }
});

byId('smtp-settings-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (!event.currentTarget.reportValidity()) return;
  setSettingsBusy('smtp-settings-form', true);
  try {
    await request('/api/settings/smtp', { method: 'PUT', body: JSON.stringify(smtpSettingsPayload()) });
    await refreshSettings({ force: true, preserveDirtySections: ['dns', 'cloudflare'] });
    notice('SMTP 设置已保存', true);
  } catch (error) {
    notice(error.message);
  } finally {
    setSettingsBusy('smtp-settings-form', false);
  }
});

byId('test-smtp-settings').addEventListener('click', async () => {
  if (!byId('smtp-settings-form').reportValidity()) return;
  setSettingsBusy('smtp-settings-form', true);
  try {
    await request('/api/settings/smtp/test', { method: 'POST', body: JSON.stringify(smtpSettingsPayload()) });
    notice('SMTP 测试邮件已发送', true);
  } catch (error) {
    notice(error.message);
  } finally {
    setSettingsBusy('smtp-settings-form', false);
  }
});

byId('reset-smtp-settings').addEventListener('click', async () => {
  if (!window.confirm('确定删除控制台 SMTP 配置，并恢复使用环境变量吗？')) return;
  setSettingsBusy('smtp-settings-form', true);
  try {
    await request('/api/settings/smtp', { method: 'DELETE' });
    await refreshSettings({ force: true, preserveDirtySections: ['dns', 'cloudflare'] });
    notice('SMTP 已恢复环境变量配置', true);
  } catch (error) {
    notice(error.message);
  } finally {
    setSettingsBusy('smtp-settings-form', false);
  }
});

byId('settings-smtp-enabled').addEventListener('change', syncSMTPControls);
byId('settings-smtp-security').addEventListener('change', () => {
  const port = Number(byId('settings-smtp-port').value);
  if (port === 465 || port === 587) byId('settings-smtp-port').value = byId('settings-smtp-security').value === 'tls' ? '465' : '587';
});
window.addEventListener('hashchange', handleHashChange);
window.addEventListener('beforeunload', (event) => {
  if (!siteFormDirty() && !settingsFormsDirty()) return;
  event.preventDefault();
  event.returnValue = '';
});

byId('show-node-form').addEventListener('click', () => show('node-form'));
byId('show-site-form').addEventListener('click', () => navigateTo('#/sites/new'));
byId('site-detail-back').addEventListener('click', () => navigateTo('#/sites'));
byId('site-missing-back').addEventListener('click', () => navigateTo('#/sites'));
byId('site-cancel').addEventListener('click', () => navigateTo('#/sites'));
document.querySelectorAll('.cancel').forEach((button) => button.addEventListener('click', () => button.closest('form').classList.add('hidden')));

byId('close-node-upgrade').addEventListener('click', closeNodeUpgrade);
byId('node-upgrade-dialog').addEventListener('close', () => {
  window.clearTimeout(upgradePollTimer);
  upgradePollTimer = null;
  upgradeNodeID = '';
});
byId('start-node-upgrade').addEventListener('click', async () => {
  if (upgradeActionPending || !upgradeNodeID) return;
  const nodeID = upgradeNodeID;
  setUpgradeBusy(true);
  setUpgradeError();
  try {
    const status = await request(`/api/nodes/${nodeID}/upgrade`, { method: 'POST' });
    if (nodeID === upgradeNodeID) {
      renderNodeUpgrade(status);
      notice('节点在线升级已开始', true);
    }
  } catch (error) {
    if (nodeID === upgradeNodeID) {
      if (error.data?.upgrade) renderNodeUpgrade(error.data.upgrade);
      setUpgradeError(error.message);
    }
  } finally {
    setUpgradeBusy(false);
  }
});

byId('close-node-uninstall').addEventListener('click', closeNodeUninstall);
byId('node-uninstall-dialog').addEventListener('close', () => {
  window.clearTimeout(uninstallPollTimer);
  uninstallPollTimer = null;
  uninstallNodeID = '';
  uninstallNodeName = '';
  uninstallCommand = '';
});
byId('site-detail-delete').addEventListener('click', () => openSiteDelete(byId('site-detail-delete').dataset.id));
byId('close-site-delete').addEventListener('click', closeSiteDelete);
byId('site-delete-dialog').addEventListener('close', () => {
  deletingSiteID = '';
  deletingSiteName = '';
  siteDeletePending = false;
});
byId('confirm-site-delete').addEventListener('click', async () => {
  if (byId('site-delete-confirm').value !== deletingSiteName) return setSiteDeleteError('请输入完整且完全一致的站点名称。');
  if (siteDeletePending || !deletingSiteID) return;
  const siteID = deletingSiteID;
  setSiteDeleteBusy(true);
  setSiteDeleteError();
  try {
    const status = await request(`/api/sites/${siteID}`, { method: 'DELETE', body: JSON.stringify({ confirmation: byId('site-delete-confirm').value }) });
    deletionStatuses.set(siteID, status);
    markSiteFormClean();
    renderSiteDeleteDialog(status);
    await refresh();
    if (siteID === deletingSiteID) notice('站点删除已开始，托管 DNS 已撤销', true);
  } catch (error) {
    if (siteID === deletingSiteID) {
      if (error.data?.deletion?.task) {
        deletionStatuses.set(siteID, error.data.deletion);
        renderSiteDeleteDialog(error.data.deletion);
      }
      setSiteDeleteError(error.message);
      await refresh().catch(() => {});
    }
  } finally {
    setSiteDeleteBusy(false);
  }
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
  const payload = siteFormPayload();
  const siteID = byId('site-id').value;
  const submitButton = byId('site-submit');
  submitButton.disabled = true;
  try {
    const savedSite = await request(siteID ? `/api/sites/${siteID}` : '/api/sites', { method: siteID ? 'PUT' : 'POST', body: JSON.stringify(payload) });
    markSiteFormClean();
    await refresh();
    if (!siteID) navigateTo(`#/sites/${encodeURIComponent(savedSite.id)}`);
		notice(siteID ? '站点已更新，请发布以应用新配置。' : (siteNeedsTLS(savedSite) ? '站点已创建，请在发布前申请 TLS 证书。' : '站点已创建，可以直接发布。'), true);
  } catch (error) {
    notice(error.message);
  } finally {
    submitButton.disabled = false;
  }
});

function siteFormPayload() {
  const backup = byId('site-backup-url').value.trim();
  const primaryURL = byId('site-primary-url').value;
  const payload = {
    name: byId('site-name').value,
    zone_id: byId('site-zone').value,
    domains: split(byId('site-domains').value),
    node_ids: selectedNodeIDs(),
    primary_origin: { url: primaryURL, host_header: byId('site-primary-host').value, tls_server_name: originURLUsesTLS(primaryURL) ? byId('site-primary-tls-name').value : '', enabled: true },
    passthrough: byId('site-passthrough').checked,
    client_max_body_size_mb: Number(byId('site-client-max-body-size').value),
		read_write_timeout_seconds: Number(byId('site-read-write-timeout').value),
		dns_ttl_seconds: byId('site-dns-ttl-inherit').checked ? null : Number(byId('site-dns-ttl').value),
		tcp_only: byId('site-tcp-only').checked,
		tcp_forwards: tcpForwardPayload(),
    enabled: byId('site-enabled').checked,
  };
  if (backup) payload.backup_origin = { url: backup, host_header: byId('site-backup-host').value, tls_server_name: originURLUsesTLS(backup) ? byId('site-backup-tls-name').value : '', enabled: true };
  return payload;
}

function siteFormSnapshot() {
  return JSON.stringify({ site_id: byId('site-id').value, payload: siteFormPayload() });
}

function siteFormDirty() {
  return siteFormReady && activeRoute.view === 'sites' && activeRoute.page !== 'list' && siteFormSnapshot() !== siteFormBaseline;
}

function markSiteFormClean() {
  siteFormBaseline = siteFormReady ? siteFormSnapshot() : '';
}

function prepareNewSiteForm() {
  byId('site-form').reset();
  byId('site-id').value = '';
  byId('site-zone').disabled = false;
  byId('site-client-max-body-size').value = String(defaultClientMaxBodySizeMB);
  byId('site-read-write-timeout').value = String(defaultReadWriteTimeoutSeconds);
	byId('site-passthrough').checked = false;
	byId('site-tcp-only').checked = false;
	byId('site-tcp-forward-list').replaceChildren();
  byId('site-dns-ttl-inherit').checked = true;
  byId('site-dns-ttl').value = String(settingsData?.dns?.default_ttl_seconds ?? defaultDNSTTLSeconds);
  syncSiteDNSTTLControl();
	byId('site-enabled').checked = true;
	syncSiteTrafficMode();
  byId('site-submit').textContent = '创建站点';
  renderNodeSelector();
  siteFormReady = true;
  markSiteFormClean();
  updateSiteFormPreview();
}

function populateSiteForm(site) {
  byId('site-id').value = site.id;
  byId('site-name').value = site.name;
  byId('site-zone').value = site.zone_id;
  byId('site-zone').disabled = true;
  byId('site-domains').value = site.domains.join(', ');
  byId('site-primary-url').value = site.primary_origin.url;
  byId('site-primary-host').value = site.primary_origin.host_header || '';
  byId('site-primary-tls-name').value = site.primary_origin.tls_server_name || '';
  byId('site-backup-url').value = site.backup_origin?.url || '';
  byId('site-backup-host').value = site.backup_origin?.host_header || '';
  byId('site-backup-tls-name').value = site.backup_origin?.tls_server_name || '';
  byId('site-client-max-body-size').value = String(site.client_max_body_size_mb ?? defaultClientMaxBodySizeMB);
  byId('site-read-write-timeout').value = String(site.read_write_timeout_seconds ?? defaultReadWriteTimeoutSeconds);
	byId('site-passthrough').checked = Boolean(site.passthrough);
	byId('site-tcp-only').checked = Boolean(site.tcp_only);
	byId('site-tcp-forward-list').replaceChildren();
	(site.tcp_forwards || []).forEach((forward) => addTCPForwardRow(forward));
  byId('site-dns-ttl-inherit').checked = site.dns_ttl_seconds == null;
  byId('site-dns-ttl').value = String(site.dns_ttl_seconds ?? settingsData?.dns?.default_ttl_seconds ?? defaultDNSTTLSeconds);
  syncSiteDNSTTLControl();
	byId('site-enabled').checked = site.enabled;
	syncSiteTrafficMode();
  byId('site-submit').textContent = '保存更改';
  renderNodeSelector(site.node_ids);
  siteFormReady = true;
  markSiteFormClean();
  updateSiteFormPreview(site);
}

byId('site-form').addEventListener('input', () => updateSiteFormPreview(sites.find((site) => site.id === byId('site-id').value) || null));
byId('site-form').addEventListener('change', () => updateSiteFormPreview(sites.find((site) => site.id === byId('site-id').value) || null));
byId('site-dns-ttl-inherit').addEventListener('change', syncSiteDNSTTLControl);
byId('site-tcp-only').addEventListener('change', () => {
	if (byId('site-tcp-only').checked && !byId('site-tcp-forward-list').children.length) addTCPForwardRow();
	syncSiteTrafficMode();
});
byId('add-tcp-forward').addEventListener('click', () => {
	addTCPForwardRow().querySelector('.tcp-forward-name').focus();
	updateSiteFormPreview(sites.find((site) => site.id === byId('site-id').value) || null);
});
byId('site-tcp-forward-list').addEventListener('click', (event) => {
	const button = event.target.closest('.remove-tcp-forward');
	if (!button) return;
	button.closest('.tcp-forward-row').remove();
	updateSiteFormPreview(sites.find((site) => site.id === byId('site-id').value) || null);
});
byId('site-tcp-forward-list').addEventListener('change', (event) => {
	const row = event.target.closest('.tcp-forward-row');
	if (row) syncTCPForwardRow(row);
});

function syncSiteDNSTTLControl() {
  const inherit = byId('site-dns-ttl-inherit').checked;
  byId('site-dns-ttl-wrap').classList.toggle('hidden', inherit);
  byId('site-dns-ttl').disabled = inherit || byId('site-name').disabled;
}

document.addEventListener('click', async (event) => {
  const button = event.target.closest('button'); if (!button || !button.dataset.id) return;
  try {
    if (button.classList.contains('enroll')) { const result = await request(`/api/nodes/${button.dataset.id}/enrollment-token`, { method: 'POST' }); byId('node-command').textContent = result.install_command; show('node-command'); }
    if (button.classList.contains('node-upgrade')) await openNodeUpgrade(button.dataset.id);
    if (button.classList.contains('node-status')) { await request(`/api/nodes/${button.dataset.id}/status`, { method: 'POST', body: JSON.stringify({ status: button.dataset.status }) }); await refresh(); }
    if (button.classList.contains('node-uninstall') || button.classList.contains('node-delete')) await openNodeUninstall(button.dataset.id);
    if (button.classList.contains('publish')) { const task = await request(`/api/sites/${button.dataset.id}/publish`, { method: 'POST' }); await refresh(); notice(`发布任务 ${task.id}：${taskStatusLabel(task.status)}`, true); }
    if (button.classList.contains('invalidate')) { const task = await request(`/api/sites/${button.dataset.id}/invalidate-cache`, { method: 'POST' }); await refresh(); notice(`缓存已刷新，任务 ${task.id}`, true); }
    if (button.classList.contains('certificate')) { const task = await request(`/api/sites/${button.dataset.id}/certificate`, { method: 'POST' }); tlsStatuses.set(button.dataset.id, { certificate_task: task, published_after_certificate: false }); renderSiteViews(); scheduleCertificatePoll(); notice(`TLS 任务 ${task.id}：${taskStatusLabel(task.status)}`, true); }
    if (button.classList.contains('manage-site')) navigateTo(`#/sites/${encodeURIComponent(button.dataset.id)}`);
    if (button.classList.contains('open-site-delete')) await openSiteDelete(button.dataset.id);
    if (button.classList.contains('allowlist')) {
      if (!byId('site-allowlist').classList.contains('hidden')) {
        hide('site-allowlist');
        button.setAttribute('aria-expanded', 'false');
      } else {
        const result = await request(`/api/sites/${button.dataset.id}/origin-allowlist`);
        byId('site-allowlist').textContent = `源站防火墙或安全组应只允许以下边缘节点 IPv4 CIDR。添加、移除或撤销节点后请同步更新。\n\n${result.ipv4_cidrs.join('\n')}`;
        show('site-allowlist');
        button.setAttribute('aria-expanded', 'true');
      }
    }
  } catch (error) { notice(error.message); }
});

syncSidebarMode();
boot();
