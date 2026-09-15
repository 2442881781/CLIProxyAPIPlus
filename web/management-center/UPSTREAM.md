# Upstream provenance

The management frontend in this directory is based on:

- Repository: https://github.com/router-for-me/Cli-Proxy-API-Management-Center
- Imported into this monorepo: 2026-09-15

The imported working snapshot did not contain its original `.git` directory, so its exact upstream base commit cannot be stated reliably. Do not invent or infer a base hash from file timestamps.

This monorepo maintains additional integrations for CLIProxyAPI, including provider credential management, provider quota views, usage monitoring, and related localization. When syncing a future upstream release, compare it against this directory and preserve the backend contracts implemented in the repository root.

Generated files are not source of truth:

- `web/management-center/dist/` is ignored.
- `static/management.html` is ignored.
- Run `./scripts/build-management-center.sh` from the repository root to verify the frontend and regenerate the embedded HTML file.
