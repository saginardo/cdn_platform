# CDN Platform 项目状态与参考技术架构

更新时间：2026-07-14（基于仓库实现和占位符部署拓扑，不记录实际生产环境信息）

## 1. 项目目标与边界

这是一个面向单管理员、小规模自用场景的自托管 CDN：一个控制面 VPS 管理约 3-10 台 Debian 12 边缘 VPS。Cloudflare 只作为权威 DNS 和 DNS-01 API 提供方，业务域名保持 DNS-only；终端用户直接连接边缘节点。

当前设计边界：单管理员、IPv4、单 Cloudflare 账户、无租户/RBAC、无控制面高可用、无 GeoDNS、无 WAF/DDoS 服务、无 URL 级删除缓存。控制面故障不会中断已经下发到边缘的流量，但会阻止新发布、DNS 调整和证书续期。

## 2. 当前实施结论

代码已经具备一个可运行的端到端闭环：节点注册、mTLS 边缘通信、站点配置、DNS-01 证书、Nginx 配置生成与回滚、缓存、流式协议、健康检查/DNS 对账、日志聚合、备份脚本和管理界面均已实现。

参考部署由一个 Compose 控制面和多台 active 边缘节点组成。边缘节点统一使用 `/opt/cdn-edge` 集中布局，并应通过全量重新发布、健康检查、DNS 对账、业务访问和日志续传验证。

## 3. 技术架构

```text
                         管理流量
管理员浏览器 ── HTTPS 443 ──> control.example.com
                              │
                              ├─ 公网反向代理层（当前为 Caddy 监听 443）
                              └─ cdn-control :${CONTROL_MTLS_PORT}
                                   │
                                   ├─ SQLite: 元数据、任务、会话、加密证书、节点状态
                                   ├─ ClickHouse: 原始访问日志和分钟聚合
                                   ├─ Cloudflare API: DNS-only A 记录对账
                                   ├─ Certbot + Cloudflare DNS 插件: DNS-01 证书签发
                                   ├─ SMTP: 告警接口
                                   └─ Restic: 控制面备份

                         边缘控制流量（mTLS）
edge-a 上的 cdn-edge-agent ── HTTPS ${CONTROL_MTLS_PORT} ──> cdn-control
       │                                      │
       ├─ 拉取 desired state                  ├─ 节点注册/CSR 签发内部客户端证书
       ├─ 原子写入 Nginx 配置与站点证书       ├─ 心跳、健康/DNS 对账
       ├─ nginx -t -> reload / 失败回滚       └─ 接收批量访问日志
       └─ 上报心跳、应用版本、日志

                         业务请求路径
客户端 ── Cloudflare DNS-only A ──> Edge Nginx :80/:443
                                          │
                                          ├─ 磁盘缓存 / stale 回退
                                          ├─ WebSocket / SSE 流式代理
                                          ├─ gRPC 原生代理
                                          └─ 主源站 -> 可选备用源站
```

### 3.1 控制面

- 语言与运行时：Go 1.26，`modernc.org/sqlite` 驱动的 SQLite。
- 管理认证：Argon2id 密码、TOTP、一次性恢复码、HttpOnly 会话 Cookie、CSRF 令牌、登录限流与审计记录。
- 部署模型：站点先创建和签发证书，再显式发布；发布生成每个边缘节点的 desired state，不直接 SSH 修改边缘。
- 证书：Certbot `dns-cloudflare` DNS-01，私钥以 `CONTROL_ENCRYPTION_KEY` 进行 AES-GCM 加密后保存在 SQLite，向边缘仅通过 mTLS 下发。
- 节点健康：每 15 秒检查 `http://EDGE_IPV4/__cdn_health`，连续 3 次失败从 DNS 池移除，连续 5 次成功恢复；所有节点均不健康时保留现有 DNS，不发布空记录。
- 日志：ClickHouse 原始请求日志保留 7 天，分钟级聚合保留 30 天；边缘控制面不可达时，本地队列暂存日志。

### 3.2 边缘面

- Edge agent 用 15 分钟的一次性注册令牌提交 CSR，获得控制面内部 CA 签发的 mTLS 客户端证书；后续所有状态拉取、心跳、日志上传均使用该证书。
- 边缘自有文件统一位于 `/opt/cdn-edge`：`bin/`、`config/`、`data/`、`logs/`、`cache/` 和 `systemd/`。系统路径仅保留 systemd 与 Nginx 的发现链接；Nginx 包、全局配置和 journald 仍由 Debian 管理。
- Agent 默认每 30 秒拉取一次状态；配置或证书写入采用原子替换，先执行 `nginx -t`，仅在成功时 reload，失败会恢复上一个已知可用版本。
- Nginx 为每个站点生成独立 `server` 和 `upstream`：80 强制跳转 HTTPS，443 使用 TLS 1.2/1.3，并通过独立的 `http2 on` 指令启用 HTTP/2。
- CDN 业务 HTTPS server 显式使用 `keepalive_timeout 300s` 和 `keepalive_requests 1000`；每个 upstream、每个 worker 的空闲回源连接池为 `keepalive 30`，HTTP/gRPC 回源连接超时统一为 10 秒。普通 HTTP 代理显式发送 `proxy_set_header Connection ""`，确保 HTTP/1.1 上游连接可以复用；流式路径按请求的 `Upgrade` 头决定 WebSocket 升级或普通 SSE 透传。

### 3.3 请求处理策略

- 普通 HTTP(S)：只缓存 GET/HEAD；缓存键包含 `site_id` 和 `cache_generation`；授权请求、带 Cookie 请求和源站 `Set-Cookie` 响应不缓存。
- 磁盘缓存：默认上限 5 GiB/节点，7 天非活跃回收，启用 cache lock、revalidate、后台刷新和上游错误时 stale 回退。
- 整站透传：HTTP(S) 站点可选启用，关闭 Nginx 缓存、请求缓冲和响应缓冲，读写超时为 1 小时，并显式转发 `Range` / `If-Range`，适用于视频及其他按字节范围读取的流量。操作语义、已验证故障根因和验收命令见 [PASSTHROUGH_MODE.md](PASSTHROUGH_MODE.md)。
- 请求体上限：业务站点默认 `128 MiB`，可按站点选择 `128 / 256 / 512 / 1024 MiB` 四个档位；修改后需重新发布才能进入边缘 Nginx 配置。
- WebSocket/SSE：站点可配置流式路径，例如 `/api` 同时匹配精确 `/api` 和 `/api/...`。这些路径关闭缓存、响应缓冲和请求缓冲，读写超时为 1 小时。WebSocket 通过 `Upgrade: websocket` 自动升级；SSE 由源站返回 `Content-Type: text/event-stream` 并持续 flush/心跳识别。
- gRPC：`grpc://`/`grpcs://` 源站使用 Nginx `grpc_pass`，客户端经 HTTP/2 接入，默认不缓存，支持 1 小时 gRPC 读写超时和可选备用源站。
- 源站容错：主/备源站支持连接、超时、无效响应和 5xx 的切换；HTTPS/gRPCS 源站启用 SNI 和 CA 校验。

## 4. 代码与模块状态

| 模块 | 状态 | 说明 |
| --- | --- | --- |
| `cmd/control` | 已实现 | 控制面进程；支持 `keygen` 和仅本机使用的 `publish-all`。 |
| `internal/control` | 已实现 | HTTP API、认证、证书任务、发布、DNS 健康对账、审计、嵌入式管理界面。 |
| `internal/store` | 已实现 | SQLite schema、迁移、站点/节点/任务/会话/证书/状态持久化。 |
| `internal/edge` | 已实现 | 注册、mTLS、配置同步、原子应用、Nginx 回滚、心跳和日志转发。 |
| `internal/nginx` | 已实现 | HTTP 缓存、整站透传、回源、TLS、WebSocket/SSE、gRPC、备用源站配置渲染。 |
| `internal/integrations` | 已实现 | Cloudflare、Certbot、SMTP 等外部适配器。 |
| `internal/logstore` | 已实现 | ClickHouse 原始日志与分钟指标读写。 |
| `internal/control/web` | 已实现 | 简体中文管理台，资源通过 `//go:embed web/*` 编入控制面二进制。 |
| `deploy/` 与 `scripts/` | 已实现 | Compose 主控安装、证书续期、Restic 备份、发布构建与 ClickHouse 配置。边缘安装资源由控制面从 `internal/control` 嵌入提供。 |

### 管理界面当前能力

- 概览：节点数、运行节点数、站点数、最近 24 小时请求量/传输量/5xx/缓存命中率。
- 节点：创建、生成幂等部署/升级命令、暂停/启用调度、撤销/重新启用、心跳与应用版本查看。
- 站点：创建、编辑、节点分配、主/备源站、流式路径、整站透传开关、发布、申请 TLS、缓存刷新、源站 CIDR 查看。
- 站点列表已经改为紧凑工作台布局：身份与发布状态、协议、节点、缓存、TLS 状态分层展示；非高频操作收进“更多”菜单。
- TLS 状态不再解析历史任务文本。接口 `GET /api/sites/{id}/tls-status` 返回最新证书任务及 `published_after_certificate`，只要签发完成后存在成功发布任务就显示“已签发”。

## 5. 参考部署模板

以下值仅用于说明部署关系。实际主机、域名、端口、版本和运行状态应从受控的运维系统实时读取，不应提交到 Git。

| 项目 | 当前值 |
| --- | --- |
| 控制面主机 | `control-host` |
| 管理域名 | `https://control.example.com` |
| 控制面监听 | Compose `control` 使用 host network 监听 `${CONTROL_MTLS_PORT}`；公网 443 由共享反向代理接入。 |
| 控制服务 | Compose `control`、`clickhouse`、`control-cert-renew` 应保持运行并通过健康检查。 |
| 数据目录 | 统一根目录 `/srv/cdn-platform`；配置、control 数据、ClickHouse、证书、日志、备份和 rollback 均在其下。 |
| ClickHouse | Compose `clickhouse` 固定为 `26.6.1.1193`，HTTP 仅映射到 `127.0.0.1:${CLICKHOUSE_HTTP_PORT}`。 |
| ClickHouse 验收 | 原始访问日志和分钟聚合应持续写入；具体数量不记录在仓库中。 |
| TLS | Compose 内使用 Cloudflare DNS-01 独立签发并每 12 小时检查续期；主控支持无重启热加载。 |
| 备份 | SQLite、ClickHouse 和 Restic 工作流应通过隔离恢复演练；凭据只保存在部署环境。 |
| 边缘节点 | `edge-a`（`203.0.113.10`）、`edge-b`（`203.0.113.11`）、`edge-c`（`203.0.113.12`）、`edge-d`（`203.0.113.13`）代表 RFC 5737 示例节点。 |
| 边缘应用状态 | 所有 active 节点的 `applied_version` 应与目标版本一致且 `last_error` 为空。 |
| Edge 服务 | `cdn-edge-agent.service` 和 `nginx.service` 均为 `active`，`nginx -t` 成功。 |
| Edge 目录迁移 | 所有边缘节点都应使用 `/opt/cdn-edge`；旧路径不得出现在 desired Nginx 配置中。 |
| 示例站点 | `api_example_com`、`app_example_com`、`node_example_com`、`stream_example_com` 代表已启用并发布的站点。 |

### 参考网络与安全分层

- 管理 UI/API 走 `control.example.com` 的公网 HTTPS 地址。
- Edge agent 使用 `EDGE_CONTROL_URL=https://control.example.com:${CONTROL_MTLS_PORT}` 直接进行 mTLS 通信，不经过公网管理反向代理。
- 控制容器使用 UID/GID `10001`，仅写 `/srv/cdn-platform/data/control` 和 Certbot 日志；ClickHouse 数据由 UID/GID `101` 持有。
- Edge agent 以 root 运行以写入 Nginx 配置、站点证书和执行 reload；其 systemd 服务同样设置 `LimitNOFILE=65536`。
- Edge 持久备份边界为 `/opt/cdn-edge/config` 与 `/opt/cdn-edge/data`；缓存可重建，访问日志在已上传到主控后可不纳入节点备份。
- Cloudflare API 令牌、控制面加密密钥等均在 `/srv/cdn-platform/config/control.env`，文档和日志中不应记录其值。

## 6. 已完成的近期修复

1. DNS-01 签发任务与控制面重启：签发任务在进程生命周期外持久化；控制面重启会将进行中的任务标为失败并要求显式重试，避免重复 ACME 操作。
2. Certbot 重复 TXT 记录：签发实现已处理此前排查出的重试和残留记录路径；应使用 `api.example.com` 等测试域名完成验收。
3. WebSocket、SSE、gRPC：已加入站点模型、校验、Nginx 生成、管理 UI 和测试；SSE/WS 使用流式路径，gRPC 使用专用源站协议。
4. 缓存上限：默认 Nginx 缓存上限从 50 GiB 调整为 5 GiB，适合小型 VPS。
5. 上游连接复用：普通主源和备用源 location 显式清空 `Connection` 头，避免 upstream keepalive 被默认 `Connection: close` 破坏；当前连接池固定为每个 upstream、每个 worker 30 个空闲连接。
6. 重新发布运维入口：新增 `cdn-control publish-all`，在 Nginx 渲染器升级后可重新构建全部已发布站点的 desired state，不需要直接编辑 SQLite 或绕过管理认证。
7. TLS UI 状态：改为签发任务与后续成功发布任务的时间线判断；消除了“实际已发布但仍显示已签发，待发布”的错误展示。
8. 管理台中文化与站点页重构：完成简体中文适配，并优化站点页面的密度、状态层级和操作菜单。
9. HTTP(S) 整站透传与 Range 修复：新增 `passthrough` 持久化字段、管理台开关、旧库迁移和 Nginx 无缓存渲染。开启后普通主/备源站位置关闭缓存与请求/响应缓冲，显式转发 `Range` / `If-Range`；完整验收方法见 [PASSTHROUGH_MODE.md](PASSTHROUGH_MODE.md)。
10. Edge 目录收敛：新装、旧布局迁移和后续升级统一使用 `/opt/cdn-edge`；迁移保留节点身份、证书、应用版本和日志队列，清空可重建缓存，并在 Nginx 或 Agent 验证失败时恢复旧服务状态。完整流程见 [EDGE_DEPLOYMENT.md](EDGE_DEPLOYMENT.md)。

## 7. 当前问题、风险与下一步

### P0：验证源站可用性

以 `api.example.com`、示例上游 `203.0.113.11:${ORIGIN_APP_PORT}` 为模板，分别验证端口监听、响应头读取和完整业务请求。仓库不记录实际源站地址、内部端口或实时故障状态。

处理顺序：先确认源站应用及其健康状态，修复或切换到可用 URL；随后从边缘和公网各发起一次请求验证。不要将源站问题归因于边缘证书、TLS 状态或 DNS。

### P1：完成可用性与运维闭环

- 配置 `/srv/cdn-platform/config/backup.env`、Restic 密码和 S3 兼容存储凭据，初始化仓库并启用 Compose `backup` profile；隔离备份恢复已通过，生产仓库尚未配置。
- 使用现有两个独立边缘节点完成 DNS 健康摘除/恢复、源站 CIDR 白名单与故障切换演练。
- 对 Cloudflare DNS 记录、站点源站防火墙和控制面 `${CONTROL_MTLS_PORT}` 访问规则做一次上线检查，确保业务记录为 DNS-only，源站仅放行边缘 IP。
- 为 ClickHouse 磁盘容量、日志写入失败和备份失败配置外部告警；当前本地日志和指标功能已启动，但尚未完成容量/告警演练。

### P2：连接与容量调优

- 参考边缘节点使用发行版 Nginx 全局默认的 `worker_connections 768`、`worker_processes auto`、`sendfile on`、`tcp_nopush on`。对少量连接足够，但 WebSocket/SSE 连接数增长后应按 VPS 的文件描述符、内存和源站能力调整 `worker_connections`、`worker_rlimit_nofile`、系统 `nofile` 和监控阈值。
- 已启用 HTTP/2，但尚未在生成配置中启用 HTTP/3/QUIC；是否增加取决于客户端覆盖、UDP 443 防火墙和运维复杂度。
- 流式路径 1 小时超时不能代替应用层保活。WebSocket 应发送 ping/pong，SSE 应每 15-30 秒发送注释或事件心跳。

### P3：产品与安全边界

- 当前无 RBAC、多租户、API token、WAF、限速、URL 级 purge、跨控制面高可用或自动扩缩容。
- 发布任务成功表示 desired state 已生成；DNS 放量还取决于边缘应用版本、健康检查阈值和 Cloudflare DNS 对账周期。面向生产时可在 UI 中继续增加“节点已应用/健康/DNS 已更新”的组合状态。
- Cloudflare Python SDK 的 Certbot 插件仍会打印版本升级警告；签发已成功，但应在维护窗口固定兼容版本或升级到受支持的主版本，避免未来自动升级造成 DNS-01 回归。

## 8. 开发、发布与验证流程

### 本地验证

```bash
cd /path/to/cdn_platform
GOCACHE=/tmp/cdn_platform_go_cache \
GOMODCACHE=/tmp/cdn_platform_go_modcache \
go test ./...
node --check internal/control/web/app.js
```

前端文件在 `internal/control/web/`，由 `internal/control/server.go` 使用 `//go:embed web/*` 编入 `cdn-control`。因此修改 UI 后必须重新编译和重启控制面，单独上传静态文件不会生效。

### 发布控制面

1. 将受信任的源码同步到 `control-host:/srv/cdn-platform/app`，并保持 `/srv/cdn-platform/compose.yaml` 使用同一版本。
2. 在 `/srv/cdn-platform` 执行 `docker compose build control` 和 `docker compose up -d`。
3. 检查 `docker compose ps`、控制容器日志和 `https://control.example.com/healthz`。
4. 需要更新已发布站点的 Nginx 渲染逻辑时，执行 `docker compose run --rm --no-deps control publish-all`，使 edge agent 拉取新的 desired state。

### 发布边缘与站点

1. 在节点页创建节点，或为现有节点获取部署/升级命令，并在边缘 root 环境运行；详细目录及迁移约束见 [EDGE_DEPLOYMENT.md](EDGE_DEPLOYMENT.md)。
2. 等待节点成功注册、心跳和 Nginx 健康检查。
3. 创建/更新站点，完成 DNS-01 TLS 签发，再点击“发布”。
4. 检查节点的 `applied_version` 追上 `node_states.version`，确认 `nginx -t`、`/__cdn_health`、证书和源站请求。
5. 在源站安全组中仅放行“源站 CIDR”接口返回的边缘地址。

### 关键实时检查

```bash
# 控制面
cd /srv/cdn-platform
sudo docker compose ps
curl -fsS https://control.example.com/healthz

# 控制面数据库中的节点和站点状态
sudo sqlite3 -header -column /srv/cdn-platform/data/control/control.db \
  'select name, public_ipv4, status, applied_version, last_error from nodes;'

# 边缘
sudo systemctl status cdn-edge-agent nginx
sudo nginx -t
curl -fsS http://127.0.0.1/__cdn_health
```

## 9. 关键文件索引

| 路径 | 用途 |
| --- | --- |
| `cmd/control/main.go` | 控制面启动、环境变量、ClickHouse、证书与健康任务、`publish-all`。 |
| `cmd/edge-agent/main.go` | 边缘 agent 启动和运行参数。 |
| `internal/control/server.go` | API 路由、认证保护、TLS 状态接口、嵌入静态资源。 |
| `internal/control/publisher.go` | 站点发布、desired state 和证书下发。 |
| `internal/control/certificates.go` | 异步 DNS-01 签发与续期。 |
| `internal/control/health.go` | 健康检查、Cloudflare DNS 对账。 |
| `internal/edge/agent.go` | mTLS 注册、同步、原子应用和回滚。 |
| `internal/nginx/render.go` | Nginx 缓存、流式、gRPC、TLS、回源配置生成。 |
| `internal/store/store.go` | SQLite schema 和持久化操作。 |
| `internal/logstore/clickhouse.go` | ClickHouse schema、访问日志和指标查询。 |
| `internal/control/web/` | 中文控制台的 HTML、CSS、JavaScript。 |
| `compose.yaml` | 主控、ClickHouse、证书续期和可选备份服务。 |
| `scripts/install-control-compose.sh` | Compose 主控目录初始化与安装。 |
| `internal/control/install-edge.sh` | 控制面嵌入并提供的边缘新装、旧布局迁移与升级脚本。 |
| `scripts/compose-backup.sh` | SQLite、ClickHouse、内部 CA、证书和控制配置的 Restic 备份。 |
| `scripts/restore-control-compose.sh` | 从 Restic 快照恢复完整主控数据。 |

## 10. 恢复开发时的第一步

1. 先运行本地完整测试，并检查 `control-host` 的控制面、ClickHouse、节点心跳和源站响应。
2. 优先解决 `${ORIGIN_APP_PORT}` 源站超时，恢复真实请求链路。
3. 配置生产 Restic 仓库并启用定时备份，再使用现有两个独立边缘节点演练 DNS 健康切换。
4. 后续功能改动遵循“先测试 -> 最小实现 -> 本地完整测试 -> 构建部署 -> 实际节点验证”的顺序；渲染器改动后使用 `publish-all` 重新生成已发布站点状态。
