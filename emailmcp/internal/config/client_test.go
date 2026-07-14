package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
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

func TestGetUserConfig_SendsGoogleIDTokenHeader(t *testing.T) {
	const wantToken = "test-google-id-token"
	const wantUser = "user-123"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if got := r.Header.Get(googleIDTokenHeader); got != wantToken {
			t.Errorf("%s = %q, want %q", googleIDTokenHeader, got, wantToken)
		}
		// Without AWS config, Bearer is also set for local/dev.
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			t.Errorf("Authorization = %q, want Bearer token", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/configs/emailmcp/"+wantUser+"/default") {
			t.Errorf("path = %q, want .../configs/emailmcp/%s/default", r.URL.Path, wantUser)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(types.Account{
			ID:       wantUser,
			Name:     "Test",
			IMAPHost: "imap.example.com",
			IMAPPort: 993,
		})
	}))
	defer ts.Close()

	c := &Client{
		BaseURL:       strings.TrimRight(ts.URL, "/"),
		ApplicationID: "emailmcp",
		HTTPClient:    ts.Client(),
		Logger:        slog.New(slog.NewTextHandler(ioDiscard{}, nil)),
	}
	acc, err := c.GetUserConfig(context.Background(), wantToken, wantUser, "default")
	if err != nil {
		t.Fatalf("GetUserConfig: %v", err)
	}
	if acc.ID != wantUser {
		t.Errorf("acc.ID = %q, want %q", acc.ID, wantUser)
	}
}

func TestSetGoogleAuth_WithSigV4DoesNotUseBearer(t *testing.T) {
	// When AWS config is present, Authorization is reserved for SigV4 and must
	// not be set to Bearer (SignHTTP would overwrite it anyway).
	cfg := aws.Config{}
	c := &Client{AWSConfig: &cfg}

	req, err := http.NewRequest(http.MethodGet, "https://example.com/configs/a/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.setGoogleAuth(req, "tok")

	if got := req.Header.Get(googleIDTokenHeader); got != "tok" {
		t.Errorf("%s = %q, want tok", googleIDTokenHeader, got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty when SigV4 is enabled", got)
	}
}
