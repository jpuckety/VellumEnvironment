package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	net_smtp "net/smtp"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/netutil"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

// ComponentVerification reports verification details for a single protocol.
type ComponentVerification struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// VerificationResult reports the combined result of IMAP and SMTP verification.
type VerificationResult struct {
	Success bool                  `json:"success"`
	IMAP    ComponentVerification `json:"imap"`
	SMTP    ComponentVerification `json:"smtp"`
}

// VerifyAccountCredentials verifies that both IMAP and SMTP endpoints are reachable
// and that the provided credentials authenticate successfully.
func (s *Server) VerifyAccountCredentials(ctx context.Context, acc *types.Account) *VerificationResult {
	res := &VerificationResult{
		Success: true,
	}

	res.IMAP = s.verifyIMAP(ctx, acc)
	if !res.IMAP.Success {
		res.Success = false
	}

	res.SMTP = s.verifySMTP(ctx, acc)
	if !res.SMTP.Success {
		res.Success = false
	}

	return res
}

func (s *Server) verifyIMAP(ctx context.Context, acc *types.Account) ComponentVerification {
	if acc.IMAPHost == "" {
		return ComponentVerification{Success: false, Error: "IMAP host is required"}
	}
	if acc.IMAPUsername == "" {
		return ComponentVerification{Success: false, Error: "IMAP username is required"}
	}
	if acc.IMAPPassword == "" {
		return ComponentVerification{Success: false, Error: "IMAP password is required for verification"}
	}

	if err := netutil.ValidatePublicHost(acc.IMAPHost); err != nil {
		return ComponentVerification{Success: false, Error: fmt.Sprintf("IMAP host not allowed: %v", err)}
	}
	if err := netutil.RequireTLSUnlessLocalhost(acc.IMAPHost, acc.IMAPUseTLS, "IMAP"); err != nil {
		return ComponentVerification{Success: false, Error: err.Error()}
	}

	port := acc.IMAPPort
	if port <= 0 {
		if acc.IMAPUseTLS {
			port = 993
		} else {
			port = 143
		}
	}

	addr := fmt.Sprintf("%s:%d", acc.IMAPHost, port)

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	type dialResult struct {
		client *imapclient.Client
		err    error
	}

	done := make(chan dialResult, 1)
	go func() {
		var client *imapclient.Client
		var err error
		if acc.IMAPUseTLS {
			client, err = imapclient.DialTLS(addr, &imapclient.Options{})
		} else {
			client, err = imapclient.DialInsecure(addr, &imapclient.Options{})
		}
		done <- dialResult{client: client, err: err}
	}()

	var client *imapclient.Client
	select {
	case <-timeoutCtx.Done():
		return ComponentVerification{Success: false, Error: fmt.Sprintf("IMAP connection timed out to %s", addr)}
	case r := <-done:
		if r.err != nil {
			return ComponentVerification{Success: false, Error: fmt.Sprintf("failed to connect to IMAP %s: %v", addr, r.err)}
		}
		client = r.client
	}
	defer client.Close()

	loginDone := make(chan error, 1)
	go func() {
		cmd := client.Login(acc.IMAPUsername, acc.IMAPPassword)
		loginDone <- cmd.Wait()
	}()

	select {
	case <-timeoutCtx.Done():
		return ComponentVerification{Success: false, Error: "IMAP authentication timed out"}
	case err := <-loginDone:
		if err != nil {
			return ComponentVerification{Success: false, Error: fmt.Sprintf("IMAP authentication failed: %v", err)}
		}
	}

	return ComponentVerification{
		Success: true,
		Message: fmt.Sprintf("Connected to %s and authenticated as %s", addr, acc.IMAPUsername),
	}
}

func (s *Server) verifySMTP(ctx context.Context, acc *types.Account) ComponentVerification {
	if acc.SMTPHost == "" {
		return ComponentVerification{Success: false, Error: "SMTP host is required"}
	}

	smtpUser := acc.SMTPUsername
	if smtpUser == "" {
		smtpUser = acc.IMAPUsername
	}
	if smtpUser == "" {
		return ComponentVerification{Success: false, Error: "SMTP username is required"}
	}

	smtpPass := acc.SMTPPassword
	if smtpPass == "" {
		smtpPass = acc.IMAPPassword
	}
	if smtpPass == "" {
		return ComponentVerification{Success: false, Error: "SMTP password is required for verification"}
	}

	if err := netutil.ValidatePublicHost(acc.SMTPHost); err != nil {
		return ComponentVerification{Success: false, Error: fmt.Sprintf("SMTP host not allowed: %v", err)}
	}
	if err := netutil.RequireTLSUnlessLocalhost(acc.SMTPHost, acc.SMTPUseTLS, "SMTP"); err != nil {
		return ComponentVerification{Success: false, Error: err.Error()}
	}

	port := acc.SMTPPort
	if port <= 0 {
		if acc.SMTPUseTLS {
			port = 587
		} else {
			port = 25
		}
	}

	addr := fmt.Sprintf("%s:%d", acc.SMTPHost, port)

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- s.dialAndAuthSMTP(timeoutCtx, addr, acc.SMTPHost, port, smtpUser, smtpPass, acc.SMTPUseTLS)
	}()

	select {
	case <-timeoutCtx.Done():
		return ComponentVerification{Success: false, Error: fmt.Sprintf("SMTP connection timed out to %s", addr)}
	case err := <-errChan:
		if err != nil {
			return ComponentVerification{Success: false, Error: err.Error()}
		}
	}

	return ComponentVerification{
		Success: true,
		Message: fmt.Sprintf("Connected to %s and authenticated as %s", addr, smtpUser),
	}
}

func (s *Server) dialAndAuthSMTP(ctx context.Context, addr, host string, port int, user, pass string, useTLS bool) error {
	tlsConfig := &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	}

	var client *net_smtp.Client
	var err error

	if useTLS && port == 465 {
		// Implicit TLS on port 465
		dialer := &net.Dialer{Timeout: 8 * time.Second}
		conn, dialErr := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
		if dialErr != nil {
			return fmt.Errorf("TLS connection to %s failed: %w", addr, dialErr)
		}
		defer conn.Close()

		client, err = net_smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("SMTP handshake with %s failed: %w", addr, err)
		}
	} else {
		// Plain connection with optional STARTTLS (port 587 / 25)
		dialer := &net.Dialer{Timeout: 8 * time.Second}
		conn, dialErr := dialer.DialContext(ctx, "tcp", addr)
		if dialErr != nil {
			return fmt.Errorf("connection to %s failed: %w", addr, dialErr)
		}
		defer conn.Close()

		client, err = net_smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("SMTP handshake with %s failed: %w", addr, err)
		}

		if useTLS {
			if ok, _ := client.Extension("STARTTLS"); ok {
				if err := client.StartTLS(tlsConfig); err != nil {
					return fmt.Errorf("STARTTLS negotiation failed: %w", err)
				}
			} else {
				return errors.New("server does not support STARTTLS")
			}
		}
	}
	defer client.Close()

	auth := net_smtp.PlainAuth("", user, pass, host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("SMTP authentication failed: %w", err)
	}

	return nil
}
