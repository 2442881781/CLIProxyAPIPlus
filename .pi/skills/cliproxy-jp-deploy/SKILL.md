---
name: cliproxy-jp-deploy
description: Publish, deploy, verify, and troubleshoot CLIProxyAPI Plus blue-green releases on the Japan 16G production server through the 16g-jp-cn-optimized-baremetal MCP server. Use when asked to deploy, release, roll out, check deployment status, validate Blue/Green slots, inspect draining, or roll back the Japan server.
compatibility: CLIProxyAPI repository with Git, Go 1.26+, GitHub/Gitee remotes, and the 16g-jp-cn-optimized-baremetal MCP server.
metadata:
  project: CLIProxyAPI
  environment: japan-production
---

# CLIProxyAPI Japan Deployment

Use the repository scripts and the `16g-jp-cn-optimized-baremetal` MCP server. Do not replace this workflow with ad hoc SSH, local cross-compilation, direct binary uploads, or another browser/agent orchestration layer.

## Safety and authorization

- Treat Japan as production.
- A request to inspect status authorizes read-only commands only.
- A request to deploy or release authorizes the required publish and production rollout for the requested revision, including the deployment script's automatic rollback. It does not authorize unrelated changes, destructive cleanup, force pushes, or manual state deletion.
- Commit and push only when the user asks to deploy local changes, publish changes, or explicitly requests commit/push. Otherwise deploy an already published revision.
- Never print, copy, commit, or expose management keys, OAuth material, auth JSON contents, deployment tokens, or environment secrets.
- Preserve unrelated worktree changes and the known untracked `server` build artifact. Never stage `server`.
- Never run two deployments concurrently. Respect `/var/lib/cliproxy/deploy/deploy.lock`.
- Do not stop an old slot manually just because a tool call is slow. Inspect deployment/session state first.

## Canonical files

Read these before changing or troubleshooting the workflow:

- `AGENTS.md`
- `scripts/cliproxy-server-deploy.sh`
- `scripts/publish-jp-deploy.sh`
- `scripts/cliproxy-merge-state.py`
- `docs/jp-server-deployment.md`

Production layout:

- public entry: Nginx `:8317`
- Blue: `127.0.0.1:18317`, `cliproxy-blue.service`
- Green: `127.0.0.1:18318`, `cliproxy-green.service`
- active marker: `/var/lib/cliproxy/deploy/active-slot`
- state root: `/var/lib/cliproxy/deploy/slots`
- writable shared config: `/var/lib/cliproxy/config/config.yaml`
- config backups: `/var/lib/cliproxy/deploy/config-backups`
- deploy command: `/usr/local/sbin/cliproxy-deploy`
- transport branch: `github-deploy/deploy-jp`

## Select an operation

### Status only

Use MCP read-only or safe commands. Do not publish, restart, reload, enable, disable, or edit production files.

### Deploy an existing published revision

Skip local commit/push. Confirm the requested deployment ref exists, then run remote preflight, deployment, and acceptance checks.

### Deploy current local changes

Verify and commit only task-related files, push `origin/main`, publish the transport snapshot, then deploy and verify it.

## Local workflow

1. Inspect the worktree:

   ```bash
   git status --short --branch
   git log -5 --oneline
   ```

2. Review all task-related diffs. Do not include the untracked `server` file.

3. For Go changes, format and run the smallest relevant tests first. The minimum release compile gate is:

   ```bash
   go build -o test-output ./cmd/server && rm test-output
   ```

   Run broader tests according to risk. If `go test ./...` fails in an unrelated pre-existing area, preserve the exact package/test failure and still require all changed packages plus the compile gate to pass.

4. For management-center changes, validate the production artifact before publishing:

   ```bash
   cd web/management-center
   bun run verify
   ```

5. For deployment script changes, also run:

   ```bash
   bash -n scripts/cliproxy-server-deploy.sh scripts/publish-jp-deploy.sh
   uv run python -m ast scripts/cliproxy-merge-state.py >/dev/null
   git diff --check
   ```

6. Before committing, load and follow the global `commit` skill. Use a concise Conventional Commit subject. Stage only intended files.

7. Push the normal branch when authorized:

   ```bash
   git push origin main
   ```

8. Publish the transport snapshot:

   ```bash
   ./scripts/publish-jp-deploy.sh HEAD
   ```

   Publishing runs `bun run build` and injects `web/management-center/dist/index.html`
   into the transport snapshot as `static/management.html`. Record both the source
   commit and generated `github-deploy/deploy-jp` commit. Never force-push
   `github-deploy/main`.

## Remote preflight through MCP

Use the exact MCP server name `16g-jp-cn-optimized-baremetal`. Discover/describe its tools if schemas are not already loaded. The configured `default` profile connects as root, so prefer `run-command`; `privileged-command` may be denied by policy.

Before deployment, check without exposing secrets:

```bash
systemctl is-active cliproxy-blue.service || true
systemctl is-active cliproxy-green.service || true
systemctl is-active cliproxy.service || true
cat /var/lib/cliproxy/deploy/active-slot 2>/dev/null || true
curl -fsS http://127.0.0.1:8317/healthz
curl -fsS http://127.0.0.1:8317/readyz || true
nginx -t
ss -Htn state established '( sport = :8317 or sport = :18317 or sport = :18318 )'
df -h /opt/cliproxy /var/lib/cliproxy
```

Also compare the installed deployment script with the fetched source script:

```bash
sha256sum /usr/local/sbin/cliproxy-deploy \
  /var/lib/cliproxy/deploy/source/scripts/cliproxy-server-deploy.sh 2>/dev/null || true
```

If the installed script lacks Blue/Green support or differs from the newly published script, fetch `deploy-jp`, verify the source commit trailer, syntax-check the fetched script, and install that script before the rollout. Do not assume an old deployer can safely self-upgrade during the same invocation.

## Deploy

Run through MCP with a TTY when supported:

```bash
/usr/local/sbin/cliproxy-deploy deploy-jp
```

Default behavior:

- build on Linux with `CGO_ENABLED=1`;
- preserve dynamic Go plugin support;
- start and validate the inactive slot;
- switch Nginx only after readiness and plugin checks;
- reject new work on the old slot;
- wait for tracked HTTP, SSE, and WebSocket connections to drain;
- stop the old slot after drain or the configured hard deadline;
- enable only the active slot for reboot recovery;
- retain slot state for the next three-way merge.

Do not shorten `CLIPROXY_DEPLOY_DRAIN_TIMEOUT` unless the user explicitly accepts possible interruption. The normal default is 7200 seconds. Existing WebSockets remain on the old Nginx worker/backend; they are not migrated.

If the MCP call times out or loses output, do not rerun immediately. Check the deployment lock, active slot marker, services, listeners, and any MCP session output to determine whether the original rollout is still running.

## Acceptance checks

A rollout is complete only when all applicable checks pass:

1. Revision and slot:

   ```bash
   cat /var/lib/cliproxy/deploy/active-slot
   cat /var/lib/cliproxy/deploy/deployed-commit
   ```

   The deployed commit must equal the intended source commit, not merely the transport commit.

2. Confirm the runtime config source and permissions: the active process's
   startup log names its effective config source (`effective config source: ...`).
   With `PGSTORE_*` enabled the database row is authoritative and is mirrored to
   `<slot>/pgstore/config/config.yaml`; `/var/lib/cliproxy/config/config.yaml`
   only bootstraps an empty store, so editing it alone does not affect the
   running process. The bootstrap file is `cliproxy:cliproxy` mode `0640`, and
   the unit keeps `ProtectSystem=strict` with the config directories in
   `ReadWritePaths`.
3. Exactly one slot is active and enabled; the other slot and legacy service are inactive and disabled:

   ```bash
   systemctl is-active cliproxy-blue.service || true
   systemctl is-active cliproxy-green.service || true
   systemctl is-active cliproxy.service || true
   systemctl is-enabled cliproxy-blue.service || true
   systemctl is-enabled cliproxy-green.service || true
   systemctl is-enabled cliproxy.service || true
   ```

4. Nginx and health:

   ```bash
   nginx -t
   curl -fsS http://127.0.0.1:8317/healthz
   curl -fsS http://127.0.0.1:8317/readyz
   curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8317/my-usage.html
   curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8317/access-keys.html
   curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8317/v0/management/rates
   ```

   Expected page codes are 200, 200, and 401 without a management key.
   Also verify that the served `/management.html` hash matches the published
   `static/management.html`, including requests that advertise gzip encoding.

5. Deployment control endpoint:

   - local request with the token must report `ready=true`, `draining=false` and sensible connection counts;
   - public request without the token must return 404;
   - do not display the token in the final report.

6. Plugins: journal output for the new process must show both `commandcode` and `opencode-go` loaded and registered. Treat any of these as deployment failure:

   ```text
   failed to load plugin
   plugin.register failed
   returned invalid metadata or no capabilities
   ```

7. Inspect recent logs for panic, fatal errors, repeated restarts, write-permission failures, and unexpected auth/state paths.

8. Confirm the active private listener matches the Nginx upstream and the inactive private port is no longer listening after drain.

## Failure and rollback

- Before the old slot is stopped, let `cliproxy-deploy` perform its automatic Nginx rollback.
- After a failed or interrupted rollout, first collect: active marker, service states, listeners, Nginx upstream, health results, deploy lock owner, and recent journals.
- Do not delete slot state, cutover baselines, releases, or auth directories to fix a rollout.
- Do not restart both slots against the same auth directory.
- A manual rollback is a production write. It is allowed within an explicitly requested deployment only to restore the immediately preceding known-good slot. Validate the old slot privately before switching Nginx back.
- If state consistency is uncertain, stop and report the exact evidence rather than improvising a merge.

## Final report

Report concisely:

- source commit and transport commit;
- active slot, private port, and service;
- local tests/build checks actually run;
- Nginx, health, page, management-auth, and plugin results;
- drain result and whether any deadline forced disconnection;
- any unrelated test failures or remaining risks;
- confirmation that no secret values were printed or committed.
