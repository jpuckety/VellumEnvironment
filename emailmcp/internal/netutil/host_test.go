package netutil

import (
	"net"
	"testing"
)

func TestIsLocalhost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"[::1]", true},
		{"imap.gmail.com", false},
		{"10.0.0.1", false},
		{"169.254.169.254", false},
	}
	for _, tc := range cases {
		if got := IsLocalhost(tc.host); got != tc.want {
			t.Errorf("IsLocalhost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestValidatePublicHost_BlocksLiterals(t *testing.T) {
	blocked := []string{
		"127.0.0.1",
		"10.1.2.3",
		"172.16.0.1",
		"192.168.1.1",
		"169.254.169.254",
		"0.0.0.0",
		"::1",
		"metadata",
		"metadata.google.internal",
		"foo.internal",
		"printer.local",
	}
	for _, h := range blocked {
		if err := ValidatePublicHost(h); err == nil {
			t.Errorf("ValidatePublicHost(%q) = nil, want error", h)
		}
	}
}

func TestValidatePublicHost_AllowsPublicLiteral(t *testing.T) {
	// 8.8.8.8 is a well-known public address; no DNS needed.
	if err := ValidatePublicHost("8.8.8.8"); err != nil {
		t.Fatalf("ValidatePublicHost(8.8.8.8) unexpected error: %v", err)
	}
}

func TestValidatePublicHost_Empty(t *testing.T) {
	if err := ValidatePublicHost(""); err == nil {
		t.Fatal("expected error for empty host")
	}
	if err := ValidatePublicHost("   "); err == nil {
		t.Fatal("expected error for blank host")
	}
}

func TestRequireTLSUnlessLocalhost(t *testing.T) {
	if err := RequireTLSUnlessLocalhost("imap.gmail.com", true, "IMAP"); err != nil {
		t.Fatalf("TLS on: %v", err)
	}
	if err := RequireTLSUnlessLocalhost("localhost", false, "IMAP"); err != nil {
		t.Fatalf("localhost without TLS should be allowed: %v", err)
	}
	if err := RequireTLSUnlessLocalhost("imap.gmail.com", false, "SMTP"); err == nil {
		t.Fatal("expected error for non-TLS remote host")
	}
}

func TestIsBlockedIP_Private(t *testing.T) {
	ips := []string{"10.0.0.1", "192.168.0.1", "172.31.255.255", "127.0.0.1", "169.254.1.1"}
	for _, s := range ips {
		ip := net.ParseIP(s)
		if !isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s) = false, want true", s)
		}
	}
	if isBlockedIP(net.ParseIP("8.8.8.8")) {
		t.Error("8.8.8.8 should not be blocked")
	}
}
