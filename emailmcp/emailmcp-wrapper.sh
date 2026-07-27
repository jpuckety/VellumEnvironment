#!/usr/bin/env bash
# EmailMCP Wrapper Script
# This script loads configuration from ~/.emailmcp and runs the EmailMCP binary.

set -e

CONFIG_FILE="$HOME/.emailmcp"
BINARY="/usr/local/bin/emailmcp-bin"

# Colors for output (to stderr)
if [ -t 2 ]; then
  RED='\033[0;31m'
  BLUE='\033[0;34m'
  NC='\033[0m'
else
  RED=''
  BLUE=''
  NC=''
fi

log_info()  { echo -e "${BLUE}[INFO]${NC}  $*" >&2; }
log_error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

# Load configuration if it exists
if [[ -f "$CONFIG_FILE" ]]; then
  # Export variables from config (simple parser)
  set -a
  # shellcheck disable=SC1090
  source "$CONFIG_FILE"
  set +a
fi

# Ensure mandatory environment variables are set.
# Per-user email account config is read/written directly from DynamoDB; the
# table name is auto-resolved from SSM (or EMAILMCP_USER_CONFIG_TABLE) and is
# therefore not required here.
if [[ -z "${GOOGLE_CLIENT_ID:-}" ]]; then
  log_error "GOOGLE_CLIENT_ID is not set."
  log_info "Please check $CONFIG_FILE or set it in your environment."
  exit 1
fi
# HTTP mode (default) needs OAuth proxy credentials; stdio does not.
if [[ "${EMAILMCP_TRANSPORT:-http}" == "http" ]]; then
  if [[ -z "${GOOGLE_CLIENT_SECRET:-}" ]]; then
    log_error "GOOGLE_CLIENT_SECRET is not set (required for HTTP MCP OAuth)."
    log_info "Please check $CONFIG_FILE or set it in your environment."
    exit 1
  fi
  if [[ -z "${PUBLIC_BASE_URL:-}" ]]; then
    log_error "PUBLIC_BASE_URL is not set (required for HTTP MCP OAuth)."
    log_info "Example: PUBLIC_BASE_URL=https://emailmcp.ecg.co"
    exit 1
  fi
fi

# Check if binary exists
if [[ ! -x "$BINARY" ]]; then
  log_error "EmailMCP binary not found or not executable at $BINARY"
  exit 1
fi

# Execute the binary with all passed arguments
exec "$BINARY" "$@"
