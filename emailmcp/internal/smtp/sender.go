package smtp

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/smtp"
	"time"

	emailpkg "github.com/jordan-wright/email"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/netutil"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

// Attachment size limits to prevent memory exhaustion via base64 payloads.
const (
	maxAttachmentBytes      = 10 << 20 // 10 MiB per attachment
	maxTotalAttachmentBytes = 25 << 20 // 25 MiB total decoded attachments
)

// Config for SMTP sender.
type Config struct {
	DefaultTimeout time.Duration
	Logger         *slog.Logger
}

// Sender manages SMTP operations.
type Sender struct {
	cfg Config
}

// NewSender creates a new SMTP sender.
func NewSender(cfg Config) *Sender {
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 30 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Sender{cfg: cfg}
}

// SendEmail sends an email using the specified account's SMTP settings.
func (s *Sender) SendEmail(ctx context.Context, acc *types.Account, input types.SendEmailInput) error {
	if acc == nil {
		return errors.New("account is required")
	}
	if err := netutil.ValidatePublicHost(acc.SMTPHost); err != nil {
		return fmt.Errorf("smtp host not allowed: %w", err)
	}
	if err := netutil.RequireTLSUnlessLocalhost(acc.SMTPHost, acc.SMTPUseTLS, "SMTP"); err != nil {
		return err
	}

	smtpPass := acc.SMTPPassword
	if smtpPass == "" {
		smtpPass = acc.IMAPPassword
	}
	if smtpPass == "" {
		return errors.New("smtp password is required")
	}

	from := input.From
	if from == "" {
		from = acc.FromAddress
	}
	if from == "" {
		from = acc.SMTPUsername
	}
	if from == "" {
		return errors.New("no from address available")
	}

	e := emailpkg.NewEmail()
	e.From = from
	e.To = input.To
	e.Cc = input.Cc
	e.Bcc = input.Bcc
	e.Subject = input.Subject
	if input.Text != "" {
		e.Text = []byte(input.Text)
	}
	if input.HTML != "" {
		e.HTML = []byte(input.HTML)
	}

	var totalAttach int
	for _, att := range input.Attachments {
		// Reject oversized base64 before decoding (base64 expands ~4/3).
		if len(att.Data) > (maxAttachmentBytes*4)/3+4 {
			return fmt.Errorf("attachment %s exceeds maximum size of %d bytes", att.Filename, maxAttachmentBytes)
		}
		data, err := base64.StdEncoding.DecodeString(att.Data)
		if err != nil {
			return fmt.Errorf("decode attachment %s: %w", att.Filename, err)
		}
		if len(data) > maxAttachmentBytes {
			return fmt.Errorf("attachment %s exceeds maximum size of %d bytes", att.Filename, maxAttachmentBytes)
		}
		totalAttach += len(data)
		if totalAttach > maxTotalAttachmentBytes {
			return fmt.Errorf("total attachment size exceeds maximum of %d bytes", maxTotalAttachmentBytes)
		}
		ct := att.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		e.Attachments = append(e.Attachments, &emailpkg.Attachment{
			Filename:    att.Filename,
			ContentType: ct,
			Header:      make(map[string][]string),
			Content:     data,
		})
	}

	addr := fmt.Sprintf("%s:%d", acc.SMTPHost, acc.SMTPPort)

	// Choose auth and send method
	auth := smtp.PlainAuth("", acc.SMTPUsername, smtpPass, acc.SMTPHost)

	timeoutCtx, cancel := context.WithTimeout(ctx, s.cfg.DefaultTimeout)
	defer cancel()

	done := make(chan error, 1)

	tlsConfig := &tls.Config{
		ServerName: acc.SMTPHost,
		MinVersion: tls.VersionTLS12,
	}

	go func() {
		var sendErr error
		if acc.SMTPUseTLS {
			// 465 = implicit TLS; 587/25 = plain then STARTTLS
			if acc.SMTPPort == 465 {
				sendErr = e.SendWithTLS(addr, auth, tlsConfig)
			} else {
				sendErr = e.SendWithStartTLS(addr, auth, tlsConfig)
			}
		} else {
			// Non-TLS is only permitted for localhost (checked above).
			sendErr = e.Send(addr, auth)
		}
		done <- sendErr
	}()

	select {
	case <-timeoutCtx.Done():
		return fmt.Errorf("smtp send timeout: %w", timeoutCtx.Err())
	case err := <-done:
		if err != nil {
			return fmt.Errorf("send email: %w", err)
		}
		s.cfg.Logger.Info("email sent", "account_id", acc.ID, "to", input.To, "subject", input.Subject)
		return nil
	}
}
