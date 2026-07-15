package imap

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(Config{
		MaxConnsPerAccount: 2,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestPoolKeyIsolatesTenants(t *testing.T) {
	a := PoolKey("user-a", "default")
	b := PoolKey("user-b", "default")
	if a == b {
		t.Fatal("pool keys for different owners with same account id must differ")
	}
	if PoolKey("user-a", "work") == PoolKey("user-a", "personal") {
		t.Fatal("pool keys for different account ids must differ")
	}
}

func TestGetOrCreatePool_RequiresOwner(t *testing.T) {
	m := testManager(t)
	_, err := m.getOrCreatePool(&types.Account{
		ID:           "default",
		IMAPHost:     "8.8.8.8",
		IMAPPassword: "secret",
		IMAPUseTLS:   true,
	})
	if err == nil {
		t.Fatal("expected error when OwnerUserID is empty")
	}
}

func TestGetOrCreatePool_BlocksPrivateHost(t *testing.T) {
	m := testManager(t)
	_, err := m.getOrCreatePool(&types.Account{
		ID:           "default",
		OwnerUserID:  "user-1",
		IMAPHost:     "10.0.0.5",
		IMAPPassword: "secret",
		IMAPUseTLS:   true,
	})
	if err == nil {
		t.Fatal("expected private host to be rejected")
	}
}

func TestGetOrCreatePool_RequiresTLSForRemote(t *testing.T) {
	m := testManager(t)
	_, err := m.getOrCreatePool(&types.Account{
		ID:           "default",
		OwnerUserID:  "user-1",
		IMAPHost:     "8.8.8.8",
		IMAPPassword: "secret",
		IMAPUseTLS:   false,
	})
	if err == nil {
		t.Fatal("expected non-TLS remote host to be rejected")
	}
}

func TestGetOrCreatePool_SeparatePoolsPerOwner(t *testing.T) {
	m := testManager(t)
	accA := &types.Account{
		ID:           "default",
		OwnerUserID:  "user-a",
		IMAPHost:     "8.8.8.8",
		IMAPPassword: "secret-a",
		IMAPUseTLS:   true,
	}
	accB := &types.Account{
		ID:           "default",
		OwnerUserID:  "user-b",
		IMAPHost:     "1.1.1.1",
		IMAPPassword: "secret-b",
		IMAPUseTLS:   true,
	}

	pA, err := m.getOrCreatePool(accA)
	if err != nil {
		t.Fatalf("pool A: %v", err)
	}
	pB, err := m.getOrCreatePool(accB)
	if err != nil {
		t.Fatalf("pool B: %v", err)
	}
	if pA == pB {
		t.Fatal("expected distinct pool instances for different owners")
	}
	if pA.imapCfg.Password == pB.imapCfg.Password {
		t.Fatal("pools must not share credentials across tenants")
	}

	// Same owner + account returns the same pool.
	pA2, err := m.getOrCreatePool(accA)
	if err != nil {
		t.Fatalf("pool A2: %v", err)
	}
	if pA != pA2 {
		t.Fatal("expected same pool for same owner+account")
	}
}

func TestDropPool_RemovesPool(t *testing.T) {
	m := testManager(t)
	acc := &types.Account{
		ID:           "default",
		OwnerUserID:  "user-a",
		IMAPHost:     "8.8.8.8",
		IMAPPassword: "secret",
		IMAPUseTLS:   true,
	}
	if _, err := m.getOrCreatePool(acc); err != nil {
		t.Fatalf("create: %v", err)
	}
	m.DropPool("user-a", "default")

	m.mu.Lock()
	_, exists := m.pools[PoolKey("user-a", "default")]
	m.mu.Unlock()
	if exists {
		t.Fatal("pool should be removed after DropPool")
	}

	// DropPool is idempotent.
	m.DropPool("user-a", "default")
}

func TestRelease_WithoutOwnerClosesConn(t *testing.T) {
	m := testManager(t)
	// Should not panic with nil/partial inputs.
	m.Release(nil, nil, false)
	m.Release(&types.Account{ID: "x"}, nil, false)
	_ = context.Background()
}
