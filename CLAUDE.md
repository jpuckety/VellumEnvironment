# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

## Repository Overview

**EmailMCP** is a multi-user MCP server that exposes IMAP/SMTP email capabilities to AI agents.

| Directory | Language | Role |
|---|---|---|
| `emailmcp/` | Go | MCP server (IMAP/SMTP, OAuth, DynamoDB) |
| `cdk/` | TypeScript (CDK) | Env stack: ECS Fargate, ALB, CodeDeploy, DynamoDB, WAF |
| `Dockerfile` | Docker | Repo-root image consumed by MCPCICD |

The detailed Go-specific guide is in [`emailmcp/CLAUDE.md`](emailmcp/CLAUDE.md).

## Commands

All commands run from the **repo root** via `run.sh`, which loads `emailmcp/.env` automatically.

```bash
./run.sh test              # go test ./... in emailmcp/
./run.sh vet               # go vet ./...
./run.sh check             # test + vet + build
./run.sh run               # local HTTP server
./run.sh docker-build      # repo-root Dockerfile
./run.sh synth             # CDK synth (dry-run)
```

AWS deploy, image promotion, and SSM secrets are owned by MCPCICD. Do not add EKS/IRSA deploy commands back into this repo.

## Operating Mode

**Cloud** — multi-user ECS Fargate behind an HTTPS ALB. Per-user email account configuration, including IMAP/SMTP credentials, is stored in DynamoDB (encrypted at rest with a customer-managed KMS key); OAuth sessions live in a second table. The Go MCP server uses the ECS task role (not IRSA). Google client id/secret come from SSM SecureString `/email-mcp/google-client-id` and `/email-mcp/google-client-secret`.

## Cross-Component Architecture

```
AI Agent (MCP OAuth → opaque bearer token)
  → EmailMCP Server (Go, ECS Fargate :8080)
      → DynamoDB user-config table (IMAP/SMTP config + credentials)
      → DynamoDB session table (opaque OAuth sessions + registered clients)
      → Google OAuth2 (authorization-code + PKCE proxy)
      → Email Provider (IMAP/SMTP)
```

## Docker

The Go server uses a multi-stage build at the **repo root**. The image is a static binary (`CGO_ENABLED=0`) running as uid `10001`. MCPCICD builds `--platform linux/amd64`. Health check: `GET /health`.
