package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds application configuration.
type Config struct {
	// Server
	ListenAddr string
	// DB
	DBPath string
	// Crypto master key is loaded via crypto package from EMAILMCP_MASTER_KEY

	// IMAP pool settings
	IMAPMaxConnsPerAccount int
	IMAPConnIdleTimeout    time.Duration

	// SMTP
	SMTPDefaultTimeout time.Duration

	// Logging
	LogLevel string

	// Transport
	Transport string // "http" or "stdio"

	// Auth & Hybrid Storage
	ApplicationID  string
	ConfigAPIURL   string
	GoogleClientID string
}

// Load reads configuration from environment with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:             getEnv("EMAILMCP_LISTEN_ADDR", ":8080"),
		DBPath:                 getEnv("EMAILMCP_DB_PATH", "./emailmcp.db"),
		IMAPMaxConnsPerAccount: getEnvInt("EMAILMCP_IMAP_MAX_CONNS", 4),
		IMAPConnIdleTimeout:    getEnvDuration("EMAILMCP_IMAP_IDLE_TIMEOUT", 5*time.Minute),
		SMTPDefaultTimeout:     getEnvDuration("EMAILMCP_SMTP_TIMEOUT", 30*time.Second),
		LogLevel:               getEnv("EMAILMCP_LOG_LEVEL", "info"),
		Transport:              getEnv("EMAILMCP_TRANSPORT", "http"),
		ApplicationID:          getEnv("APPLICATION_ID", "default"),
		ConfigAPIURL:           getEnv("CONFIG_API_URL", ""),
		GoogleClientID:         getEnv("GOOGLE_CLIENT_ID", ""),
	}

	if cfg.IMAPMaxConnsPerAccount < 1 {
		cfg.IMAPMaxConnsPerAccount = 1
	}

	return cfg, nil
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

// ValidateMasterKeyPresence is a helper that can be called at startup.
func ValidateMasterKeyPresence() error {
	if os.Getenv("EMAILMCP_MASTER_KEY") == "" {
		return fmt.Errorf("EMAILMCP_MASTER_KEY is required for credential encryption")
	}
	return nil
}
