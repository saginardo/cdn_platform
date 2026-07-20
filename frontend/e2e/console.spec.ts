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

const monitoring = {
  interval_seconds: 30,
  attempts_per_round: 3,
  healthy_score: 80,
  auto_pause_after: 4,
  targets: [
    {
      id: "target-1",
      name: "主 API",
      address: "probe-a.example.com:443",
      enabled: true,
      created_at: series[20].time,
      updated_at: series[20].time,
    },
    {
      id: "target-2",
      name: "备用入口",
      address: "192.0.2.50:8443",
      enabled: true,
      created_at: series[20].time,
      updated_at: series[21].time,
    },
  ],
  nodes: [
    {
      node_id: "node-1",
      name: "edge-hong-kong",
      public_ipv4: "203.0.113.41",
      status: "active",
      monitor_auto_paused: false,
      capable: true,
      score: 96,
      success_rate: 100,
      average_latency_ms: 63.4,
      consecutive_abnormal: 0,
      last_checked_at: now.toISOString(),
      stale: false,
      results: [
        {
          target_id: "target-1",
          target_name: "主 API",
          address: "probe-a.example.com:443",
          attempts: 3,
          successful_attempts: 3,
          average_latency_ms: 58.2,
          checked_at: now.toISOString(),
        },
        {
          target_id: "target-2",
          target_name: "备用入口",
          address: "192.0.2.50:8443",
          attempts: 3,
          successful_attempts: 3,
          average_latency_ms: 68.6,
          checked_at: now.toISOString(),
        },
      ],
    },
    {
      node_id: "node-2",
      name: "edge-singapore",
      public_ipv4: "203.0.113.42",
      status: "draining",
      monitor_auto_paused: true,
      capable: true,
      score: 35,
      success_rate: 50,
      average_latency_ms: 1320,
      consecutive_abnormal: 4,
      last_checked_at: now.toISOString(),
      stale: false,
      results: [
        {
          target_id: "target-1",
          target_name: "主 API",
          address: "probe-a.example.com:443",
          attempts: 3,
          successful_attempts: 3,
          average_latency_ms: 1320,
          checked_at: now.toISOString(),
        },
        {
          target_id: "target-2",
          target_name: "备用入口",
          address: "192.0.2.50:8443",
          attempts: 3,
          successful_attempts: 0,
          average_latency_ms: 0,
          error: "connect: connection timed out",
          checked_at: now.toISOString(),
        },
      ],
    },
  ],
};

function monitoringHistory(range: string) {
  const presets: Record<string, { duration: number; bucket: number }> = {
    "1h": { duration: 60 * 60 * 1000, bucket: 30 },
    "6h": { duration: 6 * 60 * 60 * 1000, bucket: 120 },
    "12h": { duration: 12 * 60 * 60 * 1000, bucket: 300 },
    "24h": { duration: 24 * 60 * 60 * 1000, bucket: 600 },
    "7d": { duration: 7 * 24 * 60 * 60 * 1000, bucket: 3600 },
  };
  const selectedRange = presets[range] ? range : "24h";
  const preset = presets[selectedRange];
  const points = Array.from({ length: 16 }, (_, index) => {
    const time = new Date(
      now.getTime() - preset.duration + (preset.duration * index) / 15,
    ).toISOString();
    return { time, index };
  });
  return {
    available: true,
    node: {
      id: "node-1",
      name: "edge-hong-kong",
      public_ipv4: "203.0.113.41",
      status: "active",
      monitor_auto_paused: false,
    },
    range: selectedRange,
    from: new Date(now.getTime() - preset.duration).toISOString(),
    to: now.toISOString(),
    bucket_seconds: preset.bucket,
    series: [
      {
        target_id: "target-1",
        name: "主 API",
        address: "probe-a.example.com:443",
        points: points.map(({ time, index }) => ({
          time,
          attempts: 3,
          successful_attempts: 3,
          success_rate: 100,
          average_latency_ms: 42 + (index % 5) * 3,
          failed_rounds: 0,
        })),
      },
      {
        target_id: "target-2",
        name: "备用入口",
        address: "192.0.2.50:8443",
        points: points.map(({ time, index }) => ({
          time,
          attempts: 3,
          successful_attempts: index === 8 ? 0 : 3,
          success_rate: index === 8 ? 0 : 100,
          average_latency_ms: index === 8 ? null : 71 + (index % 4) * 4,
          failed_rounds: index === 8 ? 1 : 0,
        })),
      },
    ],
  };
}

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
  let branding = {
    name: "CDN Platform",
    subtitle: "控制面板",
    logo_data_url: "",
  };
  let cacheDefaultSizeGB = 1;
  let nodeCacheOverrideGB: number | null = null;
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (
      url.pathname === "/api/settings/branding" &&
      route.request().method() === "PUT"
    ) {
      branding = {
        ...branding,
        ...(route.request().postDataJSON() as Partial<typeof branding>),
      };
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
    if (
      url.pathname === "/api/monitoring/targets" &&
      route.request().method() === "POST"
    ) {
      const input = route.request().postDataJSON() as {
        name: string;
        address: string;
      };
      await route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({
          id: "target-created",
          name: input.name,
          address: input.address,
          enabled: true,
          created_at: now.toISOString(),
          updated_at: now.toISOString(),
        }),
      });
      return;
    }
    if (
      url.pathname.startsWith("/api/monitoring/targets/") &&
      route.request().method() === "PUT"
    ) {
      const input = route.request().postDataJSON() as {
        name?: string;
        enabled?: boolean;
      };
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: url.pathname.split("/").at(-1),
          name: input.name ?? "主 API",
          address: "probe-a.example.com:443",
          enabled: input.enabled ?? true,
          created_at: now.toISOString(),
          updated_at: now.toISOString(),
        }),
      });
      return;
    }
    if (
      url.pathname === "/api/monitoring/nodes/node-1/history" &&
      route.request().method() === "GET"
    ) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          monitoringHistory(url.searchParams.get("range") ?? "24h"),
        ),
      });
      return;
    }
    if (
      url.pathname.startsWith("/api/monitoring/targets/") &&
      route.request().method() === "DELETE"
    ) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ ok: true }),
      });
      return;
    }
    const responses: Record<string, unknown> = {
      "/api/session": { user: "admin", csrf_token: "e2e-csrf" },
      "/api/branding": branding,
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
      "/api/monitoring": monitoring,
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
          notification_categories: [
            "availability",
            "monitoring",
            "certificate",
            "backup",
          ],
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

test("overview site traffic sorts by the selected column", async ({ page }) => {
  const sites = [
    {
      ...overview.sites[0],
      id: "site-alpha",
      name: "Alpha",
      requests: 10,
      bytes: 300,
    },
    {
      ...overview.sites[0],
      id: "site-bravo",
      name: "Bravo",
      requests: 30,
      bytes: 100,
    },
    {
      ...overview.sites[0],
      id: "site-charlie",
      name: "Charlie",
      requests: 20,
      bytes: 200,
    },
  ];
  await mockAPI(page, { "/api/overview": { ...overview, sites } });
  await page.goto("/#/overview");

  const table = page.getByRole("table");
  const firstRow = table.locator("tbody tr").first();
  const requestsHeader = page.getByRole("columnheader", { name: "请求数" });

  await expect(requestsHeader).toHaveAttribute("aria-sort", "descending");
  await expect(firstRow).toContainText("Bravo");

  await page.getByRole("button", { name: "按站点升序排序" }).click();
  await expect(
    page.getByRole("columnheader", { name: "站点" }),
  ).toHaveAttribute("aria-sort", "ascending");
  await expect(firstRow).toContainText("Alpha");

  await page.getByRole("button", { name: "按站点降序排序" }).click();
  await expect(firstRow).toContainText("Charlie");

  await page.getByRole("button", { name: "按传输量降序排序" }).click();
  await expect(
    page.getByRole("columnheader", { name: "传输量" }),
  ).toHaveAttribute("aria-sort", "descending");
  await expect(firstRow).toContainText("Alpha");

  await page.getByRole("button", { name: "按传输量升序排序" }).click();
  await expect(firstRow).toContainText("Bravo");
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

test("SMTP test shows progress and keeps timeout feedback visible", async ({
  page,
}) => {
  await mockAPI(page, {
    "/api/settings": {
      branding: {
        name: "CDN Platform",
        subtitle: "控制面板",
        logo_data_url: "",
      },
      cache: { default_size_gb: 1 },
      dns: { default_ttl_seconds: 60 },
      cloudflare: {
        source: "environment",
        configured: true,
        override_configured: false,
        environment_configured: true,
      },
      smtp: {
        enabled: true,
        host: "smtp.example.test",
        port: 465,
        username: "mailer",
        from_address: "cdn@example.test",
        recipients: ["ops@example.test"],
        notification_categories: [
          "availability",
          "monitoring",
          "certificate",
          "backup",
        ],
        security: "tls",
        source: "database",
        override_configured: true,
        password_configured: true,
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
  });
  let releaseFirstRequest: () => void = () => undefined;
  const firstRequestGate = new Promise<void>((resolve) => {
    releaseFirstRequest = resolve;
  });
  let attempts = 0;
  await page.route("**/api/settings/smtp/test", async (route) => {
    attempts += 1;
    if (attempts === 1) {
      await firstRequestGate;
      await route.fulfill({
        status: 504,
        contentType: "application/json",
        body: JSON.stringify({ error: "SMTP connection timed out" }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ ok: true }),
    });
  });

  await page.goto("/#/settings");
  await page.getByRole("tab", { name: "通知" }).click();
  await page.getByRole("button", { name: "发送测试邮件" }).click();
  const pendingButton = page.getByRole("button", { name: "正在发送" });
  await expect(pendingButton).toBeDisabled();
  await expect(pendingButton).toHaveAttribute("aria-busy", "true");
  await expect(pendingButton.locator(".animate-spin")).toBeVisible();

  releaseFirstRequest();
  const failure = page
    .getByRole("alert")
    .filter({ hasText: "测试邮件发送失败" });
  await expect(failure).toContainText(
    "SMTP 连接超时，请检查服务器、端口、安全连接方式及网络连通性。",
  );
  await page.getByLabel("服务器").fill("smtp-alt.example.test");
  await expect(failure).toBeVisible();

  await page.getByRole("button", { name: "发送测试邮件" }).click();
  await expect(failure).toHaveCount(0);
  await expect(page.getByText("测试邮件已发送")).toBeVisible();
  expect(attempts).toBe(2);
});

test("SMTP alert categories can be saved independently", async ({ page }) => {
  await mockAPI(page);
  let savedCategories: string[] | undefined;
  await page.route("**/api/settings/smtp", async (route) => {
    const input = route.request().postDataJSON() as {
      notification_categories: string[];
    };
    savedCategories = input.notification_categories;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ ok: true }),
    });
  });

  await page.goto("/#/settings");
  await page.getByRole("tab", { name: "通知" }).click();
  const monitoring = page.getByRole("switch", { name: "TCP 拨测异常" });
  const backup = page.getByRole("switch", { name: "备份任务" });
  await expect(monitoring).toBeChecked();
  await expect(backup).toBeChecked();
  await monitoring.click();
  await backup.click();
  await page.getByRole("button", { name: "保存 SMTP" }).click();

  await expect
    .poll(() => savedCategories)
    .toEqual(["availability", "certificate"]);
});

test("branding settings update the sidebar immediately", async ({
  page,
}, testInfo) => {
  await mockAPI(page);
  await page.goto("/#/settings");

  await expect(page.getByRole("tab", { name: "通用" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "品牌" })).toHaveCount(0);
  await expect(page.getByLabel("品牌标识")).toHaveValue("CDN Platform");
  await expect(page.getByLabel("副标题")).toHaveValue("控制面板");
  await page.getByLabel("品牌标识").fill("DustK Edge");
  await page.getByLabel("副标题").fill("边缘控制台");
  await page.getByLabel("品牌 Logo").setInputFiles({
    name: "dustk-logo.png",
    mimeType: "image/png",
    buffer: Buffer.from(
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
      "base64",
    ),
  });
  await page.getByRole("button", { name: "保存通用设置" }).click();

  const sidebar = page.locator('[data-sidebar="sidebar"]');
  await expect(sidebar.getByText("DustK Edge", { exact: true })).toBeVisible();
  await expect(sidebar.getByText("边缘控制台", { exact: true })).toBeVisible();
  await expect(
    sidebar.locator('img[src^="data:image/png;base64,"]'),
  ).toBeVisible();
  await expect(page).toHaveTitle("DustK Edge · 边缘控制台");
  await expect(
    page.locator('link[rel="icon"][data-branding-icon]'),
  ).toHaveAttribute("href", /^data:image\/png;base64,/);

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
  await expect(page).toHaveTitle("DustK Edge · 边缘控制台");

  await page.evaluate(() => window.localStorage.clear());
  await page.reload();
  await expect(bootScreen.getByText("正在验证登录状态")).toBeVisible();
  await expect(
    bootScreen.getByText("CDN Platform", { exact: true }),
  ).toHaveCount(0);
  await expect(sidebar.getByText("DustK Edge", { exact: true })).toBeVisible();
  await expect(page).toHaveTitle("DustK Edge · 边缘控制台");
  await page.screenshot({
    path: testInfo.outputPath("branding-settings.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth + 1,
    ),
  ).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath("branding-settings-mobile.png"),
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
    ["monitoring", "监测"],
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

test("monitoring workspace shows scoring, probe results, and target controls", async ({
  page,
}, testInfo) => {
  const errors = trackPageErrors(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockAPI(page);
  await page.goto("/#/monitoring");

  await expect(
    page.getByRole("heading", { name: "监测", level: 1 }),
  ).toBeVisible();
  await expect(page.getByText("edge-hong-kong")).toBeVisible();
  await expect(page.getByText("监测暂停")).toBeVisible();
  await expect(page.getByText("96", { exact: true })).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("monitoring-desktop.png"),
    fullPage: true,
  });

  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "监测", level: 1 }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth + 1,
    ),
  ).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath("monitoring-mobile.png"),
    fullPage: true,
  });

  await page.setViewportSize({ width: 1440, height: 900 });

  await page.getByRole("tab", { name: "拨测明细" }).click();
  await expect(page.getByText("connect: connection timed out")).toBeVisible();
  await expect(page.getByText("3 / 3").first()).toBeVisible();

  await page.getByRole("tab", { name: "目标配置" }).click();
  await expect(page.getByText("probe-a.example.com:443")).toBeVisible();
  await page.getByRole("button", { name: "添加目标" }).click();
  await page.getByLabel("名称").fill("新探针");
  await page
    .getByLabel("IP:端口 或 域名:端口")
    .fill("probe-new.example.com:9443");
  await page.getByRole("button", { name: "添加", exact: true }).click();
  await expect(page.getByText("拨测目标已添加")).toBeVisible();
  await page.getByRole("button", { name: "重命名 主 API" }).click();
  const renameDialog = page.getByRole("dialog");
  await renameDialog.getByLabel("名称").fill("核心 API");
  await renameDialog.getByRole("button", { name: "保存" }).click();
  await expect(page.getByText("拨测目标名称已更新")).toBeVisible();
  expect(errors).toEqual([]);
});

test("monitoring node history overlays named targets and switches range", async ({
  page,
}, testInfo) => {
  const errors = trackPageErrors(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockAPI(page);
  await page.goto("/#/monitoring");
  await page
    .getByRole("link", { name: "查看 edge-hong-kong 拨测历史" })
    .click();

  await expect(
    page.getByRole("heading", { name: "edge-hong-kong", level: 1 }),
  ).toBeVisible();
  await expect(page.getByText("主 API", { exact: true })).toBeVisible();
  await expect(page.getByText("备用入口", { exact: true })).toBeVisible();
  const chart = page.getByTestId("monitoring-history-chart");
  await expect(chart).toHaveAttribute("data-series-count", "2");
  await expect(chart.locator(".recharts-line-curve")).toHaveCount(2);

  const sevenDayResponse = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return (
      url.pathname === "/api/monitoring/nodes/node-1/history" &&
      url.searchParams.get("range") === "7d"
    );
  });
  const sevenDayTab = page.getByRole("tab", { name: "7 天" });
  await sevenDayTab.click();
  await sevenDayResponse;
  await expect(sevenDayTab).toHaveAttribute("aria-selected", "true");
  await page.screenshot({
    path: testInfo.outputPath("monitoring-history-desktop.png"),
    fullPage: true,
  });

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(chart).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth + 1,
    ),
  ).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath("monitoring-history-mobile.png"),
    fullPage: true,
  });
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
