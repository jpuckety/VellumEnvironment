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
)

// ErrConfigNotFound is returned when the Config API has no config for the user.
var ErrConfigNotFound = errors.New("config not found")

type Client struct {
	BaseURL       string
	ApplicationID string
	HTTPClient    *http.Client
	Signer        *v4.Signer
	AWSConfig     *aws.Config
}

func NewClient(baseURL, appID string) *Client {
	// Try to load AWS config for SigV4 signing if needed
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var awsCfg *aws.Config
	region := os.Getenv("AWS_REGION")
	if region != "" {
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
		if err == nil {
			awsCfg = &cfg
		}
	}

	return &Client{
		BaseURL:       strings.TrimRight(baseURL, "/"),
		ApplicationID: appID,
		HTTPClient:    &http.Client{Timeout: 10 * time.Second},
		Signer:        v4.NewSigner(),
		AWSConfig:     awsCfg,
	}
}

// sign applies SigV4 signing when AWS credentials are available (Function URL IAM auth).
func (c *Client) sign(ctx context.Context, req *http.Request, payload []byte) error {
	if c.AWSConfig == nil {
		return nil
	}
	creds, err := c.AWSConfig.Credentials.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve AWS credentials: %w", err)
	}
	sum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(sum[:])
	if err := c.Signer.SignHTTP(ctx, creds, req, payloadHash, "lambda", c.AWSConfig.Region, time.Now()); err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}
	return nil
}

func (c *Client) configURL(userID string) string {
	return fmt.Sprintf("%s/configs/%s/%s", c.BaseURL, c.ApplicationID, userID)
}

// CheckHealth calls GET /health on the config API and returns an error if it is not healthy.
func (c *Client) CheckHealth(ctx context.Context) error {
	url := c.BaseURL + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	if err := c.sign(ctx, req, nil); err != nil {
		return err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("health request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// EnsureHealthy retries CheckHealth until success or attempts are exhausted.
// Results of each attempt are logged via logger.
func (c *Client) EnsureHealthy(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
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
			return fmt.Errorf("config api health check canceled: %w", ctx.Err())
		case <-time.After(healthCheckBackoff):
		}
	}

	return fmt.Errorf("config api not healthy after %d attempts: %w", healthCheckAttempts, lastErr)
}

func (c *Client) GetUserConfig(ctx context.Context, googleToken, userID string) (*types.Account, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.configURL(userID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+googleToken)

	if err := c.sign(ctx, req, nil); err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrConfigNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("config api error: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	acc := &types.Account{}
	if err := json.NewDecoder(resp.Body).Decode(acc); err != nil {
		return nil, err
	}

	// Config API stores a single password used for IMAP (and typically SMTP).
	if acc.SMTPPassword == "" {
		acc.SMTPPassword = acc.IMAPPassword
	}
	if acc.ID == "" {
		acc.ID = userID
	}

	return acc, nil
}

// PutUserConfig creates or replaces the authenticated user's account configuration.
func (c *Client) PutUserConfig(ctx context.Context, googleToken, userID string, acc *types.Account) error {
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
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.configURL(userID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+googleToken)
	req.Header.Set("Content-Type", "application/json")

	if err := c.sign(ctx, req, body); err != nil {
		return err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("config api error: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// DeleteUserConfig removes the authenticated user's account configuration.
func (c *Client) DeleteUserConfig(ctx context.Context, googleToken, userID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.configURL(userID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+googleToken)

	if err := c.sign(ctx, req, nil); err != nil {
		return err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrConfigNotFound
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("config api error: %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}
