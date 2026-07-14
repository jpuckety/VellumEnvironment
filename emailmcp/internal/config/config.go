package config

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// Config holds application configuration.
type Config struct {
	// Server
	ListenAddr string

	// IMAP pool settings
	IMAPMaxConnsPerAccount int
	IMAPConnIdleTimeout    time.Duration

	// SMTP
	SMTPDefaultTimeout time.Duration

	// Logging
	LogLevel string

	// Transport
	Transport string // "http" or "stdio"

	// Auth & remote account storage (Config API)
	ApplicationID  string
	ConfigAPIURL   string
	GoogleClientID string
	// GoogleClientSecret is required for HTTP mode OAuth (authorization code
	// exchange with Google). Not used for ID-token verification itself.
	GoogleClientSecret string
	// PublicBaseURL is the externally reachable origin of this server
	// (e.g. https://emailmcp.ecg.co). Used for OAuth metadata and redirect URIs.
	PublicBaseURL string
}

// Load reads configuration from environment with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:             getEnv("EMAILMCP_LISTEN_ADDR", ":8080"),
		IMAPMaxConnsPerAccount: getEnvInt("EMAILMCP_IMAP_MAX_CONNS", 4),
		IMAPConnIdleTimeout:    getEnvDuration("EMAILMCP_IMAP_IDLE_TIMEOUT", 5*time.Minute),
		SMTPDefaultTimeout:     getEnvDuration("EMAILMCP_SMTP_TIMEOUT", 30*time.Second),
		LogLevel:               getEnv("EMAILMCP_LOG_LEVEL", "info"),
		Transport:              getEnv("EMAILMCP_TRANSPORT", "http"),
		ApplicationID:          getEnv("APPLICATION_ID", "default"),
		ConfigAPIURL:           getEnv("CONFIG_API_URL", ""),
		GoogleClientID:         getEnv("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret:     getEnv("GOOGLE_CLIENT_SECRET", ""),
		PublicBaseURL:          strings.TrimRight(getEnv("PUBLIC_BASE_URL", ""), "/"),
	}

	if cfg.IMAPMaxConnsPerAccount < 1 {
		cfg.IMAPMaxConnsPerAccount = 1
	}

	return cfg, nil
}

// FetchRemoteDefaults attempts to load missing configuration from AWS SSM if available.
func (c *Config) FetchRemoteDefaults(ctx context.Context) {
	if c.ConfigAPIURL != "" {
		return
	}

	region := os.Getenv("AWS_REGION")
	if region == "" {
		return
	}

	// Load AWS config
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return
	}

	client := ssm.NewFromConfig(cfg)
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String("/emailmcp/config-api/url"),
	})
	if err == nil && out.Parameter != nil && out.Parameter.Value != nil {
		c.ConfigAPIURL = *out.Parameter.Value
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
