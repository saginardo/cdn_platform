# Edge access security policies

The **Security** workspace manages global HTTP access policies and active edge bans. Policies are ordered by ascending priority and match Nginx's normalized `$uri` value before a request is redirected or proxied. The supported expression language is the RE2-compatible subset of PCRE plus non-capturing groups. Additional validation rejects Nginx variable interpolation, high-backtracking repeated groups, and overly complex expressions before a policy can reach an edge.

Two built-in policies are enabled by default. **Sensitive file scanning** detects segment-bounded probes for environment files, repository metadata, cloud and developer credentials, shell history, private keys, Terraform state, and `wp-config.php`; it uses the **IP ban** action for six hours. **PHP malicious file probing** blocks high-risk diagnostic and web-shell file names such as `phpinfo`, `shell`, `webshell`, `cmd`, `c99`, `r57`, and `wso`, while deliberately excluding normal entry points such as `index.php`, `api.php`, `admin.php`, and `config.php`. The PHP policy defaults to request-only blocking. Built-in policies can be edited or disabled but not deleted. Additional policies can either reject only the matching request or reject it and ban the source IPv4 for 1, 6, 12, or 24 hours.

## Request and ban flow

1. The controller renders enabled policies into an Nginx `map` only for nodes advertising `edge_security_v1`.
2. A match writes a structured event to `/opt/cdn-edge/logs/security.json` and closes the request with Nginx status 444 before origin proxying.
3. The Agent reads the security log every 500 milliseconds. A ban action is written to durable local state before the Agent updates nftables.
4. The Agent reports UUID-keyed events over its existing mTLS identity. The controller validates the policy, action, public IPv4, normalized path, and timestamp, and records each event idempotently. An event delayed while the controller is unavailable receives only the ban time remaining from its original observation.
5. Other edge agents pull the active global ban set every 30 seconds. Manual unban and automatic expiry are reconciled on the next pull.

Local state is stored below `/opt/cdn-edge/data`:

```text
security-bans.json
security-event-queue.json
security-log-offset
```

An edge restart reconstructs the firewall from unexpired local state and the controller. If the control plane is unavailable, a newly detected ban still applies locally and its event remains queued.

## Firewall ownership

The installer adds Debian's `nftables` package but does not replace `/etc/nftables.conf` or enable an operator firewall policy. The Agent owns only this runtime object:

```text
table inet cdn_platform
```

Its base chain has an accept policy and adds one restriction: source IPv4 addresses in the managed timeout set are dropped only for TCP ports 80 and 443. SSH, control traffic, custom TCP forwarding ports, outbound traffic, and every other nftables table remain untouched. Uninstall removes only `table inet cdn_platform`.

Only public IPv4 addresses are accepted as ban targets. Private, loopback, link-local, multicast, malformed, and IPv6 addresses are ignored. This matches the platform's current A-record and `public_ipv4` deployment model.

## Rollout

Existing agents do not receive security configuration until they have been upgraded to a release that advertises `edge_security_v1`. Upgrade one node at a time, verify the capability count in **Security**, then select **Deploy policies**. Nodes without the capability retain their previous Nginx configuration.

Use these checks on an upgraded edge:

```bash
sudo systemctl is-active cdn-edge-agent nginx
sudo nft list table inet cdn_platform
sudo nginx -T 2>/dev/null | grep -F cdn_security_policy_id
sudo tail -n 20 /opt/cdn-edge/logs/security.json
sudo journalctl -u cdn-edge-agent --since '-10 minutes' --no-pager
```

Keep public site DNS records in DNS-only/direct mode. The ban source is Nginx `$remote_addr`; putting a shared reverse proxy in front without a separately designed trusted-client-IP boundary would ban that proxy instead of the originating client.

## Current limits

- Policies are global rather than scoped to individual sites.
- At most 100 policies can exist at once. The edge queue retains the latest 10,000 events and both the edge and controller cap active ban state at 50,000 addresses.
- Matching covers normalized request paths, not query strings, headers, request bodies, or TCP forwarding traffic.
- Ban enforcement is IPv4-only and scoped to HTTP/HTTPS ports.
- The first matching policy wins. Saving a policy rebuilds capable node desired states and therefore causes a normal verified Nginx reload.
- This is malicious-path blocking, not a request-rate limiter or application authentication layer.
- The controller retains at most 100,000 recent events and 50,000 simultaneous global bans; oldest entries are evicted first when these safety limits are reached. The console shows the 500 bans with the latest expiry while reporting the full active count.
