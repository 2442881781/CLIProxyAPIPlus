# Japan Server Blue-Green Deployment

The Japan server uses an Nginx-fronted, two-slot deployment flow:

```text
client -> Nginx :8317 -> blue  127.0.0.1:18317
                       or green 127.0.0.1:18318
```

Only one slot receives new traffic. The previous slot remains alive after an
Nginx reload until its in-flight HTTP, SSE, and WebSocket connections drain or
the configured drain deadline expires.

## Layout

- services: `cliproxy-blue.service`, `cliproxy-green.service`
- slot binaries: `/opt/cliproxy/slots/{blue,green}/cli-proxy-api`
- writable shared config: `/var/lib/cliproxy/config/config.yaml`
- initial config source: `/etc/cliproxy/config.yaml` (copied only once)
- config backups: `/var/lib/cliproxy/deploy/config-backups`
- slot state: `/var/lib/cliproxy/deploy/slots/{blue,green}/auths`
- active slot marker: `/var/lib/cliproxy/deploy/active-slot`
- Nginx upstream: `/etc/nginx/conf.d/cliproxy-upstream.conf`
- source transport branch: `github-deploy/deploy-jp`
- deploy command: `/usr/local/sbin/cliproxy-deploy`

Each process receives its port and auth directory through runtime-only
environment overrides. Both slots share the writable runtime config under
`/var/lib/cliproxy/config`, while auth state remains isolated per slot. The
systemd units retain `ProtectSystem=strict` and expose only the runtime config,
the slot state, and logs as writable paths.

## Application lifecycle endpoints

- `GET /healthz`: process liveness
- `GET /readyz`: traffic eligibility; returns 503 while draining
- `GET /v0/deployment/status`: active HTTP/SSE and WebSocket counts
- `POST /v0/deployment/drain`: reject new work and begin draining

The deployment endpoints require a per-host control token and loopback source
address. The token is stored in `/run/cliproxy/deploy-control-token` and is not
proxied through Nginx for normal use.

## Publish source

Commit and push the normal project branch first, then publish a deployment
snapshot:

```bash
./scripts/publish-jp-deploy.sh
```

The publisher runs `bun run build` for `web/management-center`, injects
`dist/index.html` into the transport snapshot as `static/management.html`,
preserves the destination repository's `.github/workflows` directory, and
records the real source revision in a `Source-Commit` trailer.

## Deploy

```bash
sudo cliproxy-deploy
```

The deployment sequence is:

1. acquire the deployment lock;
2. initialize the writable runtime config from `/etc/cliproxy/config.yaml` when
   it does not yet exist, then create a root-only timestamped backup;
3. fetch the deployment branch, build a CGO-enabled Linux binary, and stage the
   published management UI with a precompressed gzip copy;
4. clone current active state into the inactive slot;
5. merge any usage or credential changes recorded while that slot previously
   drained;
6. start and validate the inactive slot on its private port;
7. verify `/readyz` and plugin loading;
8. atomically switch the Nginx upstream and reload Nginx;
9. verify both the private endpoint and public Nginx route;
10. replace `/opt/cliproxy/static/management.html` and its gzip copy, retaining
    the prior UI alongside binary release backups for rollback;
11. tell the previous slot to drain;
12. stop it after all tracked requests and WebSockets finish, or after the drain
    deadline;
13. retain its final state for the next three-way merge.

Nginx reload behavior preserves established downstream connections on old Nginx
workers. Those connections continue to the previous backend while new
connections use the new slot. Nginx does not migrate WebSockets between slots.

## State safety

The slots never share writable auth state. At cutover, the new slot receives a
clone of current state. When a previously drained slot is reused, the helper
`scripts/cliproxy-merge-state.py` performs a three-way merge:

- access-key and usage-monitor counters are merged as additive deltas;
- credential and cooldown files use a conservative change-aware merge;
- runtime logs are not merged.

This is temporary active/standby overlap, not active-active operation.

## Configuration

Useful overrides:

```bash
CLIPROXY_DEPLOY_REF=deploy-jp \
CLIPROXY_DEPLOY_GO_VERSION=1.26.0 \
CLIPROXY_DEPLOY_KEEP_RELEASES=5 \
CLIPROXY_DEPLOY_KEEP_CONFIG_BACKUPS=5 \
CLIPROXY_DEPLOY_DRAIN_TIMEOUT=7200 \
CLIPROXY_DEPLOY_DRAIN_POLL_INTERVAL=2 \
sudo cliproxy-deploy
```

`CLIPROXY_DEPLOY_DRAIN_TIMEOUT` is the hard deadline in seconds for an ordinary
blue-green update. Reaching it can interrupt an in-flight stream or WebSocket,
so keep it long enough for normal agent turns.

The deployment script never overwrites an existing runtime config from the
legacy `/etc` copy. Backups are root-owned mode `0600`; the live runtime config
is owned by `cliproxy:cliproxy` with mode `0640` so management saves and hot
reloads work inside the systemd sandbox.

The one-time migration from the legacy `cliproxy.service` cannot use the new
drain endpoint. It instead waits for existing TCP connections to disappear,
up to `CLIPROXY_DEPLOY_LEGACY_DRAIN_TIMEOUT` (default 120 seconds), before
stopping the legacy service.

## Rollback

Before the old slot is stopped, a failed post-cutover health check rewrites the
Nginx upstream to the previous slot and reloads Nginx. Recent replaced slot
binaries are retained under `/opt/cliproxy/releases`.

Operational checks:

```bash
cat /var/lib/cliproxy/deploy/active-slot
systemctl status cliproxy-blue.service cliproxy-green.service
curl -fsS http://127.0.0.1:8317/readyz
nginx -t
```
