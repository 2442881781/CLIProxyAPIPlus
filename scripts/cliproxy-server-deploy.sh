#!/usr/bin/env bash

set -euo pipefail

REPOSITORY_URL="${CLIPROXY_DEPLOY_REPOSITORY:-https://github.com/2442881781/CLIProxyAPIPlus.git}"
DEPLOY_REF="${1:-${CLIPROXY_DEPLOY_REF:-deploy-jp}}"
GO_VERSION="${CLIPROXY_DEPLOY_GO_VERSION:-1.26.0}"
STATE_DIR="${CLIPROXY_DEPLOY_STATE_DIR:-/var/lib/cliproxy/deploy}"
INSTALL_DIR="${CLIPROXY_DEPLOY_INSTALL_DIR:-/opt/cliproxy}"
SERVICE_NAME="${CLIPROXY_DEPLOY_SERVICE:-cliproxy.service}"
HEALTH_URL="${CLIPROXY_DEPLOY_HEALTH_URL:-http://127.0.0.1:18317/healthz}"
KEEP_RELEASES="${CLIPROXY_DEPLOY_KEEP_RELEASES:-5}"
SOURCE_DIR="${STATE_DIR}/source"
TOOLCHAIN_DIR="${STATE_DIR}/toolchains/go${GO_VERSION}"
GOCACHE="${STATE_DIR}/cache/build"
GOMODCACHE="${STATE_DIR}/cache/modules"
TARGET="${INSTALL_DIR}/cli-proxy-api"
RELEASE_DIR="${INSTALL_DIR}/releases"
LOCK_FILE="${STATE_DIR}/deploy.lock"

log() {
  printf '[cliproxy-deploy] %s\n' "$*"
}

fail() {
  printf '[cliproxy-deploy] ERROR: %s\n' "$*" >&2
  exit 1
}

if [[ "${EUID}" -ne 0 ]]; then
  fail "run this script as root"
fi

for command_name in curl git tar gcc systemctl flock; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "missing required command: ${command_name}"
done

install -d -m 0755 "${STATE_DIR}" "${STATE_DIR}/toolchains" "${GOCACHE}" "${GOMODCACHE}" "${RELEASE_DIR}"
exec 9>"${LOCK_FILE}"
flock -n 9 || fail "another deployment is already running"

ensure_go() {
  if [[ -x "${TOOLCHAIN_DIR}/bin/go" ]] && [[ "$("${TOOLCHAIN_DIR}/bin/go" version)" == *"go${GO_VERSION}"* ]]; then
    return
  fi

  local archive tmp_dir
  archive="${STATE_DIR}/toolchains/go${GO_VERSION}.linux-amd64.tar.gz"
  tmp_dir="${TOOLCHAIN_DIR}.tmp"
  log "downloading Go ${GO_VERSION}"
  curl -fL --retry 3 --connect-timeout 15 \
    -o "${archive}.tmp" \
    "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
  mv -f "${archive}.tmp" "${archive}"
  rm -rf "${tmp_dir}"
  install -d -m 0755 "${tmp_dir}"
  tar -xzf "${archive}" -C "${tmp_dir}" --strip-components=1
  rm -rf "${TOOLCHAIN_DIR}"
  mv "${tmp_dir}" "${TOOLCHAIN_DIR}"
}

sync_source() {
  if [[ ! -d "${SOURCE_DIR}/.git" ]]; then
    rm -rf "${SOURCE_DIR}"
    git init -q "${SOURCE_DIR}"
    git -C "${SOURCE_DIR}" remote add origin "${REPOSITORY_URL}"
  else
    git -C "${SOURCE_DIR}" remote set-url origin "${REPOSITORY_URL}"
  fi

  log "fetching ${REPOSITORY_URL} ${DEPLOY_REF}"
  git -C "${SOURCE_DIR}" fetch -q --depth=1 origin "${DEPLOY_REF}"
  git -C "${SOURCE_DIR}" checkout -q -B deploy FETCH_HEAD
  git -C "${SOURCE_DIR}" reset -q --hard FETCH_HEAD
}

source_commit() {
  local declared
  declared="$(git -C "${SOURCE_DIR}" show -s --format=%B | awk '/^Source-Commit: / { print $2; exit }')"
  if [[ -n "${declared}" ]]; then
    printf '%s' "${declared}"
  else
    git -C "${SOURCE_DIR}" rev-parse HEAD
  fi
}

rollback() {
  local backup="$1"
  log "rolling back to ${backup}"
  systemctl stop "${SERVICE_NAME}" || true
  install -m 0755 "${backup}" "${TARGET}.rollback"
  mv -f "${TARGET}.rollback" "${TARGET}"
  systemctl start "${SERVICE_NAME}"
}

ensure_go
sync_source

SOURCE_COMMIT="$(source_commit)"
SHORT_COMMIT="${SOURCE_COMMIT:0:8}"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
BUILD_OUTPUT="${STATE_DIR}/cli-proxy-api-${SHORT_COMMIT}.new"
BACKUP="${RELEASE_DIR}/cli-proxy-api-$(date -u +%Y%m%dT%H%M%SZ)"

log "building ${SHORT_COMMIT} with CGO enabled"
(
  cd "${SOURCE_DIR}"
  CGO_ENABLED=1 \
  GOCACHE="${GOCACHE}" \
  GOMODCACHE="${GOMODCACHE}" \
  "${TOOLCHAIN_DIR}/bin/go" build \
    -trimpath \
    -ldflags="-s -w -X main.Version=${SHORT_COMMIT} -X main.Commit=${SHORT_COMMIT} -X main.BuildDate=${BUILD_DATE}" \
    -o "${BUILD_OUTPUT}" \
    ./cmd/server
)

[[ -x "${BUILD_OUTPUT}" ]] || fail "build did not produce an executable"
ldd "${BUILD_OUTPUT}" >/dev/null 2>&1 || fail "build is not CGO-capable; dynamic plugins would be disabled"

if [[ -e "${TARGET}" ]]; then
  cp -a "${TARGET}" "${BACKUP}"
fi
install -m 0755 "${BUILD_OUTPUT}" "${TARGET}.new"
mv -f "${TARGET}.new" "${TARGET}"
chown root:root "${TARGET}"

log "restarting ${SERVICE_NAME}"
systemctl restart "${SERVICE_NAME}"

healthy=0
for _ in $(seq 1 45); do
  if systemctl is-active --quiet "${SERVICE_NAME}" && curl -fsS "${HEALTH_URL}" >/dev/null; then
    healthy=1
    break
  fi
  sleep 1
done

if [[ "${healthy}" -ne 1 ]]; then
  [[ -e "${BACKUP}" ]] && rollback "${BACKUP}"
  fail "health check failed: ${HEALTH_URL}"
fi

pid="$(systemctl show -p MainPID --value "${SERVICE_NAME}")"
if [[ -n "${pid}" ]] && journalctl -u "${SERVICE_NAME}" "_PID=${pid}" --no-pager | grep -q 'failed to load plugin'; then
  [[ -e "${BACKUP}" ]] && rollback "${BACKUP}"
  fail "new process reported a plugin load failure"
fi

printf '%s\n' "${SOURCE_COMMIT}" >"${STATE_DIR}/deployed-commit"
sha256sum "${TARGET}" >"${STATE_DIR}/deployed-sha256"
rm -f "${BUILD_OUTPUT}"

if [[ "${KEEP_RELEASES}" =~ ^[0-9]+$ ]]; then
  mapfile -t old_releases < <(find "${RELEASE_DIR}" -maxdepth 1 -type f -name 'cli-proxy-api-*' -printf '%T@ %p\n' | sort -nr | awk -v keep="${KEEP_RELEASES}" 'NR > keep { sub(/^[^ ]+ /, ""); print }')
  if [[ "${#old_releases[@]}" -gt 0 ]]; then
    rm -f -- "${old_releases[@]}"
  fi
fi

log "deployed ${SHORT_COMMIT}; health check passed"
systemctl --no-pager --full --lines=5 status "${SERVICE_NAME}"
