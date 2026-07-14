# Edge deployment and migration

Edge nodes use the Debian Nginx package and a host systemd service. Docker is not used. CDN Platform-owned binaries, configuration, persistent state, access logs, and cache are kept below one operational root:

```text
/opt/cdn-edge/
  .layout-version
  bin/cdn-edge-agent
  config/
    edge.env
    certs/
    nginx/cdn-platform.conf
  data/
    edge-client.key
    edge-client.crt
    edge-ca.crt
    applied-version
    access-log-queue.ndjson
    access-log-offset
  logs/access.json
  cache/
  systemd/cdn-edge-agent.service
```

Two system integration links remain outside this root because systemd and the Debian Nginx package discover configuration there:

```text
/etc/systemd/system/cdn-edge-agent.service -> /opt/cdn-edge/systemd/cdn-edge-agent.service
/etc/nginx/conf.d/cdn-platform.conf -> /opt/cdn-edge/config/nginx/cdn-platform.conf
```

Nginx itself, its global configuration, system error log, and the agent journal remain managed by Debian. The agent runs as root because it atomically writes site certificates and configuration, validates Nginx, and reloads the service. Nginx cache files are owned by `www-data`.

## Fresh installation and upgrades

Use **获取部署/升级命令** on the node page and run the generated command as root on the target Debian 12 node. The command is bound to the configured HTTPS control and binary URLs and verifies the edge binary SHA-256 before installing it.

The installer is idempotent and recognizes these states:

- No CDN deployment: installs a new `/opt/cdn-edge` layout and enrolls with the 15-minute token.
- Legacy deployment: migrates `/usr/local/bin/cdn-edge-agent`, `/etc/cdn-platform`, `/var/lib/cdn-platform`, `/var/log/cdn-platform`, `/var/cache/cdn-platform`, the Nginx include, and the systemd unit.
- Existing `/opt/cdn-edge`: replaces the checksum-verified binary and service definition while preserving configuration data, identity, logs, and cache.

For a node that already has a control-plane certificate fingerprint, the generated upgrade command contains no new enrollment token. The installer requires the complete local mTLS key/certificate/CA set instead. This preserves the node identity and avoids leaving an unused valid enrollment token after an upgrade.

A valid layout marker and the two integration links determine ownership. If unrelated or ambiguous files exist in both the old and new layouts, the installer stops instead of merging them.

## Legacy node migration

Migrate one node at a time. While legacy and migrated nodes coexist, do not publish or republish sites: the new controller renders `/opt/cdn-edge` paths that an old-layout agent cannot use.

1. Confirm the node is active, has no `last_error`, and has a replacement edge serving each public site if interruption is unacceptable.
2. In the node page, generate a fresh deployment/upgrade command and run it as root on the node.
3. The installer stops only `cdn-edge-agent`; Nginx keeps serving the already loaded configuration.
4. It copies the mTLS identity, site certificates, applied version, pending log queue, queue offset, and rotated logs. It moves the active access log on the same filesystem so the offset and open Nginx file descriptor remain valid.
5. It converts the generated Nginx paths, switches the system integration links, runs `nginx -t`, reloads Nginx, starts the agent, waits for the mTLS identity, and checks the local health endpoint.
6. Only after all checks succeed does it remove legacy paths. The Nginx disk cache is deliberately discarded and rebuilt below `/opt/cdn-edge/cache`.

If the active access log and `/opt/cdn-edge` are on different filesystems, the installer stops before switching Nginx. This avoids silently losing or replaying request logs. Move `/opt` onto the root filesystem or perform an explicitly planned maintenance migration before retrying.

After each node, verify:

```bash
sudo systemctl is-active cdn-edge-agent nginx
sudo systemctl cat cdn-edge-agent
sudo nginx -t
curl -fsS http://127.0.0.1/__cdn_health
sudo find /opt/cdn-edge -maxdepth 3 -type f -o -type l
```

After every node has migrated, run `cdn-control publish-all` through the control Compose service, wait for each node's applied version to catch up, and confirm the control UI shows recent heartbeats without `last_error`.

## Failure and rollback

Before switching paths, the installer saves the current Nginx entry, systemd unit, default Nginx site, and service state. A failed Nginx validation/reload, agent start, enrollment, or health check restores those entries and the previous edge-agent state. Temporary transaction directories are removed on success and failure.

For a fresh node, a failure after successful enrollment may consume the one-time token. Generate a new deployment command before retrying. Do not manually combine a partial legacy layout with a marked `/opt/cdn-edge` layout.

## Backup boundary

Back up these non-recreatable paths:

- `/opt/cdn-edge/config`: agent settings and site TLS certificates.
- `/opt/cdn-edge/data`: node mTLS identity, applied version, and pending access-log delivery state.

`/opt/cdn-edge/logs` is optional if uploaded access logs are already retained by the control plane. The checksum-verified binary, generated systemd unit, generated Nginx configuration, and `/opt/cdn-edge/cache` are recreatable and normally excluded from backup.

Use the control-plane **卸载节点** workflow for permanent removal. It removes the `/opt/cdn-edge` tree and integration links while preserving the Debian Nginx package and unrelated host configuration.
