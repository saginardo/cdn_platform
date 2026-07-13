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
		`最近 24 小时流量`,
		`Cloudflare 区域 ID`,
		`流式路径（WebSocket / SSE）`,
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
