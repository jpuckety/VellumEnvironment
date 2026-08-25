package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	net_smtp "net/smtp"
	"strconv"
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
	if acc == nil {
		return &VerificationResult{
			Success: false,
			IMAP:    ComponentVerification{Success: false, Error: "account is required"},
			SMTP:    ComponentVerification{Success: false, Error: "account is required"},
		}
	}

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

	addr := net.JoinHostPort(acc.IMAPHost, strconv.Itoa(port))

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var conn net.Conn
	dialer := &net.Dialer{}

	if acc.IMAPUseTLS {
		tlsConfig := &tls.Config{
			ServerName: acc.IMAPHost,
			MinVersion: tls.VersionTLS12,
		}
		rawConn, err := dialer.DialContext(timeoutCtx, "tcp", addr)
		if err != nil {
			if timeoutCtx.Err() != nil {
				return ComponentVerification{Success: false, Error: fmt.Sprintf("IMAP connection timed out to %s", addr)}
			}
			return ComponentVerification{Success: false, Error: fmt.Sprintf("failed to connect to IMAP %s: %v", addr, err)}
		}
		tlsConn := tls.Client(rawConn, tlsConfig)
		if err := tlsConn.HandshakeContext(timeoutCtx); err != nil {
			_ = rawConn.Close()
			if timeoutCtx.Err() != nil {
				return ComponentVerification{Success: false, Error: fmt.Sprintf("IMAP connection timed out to %s", addr)}
			}
			return ComponentVerification{Success: false, Error: fmt.Sprintf("failed to connect to IMAP %s: %v", addr, err)}
		}
		conn = tlsConn
	} else {
		var dialErr error
		conn, dialErr = dialer.DialContext(timeoutCtx, "tcp", addr)
		if dialErr != nil {
			if timeoutCtx.Err() != nil {
				return ComponentVerification{Success: false, Error: fmt.Sprintf("IMAP connection timed out to %s", addr)}
			}
			return ComponentVerification{Success: false, Error: fmt.Sprintf("failed to connect to IMAP %s: %v", addr, dialErr)}
		}
	}

	// Close conn if context is canceled or times out during greeting or login
	stopCancel := context.AfterFunc(timeoutCtx, func() {
		_ = conn.Close()
	})
	defer stopCancel()

	client := imapclient.New(conn, &imapclient.Options{})
	defer func() {
		_ = client.Close()
	}()

	loginDone := make(chan error, 1)
	go func() {
		cmd := client.Login(acc.IMAPUsername, acc.IMAPPassword)
		loginDone <- cmd.Wait()
	}()

	select {
	case <-timeoutCtx.Done():
		return ComponentVerification{Success: false, Error: "IMAP authentication timed out"}
	case err := <-loginDone:
		if timeoutCtx.Err() != nil {
			return ComponentVerification{Success: false, Error: "IMAP authentication timed out"}
		}
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

	addr := net.JoinHostPort(acc.SMTPHost, strconv.Itoa(port))

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
		if timeoutCtx.Err() != nil {
			return ComponentVerification{Success: false, Error: fmt.Sprintf("SMTP connection timed out to %s", addr)}
		}
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

	dialer := &net.Dialer{Timeout: 8 * time.Second}
	var conn net.Conn

	if useTLS && port == 465 {
		// Implicit TLS on port 465
		rawConn, dialErr := dialer.DialContext(ctx, "tcp", addr)
		if dialErr != nil {
			return fmt.Errorf("TLS connection to %s failed: %w", addr, dialErr)
		}
		tlsConn := tls.Client(rawConn, tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return fmt.Errorf("TLS handshake with %s failed: %w", addr, err)
		}
		conn = tlsConn
	} else {
		// Plain connection with optional STARTTLS (port 587 / 25)
		var dialErr error
		conn, dialErr = dialer.DialContext(ctx, "tcp", addr)
		if dialErr != nil {
			return fmt.Errorf("connection to %s failed: %w", addr, dialErr)
		}
	}

	// Close conn if context is canceled or times out during subsequent blocking operations
	stopCancel := context.AfterFunc(ctx, func() {
		_ = conn.Close()
	})
	defer stopCancel()

	client, err := net_smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("SMTP handshake with %s failed: %w", addr, err)
	}
	defer func() {
		_ = client.Close()
	}()

	if useTLS && port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("STARTTLS negotiation failed: %w", err)
			}
		} else {
			return errors.New("server does not support STARTTLS")
		}
	}

	auth := net_smtp.PlainAuth("", user, pass, host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("SMTP authentication failed: %w", err)
	}

	if err := client.Quit(); err != nil {
		_ = err
	}

	return nil
}
