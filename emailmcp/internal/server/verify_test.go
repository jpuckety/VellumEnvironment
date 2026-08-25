package server

import (
	"context"
	"testing"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

func TestVerifyAccountCredentials_Validation(t *testing.T) {
	srv := &Server{}
	ctx := context.Background()

	// 1. Missing password
	acc := &types.Account{
		IMAPHost:     "imap.gmail.com",
		IMAPPort:     993,
		IMAPUsername: "user@gmail.com",
		IMAPPassword: "",
		IMAPUseTLS:   true,
		SMTPHost:     "smtp.gmail.com",
		SMTPPort:     587,
		SMTPUseTLS:   true,
	}
	res := srv.VerifyAccountCredentials(ctx, acc)
	if res.Success {
		t.Fatal("expected verify to fail when password is empty")
	}
	if res.IMAP.Success {
		t.Errorf("expected IMAP to fail with empty password, got success")
	}
	if res.IMAP.Error != "IMAP password is required for verification" {
		t.Errorf("unexpected IMAP error message: %s", res.IMAP.Error)
	}

	// 2. Private host rejection (SSRF)
	acc2 := &types.Account{
		IMAPHost:     "127.0.0.1",
		IMAPPort:     993,
		IMAPUsername: "user@gmail.com",
		IMAPPassword: "password123",
		IMAPUseTLS:   true,
		SMTPHost:     "10.0.0.1",
		SMTPPort:     587,
		SMTPUseTLS:   true,
	}
	res2 := srv.VerifyAccountCredentials(ctx, acc2)
	if res2.Success {
		t.Fatal("expected verify to fail on private hosts")
	}
	if res2.IMAP.Success || res2.SMTP.Success {
		t.Errorf("expected both IMAP and SMTP to fail SSRF check, got: %+v", res2)
	}

	// 3. TLS policy enforcement
	acc3 := &types.Account{
		IMAPHost:     "8.8.8.8",
		IMAPPort:     143,
		IMAPUsername: "user@example.com",
		IMAPPassword: "password123",
		IMAPUseTLS:   false, // Remote without TLS is blocked
		SMTPHost:     "8.8.8.8",
		SMTPPort:     25,
		SMTPUseTLS:   false,
	}
	res3 := srv.VerifyAccountCredentials(ctx, acc3)
	if res3.Success {
		t.Fatal("expected verify to fail when TLS is disabled on remote host")
	}
}
