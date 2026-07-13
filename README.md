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
deploy/              systemd units and environment templates
scripts/             Debian installation and backup scripts
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

Build a Linux AMD64 release on a trusted build host, publish `cdn-edge-agent-linux-amd64` over HTTPS, then copy the repository assets and `cdn-control-linux-amd64` to the control VPS. On a fresh Debian 12 control VPS, run:

```bash
sudo ./scripts/install-control.sh ./cdn-control-linux-amd64
sudo cp deploy/examples/control.env.example /etc/cdn-platform/control.env
sudo cp deploy/examples/backup.env.example /etc/cdn-platform/backup.env
sudo chmod 0600 /etc/cdn-platform/*.env
sudo /usr/local/bin/cdn-control keygen
```

Put the generated key in `CONTROL_ENCRYPTION_KEY`. Use a Cloudflare API token scoped only to the zones this system manages, with `Zone:Read` and `DNS:Edit`. Install a publicly trusted certificate for `CONTROL_PUBLIC_URL` before starting the service, then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now clickhouse-server cdn-control cdn-backup.timer
sudo systemctl status cdn-control
```

On a constrained control VPS, install with `INSTALL_CLICKHOUSE=0 sudo ./scripts/install-control.sh ...` and set `CLICKHOUSE_DISABLED=1` in `control.env`. The controller remains fully functional, but access-log collection and aggregate metrics are disabled until ClickHouse is enabled.

The bundled ClickHouse configuration is tuned for the 2-core, 4 GiB control host: it limits the background scheduler and disables high-volume internal profiling tables such as `system.metric_log` and `system.trace_log`. CDN access logs, minute aggregates, query diagnostics, part diagnostics, errors, and asynchronous-insert diagnostics remain enabled. User-level ClickHouse limits and profiler switches are installed separately under `users.d`.

Firewall policy on the control VPS:

- TCP 443 from administrators and edge nodes.
- TCP 22 only from your administration source.
- ClickHouse port 8123 bound to localhost unless you deliberately use a separate log node.

When a conventional HTTPS reverse proxy terminates the management UI, do not send edge mTLS through that proxy. Bind the controller to a second direct TLS port, set `EDGE_CONTROL_URL` to that port, and keep `CONTROL_PUBLIC_URL` on the proxy's standard HTTPS port. Set `TRUSTED_PROXY_CIDRS` to the proxy's loopback or private address so setup restrictions, audits, and login rate limits use its `X-Real-IP` header safely.

If the reverse proxy owns the public certificate, stage its certificate and key under `/etc/cdn-platform` before starting the controller. The optional `cdn-control-tls-sync.timer` runs the configured synchronization every 15 minutes and restarts the controller only after the source material changes.

Open `https://control.example.com/`, initialize the single administrator, add the TOTP secret to an authenticator, and store the returned recovery codes offline. The setup route becomes unavailable after the first account is created.

Before first public startup, set `SETUP_ALLOW_CIDRS` to your administrator egress CIDR whenever possible. This prevents another Internet user from racing the one-time setup endpoint.

## Edge enrollment

1. Add a node in the **Nodes** view with its fixed public IPv4.
2. Set `EDGE_BINARY_URL` and `EDGE_BINARY_SHA256` to a HTTPS URL and SHA-256 digest for the `cdn-edge-agent-linux-amd64` release.
3. Use **Enroll** and run the generated command as root on that Debian 12 VPS.
4. The agent creates its private key locally, submits a CSR using the 15-minute one-time token, receives an internal mTLS certificate, and begins heartbeats every 30 seconds.

The agent keeps the last working Nginx configuration if the control plane is unavailable or a new configuration fails validation. It checks TCP 80/443 before applying a public site: a non-Nginx listener is reported to the publish task with its port, PID, and process name; the agent never stops that process. Once the port is released, click **Republish** and the agent clears Nginx's failed state and starts it automatically. Do not delete `/var/lib/cdn-platform` on an active edge node; it contains the node private key, mTLS certificate, applied version, and pending access-log queue.

## First site

1. Add the site with its Cloudflare Zone ID, hostname(s), assigned node IDs, primary origin, and optional backup origin. Use HTTP(S) origins for normal sites; enable passthrough mode for an entire HTTP(S) hostname that must not use disk cache, including media byte-range traffic. Set stream paths such as `/ws` or `/events` for WebSocket/SSE, use `ws://` or `wss://` for an all-WebSocket site, and use `grpc://` or `grpcs://` for an all-gRPC hostname.
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

Configure S3-compatible restic credentials in `/etc/cdn-platform/backup.env` and a 0600 restic password file. The daily timer retains 7 daily, 4 weekly, and 6 monthly encrypted backups.

To restore onto a replacement control VPS:

```bash
sudo systemctl stop cdn-control
sudo restic restore latest --target /tmp/cdn-restore
sudo install -o cdn-platform -g cdn-platform -m 0600 /tmp/cdn-restore/var/lib/cdn-platform/backup-staging/control.db /var/lib/cdn-platform/control.db
sudo tar -xzf /tmp/cdn-restore/var/lib/cdn-platform/backup-staging/control-secrets.tar.gz -C /var/lib/cdn-platform
sudo install -m 0600 /tmp/cdn-restore/etc/cdn-platform/control.env /etc/cdn-platform/control.env
sudo tar -xzf /tmp/cdn-restore/var/lib/cdn-platform/backup-staging/control-tls.tar.gz -C /
sudo chown -R cdn-platform:cdn-platform /var/lib/cdn-platform
sudo systemctl start cdn-control
```

The TLS archive restores the exact `CONTROL_TLS_SOURCE_CERT` and `CONTROL_TLS_SOURCE_KEY` paths from the snapshot, including certificate targets behind symbolic links. Keep the same `CONTROL_ENCRYPTION_KEY`, restore the internal CA, verify Cloudflare records, and confirm agents can still authenticate. Raw ClickHouse logs are intentionally not included in this recovery set.

## Capacity and next limits

The first deployment target is less than 100 requests/second across all sites, with 7 days of raw logs. Use at least 4 vCPU, 8 GiB RAM, and a 160 GiB NVMe control VPS. Increase storage or move ClickHouse to a separate machine before retaining raw logs longer than seven days or consistently approaching 100 RPS.
