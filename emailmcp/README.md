# EmailMCP

A production-grade **Model Context Protocol (MCP)** server written in Go that exposes full email capabilities (IMAP + SMTP) as MCP tools for AI agents.

## Features

### IMAP (Inbound)
- Secure AES-256-GCM encrypted credential storage (Standalone) or AWS Secrets Manager (Cloud)
- Robust per-account IMAP connection pooling with health checks and limits
- List folders
- Search emails (text, from, since, etc.)
- Read full emails (envelope + body + attachment metadata)
- Move, flag, and delete (single and bulk)

### Multi-User & Cloud Architecture
- **Google OAuth2 Authentication**: Secure multi-user isolation using Google ID tokens.
- **Hybrid Storage Model**: General configuration in Amazon DynamoDB and sensitive credentials in AWS Secrets Manager.
- **AWS CDK Infrastructure**: Fully automated resource provisioning using TypeScript.
- **Config API Layer**: Lightweight Python Lambda gateway for configuration management.

### SMTP (Outbound)
- Send plain text + HTML emails
- Attachments (base64 in tool calls)
- Per-account SMTP configuration with TLS support

### Multi-Account & Security
- Unified account model for IMAP + SMTP
- All operations scoped by `account_id`
- Master key from `EMAILMCP_MASTER_KEY` (never logged)
- Pure Go SQLite storage

## Requirements

- Go 1.25+ (due to MCP SDK)
- Node.js & npm (for CDK infrastructure)
- Python 3.12 (for Config API Lambda)
- AWS CLI configured with appropriate credentials
- A 32-byte base64 master encryption key (for standalone mode)

## Quick Start (Cloud Deployment)

```bash
# 1. Setup environment
./run.sh setup

# 2. Deploy to AWS (dev environment)
./run.sh deploy dev
```

The `run.sh` script automates building the Go binary, packaging the Lambda, and deploying the CDK stack.

## Standalone Quick Start

```bash
# 1. Clone and build
git clone https://github.com/jpuckett/EmailMCP
cd EmailMCP/emailmcp
go mod tidy
go build -o emailmcp ./cmd/emailmcp

# 2. Generate master key
openssl rand -base64 32
# Copy the output

# 3. Configure
cp .env.example .env
# Edit .env and set EMAILMCP_MASTER_KEY

# 4. Run
./run.sh run
```

The server listens on `:8080` by default and serves the Streamable HTTP transport at `/`.

### Installation (System-wide)

If you want to install EmailMCP to your system:

```bash
./run.sh install
```

This will:
- Build and install the binary to `/usr/local/bin/emailmcp-bin`
- Install a wrapper script to `/usr/local/bin/emailmcp`

The wrapper script automatically loads configuration from `~/.emailmcp`. You should create this file manually with your environment variables:

```bash
# Example ~/.emailmcp
EMAILMCP_MASTER_KEY=your-32-byte-base64-key
EMAILMCP_DB_PATH=/Users/youruser/.emailmcp.db
```

## MCP Tools

| Tool                  | Description                              |
|-----------------------|------------------------------------------|
| `add_email_account`   | Add IMAP + SMTP account (creds encrypted)|
| `list_email_accounts` | List accounts (no secrets)               |
| `remove_email_account`| Delete account                           |
| `list_folders`        | List IMAP mailboxes                      |
| `search_emails`       | Search folder (summaries)                |
| `read_email`          | Fetch full message by UID                |
| `move_emails`         | Bulk move UIDs                           |
| `flag_emails`         | Add/remove flags                         |
| `delete_emails`       | Mark emails deleted                      |
| `send_email`          | Send message (text/HTML + attachments)   |

All email tools require `account_id`.

## Configuration

See `.env.example`. Key variables:

- `EMAILMCP_MASTER_KEY` (required)
- `EMAILMCP_LISTEN_ADDR`
- `EMAILMCP_DB_PATH`
- Pool and timeout tuning options

## Docker

```bash
docker build -t emailmcp .
docker run -e EMAILMCP_MASTER_KEY=... -p 8080:8080 emailmcp
```

## Claude Desktop Integration

To use EmailMCP with Claude Desktop, add the following to your configuration file:

- **macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows**: `%APPDATA%\Claude\claude_desktop_config.json`

If you have performed the [System-wide Installation](#installation-system-wide), the configuration is simple as the wrapper script handles the environment:

```json
{
  "mcpServers": {
    "emailmcp": {
      "command": "/usr/local/bin/emailmcp",
      "args": ["-transport", "stdio"]
    }
  }
}
```

Otherwise, you can run it from your source directory (requires absolute paths):

```json
{
  "mcpServers": {
    "emailmcp": {
      "command": "/absolute/path/to/EmailMCP/emailmcp",
      "args": ["-transport", "stdio"],
      "env": {
        "EMAILMCP_MASTER_KEY": "your-32-byte-base64-key",
        "EMAILMCP_DB_PATH": "/absolute/path/to/EmailMCP/emailmcp.db"
      }
    }
  }
}
```

## Architecture

```
emailmcp/
  cmd/emailmcp          - Entry point
  internal/
    crypto/             - AES-256-GCM service
    store/              - SQLite account persistence
    imap/               - Connection pool + operations (go-imap/v2)
    smtp/               - Sending logic (jordan-wright/email)
    server/             - MCP server + tool registration
    config/             - Env-based configuration
    types/              - Shared domain types
```

## Development

```bash
go test ./...
go vet ./...
go build ./...
```

## Security Notes

- Never log decrypted credentials or full email bodies in production.
- Master key must be provided via environment only.
- Use TLS for IMAP/SMTP in production.
- Consider running behind a reverse proxy with auth if exposing publicly.

## Cloud Architecture

EmailMCP is now ready for cloud-native deployment with the following components:

- **Go MCP Server**: Validates Google ID tokens and dynamically fetches per-user configuration.
- **Python Config API (AWS Lambda)**: Serves as a secure gateway between the MCP server and storage.
- **Hybrid Storage**:
  - **DynamoDB**: Stores non-sensitive user metadata and IMAP server settings.
  - **Secrets Manager**: Securely stores IMAP passwords, indexed by `applicationId` and `userId`.
- **AWS CDK**: Defines and provisions all resources including KMS keys for encryption, IAM roles for least-privilege access, and CloudTrail for audit logging.

For more details on the deployment process, see the root-level `run.sh` script and the `infrastructure/` directory.

## License

MIT
