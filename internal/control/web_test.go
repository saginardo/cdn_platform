package control

import (
	"strings"
	"testing"
)

func TestEmbeddedConsoleUsesSimplifiedChinese(t *testing.T) {
	contents, err := embeddedWeb.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(contents)
	for _, expected := range []string{
		`<html lang="zh-CN">`,
		`<span class="brand">CDN Platform</span>`,
		`>概览</button>`,
		`>日志</button>`,
		`>节点</button>`,
		`>站点</button>`,
		`最近 24 小时`,
		`HTTP 4xx / 5xx`,
		`站点请求趋势`,
		`<th scope="col">传输量</th>`,
		`Cloudflare 区域 ID`,
		`id="site-client-max-body-size"`,
		`<option value="1024">1024 MiB</option>`,
		`回源读写空闲超时`,
		`id="site-read-write-timeout"`,
		`<option value="3600">60 分钟</option>`,
		`透传模式（仅 HTTP(S)，禁用 Nginx 缓存）`,
		`id="node-uninstall-dialog"`,
		`id="node-upgrade-dialog"`,
		`id="start-node-upgrade"`,
		`>开始卸载准备</button>`,
		`>生成卸载命令</button>`,
		`强制完成（不清理远端）`,
		`>删除记录</button>`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("index.html does not contain %q", expected)
		}
	}
	for _, unexpected := range []string{">Overview</button>", ">Nodes</button>", ">Sites</button>", "流式路径（WebSocket / SSE）", `id="site-stream-paths"`} {
		if strings.Contains(page, unexpected) {
			t.Fatalf("index.html still contains %q", unexpected)
		}
	}
}

func TestEmbeddedConsoleSupportsSingleNodeOnlineUpgrade(t *testing.T) {
	pageContents, err := embeddedWeb.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageContents)
	for _, expected := range []string{`<th>代理版本</th>`, `id="node-upgrade-current"`, `id="node-upgrade-target"`, `id="node-upgrade-state"`} {
		if !strings.Contains(page, expected) {
			t.Fatalf("index.html does not contain %q", expected)
		}
	}
	scriptContents, err := embeddedWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptContents)
	for _, expected := range []string{
		"function renderAgentVersion(node)",
		"function renderNodeUpgrade(status)",
		"request(`/api/nodes/${nodeID}/upgrade`)",
		"request(`/api/nodes/${nodeID}/upgrade`, { method: 'POST' })",
		"classList.contains('node-upgrade')",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
		}
	}
	stateStart := strings.Index(script, "function nodeUpgradeStateText(status)")
	if stateStart < 0 {
		t.Fatal("online upgrade state renderer is missing")
	}
	stateEnd := strings.Index(script[stateStart:], "function setUpgradeError")
	if stateEnd < 0 {
		t.Fatal("online upgrade error renderer is missing")
	}
	stateRenderer := script[stateStart : stateStart+stateEnd]
	upToDate := strings.Index(stateRenderer, "if (status.upgrade_up_to_date)")
	failed := strings.Index(stateRenderer, "if (task?.status === 'failed')")
	if upToDate < 0 || failed < 0 || upToDate > failed {
		t.Fatal("actual installed digest does not override a stale failed task in the upgrade dialog")
	}
	if !strings.Contains(script, "status.upgrade_task?.status === 'failed' && !status.upgrade_up_to_date") {
		t.Fatal("stale failed task still renders an error for an up-to-date node")
	}
	stylesContents, err := embeddedWeb.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	if styles := string(stylesContents); !strings.Contains(styles, ".agent-version") || !strings.Contains(styles, ".upgrade-facts") {
		t.Fatal("online upgrade styles are missing")
	}
}

func TestEmbeddedConsoleWaitsForAuthenticationBeforeShowingUI(t *testing.T) {
	pageContents, err := embeddedWeb.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageContents)
	for _, expected := range []string{
		`id="boot-shell" class="boot-shell" role="status" aria-live="polite" aria-busy="true"`,
		`<span class="boot-spinner" aria-hidden="true"></span>`,
		`<span class="boot-label">正在验证登录状态</span>`,
		`id="auth-shell" class="auth-shell hidden"`,
		`id="app" class="console-shell hidden"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("index.html does not contain %q", expected)
		}
	}

	scriptContents, err := embeddedWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptContents)
	for _, expected := range []string{
		"function showAuthPanel(panelID)",
		"showAuthPanel(status.initialized ? 'login-panel' : 'setup-panel')",
		"showAuthPanel('setup-panel')",
		"hide('boot-shell')",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
		}
	}

	styleContents, err := embeddedWeb.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	styles := string(styleContents)
	for _, expected := range []string{
		".boot-shell { display: grid; min-height: 100vh;",
		".boot-status { display: grid; width: min(100%, 240px);",
		".boot-spinner { display: block; width: 24px; height: 24px;",
		"@keyframes boot-spin",
		".boot-spinner { animation: none; }",
	} {
		if !strings.Contains(styles, expected) {
			t.Fatalf("styles.css does not contain %q", expected)
		}
	}
}

func TestEmbeddedConsoleRendersOverviewChartsAndManualRefresh(t *testing.T) {
	pageContents, err := embeddedWeb.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageContents)
	for _, expected := range []string{
		`id="refresh-overview"`,
		`id="overview-requests-chart"`,
		`id="overview-traffic-chart"`,
		`id="overview-errors-chart"`,
		`id="overview-status-chart"`,
		`id="overview-status-legend"`,
		`id="overview-site-table"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("index.html does not contain %q", expected)
		}
	}

	scriptContents, err := embeddedWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptContents)
	for _, expected := range []string{
		"request('/api/overview')",
		"function sparklineSVG(values, label)",
		"function renderStatusCodes(statusCodes, totalRequests)",
		"function renderOverviewSites(overviewSites)",
		"byId('refresh-overview').addEventListener('click', refreshOverview)",
		"point.error_requests",
		"site.bytes",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
		}
	}
	if strings.Contains(script, "refreshTraffic") {
		t.Fatal("app.js still contains the per-site traffic refresh path")
	}

	styleContents, err := embeddedWeb.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	styles := string(styleContents)
	for _, expected := range []string{
		"grid-template-columns: repeat(4, minmax(0, 1fr))",
		".status-overview",
		".site-sparkline",
		"@media (max-width: 1200px)",
	} {
		if !strings.Contains(styles, expected) {
			t.Fatalf("styles.css does not contain %q", expected)
		}
	}
}

func TestEmbeddedConsoleUsesDedicatedOverviewSiteAnalyticsRoute(t *testing.T) {
	pageContents, err := embeddedWeb.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageContents)
	for _, expected := range []string{
		`id="overview-main-page"`,
		`id="overview-site-detail-page"`,
		`id="overview-site-back"`,
		`id="overview-site-manage"`,
		`id="refresh-site-analytics"`,
		`id="overview-site-requests"`,
		`id="overview-site-bytes"`,
		`id="overview-site-errors"`,
		`id="overview-site-error-rate"`,
		`id="overview-site-status-chart"`,
		`id="overview-site-status-list"`,
		`data-metric="requests"`,
		`data-metric="bytes"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("index.html does not contain %q", expected)
		}
	}
	if strings.Contains(page, `id="overview-site-logs"`) {
		t.Fatal("overview analytics page exposes the deferred request-log entry")
	}
	if strings.Contains(page, `id="overview-site-hourly-table"`) {
		t.Fatal("overview analytics page still exposes the removed hourly detail table")
	}

	scriptContents, err := embeddedWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptContents)
	for _, expected := range []string{
		"segments[0] === 'overview' && segments.length === 3 && segments[1] === 'sites'",
		"page: 'site-analytics'",
		"#/overview/sites/${encodeURIComponent(route.siteID)}",
		`class="overview-site-row"`,
		`tabindex="0" role="link"`,
		"event.key !== 'Enter'",
		"function renderOverviewSiteDetail()",
		"function renderOverviewSiteStatusCodes(statusCodes, totalRequests)",
		"function analyticsSeriesSVG(series, metric)",
		"function formatAnalyticsHour(value)",
		"byId('refresh-site-analytics').addEventListener('click', refreshOverview)",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
		}
	}

	styleContents, err := embeddedWeb.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	styles := string(styleContents)
	for _, expected := range []string{".analytics-detail-header", ".analytics-summary", ".analytics-status-layout", ".analytics-segmented", ".analytics-series-chart"} {
		if !strings.Contains(styles, expected) {
			t.Fatalf("styles.css does not contain %q", expected)
		}
	}
	if strings.Contains(styles, ".analytics-hourly-table") || strings.Contains(styles, ".analytics-hourly-table-wrap") {
		t.Fatal("styles.css still contains removed hourly detail table styles")
	}
}

func TestEmbeddedConsoleUsesDedicatedLogSearchRoute(t *testing.T) {
	pageContents, err := embeddedWeb.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageContents)
	for _, expected := range []string{
		`<button class="nav" data-view="logs">日志</button>`,
		`<section id="logs" class="view hidden"`,
		`<h1 id="logs-title" tabindex="-1">日志</h1>`,
		`id="log-search-form"`,
		`id="log-time-range"`,
		`id="log-site"`,
		`id="log-node"`,
		`id="log-status"`,
		`id="log-client-ip"`,
		`id="log-cache-status"`,
		`id="log-table"`,
		`id="log-prev"`,
		`id="log-next"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("index.html does not contain %q", expected)
		}
	}

	scriptContents, err := embeddedWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptContents)
	for _, expected := range []string{
		"const consoleViews = new Set(['overview', 'logs', 'security', 'nodes', 'sites', 'settings'])",
		"function runLogSearch({ offset = 0, keepWindow = false } = {})",
		"request(`/api/logs?${params.toString()}`",
		"function renderLogRows(logs)",
		"function renderLogPagination()",
		"byId('log-search-form').addEventListener('submit'",
		"byId('log-next').addEventListener('click'",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
		}
	}

	styleContents, err := embeddedWeb.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	styles := string(styleContents)
	for _, expected := range []string{".logs-page", ".log-filter-grid", ".log-table", ".log-pagination", ".status-code-5xx"} {
		if !strings.Contains(styles, expected) {
			t.Fatalf("styles.css does not contain %q", expected)
		}
	}
}

func TestEmbeddedConsoleLocalizesStatusLabelsWithoutChangingStatusValues(t *testing.T) {
	contents, err := embeddedWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	for _, expected := range []string{
		"active: '运行中'",
		"draining: '暂停中'",
		"uninstalling: '卸载中'",
		"uninstalled: '已卸载'",
		">启用调度</button>",
		">暂停调度</button>",
		">撤销授权</button>",
		"'卸载节点'",
		"succeeded: '成功'",
		"rolled_back: '已回滚'",
		`data-status="draining"`,
		`data-status="active">启用调度</button>`,
		`/uninstall/command`,
		`/uninstall/force-complete`,
		`can_generate_command`,
		"toLocaleString('zh-CN'",
		"return 'gRPC'",
		"return 'WebSocket'",
		"publish-status",
		"重新发布",
		"查看发布详情",
		"port_conflicts",
		"site-passthrough",
		"site.passthrough",
		"client_max_body_size_mb",
		"site-client-max-body-size",
		"read_write_timeout_seconds",
		"site-read-write-timeout",
		"return 'HTTP / WS / SSE'",
		"不适用于 gRPC",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
		}
	}
	for _, retired := range []string{"site-stream-paths", "site.stream_paths"} {
		if strings.Contains(script, retired) {
			t.Fatalf("app.js still contains retired stream path reference %q", retired)
		}
	}
}

func TestEmbeddedConsolePreservesSelectedViewInURLHash(t *testing.T) {
	contents, err := embeddedWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	for _, expected := range []string{
		"const consoleViews = new Set(['overview', 'logs', 'security', 'nodes', 'sites', 'settings'])",
		"function parseRouteHash(hash)",
		"hash.replace(/^#\\/?/, '')",
		"if (segments.length === 2 && segments[1] === 'new')",
		"decodeURIComponent(segments[1])",
		"syncRouteFromLocation();",
		"window.location.hash = destination",
		"window.addEventListener('hashchange', handleHashChange)",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
		}
	}
}

func TestEmbeddedConsoleUsesDedicatedSiteEditorRoutes(t *testing.T) {
	pageContents, err := embeddedWeb.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageContents)
	for _, expected := range []string{
		`id="site-list-page"`,
		`id="site-detail-page"`,
		`id="site-detail-missing"`,
		`id="site-summary-protocol"`,
		`id="site-summary-cache"`,
		`id="site-summary-body"`,
		`id="site-summary-timeout"`,
		`id="site-summary-dns-ttl"`,
		`id="site-dns-ttl-inherit"`,
		`id="site-dns-ttl" type="number" min="60" max="300"`,
		`id="site-basic-title">基本信息`,
		`id="site-origin-title">源站与协议`,
		`id="site-primary-tls-name-wrap" class="hidden"`,
		`id="site-primary-tls-name" placeholder="origin.example.com"`,
		`id="site-backup-tls-name-wrap" class="hidden"`,
		`id="site-backup-tls-name" placeholder="backup.example.com"`,
		`id="site-policy-title">流量策略`,
		`id="site-tcp-only" type="checkbox"`,
		`id="site-tcp-title">TCP 转发`,
		`id="add-tcp-forward"`,
		`id="site-tcp-forward-list"`,
		`id="site-nodes-title">节点分配`,
		`id="site-detail-certificate"`,
		`id="site-detail-invalidate"`,
		`id="site-detail-allowlist"`,
		`id="site-detail-delete"`,
		`id="site-delete-dialog"`,
		`id="site-delete-confirm"`,
		`id="confirm-site-delete"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("index.html does not contain %q", expected)
		}
	}
	for _, retired := range []string{`class="form-grid site-form hidden"`, `class="action-menu"`, ">更多</"} {
		if strings.Contains(page, retired) {
			t.Fatalf("index.html still contains retired inline site control %q", retired)
		}
	}

	scriptContents, err := embeddedWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptContents)
	for _, expected := range []string{
		"return '#/sites/new'",
		"function renderSiteRoute(route",
		"function prepareNewSiteForm()",
		"function populateSiteForm(site)",
		"function originURLUsesTLS(value)",
		"function updateOriginTLSFields()",
		"tls_server_name: originURLUsesTLS(primaryURL) ? byId('site-primary-tls-name').value : ''",
		"tls_server_name: originURLUsesTLS(backup) ? byId('site-backup-tls-name').value : ''",
		"site.primary_origin.tls_server_name || ''",
		"site.backup_origin?.tls_server_name || ''",
		"function siteFormDirty()",
		"function confirmDiscardChanges()",
		"window.addEventListener('beforeunload'",
		"window.history.pushState(null, '', acceptedHash)",
		"markSettingsFormClean();",
		"renderSiteDetailStatus();",
		"classList.toggle('hidden', !siteCacheable(site))",
		`class="small secondary manage-site"`,
		"function renderSiteDeleteDialog(status = null)",
		"function setSiteEditorLocked(locked)",
		"/delete-status",
		"method: 'DELETE'",
		"site.deleting",
		"function tcpForwardPayload()",
		"function addTCPForwardRow(forward = {})",
		"function syncSiteTrafficMode()",
		"tcp_forwards: tcpForwardPayload()",
		"tcp_only: byId('site-tcp-only').checked",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
		}
	}
	listStart := strings.Index(script, "function renderSites()")
	listEnd := strings.Index(script, "function renderSiteViews()")
	if listStart < 0 || listEnd <= listStart {
		t.Fatal("app.js does not contain a bounded site list renderer")
	}
	listRenderer := script[listStart:listEnd]
	for _, expected := range []string{"<dt>节点</dt>", "<dt>TLS</dt>", "<dt>发布</dt>"} {
		if !strings.Contains(listRenderer, expected) {
			t.Fatalf("site list renderer does not contain %q", expected)
		}
	}
	for _, retired := range []string{"<dt>协议</dt>", "<dt>缓存</dt>", "<dt>请求体</dt>", "certificate", "action-menu"} {
		if strings.Contains(listRenderer, retired) {
			t.Fatalf("site list renderer still contains detail-only control %q", retired)
		}
	}

	styleContents, err := embeddedWeb.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	styles := string(styleContents)
	for _, expected := range []string{".site-detail-header", ".site-detail-summary", ".site-form-fields", ".site-operation", ".tcp-forward-row", ".tcp-forward-fields"} {
		if !strings.Contains(styles, expected) {
			t.Fatalf("styles.css does not contain %q", expected)
		}
	}
}

func TestEmbeddedConsoleUsesResponsiveSidebarWorkspace(t *testing.T) {
	pageContents, err := embeddedWeb.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageContents)
	for _, expected := range []string{
		`id="app" class="console-shell hidden"`,
		`<aside id="sidebar" class="sidebar" aria-label="控制台导航">`,
		`<nav class="side-nav" aria-label="主导航">`,
		`id="sidebar-toggle"`,
		`aria-controls="sidebar"`,
		`id="sidebar-backdrop"`,
		`id="mobile-page-title"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("index.html does not contain %q", expected)
		}
	}

	styleContents, err := embeddedWeb.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	styles := string(styleContents)
	for _, expected := range []string{
		"grid-template-columns: 208px minmax(0, 1fr)",
		"body.sidebar-open .sidebar",
		"@media (max-width: 800px)",
		".page { width: 100%",
	} {
		if !strings.Contains(styles, expected) {
			t.Fatalf("styles.css does not contain %q", expected)
		}
	}

	scriptContents, err := embeddedWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptContents)
	for _, expected := range []string{
		"const viewLabels = { overview: '概览', logs: '日志', security: '安全', nodes: '节点', sites: '站点', settings: '设置' }",
		"function setSidebarOpen(open, restoreFocus = false)",
		"setAttribute('aria-expanded', String(open))",
		"event.key === 'Escape'",
		"mobileSidebarQuery.addEventListener('change', syncSidebarMode)",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
		}
	}
}

func TestEmbeddedConsoleIncludesSecurityWorkspace(t *testing.T) {
	pageContents, err := embeddedWeb.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageContents)
	for _, expected := range []string{
		`data-view="security"`, `id="security"`, `id="security-policy-table"`,
		`id="security-node-table"`, `id="security-ban-table"`, `id="security-event-table"`, `id="security-policy-dialog"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("index.html does not contain %q", expected)
		}
	}
	scriptContents, err := embeddedWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptContents)
	for _, expected := range []string{
		"function renderSecurity()", "function refreshSecurity()", "function openSecurityPolicy(policy = null)",
		"/api/security/deploy", "/api/security/policies", "/api/security/bans/",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
		}
	}
	styleContents, err := embeddedWeb.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{".security-summary", ".security-pattern", ".security-request"} {
		if !strings.Contains(string(styleContents), expected) {
			t.Fatalf("styles.css does not contain %q", expected)
		}
	}
}

func TestEmbeddedConsoleIncludesRuntimeSettingsForms(t *testing.T) {
	pageContents, err := embeddedWeb.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageContents)
	for _, expected := range []string{
		`class="nav" data-view="settings">设置`,
		`id="settings" class="view hidden"`,
		`id="settings-dns-ttl" type="number" min="60" max="300"`,
		`id="settings-cloudflare-token" type="password"`,
		`id="settings-smtp-security"`,
		`<option value="starttls">STARTTLS</option>`,
		`<option value="tls">隐式 TLS</option>`,
		`id="test-smtp-settings"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("index.html does not contain %q", expected)
		}
	}
	scriptContents, err := embeddedWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptContents)
	for _, expected := range []string{
		"request('/api/settings')",
		"request('/api/settings/cloudflare'",
		"request('/api/settings/smtp/test'",
		"function settingsFormsDirty()",
		"preserveDirtySections: ['cloudflare', 'smtp']",
		"function restoreSettingsDraft(draft, sections)",
		"dns_ttl_seconds: byId('site-dns-ttl-inherit').checked ? null",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
		}
	}
	styleContents, err := embeddedWeb.ReadFile("web/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	styles := string(styleContents)
	for _, expected := range []string{".settings-page", ".settings-fields", ".settings-actions"} {
		if !strings.Contains(styles, expected) {
			t.Fatalf("styles.css does not contain %q", expected)
		}
	}
}
