package config

import (
	"context"
	"errors"
	"testing"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

func newTestStore(t *testing.T) Store {
	t.Helper()
	// Empty table name selects the in-memory implementation.
	s, err := NewStore(context.Background(), "", "emailmcp", nil)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	return s
}

func TestStore_PutGetDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.GetUserConfig(ctx, "user1", "acc1"); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("expected ErrConfigNotFound, got %v", err)
	}

	acc := &types.Account{
		ID:           "acc1",
		Name:         "Primary",
		IMAPHost:     "imap.example.com",
		IMAPPort:     993,
		IMAPUsername: "user@example.com",
		IMAPPassword: "secret",
		IMAPUseTLS:   true,
		SMTPHost:     "smtp.example.com",
		SMTPPort:     587,
	}
	if err := s.PutUserConfig(ctx, "user1", acc); err != nil {
		t.Fatalf("PutUserConfig failed: %v", err)
	}

	got, err := s.GetUserConfig(ctx, "user1", "acc1")
	if err != nil {
		t.Fatalf("GetUserConfig failed: %v", err)
	}
	if got.Name != "Primary" || got.IMAPPassword != "secret" {
		t.Fatalf("unexpected account: %+v", got)
	}
	// SMTP password falls back to the IMAP password when unset.
	if got.SMTPPassword != "secret" {
		t.Fatalf("expected smtp password to fall back to imap password, got %q", got.SMTPPassword)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("expected timestamps to be set, got created=%v updated=%v", got.CreatedAt, got.UpdatedAt)
	}

	// Another user must not see user1's account.
	if _, err := s.GetUserConfig(ctx, "user2", "acc1"); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("expected tenant isolation (ErrConfigNotFound), got %v", err)
	}

	if err := s.DeleteUserConfig(ctx, "user1", "acc1"); err != nil {
		t.Fatalf("DeleteUserConfig failed: %v", err)
	}
	if err := s.DeleteUserConfig(ctx, "user1", "acc1"); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("expected ErrConfigNotFound on second delete, got %v", err)
	}
}

func TestStore_ListScopedToUser(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.PutUserConfig(ctx, "user1", &types.Account{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUserConfig(ctx, "user1", &types.Account{ID: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUserConfig(ctx, "user2", &types.Account{ID: "c"}); err != nil {
		t.Fatal(err)
	}

	accs, err := s.ListUserConfigs(ctx, "user1")
	if err != nil {
		t.Fatalf("ListUserConfigs failed: %v", err)
	}
	if len(accs) != 2 {
		t.Fatalf("expected 2 accounts for user1, got %d", len(accs))
	}
	if accs[0].ID != "a" || accs[1].ID != "b" {
		t.Fatalf("unexpected ordering/content: %+v", accs)
	}

	other, err := s.ListUserConfigs(ctx, "user2")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 || other[0].ID != "c" {
		t.Fatalf("expected only user2's account, got %+v", other)
	}
}

func TestStore_PutPreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.PutUserConfig(ctx, "user1", &types.Account{ID: "a", Name: "v1"}); err != nil {
		t.Fatal(err)
	}
	first, err := s.GetUserConfig(ctx, "user1", "a")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.PutUserConfig(ctx, "user1", &types.Account{ID: "a", Name: "v2"}); err != nil {
		t.Fatal(err)
	}
	second, err := s.GetUserConfig(ctx, "user1", "a")
	if err != nil {
		t.Fatal(err)
	}

	if second.Name != "v2" {
		t.Fatalf("expected updated name v2, got %q", second.Name)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("expected created_at preserved (%v), got %v", first.CreatedAt, second.CreatedAt)
	}
}
