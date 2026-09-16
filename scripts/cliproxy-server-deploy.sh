#!/usr/bin/env bash

set -euo pipefail

REPOSITORY_URL="${CLIPROXY_DEPLOY_REPOSITORY:-https://github.com/2442881781/CLIProxyAPIPlus.git}"
DEPLOY_REF="${1:-${CLIPROXY_DEPLOY_REF:-deploy-jp}}"
GO_VERSION="${CLIPROXY_DEPLOY_GO_VERSION:-1.26.0}"
STATE_DIR="${CLIPROXY_DEPLOY_STATE_DIR:-/var/lib/cliproxy/deploy}"
INSTALL_DIR="${CLIPROXY_DEPLOY_INSTALL_DIR:-/opt/cliproxy}"
CONFIG_FILE="${CLIPROXY_DEPLOY_CONFIG:-/etc/cliproxy/config.yaml}"
NGINX_SITE="${CLIPROXY_DEPLOY_NGINX_SITE:-/etc/nginx/sites-available/cliproxy}"
NGINX_UPSTREAM_FILE="${CLIPROXY_DEPLOY_NGINX_UPSTREAM_FILE:-/etc/nginx/conf.d/cliproxy-upstream.conf}"
SERVICE_PREFIX="${CLIPROXY_DEPLOY_SERVICE_PREFIX:-cliproxy}"
KEEP_RELEASES="${CLIPROXY_DEPLOY_KEEP_RELEASES:-5}"
DRAIN_TIMEOUT="${CLIPROXY_DEPLOY_DRAIN_TIMEOUT:-7200}"
DRAIN_POLL_INTERVAL="${CLIPROXY_DEPLOY_DRAIN_POLL_INTERVAL:-2}"
SOURCE_DIR="${STATE_DIR}/source"
TOOLCHAIN_DIR="${STATE_DIR}/toolchains/go${GO_VERSION}"
GOCACHE="${STATE_DIR}/cache/build"
GOMODCACHE="${STATE_DIR}/cache/modules"
RELEASE_DIR="${INSTALL_DIR}/releases"
SLOTS_DIR="${INSTALL_DIR}/slots"
RUN_DIR="/run/cliproxy"
ACTIVE_SLOT_FILE="${STATE_DIR}/active-slot"
LOCK_FILE="${STATE_DIR}/deploy.lock"
MERGE_SCRIPT="${INSTALL_DIR}/bin/cliproxy-merge-state.py"
DEPLOY_SCRIPT_TARGET="/usr/local/sbin/cliproxy-deploy"
CONTROL_TOKEN_FILE="${RUN_DIR}/deploy-control-token"
CONTROL_ENV_FILE="${RUN_DIR}/deploy-control.env"

log() {
  printf '[cliproxy-deploy] %s\n' "$*"
}

fail() {
  printf '[cliproxy-deploy] ERROR: %s\n' "$*" >&2
  exit 1
}

service_for_slot() {
  printf '%s-%s.service' "${SERVICE_PREFIX}" "$1"
}

port_for_slot() {
  case "$1" in
    blue) printf '18317' ;;
    green) printf '18318' ;;
    *) fail "unknown slot: $1" ;;
  esac
}

other_slot() {
  case "$1" in
    blue) printf 'green' ;;
    green) printf 'blue' ;;
    *) fail "unknown slot: $1" ;;
  esac
}

slot_binary() {
  printf '%s/%s/cli-proxy-api' "${SLOTS_DIR}" "$1"
}

slot_state() {
  printf '%s/slots/%s' "${STATE_DIR}" "$1"
}

slot_auth() {
  printf '%s/auths' "$(slot_state "$1")"
}

slot_base() {
  printf '%s/cutover-base' "$(slot_state "$1")"
}

control_curl() {
  local method="$1" port="$2" path="$3"
  curl -fsS --connect-timeout 2 --max-time 5 \
    -X "${method}" \
    -H "X-CLIProxy-Deploy-Token: $(<"${CONTROL_TOKEN_FILE}")" \
    "http://127.0.0.1:${port}${path}"
}

copy_tree() {
  local source="$1" target="$2"
  rm -rf "${target}"
  install -d -m 0750 -o cliproxy -g cliproxy "${target}"
  if [[ -d "${source}" ]]; then
    cp -a "${source}/." "${target}/"
    chown -R cliproxy:cliproxy "${target}"
  fi
}

rollback_nginx() {
  local old_slot="$1" old_service="$2" old_port="$3"
  log "rolling nginx back to ${old_slot}"
  if [[ "${LEGACY_MIGRATION:-0}" -eq 0 ]]; then
    systemctl start "${old_service}" || true
  fi
  write_nginx_upstream "${old_slot}"
  nginx -t && systemctl reload nginx
  local rollback_health="readyz"
  if [[ "${LEGACY_MIGRATION:-0}" -eq 1 ]]; then
    rollback_health="healthz"
  fi
  if ! curl -fsS "http://127.0.0.1:${old_port}/${rollback_health}" >/dev/null; then
    fail "rollback completed but ${old_slot} is not ready"
  fi
}

write_nginx_upstream() {
  local slot="$1" port
  port="$(port_for_slot "${slot}")"
  cat >"${NGINX_UPSTREAM_FILE}.new" <<EOF
# Managed by cliproxy-deploy. Do not edit manually.
upstream cliproxy_backend {
    server 127.0.0.1:${port};
    keepalive 64;
}
EOF
  chmod 0644 "${NGINX_UPSTREAM_FILE}.new"
  mv -f "${NGINX_UPSTREAM_FILE}.new" "${NGINX_UPSTREAM_FILE}"
}

ensure_nginx_site() {
  if grep -Eq 'proxy_pass[[:space:]]+http://cliproxy_backend;' "${NGINX_SITE}"; then
    return
  fi
  python3 - "${NGINX_SITE}" <<'PY'
from pathlib import Path
import re
import sys
path = Path(sys.argv[1])
text = path.read_text()
updated, count = re.subn(r"proxy_pass\s+http://127\.0\.0\.1:1831[78];", "proxy_pass http://cliproxy_backend;", text, count=1)
if count != 1:
    raise SystemExit("expected exactly one CLIProxyAPI proxy_pass entry")
path.write_text(updated)
PY
}

ensure_systemd_units() {
  for slot in blue green; do
    port="$(port_for_slot "${slot}")"
    auth_dir="$(slot_auth "${slot}")"
    unit="/etc/systemd/system/$(service_for_slot "${slot}")"
    cat >"${unit}" <<EOF
[Unit]
Description=CLIProxyAPI Plus (${slot})
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=cliproxy
Group=cliproxy
WorkingDirectory=${INSTALL_DIR}
Environment=CLIPROXY_PORT_OVERRIDE=${port}
Environment=CLIPROXY_AUTH_DIR_OVERRIDE=${auth_dir}
EnvironmentFile=-${CONTROL_ENV_FILE}
Environment=CLIPROXY_DEPLOY_CONTROL_TOKEN_FILE=${CONTROL_TOKEN_FILE}
ExecStart=$(slot_binary "${slot}") --config ${CONFIG_FILE}
Restart=on-failure
RestartSec=5
TimeoutStopSec=35
KillSignal=SIGTERM
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${STATE_DIR}/slots/${slot} /var/lib/cliproxy/logs

[Install]
WantedBy=multi-user.target
EOF
  done
  systemctl daemon-reload
}

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

wait_ready() {
  local service="$1" port="$2"
  for _ in $(seq 1 60); do
    if systemctl is-active --quiet "${service}" && curl -fsS "http://127.0.0.1:${port}/readyz" >/dev/null; then
      return 0
    fi
    sleep 1
  done
  return 1
}

check_plugins() {
  local service="$1" pid
  pid="$(systemctl show -p MainPID --value "${service}")"
  if [[ -n "${pid}" ]] && journalctl -u "${service}" "_PID=${pid}" --no-pager | grep -q 'failed to load plugin'; then
    return 1
  fi
  return 0
}

wait_for_drain() {
  local service="$1" port="$2" deadline status active_requests active_websockets
  deadline=$((SECONDS + DRAIN_TIMEOUT))
  while (( SECONDS < deadline )); do
    if ! systemctl is-active --quiet "${service}"; then
      return 0
    fi
    if ! status="$(control_curl GET "${port}" /v0/deployment/status 2>/dev/null)"; then
      sleep "${DRAIN_POLL_INTERVAL}"
      continue
    fi
    read -r active_requests active_websockets < <(python3 -c 'import json,sys; x=json.load(sys.stdin); print(x.get("active_requests", 0), x.get("active_websockets", 0))' <<<"${status}")
    log "draining ${service}: active_requests=${active_requests} active_websockets=${active_websockets}"
    if [[ "${active_requests}" == "0" && "${active_websockets}" == "0" ]]; then
      return 0
    fi
    sleep "${DRAIN_POLL_INTERVAL}"
  done
  return 1
}

if [[ "${EUID}" -ne 0 ]]; then
  fail "run this script as root"
fi

for command_name in curl git tar gcc systemctl flock nginx python3 sha256sum ss; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "missing required command: ${command_name}"
done
[[ "${DRAIN_TIMEOUT}" =~ ^[0-9]+$ ]] || fail "CLIPROXY_DEPLOY_DRAIN_TIMEOUT must be an integer"
[[ "${DRAIN_POLL_INTERVAL}" =~ ^[0-9]+$ ]] || fail "CLIPROXY_DEPLOY_DRAIN_POLL_INTERVAL must be an integer"
getent passwd cliproxy >/dev/null || fail "missing cliproxy user"

install -d -m 0755 "${STATE_DIR}" "${STATE_DIR}/toolchains" "${GOCACHE}" "${GOMODCACHE}" "${RELEASE_DIR}" "${SLOTS_DIR}" "${INSTALL_DIR}/bin"
install -d -m 0750 -o cliproxy -g cliproxy "${STATE_DIR}/slots" "${RUN_DIR}"
exec 9>"${LOCK_FILE}"
flock -n 9 || fail "another deployment is already running"

ensure_go
sync_source
install -m 0755 "${SOURCE_DIR}/scripts/cliproxy-merge-state.py" "${MERGE_SCRIPT}"
install -m 0755 "${SOURCE_DIR}/scripts/cliproxy-server-deploy.sh" "${DEPLOY_SCRIPT_TARGET}.new"
mv -f "${DEPLOY_SCRIPT_TARGET}.new" "${DEPLOY_SCRIPT_TARGET}"

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

if [[ -f "${ACTIVE_SLOT_FILE}" ]]; then
  ACTIVE_SLOT="$(tr -d '[:space:]' <"${ACTIVE_SLOT_FILE}")"
else
  ACTIVE_SLOT="blue"
fi
[[ "${ACTIVE_SLOT}" == "blue" || "${ACTIVE_SLOT}" == "green" ]] || fail "invalid active slot: ${ACTIVE_SLOT}"
NEXT_SLOT="$(other_slot "${ACTIVE_SLOT}")"
ACTIVE_SERVICE="$(service_for_slot "${ACTIVE_SLOT}")"
NEXT_SERVICE="$(service_for_slot "${NEXT_SLOT}")"
ACTIVE_PORT="$(port_for_slot "${ACTIVE_SLOT}")"
NEXT_PORT="$(port_for_slot "${NEXT_SLOT}")"
ACTIVE_AUTH="$(slot_auth "${ACTIVE_SLOT}")"
NEXT_AUTH="$(slot_auth "${NEXT_SLOT}")"
ACTIVE_BASE="$(slot_base "${ACTIVE_SLOT}")"
NEXT_BASE="$(slot_base "${NEXT_SLOT}")"
LEGACY_MIGRATION=0
if systemctl list-unit-files cliproxy.service >/dev/null 2>&1 && systemctl is-active --quiet cliproxy.service; then
  LEGACY_MIGRATION=1
fi

if [[ "${LEGACY_MIGRATION}" -eq 0 && ! -d "${ACTIVE_AUTH}" ]]; then
  log "initializing ${ACTIVE_SLOT} state from configured auth directory"
  config_auth_dir="$(awk -F: '/^[[:space:]]*auth-dir[[:space:]]*:/ {sub(/^[^:]*:[[:space:]]*/, ""); gsub(/[\"\047]/, ""); print; exit}' "${CONFIG_FILE}")"
  [[ -n "${config_auth_dir}" && -d "${config_auth_dir}" ]] || fail "cannot resolve existing auth-dir from ${CONFIG_FILE}"
  copy_tree "${config_auth_dir}" "${ACTIVE_AUTH}"
fi

if [[ "${LEGACY_MIGRATION}" -eq 1 ]]; then
  config_auth_dir="$(awk -F: '/^[[:space:]]*auth-dir[[:space:]]*:/ {sub(/^[^:]*:[[:space:]]*/, ""); gsub(/[\"\047]/, ""); print; exit}' "${CONFIG_FILE}")"
  [[ -n "${config_auth_dir}" && -d "${config_auth_dir}" ]] || fail "cannot resolve existing auth-dir from ${CONFIG_FILE}"
  ACTIVE_AUTH="${config_auth_dir}"
fi

if [[ ! -f "$(slot_binary "${ACTIVE_SLOT}")" ]]; then
  if [[ -x "${INSTALL_DIR}/cli-proxy-api" ]]; then
    install -d -m 0755 "${SLOTS_DIR}/${ACTIVE_SLOT}"
    install -m 0755 "${INSTALL_DIR}/cli-proxy-api" "$(slot_binary "${ACTIVE_SLOT}")"
  else
    install -d -m 0755 "${SLOTS_DIR}/${ACTIVE_SLOT}"
    install -m 0755 "${BUILD_OUTPUT}" "$(slot_binary "${ACTIVE_SLOT}")"
  fi
fi

if [[ -x "$(slot_binary "${NEXT_SLOT}")" ]]; then
  cp -a "$(slot_binary "${NEXT_SLOT}")" "${BACKUP}"
fi

if [[ -f "${CONTROL_TOKEN_FILE}" ]]; then
  CONTROL_TOKEN="$(tr -d '\r\n' <"${CONTROL_TOKEN_FILE}")"
else
  CONTROL_TOKEN="$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
fi
[[ -n "${CONTROL_TOKEN}" ]] || fail "failed to create deployment control token"
printf '%s\n' "${CONTROL_TOKEN}" >"${CONTROL_TOKEN_FILE}"
printf 'CLIPROXY_DEPLOY_CONTROL_TOKEN_FILE=%s\n' "${CONTROL_TOKEN_FILE}" >"${CONTROL_ENV_FILE}"
chown root:cliproxy "${CONTROL_TOKEN_FILE}"
chown root:cliproxy "${CONTROL_ENV_FILE}"
chmod 0640 "${CONTROL_TOKEN_FILE}"
chmod 0640 "${CONTROL_ENV_FILE}"

if [[ "${LEGACY_MIGRATION}" -eq 1 ]]; then
  log "preparing the first blue-green slot from a live legacy snapshot"
  copy_tree "${ACTIVE_AUTH}" "${NEXT_AUTH}"
elif [[ -d "${NEXT_BASE}" && -d "${NEXT_AUTH}" ]]; then
  STALE_NEXT_AUTH="$(slot_state "${NEXT_SLOT}")/drained-auths"
  copy_tree "${NEXT_AUTH}" "${STALE_NEXT_AUTH}"
  copy_tree "${ACTIVE_AUTH}" "${NEXT_AUTH}"
  log "merging drained ${NEXT_SLOT} deltas into the next state clone"
  "${MERGE_SCRIPT}" "${NEXT_BASE}" "${STALE_NEXT_AUTH}" "${NEXT_AUTH}"
  rm -rf "${STALE_NEXT_AUTH}"
else
  copy_tree "${ACTIVE_AUTH}" "${NEXT_AUTH}"
fi

# Preserve the old active slot's exact cutover baseline. Its final drained
# state remains in that slot and is merged the next time the slot is reused.
copy_tree "${ACTIVE_AUTH}" "${ACTIVE_BASE}"
install -d -m 0755 "${SLOTS_DIR}/${NEXT_SLOT}"
install -m 0755 "${BUILD_OUTPUT}" "$(slot_binary "${NEXT_SLOT}").new"
mv -f "$(slot_binary "${NEXT_SLOT}").new" "$(slot_binary "${NEXT_SLOT}")"
chown root:root "$(slot_binary "${NEXT_SLOT}")"

ensure_systemd_units
if [[ "${LEGACY_MIGRATION}" -eq 0 ]] && ! systemctl is-active --quiet "${ACTIVE_SERVICE}"; then
  systemctl start "${ACTIVE_SERVICE}"
  wait_ready "${ACTIVE_SERVICE}" "${ACTIVE_PORT}" || fail "active slot failed to start during migration"
fi

write_nginx_upstream "${ACTIVE_SLOT}"
ensure_nginx_site
nginx -t
systemctl reload nginx

if [[ "${LEGACY_MIGRATION}" -eq 1 ]]; then
  log "legacy service remains active as the blue slot until cutover"
fi

log "starting inactive slot ${NEXT_SLOT} on ${NEXT_PORT}"
systemctl start "${NEXT_SERVICE}"
if ! wait_ready "${NEXT_SERVICE}" "${NEXT_PORT}"; then
  systemctl stop "${NEXT_SERVICE}" || true
  fail "new slot health check failed"
fi
if ! check_plugins "${NEXT_SERVICE}"; then
  systemctl stop "${NEXT_SERVICE}" || true
  fail "new process reported a plugin load failure"
fi

log "switching nginx to ${NEXT_SLOT}"
write_nginx_upstream "${NEXT_SLOT}"
if ! nginx -t; then
  write_nginx_upstream "${ACTIVE_SLOT}"
  nginx -t || true
  systemctl stop "${NEXT_SERVICE}" || true
  fail "nginx validation failed"
fi
systemctl reload nginx
if ! curl -fsS "http://127.0.0.1:${NEXT_PORT}/readyz" >/dev/null || ! curl -fsS "http://127.0.0.1:8317/readyz" >/dev/null; then
  rollback_nginx "${ACTIVE_SLOT}" "${ACTIVE_SERVICE}" "${ACTIVE_PORT}"
  systemctl stop "${NEXT_SERVICE}" || true
  fail "post-cutover health check failed"
fi
if ! curl -fsS -H 'Upgrade: websocket' -H 'Connection: upgrade' "http://127.0.0.1:8317/healthz" >/dev/null; then
  rollback_nginx "${ACTIVE_SLOT}" "${ACTIVE_SERVICE}" "${ACTIVE_PORT}"
  systemctl stop "${NEXT_SERVICE}" || true
  fail "nginx websocket header path check failed"
fi
printf '%s\n' "${NEXT_SLOT}" >"${ACTIVE_SLOT_FILE}.new"
mv -f "${ACTIVE_SLOT_FILE}.new" "${ACTIVE_SLOT_FILE}"

log "draining previous slot ${ACTIVE_SLOT}"
if [[ "${LEGACY_MIGRATION}" -eq 1 ]]; then
  LEGACY_DRAIN_TIMEOUT="${CLIPROXY_DEPLOY_LEGACY_DRAIN_TIMEOUT:-120}"
  [[ "${LEGACY_DRAIN_TIMEOUT}" =~ ^[0-9]+$ ]] || fail "CLIPROXY_DEPLOY_LEGACY_DRAIN_TIMEOUT must be an integer"
  log "legacy process has no drain endpoint; preserving existing connections for ${LEGACY_DRAIN_TIMEOUT}s before migration stop"
  legacy_deadline=$((SECONDS + LEGACY_DRAIN_TIMEOUT))
  while (( SECONDS < legacy_deadline )); do
    if ! ss -Htn state established "( sport = :${ACTIVE_PORT} )" | grep -q .; then
      break
    fi
    sleep "${DRAIN_POLL_INTERVAL}"
  done
  systemctl stop cliproxy.service || true
  systemctl disable cliproxy.service >/dev/null 2>&1 || true
  copy_tree "${ACTIVE_AUTH}" "$(slot_auth "${ACTIVE_SLOT}")"
else
  if ! control_curl POST "${ACTIVE_PORT}" /v0/deployment/drain >/dev/null; then
    log "warning: old slot did not accept drain command; it will be stopped after the drain timeout"
  fi
  if wait_for_drain "${ACTIVE_SERVICE}" "${ACTIVE_PORT}"; then
    log "old slot drained cleanly"
  else
    log "drain timeout reached after ${DRAIN_TIMEOUT}s; stopping old slot"
  fi
  systemctl stop "${ACTIVE_SERVICE}" || true
fi

printf '%s\n' "${SOURCE_COMMIT}" >"${STATE_DIR}/deployed-commit"
sha256sum "$(slot_binary "${NEXT_SLOT}")" >"${STATE_DIR}/deployed-sha256"
rm -f "${BUILD_OUTPUT}"

if [[ "${KEEP_RELEASES}" =~ ^[0-9]+$ ]]; then
  mapfile -t old_releases < <(find "${RELEASE_DIR}" -maxdepth 1 -type f -name 'cli-proxy-api-*' -printf '%T@ %p\n' | sort -nr | awk -v keep="${KEEP_RELEASES}" 'NR > keep { sub(/^[^ ]+ /, ""); print }')
  if [[ "${#old_releases[@]}" -gt 0 ]]; then
    rm -f -- "${old_releases[@]}"
  fi
fi

log "deployed ${SHORT_COMMIT} to ${NEXT_SLOT}; health and plugin checks passed"
systemctl --no-pager --full --lines=5 status "${NEXT_SERVICE}"
