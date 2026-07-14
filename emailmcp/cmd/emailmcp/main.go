package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/config"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/server"
)

func main() {
	transportFlag := flag.String("transport", "", "transport mode: http or stdio")
	flag.Parse()

	lvl := new(slog.LevelVar)
	lvl.Set(slog.LevelInfo)
	if v := os.Getenv("EMAILMCP_LOG_LEVEL"); v != "" {
		_ = lvl.UnmarshalText([]byte(v)) // "debug", "info", "warn", "error"
	}
	// Log to stderr because stdout is used for MCP stdio transport.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:     lvl,
		AddSource: true,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if *transportFlag != "" {
		cfg.Transport = *transportFlag
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Resolve CONFIG_API_URL from SSM when not set in the environment.
	cfg.FetchRemoteDefaults(ctx)

	if cfg.ConfigAPIURL == "" {
		logger.Error("CONFIG_API_URL is required (set env or ensure SSM parameter /emailmcp/config-api/url is available)")
		os.Exit(1)
	}

	logger.Info("config api configured; running startup health check", "url", cfg.ConfigAPIURL)
	healthClient := config.NewClient(cfg.ConfigAPIURL, cfg.ApplicationID)
	if err := healthClient.EnsureHealthy(ctx, logger); err != nil {
		logger.Error("config api health check failed; refusing to start",
			"error", err,
			"url", cfg.ConfigAPIURL,
		)
		os.Exit(1)
	}

	srv, err := server.New(ctx, cfg)
	if err != nil {
		logger.Error("failed to create mcp server", "error", err)
		os.Exit(1)
	}

	if cfg.Transport == "stdio" {
		logger.Info("EmailMCP server starting", "transport", "stdio")
		if err := srv.ServeStdio(ctx); err != nil {
			logger.Error("stdio server error", "error", err)
			os.Exit(1)
		}
	} else {
		handler := srv.HTTPHandler()

		httpServer := &http.Server{
			Addr:    cfg.ListenAddr,
			Handler: handler,
		}

		// Graceful shutdown
		go func() {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			<-sigCh
			logger.Info("shutting down...")

			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutdownCancel()

			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				logger.Error("http shutdown error", "error", err)
			}
		}()

		logger.Info("EmailMCP server starting", "addr", cfg.ListenAddr, "transport", "http")

		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}

	logger.Info("server stopped")
}
