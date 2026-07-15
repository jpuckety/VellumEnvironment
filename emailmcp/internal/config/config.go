package config

import (
	"context"
	"log/slog"
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
	// JWTSecret signs the short-lived session JWTs issued to MCP clients after
	// Google sign-in. When empty, a random key is generated at startup (sessions
	// will not survive a restart or span multiple instances).
	JWTSecret string
	// OAuthRedirectAllowlist restricts HTTPS OAuth redirect_uris when non-empty.
	// Entries are comma-separated hostnames (example.com, *.example.com) or
	// https origins/URIs. Empty means the allowlist is not enforced (any HTTPS
	// host is still accepted subject to other redirect rules). Loopback HTTP and
	// custom schemes are always allowed for desktop MCP clients.
	OAuthRedirectAllowlist []string
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
		JWTSecret:              getEnv("EMAILMCP_JWT_SECRET", ""),
		OAuthRedirectAllowlist: parseCSVEnv("OAUTH_REDIRECT_ALLOWLIST"),
	}

	if cfg.IMAPMaxConnsPerAccount < 1 {
		slog.Warn("EMAILMCP_IMAP_MAX_CONNS < 1; clamping to 1",
			"configured", cfg.IMAPMaxConnsPerAccount,
		)
		cfg.IMAPMaxConnsPerAccount = 1
	}

	// Never log GoogleClientSecret or other secrets.
	slog.Info("configuration loaded from environment",
		"listen_addr", cfg.ListenAddr,
		"imap_max_conns", cfg.IMAPMaxConnsPerAccount,
		"imap_idle_timeout", cfg.IMAPConnIdleTimeout,
		"smtp_timeout", cfg.SMTPDefaultTimeout,
		"log_level", cfg.LogLevel,
		"transport", cfg.Transport,
		"application_id", cfg.ApplicationID,
		"config_api_url_set", cfg.ConfigAPIURL != "",
		"config_api_url", cfg.ConfigAPIURL,
		"google_client_id_set", cfg.GoogleClientID != "",
		"google_client_secret_set", cfg.GoogleClientSecret != "",
		"public_base_url", cfg.PublicBaseURL,
		"jwt_secret_set", cfg.JWTSecret != "",
		"oauth_redirect_allowlist_enforced", len(cfg.OAuthRedirectAllowlist) > 0,
		"oauth_redirect_allowlist_entries", len(cfg.OAuthRedirectAllowlist),
		"aws_region", os.Getenv("AWS_REGION"),
	)

	return cfg, nil
}

// FetchRemoteDefaults attempts to load missing configuration from AWS SSM if available.
func (c *Config) FetchRemoteDefaults(ctx context.Context) {
	logger := slog.Default().With("op", "fetch_remote_defaults")

	if c.ConfigAPIURL != "" {
		logger.DebugContext(ctx, "skipping SSM lookup; CONFIG_API_URL already set",
			"config_api_url", c.ConfigAPIURL,
		)
		return
	}

	region := os.Getenv("AWS_REGION")
	if region == "" {
		logger.DebugContext(ctx, "skipping SSM lookup; AWS_REGION unset and CONFIG_API_URL empty")
		return
	}

	const paramName = "/emailmcp/config-api/url"
	logger.InfoContext(ctx, "resolving CONFIG_API_URL from SSM",
		"parameter", paramName,
		"region", region,
	)

	start := time.Now()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		logger.WarnContext(ctx, "failed to load AWS config for SSM lookup",
			"region", region,
			"error", err,
			"elapsed", time.Since(start),
		)
		return
	}

	client := ssm.NewFromConfig(cfg)
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String(paramName),
	})
	if err != nil {
		logger.WarnContext(ctx, "SSM GetParameter failed",
			"parameter", paramName,
			"region", region,
			"error", err,
			"elapsed", time.Since(start),
		)
		return
	}
	if out.Parameter == nil || out.Parameter.Value == nil || *out.Parameter.Value == "" {
		logger.WarnContext(ctx, "SSM parameter missing or empty",
			"parameter", paramName,
			"elapsed", time.Since(start),
		)
		return
	}

	c.ConfigAPIURL = *out.Parameter.Value
	logger.InfoContext(ctx, "CONFIG_API_URL resolved from SSM",
		"parameter", paramName,
		"config_api_url", c.ConfigAPIURL,
		"elapsed", time.Since(start),
	)
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseCSVEnv splits a comma-separated env var into trimmed non-empty entries.
func parseCSVEnv(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
		slog.Warn("invalid integer env var; using default",
			"key", key,
			"value", v,
			"default", def,
		)
	}
	return def
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		slog.Warn("invalid duration env var; using default",
			"key", key,
			"value", v,
			"default", def,
		)
	}
	return def
}
