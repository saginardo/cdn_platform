# CDN Platform

A small self-hosted CDN for one administrator, one Debian 12 control VPS, and 3-10 Debian 12 edge VPSs. Cloudflare is authoritative DNS only: end users connect directly to the edge nodes.

## What is implemented

- Go control plane with SQLite metadata, Argon2id password login, TOTP, one-time recovery codes, CSRF protection, audit records, and a compact management UI.
- Node-first enrollment: create a pending node, copy a 15-minute one-time bootstrap command, then bind all later edge calls to an internally issued mTLS client certificate.
- Edge agent that writes Nginx configuration and certificates atomically, checks local public-port ownership, runs `nginx -t`, reloads a healthy Nginx or starts a failed/stopped Nginx, and restores the last known-good configuration and TLS files on failure.
- Nginx OSS cache policy with a 5 GiB default disk cap per edge node, normalized cache generation, cache locking, revalidation, background refresh, stale fallback, HTTP(S) primary/backup origin failover, and cookie/authorization bypass. HTTP(S) sites can enable full passthrough mode to disable cache and buffering for the entire hostname while forwarding byte ranges; explicit stream paths support WebSocket and SSE without buffering or caching; `grpc://` and `grpcs://` origins use native gRPC proxying over the client HTTP/2 listener.
- Cloudflare DNS-only A-record reconciliation after health hysteresis: 3 failed probes remove a node; 5 successful probes restore it. If every node is bad, DNS is deliberately left unchanged.
- DNS-01 certificates through Certbot's Cloudflare plugin; certificate private keys remain encrypted in SQLite and are only delivered over mTLS.
- ClickHouse raw request logs with a 7-day TTL and minute aggregates with a 30-day TTL. Edge logs locally queue while the control plane is unavailable.
- SMTP alert interface and encrypted restic S3-compatible daily backups for the SQLite database, control configuration/TLS material, internal CA, and certificate material.

## Deliberate boundaries

- Single administrator, IPv4 only, Cloudflare DNS-only, a single Cloudflare account, no tenant/RBAC model, no GeoDNS, no URL-level purge, no WAF/DDoS service, and no control-plane high availability.
- A control-plane outage does not interrupt already deployed edge traffic. It prevents new deployment, DNS changes, and certificate renewal until restored.
- `Publish` is intentionally separate from `Create site`. A site is staged until it has a valid certificate; a publish task succeeds only after every assigned active edge reports that it loaded the target configuration.
- Site-level cache invalidation increments the cache generation in the key. Existing objects are reclaimed by Nginx `inactive` and `max_size`; no unsupported OSS purge module is required.

## Repository layout

```text
cmd/control          Control-plane executable
cmd/edge-agent       Edge agent executable
internal/control     API, auth, CA, publish, health/DNS orchestration
internal/edge        Enrollment, mTLS polling, atomic apply, local log queue
internal/nginx       Generated Nginx cache and origin configuration
internal/integrations Cloudflare, Certbot, SMTP adapters
internal/logstore    ClickHouse access-log and aggregate storage
deploy/              Compose and environment templates
scripts/             Compose control-plane helpers and release builds
```

## Build and test

```bash
GOCACHE=/private/tmp/cdn_platform_go_cache \
GOMODCACHE=/private/tmp/cdn_platform_gomodcache \
GOPATH=/private/tmp/cdn_platform_gopath \
go test ./...

./scripts/build-release.sh dist
```

`dist/SHA256SUMS` contains the exact digest required by `EDGE_BINARY_SHA256`. The controller can also serve the signed edge binary itself when `EDGE_BINARY_PATH` is configured; use `https://CONTROL_PUBLIC_URL/downloads/cdn-edge-agent-linux-amd64` as `EDGE_BINARY_URL`.

## Control-plane installation

Docker Compose is the supported deployment for the control plane and ClickHouse. It keeps configuration, SQLite, the internal CA, certificate state, ClickHouse data, logs, and backup staging below `/opt/cdn-platform`. The existing public reverse proxy can remain separate; the controller still terminates TLS on its direct port for edge mTLS. See [docs/COMPOSE_DEPLOYMENT.md](docs/COMPOSE_DEPLOYMENT.md) for installation, backup, and restore instructions.

On a fresh Debian 12 control VPS with Docker Engine and Docker Compose, run from a trusted checkout:

```bash
sudo ./scripts/install-control-compose.sh /opt/cdn-platform
sudoedit /opt/cdn-platform/config/control.env
cd /opt/cdn-platform
sudo docker compose config --quiet
sudo docker compose build control
sudo docker compose run --rm --no-deps control keygen
```

Put the generated key in `CONTROL_ENCRYPTION_KEY`. Use a Cloudflare API token scoped only to the zones this system manages, with `Zone:Read` and `DNS:Edit`. Configure `CONTROL_TLS_DOMAIN`, `CONTROL_PUBLIC_URL`, `EDGE_CONTROL_URL`, and `ACME_EMAIL`, then start the stack:

```bash
sudo docker compose up -d
sudo docker compose ps
```

The bundled ClickHouse configuration is tuned for the 2-core, 4 GiB control host: it limits the background scheduler and disables high-volume internal profiling tables such as `system.metric_log` and `system.trace_log`. CDN access logs, minute aggregates, query diagnostics, part diagnostics, errors, and asynchronous-insert diagnostics remain enabled. User-level ClickHouse limits and profiler switches are installed separately under `users.d`.

Firewall policy on the control VPS:

- TCP 443 from administrators and edge nodes.
- TCP 22 only from your administration source.
- ClickHouse port 8123 bound to localhost unless you deliberately use a separate log node.

When a conventional HTTPS reverse proxy terminates the management UI, do not send edge mTLS through that proxy. Bind the controller to a second direct TLS port, set `EDGE_CONTROL_URL` to that port, and keep `CONTROL_PUBLIC_URL` on the proxy's standard HTTPS port. Set `TRUSTED_PROXY_CIDRS` to the proxy's loopback or private address so setup restrictions, audits, and login rate limits use its `X-Real-IP` header safely.

Open `https://control.example.com/`, initialize the single administrator, add the TOTP secret to an authenticator, and store the returned recovery codes offline. The setup route becomes unavailable after the first account is created.

Before first public startup, set `SETUP_ALLOW_CIDRS` to your administrator egress CIDR whenever possible. This prevents another Internet user from racing the one-time setup endpoint.

## Edge enrollment

1. Add a node in the **Nodes** view with its fixed public IPv4.
2. Set `EDGE_BINARY_URL` and `EDGE_BINARY_SHA256` to a HTTPS URL and SHA-256 digest for the `cdn-edge-agent-linux-amd64` release.
3. Use **Enroll** and run the generated command as root on that Debian 12 VPS.
4. The agent creates its private key locally, submits a CSR using the 15-minute one-time token, receives an internal mTLS certificate, and begins heartbeats every 30 seconds.

The same generated command installs a fresh edge, migrates the legacy scattered layout, or upgrades an existing `/opt/cdn-edge` deployment. It keeps the node identity, certificates, applied version, pending access-log queue, offset, and access logs during migration; the recreatable Nginx cache starts empty. Do not publish new site state while legacy and migrated nodes coexist. See [docs/EDGE_DEPLOYMENT.md](docs/EDGE_DEPLOYMENT.md) for the layout, migration checks, backup boundary, and rollback behavior.

The agent keeps the last working Nginx configuration if the control plane is unavailable or a new configuration fails validation. It checks TCP 80/443 before applying a public site: a non-Nginx listener is reported to the publish task with its port, PID, and process name; the agent never stops that process. Once the port is released, click **Republish** and the agent clears Nginx's failed state and starts it automatically. Do not delete `/opt/cdn-edge/data` on an active edge node; it contains the node private key, mTLS certificate, applied version, and pending access-log queue.

## Edge uninstall

Revoking authorization only invalidates the node certificate; it does not remove software or data from the edge host. To retire a host, use the separate **Uninstall node** workflow:

1. Pause scheduling or revoke authorization for the node.
2. Remove it from every site, assign replacement active nodes, and publish each changed site. A disabled site is exempt from the replacement-node requirement.
3. Start uninstall preparation. The controller removes only Cloudflare A records whose managed comment exactly identifies that node, then enforces a 75-second DNS safety wait.
4. Generate the 30-minute workflow command and run it as root on the edge host. The script stops the agent and removes `/opt/cdn-edge`, its systemd/Nginx integration links, and any legacy CDN Platform paths. It validates and reloads Nginx, but preserves the Nginx package, service, system logs, and unrelated configuration.
5. A successful callback retains the node as **Uninstalled** for audit history. Deleting that control-plane record is a separate confirmation-protected action.

If Nginx validation or reload fails before cleanup is committed, the script restores the platform configuration and the previous edge-agent service state. **Force complete** only changes control-plane state when a host is permanently unreachable; it does not verify or perform remote cleanup.

## First site

1. Add the site with its Cloudflare Zone ID, hostname(s), assigned node IDs, primary origin, and optional backup origin. Generated HTTPS sites default to a 128 MiB request-body limit; the site form can raise it to the fixed 256, 512, or 1024 MiB presets. Use HTTP(S) origins for normal sites; enable passthrough mode for an entire HTTP(S) hostname that must not use disk cache, including media byte-range traffic. Set stream paths such as `/ws` or `/events` for WebSocket/SSE, use `ws://` or `wss://` for an all-WebSocket site, and use `grpc://` or `grpcs://` for an all-gRPC hostname.
2. In Cloudflare, keep these hostname records as DNS-only. The control plane only manages records tagged with `cdn-platform:site=<site-id>;...`; it refuses a hostname already occupied by an untagged or another site's A record.
3. Run **Issue TLS**. The control VPS queues an asynchronous DNS-01 job via the scoped Cloudflare token and stores the resulting certificate encrypted. The Sites view polls its status; reloading the page does not cancel it. Only one active certificate job may exist per site, so repeated clicks reuse that job.
4. Run **Publish**. The controller builds each affected node's desired state and waits up to 90 seconds for assigned active nodes to validate and apply it. The Sites view shows per-node conflicts or timeout details; after resolving a conflict, click **Republish**.
5. Wait for an edge to be active and pass five health probes. The controller then creates 60-second DNS-only A records.
6. Fetch `GET /api/sites/{site-id}/origin-allowlist` from the authenticated API and install those `/32` CIDRs in the source origin firewall/security group. This prevents direct origin bypass.

The edge health endpoint is `http://EDGE_IPV4/__cdn_health`; expose port 80 and 443 publicly on edge nodes. The origin itself should permit inbound traffic only from the returned edge CIDRs.

### Range 流量与透传模式

对于不需要视频缓存、只需要稳定转发 HTTP(S) Range 流量的整站代理，启用“透传模式（仅 HTTP(S)，禁用 Nginx 缓存）”并重新发布。不要在保留 `proxy_cache` 的前提下只补充 `Range` / `If-Range`；这不能保证正确回源范围语义。完整的启用条件、限制、故障根因和 `206` 验证命令见 [docs/PASSTHROUGH_MODE.md](docs/PASSTHROUGH_MODE.md)。

Certificate jobs use `CERTIFICATE_ISSUE_TIMEOUT` (default `10m`). A control-plane stop or restart marks an in-flight job as failed rather than retrying it automatically, to avoid duplicate ACME requests; click **Issue TLS** again after the controller is healthy. The authenticated APIs `GET /api/sites/{site-id}/certificate-task` and `GET /api/tasks/{task-id}` expose the persisted task state and failure detail.

## Runtime behavior

```text
Client -> Cloudflare DNS-only A -> Edge Nginx -> primary origin -> optional backup origin
                                      |
                                      +-> disk cache / stale on upstream failure
                                          or full passthrough for configured sites

Administrator -> Control API/UI -> Cloudflare DNS / Certbot DNS-01 / SMTP / SQLite / ClickHouse
Edge agent --- mTLS ---> desired state, heartbeat, batched access logs
```

The desired failure window is about 2-5 minutes: a node needs three 15-second failed probes before it is removed, then recursive resolver caching applies to the 60-second DNS TTL. When every assigned node appears unhealthy, records are retained and an alert is sent rather than intentionally publishing an empty answer set.

## Backup and restore

The Compose backup workflow uses SQLite's online backup API and a native ClickHouse backup, then writes the complete recovery set to encrypted Restic storage. It retains 7 daily, 4 weekly, and 6 monthly snapshots. Configuration and restore steps are documented in [docs/COMPOSE_DEPLOYMENT.md](docs/COMPOSE_DEPLOYMENT.md).

## Capacity and next limits

The first deployment target is less than 100 requests/second across all sites, with 7 days of raw logs. Use at least 4 vCPU, 8 GiB RAM, and a 160 GiB NVMe control VPS. Increase storage or move ClickHouse to a separate machine before retaining raw logs longer than seven days or consistently approaching 100 RPS.
