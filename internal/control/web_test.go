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
		`>节点</button>`,
		`>站点</button>`,
		`最近 24 小时`,
		`HTTP 4xx / 5xx`,
		`站点请求趋势`,
		`<th scope="col">传输量</th>`,
		`Cloudflare 区域 ID`,
		`流式路径（WebSocket / SSE）`,
		`id="site-client-max-body-size"`,
		`<option value="1024">1024 MiB</option>`,
		`透传模式（仅 HTTP(S)，禁用 Nginx 缓存）`,
		`id="node-uninstall-dialog"`,
		`>开始卸载准备</button>`,
		`>生成卸载命令</button>`,
		`强制完成（不清理远端）`,
		`>删除记录</button>`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("index.html does not contain %q", expected)
		}
	}
	for _, unexpected := range []string{">Overview</button>", ">Nodes</button>", ">Sites</button>"} {
		if strings.Contains(page, unexpected) {
			t.Fatalf("index.html still contains %q", unexpected)
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
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
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
		"const consoleViews = new Set(['overview', 'nodes', 'sites'])",
		"window.location.hash.replace(/^#\\/?/, '')",
		"syncViewFromLocation();",
		"window.location.hash = hash",
		"window.addEventListener('hashchange', syncViewFromLocation)",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("app.js does not contain %q", expected)
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
		"const viewLabels = { overview: '概览', nodes: '节点', sites: '站点' }",
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
