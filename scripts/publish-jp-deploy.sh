#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REMOTE="${CLIPROXY_DEPLOY_REMOTE:-github-deploy}"
REMOTE_BRANCH="${CLIPROXY_DEPLOY_BRANCH:-deploy-jp}"
REMOTE_BASE_BRANCH="${CLIPROXY_DEPLOY_BASE_BRANCH:-main}"
SOURCE_REF="${1:-HEAD}"
WEB_DIR="${ROOT_DIR}/web/management-center"
MANAGEMENT_OUTPUT="${WEB_DIR}/dist/index.html"

cd "${ROOT_DIR}"

SOURCE_COMMIT="$(git rev-parse "${SOURCE_REF}^{commit}")"
SOURCE_TREE="$(git rev-parse "${SOURCE_COMMIT}^{tree}")"

if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "Error: tracked working-tree changes must be committed before publishing." >&2
  exit 1
fi
if [[ "${SOURCE_COMMIT}" != "$(git rev-parse HEAD)" ]]; then
  echo "Error: frontend deployment assets can only be built from the current HEAD." >&2
  exit 1
fi

if ! command -v bun >/dev/null 2>&1; then
  echo "Error: Bun is required to build the management center." >&2
  exit 1
fi

(
  cd "${WEB_DIR}"
  bun run build
)
[[ -s "${MANAGEMENT_OUTPUT}" ]] || {
  echo "Error: frontend build did not produce dist/index.html." >&2
  exit 1
}

if ! git remote get-url "${REMOTE}" >/dev/null 2>&1; then
  echo "Error: git remote ${REMOTE} is not configured." >&2
  exit 1
fi

# GitHub OAuth credentials may lack workflow scope. Build a deployment commit
# that preserves the destination repository's workflow files while publishing
# the current project's source tree.
git fetch -q "${REMOTE}" "${REMOTE_BASE_BRANCH}"
REMOTE_BASE="$(git rev-parse FETCH_HEAD)"
DEPLOY_PARENT="${REMOTE_BASE}"
if git ls-remote --exit-code --heads "${REMOTE}" "${REMOTE_BRANCH}" >/dev/null 2>&1; then
  git fetch -q "${REMOTE}" "${REMOTE_BRANCH}"
  DEPLOY_PARENT="$(git rev-parse FETCH_HEAD)"
fi
INDEX_FILE="$(mktemp)"
rm -f "${INDEX_FILE}"
cleanup() {
  rm -f "${INDEX_FILE}"
}
trap cleanup EXIT
export GIT_INDEX_FILE="${INDEX_FILE}"

git read-tree "${SOURCE_TREE}"
MANAGEMENT_BLOB="$(git hash-object -w "${MANAGEMENT_OUTPUT}")"
printf '100644 %s\tstatic/management.html\n' "${MANAGEMENT_BLOB}" |
  git update-index --index-info
while IFS= read -r -d '' path; do
  git update-index --force-remove -- "${path}"
done < <(git ls-tree -r -z --name-only "${SOURCE_TREE}" -- .github/workflows)
git ls-tree -r "${REMOTE_BASE}" -- .github/workflows | git update-index --index-info
DEPLOY_TREE="$(git write-tree)"
DEPLOY_COMMIT="$(
  printf 'chore(deploy): publish %s\n\nSource-Commit: %s\n' "${SOURCE_COMMIT:0:8}" "${SOURCE_COMMIT}" |
    git commit-tree "${DEPLOY_TREE}" -p "${DEPLOY_PARENT}"
)"

unset GIT_INDEX_FILE
git push "${REMOTE}" "${DEPLOY_COMMIT}:refs/heads/${REMOTE_BRANCH}"
printf 'Published source %s as %s/%s (%s)\n' \
  "${SOURCE_COMMIT}" "${REMOTE}" "${REMOTE_BRANCH}" "${DEPLOY_COMMIT}"
