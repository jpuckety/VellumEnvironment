# EmailMCP - AI Agent Guidelines

## Project Overview
EmailMCP is a secure MCP server that exposes IMAP (inbound) and SMTP (outbound) email capabilities to AI agents via the Model Context Protocol using Streamable HTTP.

## Core Principles
- Security first: All credentials must be encrypted at rest using AES-256-GCM. Never log decrypted secrets.
- Concurrency matters: IMAP connection pooling and SMTP client management must be efficient and safe.
- Keep it idiomatic: Write clean, simple Go. Prefer standard library solutions when reasonable.
- MCP tools should be reliable and well-described — they become the agent's capabilities.

## Security Rules (Non-Negotiable)
- Never log passwords, decrypted credentials, or sensitive email content.
- All new account-related code must go through the encryption service.
- Master encryption key is only read from the `EMAILMCP_MASTER_KEY` environment variable.
- When adding new fields that might be sensitive, default to encrypting them.

## Architecture Overview
- `internal/crypto` — AES-256-GCM encryption/decryption service
- `internal/store` — Account persistence (SQLite)
- `internal/imap` — IMAP connection pool and operations
- `internal/smtp` — SMTP client management and sending logic
- `internal/server` — MCP server setup and tool registration
- `cmd/emailmcp` — Application entrypoint

## Coding Standards
- Use `context.Context` for all long-running or cancellable operations.
- Wrap errors with `%w` for proper error chains.
- Prefer small, focused functions and clear interfaces.
- Use `log/slog` for structured logging with appropriate levels.
- Keep MCP tool handlers relatively thin — move business logic into service packages.

## Adding New MCP Tools
1. Define the tool in the appropriate service package first.
2. Create a clear input/output schema with good descriptions.
3. Register the tool in `internal/server`.
4. Update `agents.md` if the new tool introduces new patterns or constraints.
5. Add basic validation and error handling.

## Working with IMAP
- Always acquire connections from the pool — never create ad-hoc clients.
- Be mindful of folder selection state when reusing connections.
- Respect per-account connection limits.
- Use UID-based operations for stability.
- After errors during use of a pooled connection, release with the error flag so the connection is discarded.

## Working with SMTP
- Reuse SMTP clients when possible but handle connection failures gracefully.
- Support both plain text and HTML bodies.
- Attachment handling should be clean and memory-efficient for reasonable sizes.
- Attachments arrive base64-encoded in tool input; decode only when sending.

## Testing & Quality
- Write tests for the encryption service and connection pool logic.
- Use table-driven tests where appropriate.
- Run `go test ./...` and `go vet ./...` before considering changes complete.
- Ensure `go build ./...` succeeds.

## Project-Specific Patterns
- Accounts always require both IMAP and SMTP configuration on creation.
- All IMAP/SMTP operations take a full `*types.Account` (with encrypted fields) and decrypt inside the service.
- The crypto service is the single source of truth for encrypt/decrypt.
- When modifying account fields, make sure both store and crypto paths are updated.
- The main server uses Streamable HTTP exclusively (`mcp.NewStreamableHTTPHandler`).

## GoLand Specific
- The project uses standard Go modules. GoLand should resolve dependencies cleanly.
- Run configurations should use environment variables for `EMAILMCP_MASTER_KEY`.

## Common Pitfalls to Avoid
- Storing plaintext passwords in structs that get logged or serialized.
- Creating long-lived IMAP connections outside the pool manager.
- Assuming folder selection state persists across pool acquires.
- Forgetting to close or properly release connections on error paths.
- Using sequence numbers instead of UIDs for operations across sessions.
