package server

import (
	"testing"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/netutil"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

func TestBoolDefault(t *testing.T) {
	if !boolDefault(nil, true) {
		t.Fatal("nil pointer should use default true")
	}
	if boolDefault(nil, false) {
		t.Fatal("nil pointer should use default false")
	}
	f := false
	if boolDefault(&f, true) {
		t.Fatal("explicit false should win over default true")
	}
	tr := true
	if !boolDefault(&tr, false) {
		t.Fatal("explicit true should win over default false")
	}
}

func TestValidateAccountEndpoints_BlocksPrivate(t *testing.T) {
	acc := &types.Account{
		IMAPHost:   "10.0.0.1",
		IMAPUseTLS: true,
		SMTPHost:   "smtp.gmail.com",
		SMTPUseTLS: true,
	}
	if err := validateAccountEndpoints(acc); err == nil {
		t.Fatal("expected private IMAP host to be rejected")
	}
}

func TestValidateAccountEndpoints_RequiresTLS(t *testing.T) {
	acc := &types.Account{
		IMAPHost:   "8.8.8.8",
		IMAPUseTLS: false,
		SMTPHost:   "8.8.8.8",
		SMTPUseTLS: true,
	}
	if err := validateAccountEndpoints(acc); err == nil {
		t.Fatal("expected non-TLS remote IMAP to be rejected")
	}
}

func TestValidateAccountEndpoints_OK(t *testing.T) {
	acc := &types.Account{
		IMAPHost:   "8.8.8.8",
		IMAPUseTLS: true,
		SMTPHost:   "1.1.1.1",
		SMTPUseTLS: true,
	}
	if err := validateAccountEndpoints(acc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequireTLSUnlessLocalhost_MatchesPolicy(t *testing.T) {
	// Localhost may skip TLS per policy (SSRF still blocks dialing loopback).
	if err := netutil.RequireTLSUnlessLocalhost("localhost", false, "SMTP"); err != nil {
		t.Fatalf("localhost non-TLS should be allowed by TLS policy: %v", err)
	}
	if err := netutil.ValidatePublicHost("localhost"); err == nil {
		t.Fatal("localhost must still be blocked by SSRF host validation")
	}
}
