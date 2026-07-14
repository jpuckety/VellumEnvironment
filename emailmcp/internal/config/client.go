package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

// Default health-check retry settings for Lambda cold starts.
const (
	healthCheckAttempts = 5
	healthCheckBackoff  = 2 * time.Second

	// googleIDTokenHeader carries the Google ID token for Config API user auth.
	// It must not use Authorization: SigV4 overwrites that header when the
	// Function URL uses AWS_IAM auth.
	googleIDTokenHeader = "X-Google-ID-Token"
)

// ErrConfigNotFound is returned when the Config API has no config for the user.
var ErrConfigNotFound = errors.New("config not found")

type Client struct {
	BaseURL       string
	ApplicationID string
	HTTPClient    *http.Client
	Signer        *v4.Signer
	AWSConfig     *aws.Config
	// Logger is optional; when nil, slog.Default() is used.
	Logger *slog.Logger
}

func NewClient(baseURL, appID string) *Client {
	// Try to load AWS config for SigV4 signing if needed
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	logger := slog.Default()
	var awsCfg *aws.Config
	region := os.Getenv("AWS_REGION")
	if region == "" {
		logger.Debug("config api client: AWS_REGION unset; SigV4 signing disabled")
	} else {
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
		if err != nil {
			logger.Warn("config api client: failed to load AWS config; SigV4 signing disabled",
				"region", region,
				"error", err,
			)
		} else {
			awsCfg = &cfg
			logger.Debug("config api client: AWS config loaded for SigV4", "region", region)
		}
	}

	c := &Client{
		BaseURL:       strings.TrimRight(baseURL, "/"),
		ApplicationID: appID,
		HTTPClient:    &http.Client{Timeout: 10 * time.Second},
		Signer:        v4.NewSigner(),
		AWSConfig:     awsCfg,
		Logger:        logger,
	}
	logger.Info("config api client created",
		"base_url", c.BaseURL,
		"application_id", c.ApplicationID,
		"sigv4_enabled", c.AWSConfig != nil,
	)
	return c
}

func (c *Client) log() *slog.Logger {
	if c != nil && c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// sign applies SigV4 signing when AWS credentials are available (Function URL IAM auth).
func (c *Client) sign(ctx context.Context, req *http.Request, payload []byte) error {
	if c.AWSConfig == nil {
		c.log().DebugContext(ctx, "config api: skipping SigV4 (no AWS config)",
			"method", req.Method,
			"url", req.URL.String(),
		)
		return nil
	}
	creds, err := c.AWSConfig.Credentials.Retrieve(ctx)
	if err != nil {
		c.log().ErrorContext(ctx, "config api: failed to retrieve AWS credentials for SigV4",
			"method", req.Method,
			"url", req.URL.String(),
			"error", err,
		)
		return fmt.Errorf("failed to retrieve AWS credentials: %w", err)
	}
	sum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(sum[:])
	if err := c.Signer.SignHTTP(ctx, creds, req, payloadHash, "lambda", c.AWSConfig.Region, time.Now()); err != nil {
		c.log().ErrorContext(ctx, "config api: SigV4 signing failed",
			"method", req.Method,
			"url", req.URL.String(),
			"region", c.AWSConfig.Region,
			"error", err,
		)
		return fmt.Errorf("failed to sign request: %w", err)
	}
	c.log().DebugContext(ctx, "config api: request signed with SigV4",
		"method", req.Method,
		"url", req.URL.String(),
		"region", c.AWSConfig.Region,
		"payload_bytes", len(payload),
	)
	return nil
}

func (c *Client) configURL(userID, accountID string) string {
	if accountID == "" {
		return fmt.Sprintf("%s/configs/%s/%s", c.BaseURL, c.ApplicationID, userID)
	}
	return fmt.Sprintf("%s/configs/%s/%s/%s", c.BaseURL, c.ApplicationID, userID, accountID)
}

// setGoogleAuth attaches the Google ID token for Config API application-level auth.
// When SigV4 is enabled, Authorization is reserved for AWS IAM and the token is
// only sent on X-Google-ID-Token. Without SigV4, Bearer Authorization is also set
// for simpler local/dev setups that do not use Function URL IAM auth.
func (c *Client) setGoogleAuth(req *http.Request, googleToken string) {
	req.Header.Set(googleIDTokenHeader, googleToken)
	if c.AWSConfig == nil {
		req.Header.Set("Authorization", "Bearer "+googleToken)
	}
}

// CheckHealth calls GET /health on the config API and returns an error if it is not healthy.
func (c *Client) CheckHealth(ctx context.Context) error {
	url := c.BaseURL + "/health"
	logger := c.log().With("op", "config_api_health", "url", url)
	logger.DebugContext(ctx, "starting health check")

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logger.ErrorContext(ctx, "failed to build health request", "error", err)
		return err
	}

	if err := c.sign(ctx, req, nil); err != nil {
		return err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		logger.WarnContext(ctx, "health request failed", "error", err, "elapsed", time.Since(start))
		return fmt.Errorf("health request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		logger.WarnContext(ctx, "health check non-OK status",
			"status", resp.StatusCode,
			"body", strings.TrimSpace(string(body)),
			"elapsed", time.Since(start),
		)
		return fmt.Errorf("health check returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	logger.DebugContext(ctx, "health check OK", "elapsed", time.Since(start))
	return nil
}

// EnsureHealthy retries CheckHealth until success or attempts are exhausted.
// Results of each attempt are logged via logger.
func (c *Client) EnsureHealthy(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = c.log()
	}

	var lastErr error
	for attempt := 1; attempt <= healthCheckAttempts; attempt++ {
		start := time.Now()
		err := c.CheckHealth(ctx)
		elapsed := time.Since(start)

		if err == nil {
			logger.Info("config api health check succeeded",
				"url", c.BaseURL+"/health",
				"attempt", attempt,
				"elapsed", elapsed,
			)
			return nil
		}

		lastErr = err
		logger.Warn("config api health check failed",
			"url", c.BaseURL+"/health",
			"attempt", attempt,
			"max_attempts", healthCheckAttempts,
			"elapsed", elapsed,
			"error", err,
		)

		if attempt == healthCheckAttempts {
			break
		}

		select {
		case <-ctx.Done():
			logger.Warn("config api health check canceled",
				"attempt", attempt,
				"error", ctx.Err(),
			)
			return fmt.Errorf("config api health check canceled: %w", ctx.Err())
		case <-time.After(healthCheckBackoff):
			logger.Debug("config api health check retrying after backoff",
				"backoff", healthCheckBackoff,
				"next_attempt", attempt+1,
			)
		}
	}

	return fmt.Errorf("config api not healthy after %d attempts: %w", healthCheckAttempts, lastErr)
}

func (c *Client) GetUserConfig(ctx context.Context, googleToken, userID, accountID string) (*types.Account, error) {
	url := c.configURL(userID, accountID)
	logger := c.log().With(
		"op", "config_api_get",
		"user_id", userID,
		"account_id", accountID,
		"application_id", c.ApplicationID,
		"url", url,
	)
	logger.DebugContext(ctx, "fetching user config")

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logger.ErrorContext(ctx, "failed to build get request", "error", err)
		return nil, err
	}
	c.setGoogleAuth(req, googleToken)

	if err := c.sign(ctx, req, nil); err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		logger.ErrorContext(ctx, "get user config request failed", "error", err, "elapsed", time.Since(start))
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		logger.InfoContext(ctx, "user config not found",
			"status", resp.StatusCode,
			"elapsed", time.Since(start),
		)
		return nil, ErrConfigNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		logger.ErrorContext(ctx, "get user config API error",
			"status", resp.StatusCode,
			"body", strings.TrimSpace(string(body)),
			"elapsed", time.Since(start),
		)
		return nil, fmt.Errorf("config api error: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	acc := &types.Account{}
	if err := json.NewDecoder(resp.Body).Decode(acc); err != nil {
		logger.ErrorContext(ctx, "failed to decode user config response", "error", err, "elapsed", time.Since(start))
		return nil, err
	}

	// Config API stores a single password used for IMAP (and typically SMTP).
	if acc.SMTPPassword == "" {
		acc.SMTPPassword = acc.IMAPPassword
	}
	if acc.ID == "" {
		acc.ID = userID
	}

	logger.InfoContext(ctx, "user config loaded",
		"account_id", acc.ID,
		"name", acc.Name,
		"imap_host", acc.IMAPHost,
		"imap_port", acc.IMAPPort,
		"imap_username", acc.IMAPUsername,
		"imap_use_tls", acc.IMAPUseTLS,
		"smtp_host", acc.SMTPHost,
		"smtp_port", acc.SMTPPort,
		"smtp_username", acc.SMTPUsername,
		"smtp_use_tls", acc.SMTPUseTLS,
		"from_address", acc.FromAddress,
		"has_imap_password", acc.IMAPPassword != "",
		"has_smtp_password", acc.SMTPPassword != "",
		"elapsed", time.Since(start),
	)
	return acc, nil
}

// PutUserConfig creates or replaces an email account configuration.
func (c *Client) PutUserConfig(ctx context.Context, googleToken, userID string, acc *types.Account) error {
	url := c.configURL(userID, acc.ID)
	logger := c.log().With(
		"op", "config_api_put",
		"user_id", userID,
		"account_id", acc.ID,
		"application_id", c.ApplicationID,
		"url", url,
	)
	if acc != nil {
		logger = logger.With(
			"account_id", acc.ID,
			"name", acc.Name,
			"imap_host", acc.IMAPHost,
			"imap_port", acc.IMAPPort,
			"smtp_host", acc.SMTPHost,
			"smtp_port", acc.SMTPPort,
			"has_imap_password", acc.IMAPPassword != "",
			"has_distinct_smtp_password", acc.SMTPPassword != "" && acc.SMTPPassword != acc.IMAPPassword,
		)
	}
	logger.DebugContext(ctx, "putting user config")

	// Password is stored in Secrets Manager by the Config API; remaining fields go to DynamoDB.
	payload := map[string]any{
		"id":            acc.ID,
		"name":          acc.Name,
		"imap_host":     acc.IMAPHost,
		"imap_port":     acc.IMAPPort,
		"imap_username": acc.IMAPUsername,
		"imap_use_tls":  acc.IMAPUseTLS,
		"smtp_host":     acc.SMTPHost,
		"smtp_port":     acc.SMTPPort,
		"smtp_username": acc.SMTPUsername,
		"smtp_use_tls":  acc.SMTPUseTLS,
		"from_address":  acc.FromAddress,
		"password":      acc.IMAPPassword,
	}
	if acc.SMTPPassword != "" && acc.SMTPPassword != acc.IMAPPassword {
		payload["smtp_password"] = acc.SMTPPassword
	}

	body, err := json.Marshal(payload)
	if err != nil {
		logger.ErrorContext(ctx, "failed to marshal put payload", "error", err)
		return err
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		logger.ErrorContext(ctx, "failed to build put request", "error", err)
		return err
	}
	c.setGoogleAuth(req, googleToken)
	req.Header.Set("Content-Type", "application/json")

	if err := c.sign(ctx, req, body); err != nil {
		return err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		logger.ErrorContext(ctx, "put user config request failed", "error", err, "elapsed", time.Since(start))
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		logger.ErrorContext(ctx, "put user config API error",
			"status", resp.StatusCode,
			"body", strings.TrimSpace(string(respBody)),
			"payload_bytes", len(body),
			"elapsed", time.Since(start),
		)
		return fmt.Errorf("config api error: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	logger.InfoContext(ctx, "user config saved",
		"payload_bytes", len(body),
		"elapsed", time.Since(start),
	)
	return nil
}

// DeleteUserConfig removes an email account configuration.
func (c *Client) DeleteUserConfig(ctx context.Context, googleToken, userID, accountID string) error {
	url := c.configURL(userID, accountID)
	logger := c.log().With(
		"op", "config_api_delete",
		"user_id", userID,
		"account_id", accountID,
		"application_id", c.ApplicationID,
		"url", url,
		"has_google_token", googleToken != "",
	)
	logger.DebugContext(ctx, "deleting account config")

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		logger.ErrorContext(ctx, "failed to build delete request", "error", err)
		return err
	}
	c.setGoogleAuth(req, googleToken)

	if err := c.sign(ctx, req, nil); err != nil {
		return err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		logger.ErrorContext(ctx, "delete account config request failed", "error", err, "elapsed", time.Since(start))
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		logger.InfoContext(ctx, "account config not found for delete",
			"status", resp.StatusCode,
			"elapsed", time.Since(start),
		)
		return ErrConfigNotFound
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		logger.ErrorContext(ctx, "delete account config API error",
			"status", resp.StatusCode,
			"body", strings.TrimSpace(string(respBody)),
			"elapsed", time.Since(start),
		)
		return fmt.Errorf("config api error: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	logger.InfoContext(ctx, "account config deleted", "elapsed", time.Since(start))
	return nil
}

// ListUserConfigs returns all email accounts for the authenticated user.
func (c *Client) ListUserConfigs(ctx context.Context, googleToken, userID string) ([]*types.Account, error) {
	url := c.configURL(userID, "")
	logger := c.log().With(
		"op", "config_api_list",
		"user_id", userID,
		"application_id", c.ApplicationID,
		"url", url,
	)
	logger.DebugContext(ctx, "listing user configs")

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logger.ErrorContext(ctx, "failed to build list request", "error", err)
		return nil, err
	}
	c.setGoogleAuth(req, googleToken)

	if err := c.sign(ctx, req, nil); err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		logger.ErrorContext(ctx, "list user configs request failed", "error", err, "elapsed", time.Since(start))
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		logger.ErrorContext(ctx, "list user configs API error",
			"status", resp.StatusCode,
			"body", strings.TrimSpace(string(respBody)),
			"elapsed", time.Since(start),
		)
		return nil, fmt.Errorf("config api error: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var accs []*types.Account
	if err := json.NewDecoder(resp.Body).Decode(&accs); err != nil {
		logger.ErrorContext(ctx, "failed to decode list response", "error", err, "elapsed", time.Since(start))
		return nil, err
	}

	logger.InfoContext(ctx, "user configs loaded", "count", len(accs), "elapsed", time.Since(start))
	return accs, nil
}
