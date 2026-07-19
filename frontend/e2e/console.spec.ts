import { expect, test, type Page } from "@playwright/test";

const now = new Date("2026-07-18T10:00:00Z");
const series = Array.from({ length: 24 }, (_, index) => ({
  time: new Date(now.getTime() - (23 - index) * 60 * 60 * 1000).toISOString(),
  requests: 900 + index * 57 + (index % 4) * 160,
  bytes: 72_000_000 + index * 4_200_000,
  error_requests: 12 + (index % 5) * 6,
}));

const overview = {
  from: series[0].time,
  to: now.toISOString(),
  bucket_seconds: 3600,
  totals: { requests: 38241, bytes: 3_948_238_121, error_requests: 612 },
  series,
  status_codes: [
    { code: 200, requests: 34120 },
    { code: 304, requests: 2100 },
    { code: 404, requests: 1240 },
    { code: 502, requests: 781 },
  ],
  sites: [
    {
      id: "site-1",
      name: "静态资源主站",
      domains: ["cdn.example.com", "static.example.com"],
      requests: 28130,
      bytes: 3_122_000_000,
      error_requests: 342,
      status_codes: [
        { code: 200, requests: 27100 },
        { code: 404, requests: 1030 },
      ],
      series,
    },
    {
      id: "site-2",
      name: "API 加速",
      domains: ["api.example.com"],
      requests: 10111,
      bytes: 826_238_121,
      error_requests: 270,
      status_codes: [
        { code: 200, requests: 9330 },
        { code: 502, requests: 781 },
      ],
      series: series.map((point) => ({
        ...point,
        requests: Math.round(point.requests / 3),
        bytes: Math.round(point.bytes / 4),
      })),
    },
  ],
};

const site = {
  id: "site-1",
  name: "静态资源主站",
  zone_id: "zone-1",
  domains: ["cdn.example.com"],
  node_ids: [],
  primary_origin: {
    url: "https://origin.example.com",
    host_header: "origin.example.com",
    tls_server_name: "origin.example.com",
    enabled: true,
  },
  stream_paths: [],
  passthrough: false,
  client_max_body_size_mb: 128,
  read_write_timeout_seconds: 360,
  dns_ttl_seconds: null,
  tcp_only: false,
  tcp_forwards: [],
  cache_generation: 2,
  config_version: 8,
  published: true,
  enabled: true,
  deleting: false,
  created_at: now.toISOString(),
  updated_at: now.toISOString(),
};

const accessLogs = [
  {
    id: "request-404",
    timestamp: now.toISOString(),
    node_id: "node-1",
    site_id: "site-1",
    client_ip: "203.0.113.25",
    host: "cdn.example.com",
    scheme: "https",
    protocol: "HTTP/2.0",
    method: "GET",
    path: "/assets/releases/2026/07/18/a-very-long-directory-name/another-very-long-directory-name/application.bundle.js",
    status: 404,
    request_bytes: 2048,
    bytes: 8192,
    duration_ms: 37,
    upstream: "192.0.2.10:443",
    upstream_status: "404",
    upstream_response_time: "0.036",
    cache_status: "MISS",
    user_agent: "Mozilla/5.0 (Playwright request detail test)",
    referer: "https://cdn.example.com/releases",
    content_type: "application/json",
    response_content_type: "text/html; charset=utf-8",
    accept: "text/html,application/xhtml+xml",
    range: "bytes=0-4095",
  },
  {
    id: "request-502",
    timestamp: series[22].time,
    node_id: "node-1",
    site_id: "site-1",
    client_ip: "203.0.113.26",
    host: "cdn.example.com",
    scheme: "https",
    protocol: "HTTP/2.0",
    method: "GET",
    path: "/api/unavailable",
    status: 502,
    request_bytes: 512,
    bytes: 128,
    duration_ms: 1001,
    upstream: "192.0.2.10:443",
    upstream_status: "502",
    upstream_response_time: "1.000",
    cache_status: "MISS",
    user_agent: "curl/8.10.1",
    referer: "",
    content_type: "",
    response_content_type: "text/plain",
    accept: "*/*",
    range: "",
  },
];

async function mockAPI(page: Page, overrides: Record<string, unknown> = {}) {
  let branding = { name: "CDN Platform", subtitle: "控制面板" };
  let cacheDefaultSizeGB = 1;
  let nodeCacheOverrideGB: number | null = null;
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (
      url.pathname === "/api/settings/branding" &&
      route.request().method() === "PUT"
    ) {
      branding = route.request().postDataJSON() as typeof branding;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(branding),
      });
      return;
    }
    if (
      url.pathname === "/api/nodes/node-1/cache" &&
      route.request().method() === "PUT"
    ) {
      const input = route.request().postDataJSON() as {
        cache_max_size_gb: number | null;
      };
      nodeCacheOverrideGB = input.cache_max_size_gb;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          default_size_gb: cacheDefaultSizeGB,
          override_size_gb: nodeCacheOverrideGB,
          effective_size_gb: nodeCacheOverrideGB ?? cacheDefaultSizeGB,
        }),
      });
      return;
    }
    if (
      url.pathname === "/api/settings/cache" &&
      route.request().method() === "PUT"
    ) {
      const input = route.request().postDataJSON() as {
        default_size_gb: number;
      };
      cacheDefaultSizeGB = input.default_size_gb;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ default_size_gb: cacheDefaultSizeGB }),
      });
      return;
    }
    const responses: Record<string, unknown> = {
      "/api/session": { user: "admin", csrf_token: "e2e-csrf" },
      "/api/messages": { messages: [], unread_count: 0 },
      "/api/overview": overview,
      "/api/sites": [site],
      "/api/sites/site-1/publish-status": {
        task: {
          id: "publish-1",
          kind: "publish",
          site_id: "site-1",
          status: "succeeded",
          created_at: now.toISOString(),
          updated_at: now.toISOString(),
        },
        nodes: [],
      },
      "/api/sites/site-1/tls-status": {
        certificate_task: {
          id: "tls-1",
          kind: "issue_certificate",
          site_id: "site-1",
          status: "succeeded",
          created_at: now.toISOString(),
          updated_at: now.toISOString(),
        },
        published_after_certificate: true,
      },
      "/api/nodes": [],
      "/api/logs": {
        logs: accessLogs,
        from: series[22].time,
        to: now.toISOString(),
        offset: 0,
        page_size: 20,
        has_more: false,
      },
      "/api/logs/request-404": accessLogs[0],
      "/api/logs/request-502": accessLogs[1],
      "/api/security": {
        policies: [],
        rate_limit_policies: [],
        bans: [],
        active_ban_count: 0,
        events: [],
        nodes: [],
      },
      "/api/settings": {
        branding,
        cache: { default_size_gb: cacheDefaultSizeGB },
        dns: { default_ttl_seconds: 60 },
        cloudflare: {
          source: "environment",
          configured: true,
          override_configured: false,
          environment_configured: true,
        },
        smtp: {
          enabled: false,
          host: "",
          port: 587,
          username: "",
          from_address: "",
          recipients: [],
          security: "starttls",
          source: "unconfigured",
          override_configured: false,
          password_configured: false,
          environment_configured: false,
        },
        backup: {
          repository: "",
          access_key_id: "",
          region: "us-east-1",
          backup_time: "03:25",
          random_delay_seconds: 1200,
          source: "unconfigured",
          configured: false,
          override_configured: false,
          secret_access_key_configured: false,
          restic_password_configured: false,
          environment_configured: false,
        },
      },
      "/api/backups/status": null,
      "/api/backups/snapshots": [],
      "/api/backups/restores/current": null,
      ...overrides,
    };
    let data = responses[url.pathname];
    if (
      url.pathname === "/api/nodes/node-1" &&
      data &&
      typeof data === "object"
    ) {
      data = {
        ...(data as Record<string, unknown>),
        cache: {
          default_size_gb: cacheDefaultSizeGB,
          override_size_gb: nodeCacheOverrideGB,
          effective_size_gb: nodeCacheOverrideGB ?? cacheDefaultSizeGB,
        },
      };
    }
    if (data === undefined) {
      await route.fulfill({
        status: 404,
        contentType: "application/json",
        body: JSON.stringify({ error: "not mocked" }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(data),
    });
  });
}

test("desktop overview renders shadcn chart and aligned navigation", async ({
  page,
}, testInfo) => {
  const errors = trackPageErrors(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockAPI(page);
  await page.goto("/#/overview");

  await expect(
    page.getByRole("heading", { name: "概览", level: 1 }),
  ).toBeVisible();
  await expect(page.getByText("38,241", { exact: true })).toBeVisible();
  await expect(page.getByText("静态资源主站")).toBeVisible();
  const chart = page.locator('[data-slot="chart"] svg').first();
  await expect(chart).toBeVisible();
  expect((await chart.boundingBox())?.height).toBeGreaterThan(200);
  await expect(chart.locator("path.recharts-line-curve")).toHaveCount(1);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth + 1,
    ),
  ).toBe(true);
  expect(errors).toEqual([]);

  await page.screenshot({
    path: testInfo.outputPath("overview-desktop.png"),
    fullPage: true,
  });
});

test("list pagination renders at most 20 entries per page", async ({
  page,
}) => {
  const sites = Array.from({ length: 25 }, (_, index) => ({
    ...overview.sites[0],
    id: `site-${index + 1}`,
    name: `分页站点 ${String(index + 1).padStart(2, "0")}`,
  }));
  await mockAPI(page, { "/api/overview": { ...overview, sites } });
  await page.goto("/#/overview");

  const rows = page.locator("tbody tr");
  await expect(rows).toHaveCount(20);
  await expect(page.getByText("第 1-20 条，共 25 个站点")).toBeVisible();
  await expect(page.getByText("分页站点 20")).toBeVisible();
  await expect(page.getByText("分页站点 21")).toHaveCount(0);

  await page.getByRole("button", { name: "下一页" }).click();
  await expect(rows).toHaveCount(5);
  await expect(page.getByText("第 21-25 条，共 25 个站点")).toBeVisible();
  await expect(page.getByText("分页站点 21")).toBeVisible();
  await expect(page.getByText("分页站点 20")).toHaveCount(0);
});

test("sites list shows only the publish status", async ({ page }) => {
  const tlsRequests: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname.endsWith("/tls-status")) {
      tlsRequests.push(request.url());
    }
  });
  await mockAPI(page);
  await page.goto("/#/sites");

  await expect(
    page.getByRole("columnheader", { name: "发布状态" }),
  ).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "版本" })).toBeVisible();
  const row = page.getByRole("row").filter({ hasText: site.name });
  await expect(row.getByText("V8", { exact: true })).toBeVisible();
  await expect(row.getByText("Cache Version V2", { exact: true })).toHaveCount(
    0,
  );
  await expect(row.getByText("成功", { exact: true })).toHaveCount(1);
  expect(tlsRequests).toEqual([]);

  await row.getByRole("link", { name: `管理 ${site.name}` }).click();
  await expect(page.getByText("缓存版本", { exact: true })).toBeVisible();
  await expect(
    page.getByText("Cache Version V2", { exact: true }),
  ).toBeVisible();
});

test("branding settings update the sidebar immediately", async ({
  page,
}, testInfo) => {
  await mockAPI(page);
  await page.goto("/#/settings");

  await expect(page.getByLabel("品牌标识")).toHaveValue("CDN Platform");
  await expect(page.getByLabel("副标题")).toHaveValue("控制面板");
  await page.getByLabel("品牌标识").fill("DustK Edge");
  await page.getByLabel("副标题").fill("边缘控制台");
  await page.getByRole("button", { name: "保存品牌" }).click();

  const sidebar = page.locator('[data-sidebar="sidebar"]');
  await expect(sidebar.getByText("DustK Edge", { exact: true })).toBeVisible();
  await expect(sidebar.getByText("边缘控制台", { exact: true })).toBeVisible();

  await page.route("**/api/session", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 1_000));
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ user: "admin", csrf_token: "e2e-csrf" }),
    });
  });
  await page.reload();
  const bootScreen = page.locator("main");
  await expect(bootScreen.getByText("正在验证登录状态")).toBeVisible();
  await expect(
    bootScreen.getByText("DustK Edge", { exact: true }),
  ).toBeVisible();
  await expect(
    bootScreen.getByText("CDN Platform", { exact: true }),
  ).toHaveCount(0);
  await expect(sidebar.getByText("DustK Edge", { exact: true })).toBeVisible();

  await page.evaluate(() => window.localStorage.clear());
  await page.reload();
  await expect(bootScreen.getByText("正在验证登录状态")).toBeVisible();
  await expect(
    bootScreen.getByText("CDN Platform", { exact: true }),
  ).toHaveCount(0);
  await expect(sidebar.getByText("DustK Edge", { exact: true })).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("branding-settings.png"),
    fullPage: true,
  });
});

test("theme menu supports light dark and system modes", async ({
  page,
}, testInfo) => {
  await page.emulateMedia({ colorScheme: "light" });
  await mockAPI(page);
  await page.goto("/#/overview");

  const sidebar = page.locator('[data-sidebar="sidebar"]');
  await expect(sidebar.getByText("消息中心", { exact: true })).toHaveCount(0);

  await page.getByRole("button", { name: "主题：跟随系统" }).click();
  await page.getByRole("menuitemradio", { name: "深色" }).click();
  await expect(page.locator("html")).toHaveClass(/dark/);
  await expect(page.getByRole("button", { name: "主题：深色" })).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("theme-dark.png"),
    fullPage: true,
  });

  await page.getByRole("button", { name: "主题：深色" }).click();
  await page.getByRole("menuitemradio", { name: "浅色" }).click();
  await expect(page.locator("html")).toHaveClass(/light/);

  await page.getByRole("button", { name: "主题：浅色" }).click();
  await page.getByRole("menuitemradio", { name: "跟随系统" }).click();
  await expect(
    page.getByRole("button", { name: "主题：跟随系统" }),
  ).toBeVisible();

  await page.getByRole("button", { name: "打开消息中心" }).click();
  await expect(page.getByRole("heading", { name: "消息中心" })).toBeVisible();
});

test("mobile sidebar closes after hash navigation without horizontal overflow", async ({
  page,
}, testInfo) => {
  const errors = trackPageErrors(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await mockAPI(page);
  await page.goto("/#/overview");

  await page.getByRole("button", { name: "切换侧边栏" }).click();
  await expect(page.getByText("工作区", { exact: true })).toBeVisible();
  const logLink = page.getByRole("link", { name: "日志" });
  expect(
    await logLink.evaluate((element) => getComputedStyle(element).textAlign),
  ).toBe("left");
  await page.screenshot({
    path: testInfo.outputPath("sidebar-mobile.png"),
    fullPage: true,
  });
  await logLink.click();
  await expect(
    page.getByRole("heading", { name: "日志", level: 1 }),
  ).toBeVisible();
  await expect(page.getByText("工作区", { exact: true })).toBeHidden();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth + 1,
    ),
  ).toBe(true);
  expect(errors).toEqual([]);

  await page.screenshot({
    path: testInfo.outputPath("logs-mobile.png"),
    fullPage: true,
  });
});

test("security tabs fit on one line without scrollbars", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await mockAPI(page);
  await page.goto("/#/security");

  const tabs = page.locator('[data-slot="tabs-list"]');
  await expect(tabs.getByRole("tab")).toHaveCount(5);
  expect(
    await tabs.evaluate((element) => ({
      horizontalOverflow: element.scrollWidth > element.clientWidth,
      overflowX: getComputedStyle(element).overflowX,
      overflowY: getComputedStyle(element).overflowY,
      rows: new Set(
        Array.from(element.children, (child) =>
          Math.round(child.getBoundingClientRect().top),
        ),
      ).size,
    })),
  ).toEqual({
    horizontalOverflow: false,
    overflowX: "visible",
    overflowY: "visible",
    rows: 1,
  });
});

test("rate limit errors can escalate from 429 to an IP ban", async ({
  page,
}) => {
  const securityOverview = {
    policies: [],
    rate_limit_policies: [],
    bans: [],
    active_ban_count: 0,
    events: [],
    nodes: [],
  };
  await mockAPI(page, {
    "/api/security": securityOverview,
    "/api/security/rate-limit-policies": securityOverview,
  });
  await page.goto("/#/security");
  await page.getByRole("tab", { name: "请求限速" }).click();
  await page.getByRole("button", { name: "新增" }).click();

  await page.getByLabel("名称").fill("API 错误突发");
  await page.getByLabel("每秒请求上限").fill("8");
  await page.getByRole("switch", { name: "连续超限后封禁 IP" }).click();
  await expect(
    page.getByRole("switch", { name: "仅统计指定响应" }),
  ).toBeChecked();
  await expect(
    page.getByRole("switch", { name: "仅统计指定响应" }),
  ).toBeDisabled();
  await expect(page.getByRole("checkbox", { name: "2xx" })).toBeDisabled();
  await expect(page.getByRole("checkbox", { name: "4xx" })).toBeChecked();
  await expect(page.getByRole("checkbox", { name: "5xx" })).toBeChecked();
  await page.getByLabel("连续 429 次数").fill("4");
  const banDuration = page.getByText("封禁时间", { exact: true }).locator("..");
  await banDuration.getByRole("combobox").click();
  await page.getByRole("option", { name: "6 小时" }).click();

  const requestPromise = page.waitForRequest(
    (request) =>
      new URL(request.url()).pathname === "/api/security/rate-limit-policies" &&
      request.method() === "POST",
  );
  await page.getByRole("button", { name: "保存并发布" }).click();
  const request = await requestPromise;
  expect(request.postDataJSON()).toMatchObject({
    name: "API 错误突发",
    requests_per_second: 8,
    response_condition_enabled: true,
    response_status_classes: [4, 5],
    ban_enabled: true,
    ban_after_consecutive_429: 4,
    ban_duration_seconds: 21600,
  });
});

test("log rows truncate long paths, color errors, and open request details", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await mockAPI(page);
  await page.goto("/#/logs");

  const notFoundRow = page.getByRole("row", {
    name: new RegExp(`查看请求 GET ${accessLogs[0].path}`),
  });
  const longPath = notFoundRow.locator("code");
  await expect(longPath).toHaveAttribute("title", accessLogs[0].path);
  expect(
    await longPath.evaluate(
      (element) => element.scrollWidth > element.clientWidth,
    ),
  ).toBe(true);
  const requestCell = notFoundRow.locator("td").nth(1);
  const statusCell = notFoundRow.locator("td").nth(2);
  const [requestBox, statusBox] = await Promise.all([
    requestCell.boundingBox(),
    statusCell.boundingBox(),
  ]);
  expect(requestBox?.x).toBeDefined();
  expect(statusBox?.x).toBeDefined();
  expect((requestBox?.x ?? 0) + (requestBox?.width ?? 0)).toBeLessThanOrEqual(
    (statusBox?.x ?? 0) + 1,
  );
  await expect(notFoundRow.getByText("404", { exact: true })).toHaveClass(
    /bg-amber-50/,
  );
  const badGatewayRow = page.getByRole("row", {
    name: new RegExp(`查看请求 GET ${accessLogs[1].path}`),
  });
  await expect(badGatewayRow.getByText("502", { exact: true })).toHaveClass(
    /bg-red-50/,
  );

  await notFoundRow.click();
  await expect(
    page.getByRole("heading", { name: "请求详情", level: 1 }),
  ).toBeVisible();
  await expect(page.getByText(accessLogs[0].user_agent)).toBeVisible();
  await expect(page.getByText("请求大小", { exact: true })).toBeVisible();
  await expect(page.getByText("响应大小", { exact: true })).toBeVisible();
  await expect(page.getByText("Range", { exact: true })).toBeVisible();
  await expect(page.getByText("bytes=0-4095", { exact: true })).toBeVisible();
});

test("cache defaults are configurable and overridden by individual nodes", async ({
  page,
}) => {
  const node = {
    id: "node-1",
    name: "cache-edge",
    public_ipv4: "203.0.113.41",
    status: "active",
    capabilities: [],
    applied_version: 8,
    last_heartbeat_at: now.toISOString(),
    created_at: now.toISOString(),
    updated_at: now.toISOString(),
    upgrade_capable: false,
    upgrade_up_to_date: false,
    can_upgrade: false,
    upgrade_blocker: "升级边缘代理后可使用在线升级",
  };
  await mockAPI(page, {
    "/api/nodes/node-1": {
      node,
      machine: {
        available: false,
        unavailable_reason: "升级边缘代理后可查看机器状态",
        stale: false,
      },
      cache: {
        default_size_gb: 1,
        override_size_gb: null,
        effective_size_gb: 1,
      },
      sites: [],
    },
    "/api/nodes/node-1/cache-status": {
      available: false,
      unavailable_reason: "缓存统计暂不可用",
      from: series[0].time,
      to: now.toISOString(),
      requests: 0,
      bytes: 0,
      cache_lookups: 0,
      cache_hits: 0,
      cache_misses: 0,
      bypasses: 0,
      uncached: 0,
      hit_rate: 0,
      statuses: [],
      storage: {
        available: false,
        unavailable_reason: "升级边缘代理后可查看缓存空间",
        used_bytes: 0,
        total_bytes: 0,
        stale: false,
      },
    },
    "/api/nodes/node-1/uninstall": {
      node,
      job: null,
      blockers: [],
      can_generate_command: false,
      ready_in_seconds: 0,
    },
  });
  await page.goto("/#/settings");
  await page.getByRole("tab", { name: "网络与 DNS" }).click();
  const cacheSize = page.getByLabel("节点默认总上限（GB）");
  await expect(cacheSize).toHaveValue("1");
  await cacheSize.fill("4");
  await page.getByRole("button", { name: "保存缓存配置" }).click();
  await expect(page.getByText("全局缓存上限已保存")).toBeVisible();

  await page.goto("/#/nodes/node-1");
  await expect(page.getByText("全局默认 4 GB")).toBeVisible();
  const override = page.getByLabel("覆写全局缓存配额");
  const nodeCacheSize = page.getByLabel("节点缓存总上限（GB）");
  await expect(override).not.toBeChecked();
  await expect(nodeCacheSize).toBeDisabled();
  await expect(nodeCacheSize).toHaveValue("4");

  await override.click();
  await nodeCacheSize.fill("2");
  await page.getByRole("button", { name: "保存缓存配置" }).click();
  await expect(page.getByText("节点缓存配额已保存")).toBeVisible();
  await expect(page.getByText("当前配置 2 GB")).toBeVisible();
});

test("canceled node uninstall returns the panel to its idle state", async ({
  page,
}) => {
  const node = {
    id: "node-1",
    name: "lightlayer-hk",
    public_ipv4: "203.0.113.42",
    status: "active",
    capabilities: [],
    applied_version: 47,
    last_heartbeat_at: now.toISOString(),
    created_at: now.toISOString(),
    updated_at: now.toISOString(),
    upgrade_capable: false,
    upgrade_up_to_date: false,
    can_upgrade: false,
    upgrade_blocker: "升级边缘代理后可使用在线升级",
  };
  await mockAPI(page, {
    "/api/nodes/node-1": {
      node,
      machine: {
        available: false,
        unavailable_reason: "升级边缘代理后可查看机器状态",
        stale: false,
      },
      cache: {
        default_size_gb: 1,
        override_size_gb: null,
        effective_size_gb: 1,
      },
      sites: [],
    },
    "/api/nodes/node-1/cache-status": {
      available: false,
      unavailable_reason: "缓存统计暂不可用",
      from: series[0].time,
      to: now.toISOString(),
      requests: 0,
      bytes: 0,
      cache_lookups: 0,
      cache_hits: 0,
      cache_misses: 0,
      bypasses: 0,
      uncached: 0,
      hit_rate: 0,
      statuses: [],
      storage: {
        available: false,
        unavailable_reason: "升级边缘代理后可查看缓存空间",
        used_bytes: 0,
        total_bytes: 0,
        stale: false,
      },
    },
    "/api/nodes/node-1/uninstall": {
      node,
      job: {
        node_id: "node-1",
        status: "canceled",
        previous_status: "draining",
        ready_at: now.toISOString(),
        affected_site_ids: ["site-1"],
        forced: false,
        created_at: now.toISOString(),
        updated_at: now.toISOString(),
      },
      blockers: [
        {
          code: "still_assigned",
          site_id: "site-1",
          site_name: "静态资源主站",
          detail: "remove this node from the site",
        },
      ],
      can_generate_command: false,
      ready_in_seconds: 0,
    },
  });

  await page.goto("/#/nodes/node-1");

  await expect(
    page.getByRole("heading", { name: "lightlayer-hk", level: 1 }),
  ).toBeVisible();
  await expect(page.getByText("已取消", { exact: true })).toHaveCount(0);
  await expect(
    page.getByText("remove this node from the site", { exact: true }),
  ).toHaveCount(0);
  await expect(page.getByRole("button", { name: "准备卸载" })).toBeDisabled();
  await expect(
    page.getByText("暂停调度或撤销授权后才能准备卸载。"),
  ).toBeVisible();
});

test("all primary workspaces and the new-site editor mount without runtime errors", async ({
  page,
}) => {
  const errors = trackPageErrors(page);
  await mockAPI(page);

  for (const [path, heading] of [
    ["security", "安全"],
    ["nodes", "节点"],
    ["sites", "站点"],
    ["sites/new", "添加站点"],
    ["settings", "设置"],
  ]) {
    await page.goto(`/#/${path}`);
    await expect(
      page.getByRole("heading", { name: heading, level: 1 }),
    ).toBeVisible();
  }
  await page.getByRole("tab", { name: "备份与恢复" }).click();
  await expect(page.getByText("S3 在线恢复")).toBeVisible();
  expect(errors).toEqual([]);
});

test("login screen renders without an authenticated session", async ({
  page,
}, testInfo) => {
  const errors = trackPageErrors(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/api/session", (route) =>
    route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ error: "authentication required" }),
    }),
  );
  await page.route("**/api/setup/status", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ initialized: true }),
    }),
  );
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "登录控制面" })).toBeVisible();
  await expect(page.getByLabel("管理员密码")).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth + 1,
    ),
  ).toBe(true);
  expect(errors).toEqual([]);
  await page.screenshot({
    path: testInfo.outputPath("login-mobile.png"),
    fullPage: true,
  });
});

function trackPageErrors(page: Page) {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  return errors;
}
