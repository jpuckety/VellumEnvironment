# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

This repository contains **EmailMCP** — a production-grade MCP (Model Context Protocol) server written in Go that exposes IMAP and SMTP email capabilities as MCP tools for AI agents. All code lives in `emailmcp/`.

## Commands

All Go commands must run from within `emailmcp/`, or use `run.sh` from the repo root.

```bash
# From emailmcp/
go build -o emailmcp ./cmd/emailmcp   # build native binary
go test ./...                          # run all tests
go test ./internal/crypto/...          # run a single package's tests
go vet ./...                           # vet
go build ./...                         # verify everything compiles

# From repo root via run.sh
./run.sh setup         # copy .env.example → .env
./run.sh key           # generate a 32-byte base64 master key
./run.sh build         # build native + Linux x86_64 binaries
./run.sh run           # load .env and start the server
./run.sh run --transport stdio  # run in stdio MCP mode
./run.sh check         # test + vet + build
./run.sh install       # install binary + wrapper to /usr/local/bin
./run.sh clean         # remove binaries and local DB files
```

The server requires `EMAILMCP_MASTER_KEY` (a 32-byte base64 key) to start. The `run.sh` script loads it from `emailmcp/.env` automatically.

## Architecture

```
emailmcp/
  cmd/emailmcp/         Entry point — wires together config, store, crypto, and server
  internal/
    config/             Env-based config (EMAILMCP_* vars, sensible defaults)
    crypto/             AES-256-GCM encryption service (single source of encrypt/decrypt)
    store/              SQLite account persistence (modernc.org/sqlite, pure Go)
    imap/               Per-account connection pool + all IMAP operations (go-imap/v2)
    smtp/               Stateless per-send SMTP client (jordan-wright/email)
    server/             MCP server setup, all tool registration and handlers
    types/              Shared domain types (Account, EmailSummary, EmailMessage, etc.)
```

**Request flow:** MCP client → Streamable HTTP or stdio transport → `server.go` handler (thin) → `imap.Manager` or `smtp.Sender` → `store` / `crypto` as needed.

### MCP Tool Registration

All 10 tools are registered in `server.go:registerTools()` using `mcp.AddTool`. Handler functions live in `server.go` and delegate to service packages. Tool input structs use `jsonschema` tags for the MCP schema.

### IMAP Connection Pool

`internal/imap/pool.go` maintains a per-account pool of `*imapclient.Client` connections. The pattern for every IMAP operation:

```go
conn, err := m.Acquire(ctx, acc)
hadErr := false
defer func() { m.Release(acc.ID, conn, hadErr) }()
// ... use conn.client ...
// on error: hadErr = true (connection is discarded, not returned to pool)
```

Connections track `lastSelected` folder to reduce churn but always re-`SELECT` before operations. Always use UID-based operations (`UIDSearch`, `UIDSetNum`) — never sequence numbers.

### Credential Handling

Passwords are encrypted at rest via `crypto.Service.EncryptString` before reaching `store.Store`, and decrypted inside the service layer (`imap.Manager.getOrCreatePool`, `smtp.Sender.SendEmail`) — never outside. `types.Account` carries `IMAPPasswordEnc` / `SMTPPasswordEnc`; the plaintext never appears in logs or serialized responses.

### Transports

The server supports two MCP transports selected at startup:
- **HTTP** (default): Streamable HTTP on `:8080`, served via `mcp.NewStreamableHTTPHandler`. Logs are written to stderr; SSE connections are logged at Info, regular requests at Debug.
- **stdio**: `mcp.StdioTransport` for Claude Desktop integration. Stdout is reserved for the protocol; all logging goes to stderr.

## Adding New MCP Tools

1. Implement the operation in the appropriate service package (`imap/` or `smtp/`).
2. Define an input struct with `json` and `jsonschema` tags in `server.go`.
3. Write a thin handler method on `*Server` that calls the service.
4. Register with `mcp.AddTool` in `registerTools()`.
5. Update `emailmcp/agents.md` if new patterns or constraints are introduced.

## Security Rules

- `EMAILMCP_MASTER_KEY` is read only from the environment — never hardcode or log it.
- Never log decrypted passwords or email body content.
- All new credential-like fields must go through `crypto.Service` before storage.
- HTTP request/response logging in `httpLogging()` redacts `Authorization`, `Cookie`, and `Proxy-Authorization` headers and never logs bodies.
