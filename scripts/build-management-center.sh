#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="${ROOT_DIR}/web/management-center"
OUTPUT_FILE="${ROOT_DIR}/static/management.html"

if ! command -v bun >/dev/null 2>&1; then
  echo "Error: Bun is required to build the management center." >&2
  exit 1
fi

cd "${WEB_DIR}"
bun install --frozen-lockfile
bun run verify

install -d "$(dirname "${OUTPUT_FILE}")"
install -m 0644 dist/index.html "${OUTPUT_FILE}"

echo "Management center built: ${OUTPUT_FILE}"
