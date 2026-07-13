#!/usr/bin/env bash
# EmailMCP Wrapper Script
# This script loads configuration from /usr/local/etc/emailmcp.cfg and runs the EmailMCP binary.

set -e

CONFIG_FILE="/usr/local/etc/emailmcp.cfg"
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

# Ensure mandatory environment variables are set
if [[ -z "${EMAILMCP_MASTER_KEY:-}" ]]; then
  log_error "EMAILMCP_MASTER_KEY is not set."
  log_info "Please check $CONFIG_FILE or set it in your environment."
  exit 1
fi

# Check if binary exists
if [[ ! -x "$BINARY" ]]; then
  log_error "EmailMCP binary not found or not executable at $BINARY"
  exit 1
fi

# Execute the binary with all passed arguments
exec "$BINARY" "$@"
