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
    control.env                bootstrap secrets and runtime fallbacks
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

The authenticated **Settings** view stores runtime overrides in SQLite. Cloudflare Token, SMTP password, S3 secret access key, and Restic repository password values are encrypted with `CONTROL_ENCRYPTION_KEY`; API responses never return them. Database overrides take precedence over their environment fallbacks, while reset actions restore those fallbacks. Retain the environment Cloudflare token because a fresh installation needs it before the UI and database exist. Control-certificate bootstrap and renewal containers mount the control database read-only and refresh their temporary Certbot credentials before each certificate operation.

When a release changes generated Nginx paths, deploy the new controller without publishing site changes, migrate every legacy edge using its generated deployment/upgrade command, and only then run `docker compose run --rm --no-deps control publish-all`. Existing desired state is retained across the controller restart, so this order keeps legacy nodes on their last working configuration during migration.

To rebuild only one site's affected nodes after a renderer fix, use `docker compose run --rm --no-deps control publish-site <site-id>`. This preserves unrelated node versions and avoids a fleet-wide Nginx reload.

## Backup

The backup container uses SQLite's online backup API and a native ClickHouse `BACKUP DATABASE` operation. It does not copy either live database directory. `config/backup.env` and `config/restic-password` are bootstrap and fallback settings. After the controller is running, the authenticated **Settings > S3 backup** form can override the repository URL, S3 credentials, region, Restic password, daily time, and random delay. The scheduler reloads these effective settings at least once per minute, so saving or resetting the form does not require a container restart.

The web override is stored inside the control database, so it cannot be the only recovery copy of the credentials needed to open that database's Restic snapshot. Always retain the repository coordinates, S3 credentials, `CONTROL_ENCRYPTION_KEY`, and Restic password in a separate offline recovery record. Keep working fallback values in `config/backup.env` and `config/restic-password` when practical.

Initialize a new Restic repository once before starting the scheduler:

```bash
cd /opt/cdn-platform
sudo docker compose --profile backup run --rm --entrypoint \
  /usr/local/lib/cdn-platform/compose-backup-restic.sh backup init
```

The wrapper resolves the database override first and falls back to `config/backup.env`. Saving settings does not initialize or migrate a repository. Initialize each new repository once, then use **Validate repository** in the web form. Other manual Restic operations must use the same wrapper, for example:

```bash
sudo docker compose --profile backup run --rm --entrypoint \
  /usr/local/lib/cdn-platform/compose-backup-restic.sh backup \
  snapshots --tag cdn-control-compose
```

Start the optional scheduler only after these values are complete:

```bash
cd /opt/cdn-platform
sudo docker compose --profile backup up -d backup
sudo docker compose --profile backup run --rm --entrypoint \
  /usr/local/lib/cdn-platform/compose-backup.sh backup
```

The default schedule is 03:25 Asia/Shanghai with up to 20 minutes of random delay. Retention is 7 daily, 4 weekly, and 6 monthly snapshots. The backup includes the `cdn_platform` ClickHouse database, not recreatable `system` diagnostic tables. After an upgrade that changes the backup scripts or image, rebuild the image and recreate the optional scheduler with `docker compose --profile backup up -d --build backup`.

## Restore

On a replacement host, install the same source revision and populate `config/backup.env`, `config/restic-password`, and `CONTROL_ENCRYPTION_KEY` from the offline recovery record. Disaster recovery intentionally uses these environment credentials because the web override is inside the snapshot being restored. Do not initialize the control plane first. Then run:

```bash
sudo CDN_PLATFORM_ROOT=/opt/cdn-platform \
  ./app/scripts/restore-control-compose.sh latest
```

The restore script refuses to overwrite an existing SQLite database unless `ALLOW_NONEMPTY_RESTORE=1` is explicitly set. It restores control secrets and TLS state, starts an empty ClickHouse, restores its native backup, and then starts the control services. After restoration, verify the public health endpoint and confirm that edge heartbeats resume before making configuration changes.
