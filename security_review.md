# EmailMCP Security Review — Status

**Scope:** Monorepo security assessment of EmailMCP (Go MCP server) and CDK/EKS infrastructure.

> **Architecture update (2026-07-27):** The Python Config API Lambda has been removed. The Go MCP server now reads/writes per-user email account configuration — including IMAP/SMTP credentials — **directly** in the `EmailMCPUserConfigs` DynamoDB table via IRSA (AWS SDK). Credentials are stored as item attributes, encrypted at rest with the shared customer-managed KMS key; **AWS Secrets Manager is no longer used**. Findings below that referenced the Config API or Secrets Manager have been updated in place; the underlying protections now apply to the Go server's direct DynamoDB access.

**Original review:** 2026-07-15 (analysis only; no code changes in the initial report).

**This document:** Status of identified issues after remediation work on `master`. Update as further items are closed.

---

## Executive summary

The design has solid foundations: Google OAuth with PKCE, DynamoDB-backed opaque session tokens, server-side user identity from the session (subject) rather than client-supplied identifiers, KMS-encrypted DynamoDB for both sessions and account credentials, IRSA least-privilege, non-root container, and careful log redaction for `Authorization`/`Cookie`.

Highest-priority multi-tenant and credential issues from the review have been **fixed**. Remaining work is mostly product-policy, abuse controls, multi-replica session architecture, and production infrastructure hardening.

| Priority band | Fixed | Open / partial |
|---------------|-------|----------------|
| Critical (P0) | 4 / 4 | — |
| High (P0–P1) | 3 / 3 | — |
| Medium | 8 / 13 | 4 open + 1 partial (2 JWT items now N/A) |
| Low / defense-in-depth | 2 partial | most deferred |

---

## Status legend

| Status | Meaning |
|--------|---------|
| **Fixed** | Remediation merged; covered by tests and/or config wiring where applicable |
| **Partial** | Mitigated but not fully closed (ops follow-up or incomplete design) |
| **Open** | Not yet addressed |
| **Accepted risk** | Known; deferred by design or environment |

---

## Critical

| # | Issue | Status | Notes |
|---|--------|--------|-------|
| **1** | Cross-tenant IMAP pool keyed only by account ID | **Fixed** | Pools keyed by `(OwnerUserID, accountID)` via `PoolKey`; `DropPool` on remove and after credential rotation on add |
| **2** | Config API accepted attacker-controlled `secretArn` (credential IDOR) | **Fixed / superseded** | Historically fixed by the Config API allowlist. Now moot: Secrets Manager is removed and there is no `secretArn`. The Go server keys account items by the session subject (`<userId>#<accountId>`) and stores credentials itself; no client-supplied credential handle is accepted |
| **3** | OAuth allowed arbitrary HTTPS redirect URIs | **Fixed** (opt-in) | `OAUTH_REDIRECT_ALLOWLIST` (comma-separated hosts / `*.host` / `https://…` URIs). **Empty = not enforced.** Loopback HTTP + custom schemes always allowed. Wired via `.env` → EKS ConfigMap |
| **4** | SMTP/IMAP TLS defaults effectively off | **Fixed** | `*bool` TLS flags default to true; non-TLS refused except explicit localhost (SSRF still blocks loopback in practice → TLS required for dialable hosts) |

---

## High

| # | Issue | Status | Notes |
|---|--------|--------|-------|
| **5** | SSRF via attacker-chosen IMAP/SMTP hosts | **Fixed** | `internal/netutil` blocks private/link-local/metadata ranges and known metadata hostnames; enforced on account add, get, IMAP dial, SMTP send |
| **6** | Distinct `smtp_password` written to DynamoDB | **Fixed** | IMAP + SMTP passwords are stored as attributes in the `EmailMCPUserConfigs` item (encrypted at rest with the customer-managed KMS key); `list_email_accounts` returns summaries only and never includes passwords; SMTP password falls back to the IMAP password when unset |
| **7** | Google ID token exposed to MCP client in access token | **Fixed** | Access tokens are now opaque random tokens; the Google ID token is verified at sign-in and stored server-side in the DynamoDB session store (`internal/server/store.go`), never returned to the client. It is no longer forwarded anywhere downstream — the server derives the user identity (subject) from the session and uses it directly for DynamoDB access. 1h access TTL, capped by the Google token expiry |

---

## Medium

| # | Issue | Status | Notes |
|---|--------|--------|-------|
| **8** | Google ID token not cryptographically verified on OAuth callback | **Fixed** | `verifyGoogleIDToken` via Google JWKS (`google.golang.org/api/idtoken`) on callback, token, and refresh; checks `email_verified` when present |
| **9** | `GOOGLE_CLIENT_ID` optional | **Fixed** | Non-empty `GOOGLE_CLIENT_ID` required by the Go server; OAuth callback fails closed when the audience is missing. (The Config API that previously duplicated this check has been removed.) |
| **10** | No HTTP server timeouts / request size limits | **Fixed** | `ReadHeaderTimeout` 10s, `ReadTimeout` 60s, `WriteTimeout`/`IdleTimeout` 120s; `MaxBytesHandler` 32 MiB; attachments 10 MiB each / 25 MiB total |
| **11** | OAuth state fully in-memory; no refresh-token rotation | **Partial** | Sessions (opaque access/refresh tokens) and DCR clients now persisted in DynamoDB (`EmailMCPSessions`), surviving restarts and spanning replicas. Access tokens rotate on refresh; the refresh token stays stable (original expiry preserved). Short-lived auth codes / pending authorizations remain in-memory |
| **12** | Session signing key management (`EMAILMCP_JWT_SECRET`) | **N/A (removed)** | Session JWTs replaced by opaque tokens in a DynamoDB session store; there is no signing key to manage. Table name resolved via `EMAILMCP_SESSION_TABLE` or SSM `/emailmcp/session-table/name` |
| **13** | Session JWT audience not validated | **N/A (removed)** | Opaque access tokens are resolved against the server-side session store and checked for expiry; there is no JWT audience to validate |
| **14** | Error detail leakage | **Fixed** | Generic client messages for auth/storage failures; details logged server-side only |
| **15** | Arbitrary `From` on `send_email` | **Open** | Clients can set any From; consider binding to configured `from_address` / username |
| **16** | No rate limiting | **Open** | OAuth register/authorize/token and MCP tools lack rate limits |
| **17** | Open CORS on OAuth JSON (`Access-Control-Allow-Origin: *`) | **Open** | Prefer explicit origins or omit if browser clients are not required |
| **18** | Infrastructure data-loss / recovery choices | **Open** | DynamoDB/KMS `DESTROY` removal policy, limited K8s NetworkPolicy/resource limits — fine for dev; tighten for production |
| **19** | Client secret ignored on token endpoint while advertised | **Fixed** | Metadata advertises only `none`; DCR rejects confidential auth methods; secrets ignored (public clients + PKCE) |
| **20** | Misleading AS metadata `jwks_uri` (Google certs vs opaque tokens) | **Fixed** | `jwks_uri` omitted (access tokens are opaque, not JWTs) |

---

## Low / defense-in-depth

| Issue | Status | Notes |
|-------|--------|-------|
| `applicationId` not authorized (any app ID under own `sub`) | **Open** | Scope to known application IDs if multi-app isolation is required |
| Account ID slug collisions / weak uniqueness | **Open** | Overwrite-on-same-id behavior remains |
| Pool password never refreshed without DropPool | **Partial** | Drop on remove + after successful add; mid-life rotation other paths should call `DropPool` |
| Remove account does not close pool | **Fixed** | `DropPool` on `remove_email_account` |
| `list_email_accounts` hydrating secrets | **Fixed** | List returns `AccountSummary` only; passwords are never read into the list response |
| Credentials at rest for email accounts | **Note (design change)** | Passwords moved from Secrets Manager into the `EmailMCPUserConfigs` DynamoDB item, encrypted at rest with the customer-managed KMS key. Trade-off vs. a dedicated secrets store; consider application-layer envelope encryption if a stronger blast-radius boundary is required |
| Stdio transport unauthenticated | **Accepted risk** | Documented; local desktop only |
| Health endpoints unauthenticated | **Accepted risk** | Expected for probes; keep free of sensitive data |
| Ingress idle timeout / stale Tomcat comment | **Open** | Copy/paste cleanup |
| HTML escape omits `'` | **Open** | Minor for current HTML template |
| CloudTrail / log bucket retention | **Open** | Ops / org standards |
| Docker non-root + static binary | **OK** | Good |
| IRSA on SA `emailmcp:emailmcp` | **OK** | Good |
| Function URL `AWS_IAM` | **OK** | Good |
| Log redaction of Authorization | **OK** | Also Cookie / Proxy-Authorization |

---

## What is working well (unchanged from review)

- **User identity from the session subject** (not client-supplied): the Go server derives `userId` from the verified session, so accounts are strictly scoped to their owner
- **PKCE S256 required** on authorize; one-time auth codes; state binding
- **Passwords not returned** from MCP `list_email_accounts` (summaries only)
- **IRSA least-privilege**: the pod's role is granted read/write only to the two EmailMCP DynamoDB tables + the shared KMS key
- **KMS CMK + PITR** on DynamoDB (sessions and account credentials); key rotation enabled
- **No local credential DB** / master key design
- **HTTP logging** avoids bodies and redacts common secret headers
- **TLS 1.2+** for SMTP when TLS is enabled
- **Container runs as non-root**; multi-stage minimal image

---

## Configuration knobs added for security

| Variable | Where | Purpose |
|----------|--------|---------|
| `EMAILMCP_SESSION_TABLE` | env / SSM `/emailmcp/session-table/name` | DynamoDB table for opaque OAuth sessions + DCR clients (stable sessions across restarts/replicas) |
| `EMAILMCP_USER_CONFIG_TABLE` | env / SSM `/emailmcp/user-config-table/name` | DynamoDB table for per-user email account config + KMS-encrypted credentials (read/written directly by the Go server) |
| `OAUTH_REDIRECT_ALLOWLIST` | `.env` → EKS ConfigMap | HTTPS OAuth redirect allowlist; **blank = not enforced** |

---

## Suggested remaining roadmap

| Priority | Item | Effort |
|----------|------|--------|
| **P1** | Populate `OAUTH_REDIRECT_ALLOWLIST` in production `.env` (enforcement currently opt-in) | Ops |
| **P1** | Rate limits on OAuth + MCP tools | Medium |
| **P1** | Bind `From` to account identity on `send_email` | Small |
| **P2** | Persist short-lived auth codes / pending authorizations too (currently in-memory) | Small |
| **P2** | Add refresh-token rotation and consider application-layer envelope encryption for stored credentials / Google ID token | Medium |
| **P2** | Production CDK: retain tables, resource limits, NetworkPolicy | Small–medium |
| **P2** | Restrict CORS origins if browser clients are used | Small |

---

## Verification checklist (from original review)

| Test | Status |
|------|--------|
| Pool isolation: two users, both `account_id=default` | Covered by unit tests (`internal/imap/pool_test.go`) |
| Account item scoped to session subject (no client-supplied credential handle) | Covered by `config.Store` design (`internal/config/store_test.go`) |
| Redirect URI `https://evil.test/cb` with allowlist set | Covered by `oauth_test.go` when allowlist non-empty |
| TLS default on account add without TLS flags | Covered by security / pool tests |
| SSRF: `imap_host=169.254.169.254` | Covered by `internal/netutil` tests |

---

## Document history

| Date | Change |
|------|--------|
| 2026-07-15 | Initial security review (analysis) |
| 2026-07-15 | Fixed #1, #2, #4, #5, #6 (tenant pool, secrets, TLS, SSRF) |
| 2026-07-15 | Batch A: #8–#10, #12–#14, #19–#20 (JWT verify, timeouts, secrets wiring, metadata) |
| 2026-07-15 | #3 OAuth redirect allowlist (`OAUTH_REDIRECT_ALLOWLIST`) |
| 2026-07-15 | This status file created |
| 2026-07-27 | Replaced session JWTs with opaque tokens + DynamoDB session store (`EmailMCPSessions`): #7 fixed, #11 partial, #12/#13 N/A; new table + `refresh-index` GSI + IRSA grants |
| 2026-07-27 | Removed the Config API Lambda + Secrets Manager; the Go server now reads/writes the `EmailMCPUserConfigs` table directly via IRSA with credentials as KMS-encrypted attributes (#2 superseded, #6/#7/#9/#14 updated) |

*Keep this file updated when closing remaining items or accepting new risks.*
