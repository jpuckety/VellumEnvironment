# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Overview

This is a monorepo for **EmailMCP** — a multi-user MCP server that exposes IMAP/SMTP email capabilities to AI agents. It has three components that deploy together:

| Directory | Language | Role |
|---|---|---|
| `emailmcp/` | Go | MCP server (core logic, IMAP/SMTP, Config API client) |
| `emailmcp-config-api/` | Python | AWS Lambda — Config API gateway for multi-user cloud mode |
| `infrastructure/` | TypeScript (CDK) | AWS infrastructure (DynamoDB, Lambda, ECR, KMS, CloudTrail) |

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
./run.sh infra-deploy      # CDK deploy (packages Lambda first; requires OIDC for IRSA)
./run.sh infra-destroy     # CDK destroy
./run.sh synth             # CDK synth (dry-run)
./run.sh package-lambda    # Re-package Python Lambda into emailmcp-config-api/dist/

# EKS Deployment
./run.sh eks-deploy        # Full deploy: CDK + build/push Docker + kubectl apply
./run.sh eks-refresh       # Rebuild image, re-apply manifests, rollout restart
./run.sh eks-status        # kubectl get pods/svc/deploy/ing in emailmcp namespace
./run.sh eks-logs          # kubectl logs -f for emailmcp pods
./run.sh undeploy-eks      # Remove K8s resources (does not destroy CDK stack)
./run.sh build-push        # Build Docker image and push to ECR
./run.sh clean             # Remove build artifacts and Lambda dist/
```

Single package test from within the Go module:
```bash
cd emailmcp && go test ./internal/config/...
```

## Operating Mode

**Cloud-only** — multi-user deployment. Account metadata goes to DynamoDB; IMAP passwords go to AWS Secrets Manager. The MCP server fetches per-user config from the Config API Lambda (authenticated with SigV4 + Google ID tokens). Requires `CONFIG_API_URL`, `GOOGLE_CLIENT_ID`, and IRSA (for EKS). There is no local SQLite or master-key encryption path.

## Cross-Component Architecture

```
AI Agent (Google ID Token) 
  → EmailMCP Server (Go, EKS / localhost:8080)
      → Config API Lambda (Python, Function URL + AWS IAM auth)
           → DynamoDB (non-sensitive IMAP/SMTP config)
           → Secrets Manager (IMAP passwords, keyed emailmcp/imap/{appId}/{userId})
      → Email Provider (IMAP/SMTP, using credentials from Config API)
```

**Config API** (`emailmcp-config-api/main.py`): A simple Python Lambda. Routes are `GET|PUT|DELETE /configs/{applicationId}/{userId}` and `GET /health`. Validates the Google ID token on every config request, then reads/writes DynamoDB and Secrets Manager. No framework — plain `boto3`. Deployed via CDK as a Lambda Function URL with `AWS_IAM` auth.

**Infrastructure** (`infrastructure/lib/infrastructure-stack.ts`): Single CDK stack (`InfrastructureStack`) that provisions everything — KMS key, DynamoDB table (PK: `applicationId`, SK: `userId`), Config API Lambda + Function URL, IRSA IAM role, ECR repo, ACM certificate, and SSM parameter `/emailmcp/config-api/url`. The IRSA role is conditional on EKS OIDC context being present; deploying without it removes the role.

**OIDC / IRSA resolution order** (for `infra-deploy`):
1. `EKS_OIDC_PROVIDER_ARN` env var (from `emailmcp/.env`, preferred)
2. `infrastructure/cdk.local.json` (gitignored, durable local override)
3. Live detection from `kubectl` + `aws eks describe-cluster`

Never commit OIDC ARNs or account IDs to `infrastructure/cdk.json` — they identify your AWS account and cluster. Use `emailmcp/.env` or `infrastructure/cdk.local.json` (both gitignored).

## Lambda Packaging

`./run.sh package-lambda` installs Python deps for `manylinux2014_x86_64` (Lambda's runtime) into `emailmcp-config-api/dist/`. This cross-platform install is required — installing on macOS with a host pip produces Darwin `.dylib` binaries that fail on Lambda with "invalid ELF header". The script validates that no macOS-native `.so` or `.dylib` files slipped into the package.

## Docker

The Go server uses a multi-stage build. The `Dockerfile` is in `emailmcp/` and uses `CGO_ENABLED=0` for a static binary, targeting `$TARGETOS/$TARGETARCH` from `BUILDPLATFORM` — supports `linux/amd64` and `linux/arm64`. `run.sh` auto-detects the EKS node architecture before building.
