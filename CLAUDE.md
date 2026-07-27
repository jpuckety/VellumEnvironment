# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Overview

This is a monorepo for **EmailMCP** — a multi-user MCP server that exposes IMAP/SMTP email capabilities to AI agents. It has two components that deploy together:

| Directory | Language | Role |
|---|---|---|
| `emailmcp/` | Go | MCP server (core logic, IMAP/SMTP, OAuth, direct DynamoDB access) |
| `infrastructure/` | TypeScript (CDK) | AWS infrastructure (DynamoDB, ECR, KMS, CloudTrail) |

The detailed Go-specific guide is in [`emailmcp/CLAUDE.md`](emailmcp/CLAUDE.md).

## Commands

All commands run from the **repo root** via `run.sh`, which loads `emailmcp/.env` automatically.

```bash
# Development
./run.sh test              # go test ./... in emailmcp/
./run.sh vet               # go vet ./...
./run.sh check             # test + vet

# Local run (Docker Compose — starts EmailMCP + vellum-assistant sidecar)
docker-compose up

# Infrastructure
./run.sh infra-deploy      # CDK deploy (requires OIDC for IRSA)
./run.sh infra-destroy     # CDK destroy
./run.sh synth             # CDK synth (dry-run)

# EKS Deployment
./run.sh eks-deploy        # Full deploy: CDK + build/push Docker + kubectl apply
./run.sh eks-refresh       # Rebuild image, re-apply manifests, rollout restart
./run.sh eks-status        # kubectl get pods/svc/deploy/ing in emailmcp namespace
./run.sh eks-logs          # kubectl logs -f for emailmcp pods
./run.sh undeploy-eks      # Remove K8s resources (does not destroy CDK stack)
./run.sh build-push        # Build Docker image and push to ECR
./run.sh clean             # Remove build artifacts
```

Single package test from within the Go module:
```bash
cd emailmcp && go test ./internal/config/...
```

## Operating Mode

**Cloud-only** — multi-user deployment. Per-user email account configuration, including IMAP/SMTP credentials, is stored in the `EmailMCPUserConfigs` DynamoDB table (encrypted at rest with a customer-managed KMS key); OAuth sessions live in the `EmailMCPSessions` table. The Go MCP server reads/writes both tables directly via the AWS SDK using an IRSA role — there is no Config API Lambda and no AWS Secrets Manager. Requires `GOOGLE_CLIENT_ID` and IRSA (for EKS); DynamoDB table names are resolved from SSM (or `EMAILMCP_USER_CONFIG_TABLE` / `EMAILMCP_SESSION_TABLE`). There is no local SQLite or master-key encryption path.

## Cross-Component Architecture

```
AI Agent (MCP OAuth → opaque bearer token)
  → EmailMCP Server (Go, EKS / localhost:8080)
      → DynamoDB EmailMCPUserConfigs (IMAP/SMTP config + credentials, KMS-encrypted)
      → DynamoDB EmailMCPSessions (opaque OAuth sessions + registered clients)
      → Google OAuth2 (authorization-code + PKCE proxy, ID-token verification)
      → Email Provider (IMAP/SMTP, using stored credentials)
```

**Account & session storage** (`emailmcp/internal/config/store.go`, `emailmcp/internal/server/store.go`): The Go server owns all persistence. `config.Store` reads/writes per-user email accounts (keyed PK `applicationId`, SK `<userId>#<accountId>`) in `EmailMCPUserConfigs`; the session store keeps opaque access/refresh tokens and Dynamic Client Registrations in `EmailMCPSessions`. Both fall back to an in-memory implementation when no table is configured (local/stdio, tests).

**Infrastructure** (`infrastructure/lib/infrastructure-stack.ts`): Single CDK stack (`InfrastructureStack`) that provisions everything — KMS key, the `EmailMCPUserConfigs` DynamoDB table (PK: `applicationId`, SK: `userId`), the `EmailMCPSessions` table, IRSA IAM role (granted read/write to both tables + KMS), ECR repo, ACM certificate, and SSM parameters `/emailmcp/user-config-table/name` and `/emailmcp/session-table/name`. The IRSA role is conditional on EKS OIDC context being present; deploying without it removes the role.

**OIDC / IRSA resolution order** (for `infra-deploy`):
1. `EKS_OIDC_PROVIDER_ARN` env var (from `emailmcp/.env`, preferred)
2. `infrastructure/cdk.local.json` (gitignored, durable local override)
3. Live detection from `kubectl` + `aws eks describe-cluster`

Never commit OIDC ARNs or account IDs to `infrastructure/cdk.json` — they identify your AWS account and cluster. Use `emailmcp/.env` or `infrastructure/cdk.local.json` (both gitignored).

## Docker

The Go server uses a multi-stage build. The `Dockerfile` is in `emailmcp/` and uses `CGO_ENABLED=0` for a static binary, targeting `$TARGETOS/$TARGETARCH` from `BUILDPLATFORM` — supports `linux/amd64` and `linux/arm64`. `run.sh` auto-detects the EKS node architecture before building.
