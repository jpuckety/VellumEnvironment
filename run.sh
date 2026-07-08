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
BINARY_PATH="./${BINARY_NAME}"
DB_GLOB="emailmcp.db*"

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
  run             Run the MCP server (loads .env if present)
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
  EMAILMCP_MASTER_KEY=xxx ./run.sh run
  ./run.sh docker-build
  ./run.sh docker-run
EOF
}

# Ensure we are in the project root
if [[ ! -f "go.mod" || ! -d "cmd/emailmcp" ]]; then
  log_error "This script must be run from the EmailMCP project root."
  exit 1
fi

load_env() {
  if [[ -f ".env" ]]; then
    log_info "Loading environment from .env"
    # Export variables from .env (simple parser, ignores comments and empty lines)
    set -a
    # shellcheck disable=SC1091
    source .env
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
  go build -o "${BINARY_PATH}" ./cmd/emailmcp
  log_success "Built ${BINARY_PATH}"
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

  exec "${BINARY_PATH}"
}

cmd_test() {
  log_info "Running tests..."
  go test ./...
  log_success "Tests passed"
}

cmd_vet() {
  log_info "Running go vet..."
  go vet ./...
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
  if [[ -f ".env" ]]; then
    log_warn ".env already exists. Not overwriting."
    log_info "You can edit it manually or remove it and run setup again."
    return 0
  fi

  if [[ ! -f ".env.example" ]]; then
    log_error ".env.example not found."
    exit 1
  fi

  cp .env.example .env
  log_success "Created .env from .env.example"
  log_info "Edit .env and set a real EMAILMCP_MASTER_KEY (use ./run.sh key)"
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
  docker build -t emailmcp .
  log_success "Docker image 'emailmcp' built"
}

cmd_docker_run() {
  if [[ -z "${EMAILMCP_MASTER_KEY:-}" ]]; then
    log_error "EMAILMCP_MASTER_KEY must be set when running via Docker."
    log_info "Example:"
    log_info "  EMAILMCP_MASTER_KEY=yourkey ./run.sh docker-run"
    log_info "Or:"
    log_info "  docker run -e EMAILMCP_MASTER_KEY=yourkey -p 8081:8080 emailmcp"
    exit 1
  fi

  log_info "Running Docker container..."
  docker run --rm \
    -e EMAILMCP_MASTER_KEY="${EMAILMCP_MASTER_KEY}" \
    -e EMAILMCP_LISTEN_ADDR="${EMAILMCP_LISTEN_ADDR:-:8080}" \
    -p 8001:8080 \
    emailmcp
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
    docker-run|docker_run)
      cmd_docker_run "$@"
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
