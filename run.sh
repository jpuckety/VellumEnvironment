#!/usr/bin/env bash
#
# run.sh — local build, test, and run wrapper for email-mcp.
#
# AWS deploy, image promotion, and SSM secrets are owned by MCPCICD
# (https://github.com/jpuckety/MCPCICD). This script is for laptop use:
# Go test/vet, local HTTP/stdio servers, docker build, and optional CDK synth.
#
# Usage:
#   ./run.sh <command> [extra args...]
#
# Run "./run.sh help" for the full command list.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="${SCRIPT_DIR}"
APP_DIR="${PROJECT_ROOT}/emailmcp"
WEB_DIR="${PROJECT_ROOT}/web"
CDK_DIR="${PROJECT_ROOT}/cdk"
ENV_FILE="${ENV_FILE:-${APP_DIR}/.env}"
IMAGE_NAME="${IMAGE_NAME:-email-mcp:latest}"

log()  { printf '\033[1;34m[run]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[run]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[run]\033[0m %s\n' "$*" >&2; exit 1; }

require() {
  command -v "$1" >/dev/null 2>&1 || die "Required command '$1' is not installed or not on PATH."
}

load_dotenv() {
  local file="$1"
  [[ -f "${file}" ]] || return 0
  log "Loading environment variables from ${file#${PROJECT_ROOT}/}"
  local line key value
  while IFS= read -r line || [[ -n "${line}" ]]; do
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [[ -z "${line}" || "${line}" == \#* ]] && continue
    line="${line#export }"
    [[ "${line}" == *=* ]] || continue
    key="${line%%=*}"
    value="${line#*=}"
    key="${key%"${key##*[![:space:]]}"}"
    key="${key#"${key%%[![:space:]]*}"}"
    [[ "${key}" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
    value="${value%"${value##*[![:space:]]}"}"
    value="${value#"${value%%[![:space:]]*}"}"
    if [[ ( "${value}" == \"*\" || "${value}" == \'*\' ) && ${#value} -ge 2 ]]; then
      value="${value:1:${#value}-2}"
    fi
    if [[ -z "${!key:-}" ]]; then
      export "${key}=${value}"
    fi
  done < "${file}"
}

go_in_app() {
  require go
  ( cd "${APP_DIR}" && go "$@" )
}

TEST_ISOLATED_VARS=(
  GOOGLE_CLIENT_ID GOOGLE_CLIENT_SECRET PUBLIC_BASE_URL APPLICATION_ID
  EMAILMCP_SESSION_TABLE EMAILMCP_USER_CONFIG_TABLE OAUTH_REDIRECT_ALLOWLIST
)

cmd_test() {
  require go
  cmd_build_web
  (
    cd "${APP_DIR}"
    local var
    for var in "${TEST_ISOLATED_VARS[@]}"; do
      unset "${var}"
    done
    go test ./... "$@"
  )
}

cmd_vet() {
  go_in_app vet ./... "$@"
}

cmd_build_web() {
  if command -v npm >/dev/null 2>&1 && [[ -d "${WEB_DIR}" ]]; then
    log "Building Angular frontend in ${WEB_DIR}..."
    ( cd "${WEB_DIR}" && [[ -d node_modules ]] || npm install --no-audit --no-fund )
    ( cd "${WEB_DIR}" && npm run build )
  else
    log "Skipping web build (npm not available or web/ missing)."
  fi
}

cmd_build() {
  cmd_build_web
  go_in_app build -o emailmcp ./cmd/emailmcp "$@"
}

cmd_check() {
  cmd_test "$@"
  cmd_vet
  cmd_build
}

cmd_clean() {
  rm -f "${APP_DIR}/emailmcp" "${APP_DIR}/emailmcp-x86_64"
  log "Removed local Go binaries."
}

cmd_run() {
  require go
  log "Starting HTTP MCP server from ${APP_DIR}..."
  ( cd "${APP_DIR}" && go run ./cmd/emailmcp "$@" )
}

cmd_run_stdio() {
  require go
  log "Starting stdio MCP server from ${APP_DIR}..."
  ( cd "${APP_DIR}" && go run ./cmd/emailmcp --transport stdio "$@" )
}

cmd_docker_build() {
  require docker
  log "Building container image '${IMAGE_NAME}' from Dockerfile..."
  ( cd "${PROJECT_ROOT}" && docker build -t "${IMAGE_NAME}" -f Dockerfile . "$@" )
  log "Built image: ${IMAGE_NAME}"
}

cdk_run() {
  require npx
  [[ -d "${CDK_DIR}/node_modules" ]] || cmd_cdk_install
  ( cd "${CDK_DIR}" && npx cdk "$@" )
}

cmd_cdk_install() {
  require npm
  log "Installing CDK dependencies..."
  ( cd "${CDK_DIR}" && npm install --no-audit --no-fund )
}

cmd_synth() {
  log "Synthesizing the CloudFormation template..."
  cdk_run synth "$@"
}

usage() {
  cat <<'EOF'
run.sh — local build, test, and run wrapper for email-mcp

Usage: ./run.sh <command> [extra args...]

AWS deploy, image promotion, and SSM secrets live in MCPCICD, not here.

Build & test:
  test             go test ./... in emailmcp/ (runtime secrets unset).
  vet              go vet ./...
  build            Build the native binary in emailmcp/.
  check            test + vet + build.
  clean            Remove local Go binaries.

Run locally:
  run              Run the HTTP MCP server on :8080.
  run-stdio        Run the stdio MCP server (logs to stderr).

Container:
  docker-build     Build the container image from the repo-root Dockerfile
                   (IMAGE_NAME env, default email-mcp:latest).

CDK (local check only):
  cdk-install      Install CDK Node dependencies (cdk/).
  synth            Synthesize the CloudFormation template.

Environment overrides:
  IMAGE_NAME        Docker image tag for docker-build (default email-mcp:latest).
  ENV_FILE          Path to the KEY=VALUE file (default: emailmcp/.env).

Configuration file:
  emailmcp/.env     Optional, git-ignored KEY=VALUE file loaded automatically.
                    Values already set in the environment take precedence.
                    Pipeline / ECS do not read this file.

Any extra args after the command are forwarded to the underlying tool.
EOF
}

main() {
  [[ -d "${APP_DIR}" && -f "${APP_DIR}/go.mod" ]] || die "This script must be run from the EmailMCP project root."
  load_dotenv "${ENV_FILE}"

  local cmd="${1:-help}"
  [[ $# -gt 0 ]] && shift || true

  case "${cmd}" in
    test)                          cmd_test "$@" ;;
    vet)                           cmd_vet "$@" ;;
    build)                         cmd_build "$@" ;;
    build-web|build_web)           cmd_build_web "$@" ;;
    check)                         cmd_check "$@" ;;
    clean)                         cmd_clean "$@" ;;
    run|run-http|run_http)         cmd_run "$@" ;;
    run-stdio|run_stdio)           cmd_run_stdio "$@" ;;
    docker-build|docker_build)     cmd_docker_build "$@" ;;
    cdk-install|cdk_install)       cmd_cdk_install "$@" ;;
    synth)                         cmd_synth "$@" ;;
    help|-h|--help)                usage ;;
    eks-deploy|undeploy-eks|eks-refresh|eks-status|eks-logs|infra-deploy|infra-destroy|build-push|deploy|undeploy|destroy)
      die "'${cmd}' was removed from this repo. Use MCPCICD for AWS secrets, bootstrap, and deploy."
      ;;
    *) warn "Unknown command: ${cmd}"; echo; usage; exit 2 ;;
  esac
}

main "$@"
