package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

type Client struct {
	BaseURL       string
	ApplicationID string
	HTTPClient    *http.Client
}

func NewClient(baseURL, appID string) *Client {
	return &Client{
		BaseURL:       baseURL,
		ApplicationID: appID,
		HTTPClient:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) GetUserConfig(ctx context.Context, googleToken, userID string) (*types.Account, error) {
	url := fmt.Sprintf("%s/configs/%s/%s", c.BaseURL, c.ApplicationID, userID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+googleToken)

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
