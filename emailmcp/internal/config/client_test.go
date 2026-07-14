package config

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckHealth_OK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q, want /health", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	c := &Client{
		BaseURL:    strings.TrimRight(ts.URL, "/"),
		HTTPClient: ts.Client(),
	}
	if err := c.CheckHealth(context.Background()); err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
}

func TestCheckHealth_NonOK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := &Client{
		BaseURL:    strings.TrimRight(ts.URL, "/"),
		HTTPClient: ts.Client(),
	}
	err := c.CheckHealth(context.Background())
	if err == nil {
		t.Fatal("expected error for non-200 health response")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error = %q, want status mention", err)
	}
}

func TestEnsureHealthy_SucceedsAfterRetry(t *testing.T) {
	// Temporarily shrink retry constants is not possible; use a server that fails once then succeeds.
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			http.Error(w, "cold start", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	c := &Client{
		BaseURL:    strings.TrimRight(ts.URL, "/"),
		HTTPClient: ts.Client(),
	}

	// Use a short-lived context so we don't hang if retries fail; backoff is 2s so one retry is fine.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(ioDiscard{}, nil))
	if err := c.EnsureHealthy(ctx, logger); err != nil {
		t.Fatalf("EnsureHealthy: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", attempts)
	}
}

// ioDiscard is a minimal io.Writer that discards all writes.
type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
