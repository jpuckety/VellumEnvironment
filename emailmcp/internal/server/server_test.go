package server

import (
	"context"
	"io"
	"log/slog"
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
	// In-memory config store (no DynamoDB table configured).
	store, err := config.NewStore(context.Background(), "", "emailmcp", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	if err := store.PutUserConfig(context.Background(), "user1", &types.Account{ID: "acc1", Name: "Account 1"}); err != nil {
		t.Fatalf("PutUserConfig failed: %v", err)
	}

	s := &Server{
		configStore: store,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	ctx := context.WithValue(context.Background(), userContextKey, &UserInfo{Subject: "user1", Email: "user1@example.com"})

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
