#!/usr/bin/env bash
#
# run.sh - Helper script for common EmailMCP development and runtime tasks.
# See README.md for details on the project.
#
# Usage:
#   ./run.sh <command> [args]
#
# Common commands:
#   build         Build the emailmcp binary
#   install       Install the binary to /usr/local/bin
#   run           Build (if needed) and run the server
#   test          Run go test ./...
#   vet           Run go vet ./...
#   check         Run test + vet + build
#   key           Generate a new 32-byte base64 master key
#   setup         Copy .env.example to .env (if missing)
#   clean         Remove build artifacts and local databases
#   docker-build  Build the Docker image
#   docker-run    Run the Docker image (requires EMAILMCP_MASTER_KEY)
#   help          Show this help message

set -euo pipefail

BINARY_NAME="emailmcp"
APP_DIR="emailmcp"
BINARY_PATH="${APP_DIR}/${BINARY_NAME}"
DB_GLOB="${APP_DIR}/emailmcp.db*"

# Colors for output (if supported)
if [ -t 1 ]; then
  RED='\033[0;31m'
  GREEN='\033[0;32m'
  YELLOW='\033[1;33m'
  BLUE='\033[0;34m'
  NC='\033[0m' # No Color
else
  RED=''
  GREEN=''
  YELLOW=''
  BLUE=''
  NC=''
fi

log_info()    { echo -e "${BLUE}[INFO]${NC} $*"; }
log_success() { echo -e "${GREEN}[OK]${NC}   $*"; }
log_warn()    { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error()   { echo -e "${RED}[ERROR]${NC} $*" >&2; }

usage() {
  cat <<EOF
EmailMCP run helper

Usage: ./run.sh <command> [options]

Commands:
  build           Build the ${BINARY_NAME} binary
  install         Build and install the binary and wrapper script
  run             Run the MCP server (loads .env if present)
                  [--transport http|stdio] Default depends on config.
  test            Run all tests (go test ./...)
  vet             Run go vet ./...
  check           Run test + vet + build
  key             Generate a new master encryption key
  setup           Prepare .env from .env.example
  clean           Remove binary, DB files, and other artifacts
  docker-build    Build Docker image as 'emailmcp'
  docker-run      Run Docker container (port 8080)
  help            Show this message

Examples:
  ./run.sh setup
  ./run.sh key
  ./run.sh build
  ./run.sh run
  ./run.sh run --transport stdio
  EMAILMCP_MASTER_KEY=xxx ./run.sh run
  ./run.sh docker-build
  ./run.sh docker-run
EOF
}

# Ensure we are in the project root
if [[ ! -d "${APP_DIR}" || ! -f "${APP_DIR}/go.mod" ]]; then
  log_error "This script must be run from the EmailMCP project root."
  exit 1
fi

load_env() {
  if [[ -f "${APP_DIR}/.env" ]]; then
    log_info "Loading environment from ${APP_DIR}/.env"
    # Export variables from .env (simple parser, ignores comments and empty lines)
    set -a
    # shellcheck disable=SC1091
    source "${APP_DIR}/.env"
    set +a
  fi
}

require_master_key() {
  if [[ -z "${EMAILMCP_MASTER_KEY:-}" ]]; then
    log_error "EMAILMCP_MASTER_KEY is not set."
    log_info "Generate one with: ./run.sh key"
    log_info "Or export it: export EMAILMCP_MASTER_KEY=..."
    log_info "Or put it in a .env file and use ./run.sh run"
    exit 1
  fi
}

cmd_build() {
  log_info "Building ${BINARY_NAME}..."
  (cd "${APP_DIR}" && go build -o "${BINARY_NAME}" ./cmd/emailmcp)
  log_success "Built ${BINARY_PATH}"
}

cmd_install() {
  cmd_build
  log_info "Installing ${BINARY_NAME} binary to /usr/local/bin/${BINARY_NAME}-bin..."
  if sudo install -m 755 "${BINARY_PATH}" /usr/local/bin/"${BINARY_NAME}-bin"; then
    log_success "Successfully installed ${BINARY_NAME}-bin"
  else
    log_error "Failed to install ${BINARY_NAME}-bin"
    exit 1
  fi

  log_info "Installing wrapper script to /usr/local/bin/${BINARY_NAME}..."
  if sudo install -m 755 "${APP_DIR}/emailmcp-wrapper.sh" /usr/local/bin/"${BINARY_NAME}"; then
    log_success "Successfully installed ${BINARY_NAME} wrapper"
  else
    log_error "Failed to install ${BINARY_NAME} wrapper"
    exit 1
  fi

}

cmd_run() {
  load_env
  require_master_key

  if [[ ! -x "${BINARY_PATH}" ]]; then
    log_warn "Binary not found or not executable. Building first..."
    cmd_build
  fi

  log_info "Starting EmailMCP server..."
  log_info "Listen address: ${EMAILMCP_LISTEN_ADDR:-:8080}"
  log_info "Database:       ${EMAILMCP_DB_PATH:-./emailmcp.db}"
  echo

  exec "${BINARY_PATH}" "$@"
}

cmd_test() {
  log_info "Running tests..."
  (cd "${APP_DIR}" && go test ./...)
  log_success "Tests passed"
}

cmd_vet() {
  log_info "Running go vet..."
  (cd "${APP_DIR}" && go vet ./...)
  log_success "Vet passed"
}

cmd_check() {
  cmd_test
  cmd_vet
  cmd_build
  log_success "All checks passed"
}

cmd_key() {
  if ! command -v openssl >/dev/null 2>&1; then
    log_error "openssl is required to generate a key."
    log_info "You can generate one manually with: head -c 32 /dev/urandom | base64"
    exit 1
  fi

  log_info "Generating new 32-byte base64 master key..."
  key=$(openssl rand -base64 32)

  echo
  echo "=========================================="
  echo "EMAILMCP_MASTER_KEY=${key}"
  echo "=========================================="
  echo
  log_success "Copy the value above into your .env file or export it."
  log_warn "Keep this key secret. It is used to encrypt all credentials."
}

cmd_setup() {
  if [[ -f "${APP_DIR}/.env" ]]; then
    log_warn "${APP_DIR}/.env already exists. Not overwriting."
    log_info "You can edit it manually or remove it and run setup again."
    return 0
  fi

  if [[ ! -f "${APP_DIR}/.env.example" ]]; then
    log_error "${APP_DIR}/.env.example not found."
    exit 1
  fi

  cp "${APP_DIR}/.env.example" "${APP_DIR}/.env"
  log_success "Created ${APP_DIR}/.env from ${APP_DIR}/.env.example"
  log_info "Edit ${APP_DIR}/.env and set a real EMAILMCP_MASTER_KEY (use ./run.sh key)"
}

cmd_clean() {
  log_info "Cleaning build artifacts and local databases..."

  if [[ -f "${BINARY_PATH}" ]]; then
    rm -f "${BINARY_PATH}"
    log_success "Removed ${BINARY_PATH}"
  fi

  # Remove database files (including WAL files)
  if compgen -G "${DB_GLOB}" > /dev/null; then
    rm -f ${DB_GLOB}
    log_success "Removed database files matching ${DB_GLOB}"
  fi

  # Remove common temp files
  rm -f *.out 2>/dev/null || true

  log_success "Clean complete"
}

cmd_docker_build() {
  log_info "Building Docker image 'emailmcp'..."
  docker build -t emailmcp "${APP_DIR}"
  log_success "Docker image 'emailmcp' built"
}

cmd_docker_compose() {
  log_info "Running Docker container..."
  docker compose up -d
}

main() {
  if [[ $# -eq 0 ]]; then
    usage
    exit 0
  fi

  local cmd="$1"
  shift || true

  case "$cmd" in
    build)
      cmd_build "$@"
      ;;
    install)
      cmd_install "$@"
      ;;
    run)
      cmd_run "$@"
      ;;
    test)
      cmd_test "$@"
      ;;
    vet)
      cmd_vet "$@"
      ;;
    check)
      cmd_check "$@"
      ;;
    key)
      cmd_key "$@"
      ;;
    setup)
      cmd_setup "$@"
      ;;
    clean)
      cmd_clean "$@"
      ;;
    docker-build|docker_build)
      cmd_docker_build "$@"
      ;;
    docker-run|docker_run|docker-compose|docker_compose)
      cmd_docker_compose "$@"
      ;;
    help|-h|--help)
      usage
      ;;
    *)
      log_error "Unknown command: $cmd"
      echo
      usage
      exit 1
      ;;
  esac
}

main "$@"
