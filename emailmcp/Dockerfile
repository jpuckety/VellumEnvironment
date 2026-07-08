# syntax=docker/dockerfile:1
# Multi-stage build for EmailMCP

# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Cache go modules
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /emailmcp ./cmd/emailmcp

# Final minimal image
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -g '' emailmcp && \
    mkdir /data && chown emailmcp:emailmcp /data

WORKDIR /app

COPY --from=builder /emailmcp /app/emailmcp

USER emailmcp

# Default envs (override at runtime)
ENV EMAILMCP_LISTEN_ADDR=:8080 \
    EMAILMCP_DB_PATH=/data/emailmcp.db

EXPOSE 8080

ENTRYPOINT ["/app/emailmcp"]
