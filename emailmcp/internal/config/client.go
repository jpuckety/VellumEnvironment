package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

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
		BaseURL:       baseURL,
		ApplicationID: appID,
		HTTPClient:    &http.Client{Timeout: 10 * time.Second},
		Signer:        v4.NewSigner(),
		AWSConfig:     awsCfg,
	}
}

func (c *Client) GetUserConfig(ctx context.Context, googleToken, userID string) (*types.Account, error) {
	url := fmt.Sprintf("%s/configs/%s/%s", c.BaseURL, c.ApplicationID, userID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+googleToken)

	// Sign the request if AWS configuration is available
	if c.AWSConfig != nil {
		creds, err := c.AWSConfig.Credentials.Retrieve(ctx)
		if err == nil {
			// For GET requests, the payload hash is the hash of an empty string
			emptyHash := sha256.Sum256([]byte(""))
			payloadHash := hex.EncodeToString(emptyHash[:])

			err = c.Signer.SignHTTP(ctx, creds, req, payloadHash, "lambda", c.AWSConfig.Region, time.Now())
			if err != nil {
				// Log error but continue? Or fail?
				// Better to fail if we expect IAM auth
				return nil, fmt.Errorf("failed to sign request: %w", err)
			}
		}
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("config not found for user %s", userID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("config api error: %s", resp.Status)
	}

	acc := &types.Account{}
	if err := json.NewDecoder(resp.Body).Decode(acc); err != nil {
		return nil, err
	}

	return acc, nil
}
