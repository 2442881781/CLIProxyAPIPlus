#!/usr/bin/env bash

set -euo pipefail

REPOSITORY_URL="${CLIPROXY_DEPLOY_REPOSITORY:-https://github.com/2442881781/CLIProxyAPIPlus.git}"
DEPLOY_REF="${1:-${CLIPROXY_DEPLOY_REF:-deploy-jp}}"
GO_VERSION="${CLIPROXY_DEPLOY_GO_VERSION:-1.26.0}"
STATE_DIR="${CLIPROXY_DEPLOY_STATE_DIR:-/var/lib/cliproxy/deploy}"
INSTALL_DIR="${CLIPROXY_DEPLOY_INSTALL_DIR:-/opt/cliproxy}"
LEGACY_CONFIG_FILE="${CLIPROXY_DEPLOY_LEGACY_CONFIG:-/etc/cliproxy/config.yaml}"
CONFIG_FILE="${CLIPROXY_DEPLOY_CONFIG:-/var/lib/cliproxy/config/config.yaml}"
CONFIG_DIR="$(dirname "${CONFIG_FILE}")"
CONFIG_BACKUP_DIR="${STATE_DIR}/config-backups"
NGINX_SITE="${CLIPROXY_DEPLOY_NGINX_SITE:-/etc/nginx/sites-available/cliproxy}"
NGINX_UPSTREAM_FILE="${CLIPROXY_DEPLOY_NGINX_UPSTREAM_FILE:-/etc/nginx/conf.d/cliproxy-upstream.conf}"
SERVICE_PREFIX="${CLIPROXY_DEPLOY_SERVICE_PREFIX:-cliproxy}"
KEEP_RELEASES="${CLIPROXY_DEPLOY_KEEP_RELEASES:-5}"
KEEP_CONFIG_BACKUPS="${CLIPROXY_DEPLOY_KEEP_CONFIG_BACKUPS:-5}"
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
MANAGEMENT_SOURCE="${SOURCE_DIR}/static/management.html"
MANAGEMENT_DIR="${INSTALL_DIR}/static"
MANAGEMENT_TARGET="${MANAGEMENT_DIR}/management.html"
MANAGEMENT_GZIP_TARGET="${MANAGEMENT_TARGET}.gz"
MANAGEMENT_UI_ACTIVATED=0
MANAGEMENT_UI_HAD_PREVIOUS=0
CONTROL_TOKEN_FILE="${RUN_DIR}/deploy-control-token"
CONTROL_ENV_FILE="${RUN_DIR}/deploy-control.env"
PGSTORE_ENV_FILE="${CLIPROXY_DEPLOY_PGSTORE_ENV:-/etc/cliproxy/pgstore.env}"

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

ensure_runtime_config() {
  install -d -m 0750 -o cliproxy -g cliproxy "${CONFIG_DIR}"
  if [[ ! -e "${CONFIG_FILE}" ]]; then
    [[ -f "${LEGACY_CONFIG_FILE}" ]] || fail "missing initial config: ${LEGACY_CONFIG_FILE}"
    log "initializing writable runtime config from ${LEGACY_CONFIG_FILE}"
    install -m 0640 -o cliproxy -g cliproxy "${LEGACY_CONFIG_FILE}" "${CONFIG_FILE}.new"
    mv -f "${CONFIG_FILE}.new" "${CONFIG_FILE}"
  fi
  [[ -f "${CONFIG_FILE}" && ! -L "${CONFIG_FILE}" ]] || fail "runtime config must be a regular file: ${CONFIG_FILE}"
  chown cliproxy:cliproxy "${CONFIG_FILE}"
  chmod 0640 "${CONFIG_FILE}"
}

backup_runtime_config() {
  local backup stamp
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  backup="${CONFIG_BACKUP_DIR}/config-${stamp}.yaml"
  install -d -m 0700 -o root -g root "${CONFIG_BACKUP_DIR}"
  install -m 0600 -o root -g root "${CONFIG_FILE}" "${backup}"

  mapfile -t old_config_backups < <(find "${CONFIG_BACKUP_DIR}" -maxdepth 1 -type f -name 'config-*.yaml' -printf '%T@ %p\n' | sort -nr | awk -v keep="${KEEP_CONFIG_BACKUPS}" 'NR > keep { sub(/^[^ ]+ /, ""); print }')
  if [[ "${#old_config_backups[@]}" -gt 0 ]]; then
    rm -f -- "${old_config_backups[@]}"
  fi
}

stage_management_ui() {
  [[ -s "${MANAGEMENT_SOURCE}" ]] || fail "deployment snapshot is missing static/management.html"
  install -d -m 0755 "${MANAGEMENT_DIR}"
  install -m 0644 "${MANAGEMENT_SOURCE}" "${MANAGEMENT_TARGET}.new"
  gzip -9 -c "${MANAGEMENT_TARGET}.new" >"${MANAGEMENT_GZIP_TARGET}.new"
  chmod 0644 "${MANAGEMENT_GZIP_TARGET}.new"
}

activate_management_ui() {
  if [[ -f "${MANAGEMENT_TARGET}" ]]; then
    cp -a "${MANAGEMENT_TARGET}" "${MANAGEMENT_BACKUP}" || return 1
    MANAGEMENT_UI_HAD_PREVIOUS=1
  fi
  if [[ -f "${MANAGEMENT_GZIP_TARGET}" ]]; then
    cp -a "${MANAGEMENT_GZIP_TARGET}" "${MANAGEMENT_GZIP_BACKUP}" || return 1
  fi
  MANAGEMENT_UI_ACTIVATED=1
  mv -f "${MANAGEMENT_TARGET}.new" "${MANAGEMENT_TARGET}" || return 1
  mv -f "${MANAGEMENT_GZIP_TARGET}.new" "${MANAGEMENT_GZIP_TARGET}" || return 1
}

restore_management_ui() {
  [[ "${MANAGEMENT_UI_ACTIVATED}" -eq 1 ]] || return 0
  if [[ "${MANAGEMENT_UI_HAD_PREVIOUS}" -eq 1 && -f "${MANAGEMENT_BACKUP}" ]]; then
    cp -a "${MANAGEMENT_BACKUP}" "${MANAGEMENT_TARGET}"
    if [[ -f "${MANAGEMENT_GZIP_BACKUP}" ]]; then
      cp -a "${MANAGEMENT_GZIP_BACKUP}" "${MANAGEMENT_GZIP_TARGET}"
    else
      rm -f "${MANAGEMENT_GZIP_TARGET}"
    fi
  else
    rm -f "${MANAGEMENT_TARGET}" "${MANAGEMENT_GZIP_TARGET}"
  fi
  MANAGEMENT_UI_ACTIVATED=0
}

rollback_nginx() {
  local old_slot="$1" old_service="$2" old_port="$3"
  restore_management_ui
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
	storage_environment="Environment=CLIPROXY_AUTH_DIR_OVERRIDE=${auth_dir}"
	if [[ -f "${PGSTORE_ENV_FILE}" ]]; then
	  storage_environment="EnvironmentFile=-${PGSTORE_ENV_FILE}
Environment=PGSTORE_LOCAL_PATH=$(slot_state "${slot}")"
	fi
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
${storage_environment}
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
ReadWritePaths=${STATE_DIR}/slots/${slot} /var/lib/cliproxy/logs ${CONFIG_DIR}

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
  if [[ -n "${pid}" ]] && journalctl -u "${service}" "_PID=${pid}" --no-pager | grep -Eq 'failed to load plugin|plugin\.register failed|returned invalid metadata or no capabilities'; then
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

for command_name in curl dirname find git tar gcc gzip systemctl flock nginx python3 sha256sum ss; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "missing required command: ${command_name}"
done
[[ "${DRAIN_TIMEOUT}" =~ ^[0-9]+$ ]] || fail "CLIPROXY_DEPLOY_DRAIN_TIMEOUT must be an integer"
[[ "${DRAIN_POLL_INTERVAL}" =~ ^[0-9]+$ ]] || fail "CLIPROXY_DEPLOY_DRAIN_POLL_INTERVAL must be an integer"
[[ "${KEEP_CONFIG_BACKUPS}" =~ ^[0-9]+$ ]] || fail "CLIPROXY_DEPLOY_KEEP_CONFIG_BACKUPS must be an integer"
getent passwd cliproxy >/dev/null || fail "missing cliproxy user"

install -d -m 0755 "${STATE_DIR}" "${STATE_DIR}/toolchains" "${GOCACHE}" "${GOMODCACHE}" "${RELEASE_DIR}" "${SLOTS_DIR}" "${INSTALL_DIR}/bin"
install -d -m 0750 -o cliproxy -g cliproxy "${STATE_DIR}/slots" "${RUN_DIR}"
exec 9>"${LOCK_FILE}"
flock -n 9 || fail "another deployment is already running"

ensure_runtime_config
backup_runtime_config

ensure_go
sync_source
install -m 0755 "${SOURCE_DIR}/scripts/cliproxy-merge-state.py" "${MERGE_SCRIPT}"
install -m 0755 "${SOURCE_DIR}/scripts/cliproxy-server-deploy.sh" "${DEPLOY_SCRIPT_TARGET}.new"
mv -f "${DEPLOY_SCRIPT_TARGET}.new" "${DEPLOY_SCRIPT_TARGET}"

SOURCE_COMMIT="$(source_commit)"
SHORT_COMMIT="${SOURCE_COMMIT:0:8}"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
BUILD_OUTPUT="${STATE_DIR}/cli-proxy-api-${SHORT_COMMIT}.new"
RELEASE_STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP="${RELEASE_DIR}/cli-proxy-api-${RELEASE_STAMP}"
MANAGEMENT_BACKUP="${RELEASE_DIR}/management-${RELEASE_STAMP}.html"
MANAGEMENT_GZIP_BACKUP="${MANAGEMENT_BACKUP}.gz"

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
stage_management_ui

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

if [[ "${LEGACY_MIGRATION}" -eq 0 && ! -d "${ACTIVE_AUTH}" && ! -f "${PGSTORE_ENV_FILE}" ]]; then
  log "initializing ${ACTIVE_SLOT} state from configured auth directory"
  config_auth_dir="$(awk -F: '/^[[:space:]]*auth-dir[[:space:]]*:/ {sub(/^[^:]*:[[:space:]]*/, ""); gsub(/[\"\047]/, ""); print; exit}' "${CONFIG_FILE}")"
  [[ -n "${config_auth_dir}" && -d "${config_auth_dir}" ]] || fail "cannot resolve existing auth-dir from ${CONFIG_FILE}"
  copy_tree "${config_auth_dir}" "${ACTIVE_AUTH}"
fi

if [[ -f "${PGSTORE_ENV_FILE}" ]]; then
  install -d -m 0750 -o cliproxy -g cliproxy "$(slot_state "${ACTIVE_SLOT}")/pgstore/auths"
  if [[ ! -e "$(slot_state "${ACTIVE_SLOT}")/pgstore/config/config.yaml" ]]; then
    install -d -m 0750 -o cliproxy -g cliproxy "$(slot_state "${ACTIVE_SLOT}")/pgstore/config"
    install -m 0600 -o cliproxy -g cliproxy "${CONFIG_FILE}" "$(slot_state "${ACTIVE_SLOT}")/pgstore/config/config.yaml"
  fi
  if [[ -d "${ACTIVE_AUTH}" ]] && ! find "$(slot_state "${ACTIVE_SLOT}")/pgstore/auths" -mindepth 1 -print -quit | grep -q .; then
    cp -a "${ACTIVE_AUTH}/." "$(slot_state "${ACTIVE_SLOT}")/pgstore/auths/"
    chown -R cliproxy:cliproxy "$(slot_state "${ACTIVE_SLOT}")/pgstore"
  fi
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
elif [[ -f "${PGSTORE_ENV_FILE}" ]]; then
  log "PostgreSQL store enabled; preparing an isolated spool for ${NEXT_SLOT}"
  install -d -m 0750 -o cliproxy -g cliproxy "$(slot_state "${NEXT_SLOT}")/pgstore"
  if [[ -d "$(slot_state "${ACTIVE_SLOT}")/pgstore" ]]; then
    copy_tree "$(slot_state "${ACTIVE_SLOT}")/pgstore" "$(slot_state "${NEXT_SLOT}")/pgstore"
  fi
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

if [[ ! -f "${PGSTORE_ENV_FILE}" ]]; then
  chown -R cliproxy:cliproxy "${NEXT_AUTH}"
  find "${NEXT_AUTH}" -type d -exec chmod 0750 {} +
  find "${NEXT_AUTH}" -type f -exec chmod 0600 {} +
fi

# Preserve the old active slot's exact cutover baseline for file-backed auth.
# PostgreSQL-backed slots use the database as the authoritative state.
if [[ ! -f "${PGSTORE_ENV_FILE}" ]]; then
  copy_tree "${ACTIVE_AUTH}" "${ACTIVE_BASE}"
fi
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
if ! activate_management_ui; then
  rollback_nginx "${ACTIVE_SLOT}" "${ACTIVE_SERVICE}" "${ACTIVE_PORT}"
  systemctl stop "${NEXT_SERVICE}" || true
  fail "management UI activation failed"
fi
if ! curl -fsS "http://127.0.0.1:8317/management.html" >/dev/null; then
  rollback_nginx "${ACTIVE_SLOT}" "${ACTIVE_SERVICE}" "${ACTIVE_PORT}"
  systemctl stop "${NEXT_SERVICE}" || true
  fail "management UI health check failed"
fi
printf '%s\n' "${NEXT_SLOT}" >"${ACTIVE_SLOT_FILE}.new"
mv -f "${ACTIVE_SLOT_FILE}.new" "${ACTIVE_SLOT_FILE}"
systemctl enable "${NEXT_SERVICE}" >/dev/null
systemctl disable "${ACTIVE_SERVICE}" >/dev/null 2>&1 || true

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
  mapfile -t old_management_releases < <(find "${RELEASE_DIR}" -maxdepth 1 -type f -name 'management-*.html' -printf '%T@ %p\n' | sort -nr | awk -v keep="${KEEP_RELEASES}" 'NR > keep { sub(/^[^ ]+ /, ""); print }')
  for old_management in "${old_management_releases[@]}"; do
    rm -f -- "${old_management}" "${old_management}.gz"
  done

fi

log "deployed ${SHORT_COMMIT} to ${NEXT_SLOT}; health and plugin checks passed"
systemctl --no-pager --full --lines=5 status "${NEXT_SERVICE}"
