package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/config"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

func TestGetAccount_RequiredAccountID(t *testing.T) {
	s := &Server{
		logger: slog.Default(),
	}

	// Mock authenticated context
	ctx := context.WithValue(context.Background(), userContextKey, &UserInfo{Subject: "user1", Email: "user1@example.com"})
	ctx = context.WithValue(ctx, tokenContextKey, "fake-token")

	// Call with empty accountID
	_, err := s.getAccount(ctx, "")
	if err == nil {
		t.Fatal("expected error when accountID is empty, got nil")
	}
	if err.Error() != "account_id is required" {
		t.Errorf("expected 'account_id is required' error, got %q", err.Error())
	}
}

func TestListEmailAccounts_ReturnsMap(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]types.Account{
			{ID: "acc1", Name: "Account 1"},
		})
	}))
	defer ts.Close()

	s := &Server{
		configClient: config.NewClient(ts.URL, "emailmcp"),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	ctx := context.WithValue(context.Background(), userContextKey, &UserInfo{Subject: "user1", Email: "user1@example.com"})
	ctx = context.WithValue(ctx, tokenContextKey, "fake-token")

	_, result, err := s.listEmailAccounts(ctx, nil, struct{}{})
	if err != nil {
		t.Fatalf("listEmailAccounts failed: %v", err)
	}

	resMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", result)
	}

	accs, ok := resMap["accounts"]
	if !ok {
		t.Fatal("expected 'accounts' key in result map")
	}

	summaries, ok := accs.([]types.AccountSummary)
	if !ok {
		t.Fatalf("expected []types.AccountSummary, got %T", accs)
	}

	if len(summaries) != 1 || summaries[0].ID != "acc1" {
		t.Errorf("unexpected summaries: %v", summaries)
	}
}
