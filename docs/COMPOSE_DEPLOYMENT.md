# Control-plane Docker Compose deployment

Docker Compose is the supported control-plane deployment. Edge nodes use the host Nginx package and a systemd agent, with CDN-owned files consolidated below `/opt/cdn-edge`; see [EDGE_DEPLOYMENT.md](EDGE_DEPLOYMENT.md).

## Persistent layout

The installer creates one operational and backup boundary:

```text
/opt/cdn-platform/
  app/                         pinned source used for local image builds
  compose.yaml
  .env
  config/
    control.env                control secrets and runtime settings
    backup.env                 restic and backup schedule settings
    restic-password            restic repository password
  data/
    control/                   SQLite, internal CA, site Certbot state
    control-tls/               direct control certificate and renewal state
    clickhouse/                ClickHouse data
  logs/
    certbot-sites/
    certbot-control/
    clickhouse/
  backup/staging/clickhouse/   native ClickHouse backup disk
```

The shared host-network Caddy installation stays outside this directory. It proxies `control.example.com` from public port 443 to `https://127.0.0.1:${CONTROL_MTLS_PORT}`. The control container terminates TLS itself so edge mTLS remains end-to-end.

## Fresh installation

Run from a trusted repository checkout on a Debian 12 host with Docker Engine and Docker Compose:

```bash
sudo ./scripts/install-control-compose.sh /opt/cdn-platform
sudoedit /opt/cdn-platform/config/control.env
cd /opt/cdn-platform
sudo docker compose config --quiet
sudo docker compose build control
sudo docker compose run --rm --no-deps control keygen
```

Put the generated key in `CONTROL_ENCRYPTION_KEY`. Set `CONTROL_TLS_DOMAIN`, `CONTROL_PUBLIC_URL`, `EDGE_CONTROL_URL`, the scoped Cloudflare token, and ACME email. Then start the stack:

```bash
sudo docker compose up -d
sudo docker compose ps
curl -fsS https://control.example.com/healthz
```

The control image contains the exact edge-agent binary it serves. The controller calculates its SHA-256 at startup; a configured `EDGE_BINARY_SHA256` must match or startup fails.

When a release changes generated Nginx paths, deploy the new controller without publishing site changes, migrate every legacy edge using its generated deployment/upgrade command, and only then run `docker compose run --rm --no-deps control publish-all`. Existing desired state is retained across the controller restart, so this order keeps legacy nodes on their last working configuration during migration.

## Backup

The backup container uses SQLite's online backup API and a native ClickHouse `BACKUP DATABASE` operation. It does not copy either live database directory. Configure `config/backup.env`, write a non-empty `config/restic-password`, and store the Restic repository coordinates, password, and S3 credentials in a separate offline recovery record.

Initialize a new Restic repository once before starting the scheduler:

```bash
cd /opt/cdn-platform
sudo docker compose --profile backup run --rm --entrypoint restic backup init
```

Start the optional scheduler only after these values are complete:

```bash
cd /opt/cdn-platform
sudo docker compose --profile backup up -d backup
sudo docker compose --profile backup run --rm --entrypoint \
  /usr/local/lib/cdn-platform/compose-backup.sh backup
```

The default schedule is 03:25 Asia/Shanghai with up to 20 minutes of random delay. Retention is 7 daily, 4 weekly, and 6 monthly snapshots. The backup includes the `cdn_platform` ClickHouse database, not recreatable `system` diagnostic tables.

## Restore

On a replacement host, install the same source revision and populate `config/backup.env`, `config/restic-password`, and the S3 credentials from the offline recovery record. Do not initialize the control plane first. Then run:

```bash
sudo CDN_PLATFORM_ROOT=/opt/cdn-platform \
  ./app/scripts/restore-control-compose.sh latest
```

The restore script refuses to overwrite an existing SQLite database unless `ALLOW_NONEMPTY_RESTORE=1` is explicitly set. It restores control secrets and TLS state, starts an empty ClickHouse, restores its native backup, and then starts the control services. After restoration, verify the public health endpoint and confirm that edge heartbeats resume before making configuration changes.
