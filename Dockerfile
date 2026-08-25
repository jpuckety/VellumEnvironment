# syntax=docker/dockerfile:1

# ============================================================================
# Frontend stage: build the Angular web application
# ============================================================================
FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend-builder
WORKDIR /web

COPY web/package*.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

# ============================================================================
# Build stage: compile a static Go binary from emailmcp/.
# ============================================================================
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

RUN apk add --no-cache git ca-certificates tzdata

COPY emailmcp/go.mod emailmcp/go.sum ./
RUN go mod download

COPY emailmcp/ ./
COPY --from=frontend-builder /emailmcp/internal/server/dist ./internal/server/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /emailmcp ./cmd/emailmcp

# ============================================================================
# Runtime stage: non-root Alpine image matching ECS volume-init uid 10001.
# ============================================================================
FROM alpine:3.21
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata wget \
 && addgroup -S --gid 10001 app \
 && adduser -S --uid 10001 --ingroup app --home /app --shell /sbin/nologin app

COPY --from=builder --chown=10001:10001 /emailmcp /app/emailmcp

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/health || exit 1

USER 10001

ENV EMAILMCP_LISTEN_ADDR=:8080

ENTRYPOINT ["/app/emailmcp"]
