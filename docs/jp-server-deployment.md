# Japan Server Deployment

The Japan server uses a small pull-and-build deployment flow designed for the
existing systemd installation:

- service: `cliproxy.service`
- binary: `/opt/cliproxy/cli-proxy-api`
- persistent deployment state: `/var/lib/cliproxy/deploy`
- source transport branch: `github-deploy/deploy-jp`
- health check: `http://127.0.0.1:18317/healthz`

The server script keeps the Go toolchain, module cache, build cache, source
checkout, deployed commit, checksum, and recent binary backups. Builds use
CGO so the existing Go plugins continue to load.

## Publish source

Commit and push the normal project branch first, then publish a deployment
snapshot:

```bash
./scripts/publish-jp-deploy.sh
```

The GitHub credential used by this checkout cannot update workflow files. The
publisher therefore creates a transport commit that keeps the destination
repository's `.github/workflows` directory while copying the current source
tree. The commit records the real source revision in a `Source-Commit` trailer.

Environment overrides:

```bash
CLIPROXY_DEPLOY_REMOTE=github-deploy \
CLIPROXY_DEPLOY_BRANCH=deploy-jp \
CLIPROXY_DEPLOY_BASE_BRANCH=main \
./scripts/publish-jp-deploy.sh HEAD
```

## Deploy on the server

Install `scripts/cliproxy-server-deploy.sh` as
`/usr/local/sbin/cliproxy-deploy`, then run:

```bash
sudo cliproxy-deploy
```

The first run downloads the pinned Go toolchain. Later runs fetch only the
latest deployment ref and reuse Go module/build caches.

The deployment sequence is:

1. acquire a deployment lock;
2. fetch the deployment branch;
3. build a CGO-enabled Linux binary with version metadata;
4. copy the current binary into `/opt/cliproxy/releases`;
5. atomically replace the executable and restart systemd;
6. wait for `/healthz`;
7. verify that the new process did not report plugin load failures;
8. automatically restore the backup on failure;
9. keep the latest five release backups by default.

Useful overrides:

```bash
CLIPROXY_DEPLOY_REF=deploy-jp \
CLIPROXY_DEPLOY_GO_VERSION=1.26.0 \
CLIPROXY_DEPLOY_KEEP_RELEASES=5 \
sudo cliproxy-deploy
```

To deploy another ref explicitly:

```bash
sudo cliproxy-deploy <branch-or-tag>
```
