package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jpuckett/EmailMCP/internal/config"
	"github.com/jpuckett/EmailMCP/internal/crypto"
	"github.com/jpuckett/EmailMCP/internal/server"
	"github.com/jpuckett/EmailMCP/internal/store"
)

func main() {
	lvl := new(slog.LevelVar)
	lvl.Set(slog.LevelInfo)
	if v := os.Getenv("EMAILMCP_LOG_LEVEL"); v != "" {
		_ = lvl.UnmarshalText([]byte(v)) // "debug", "info", "warn", "error"
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level:     lvl,
		AddSource: true,
	}))
	slog.SetDefault(logger)

	if err := config.ValidateMasterKeyPresence(); err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.New(ctx, cfg.DBPath)
	if err != nil {
		logger.Error("failed to open store", "error", err, "path", cfg.DBPath)
		os.Exit(1)
	}
	defer st.Close()

	cryptoSvc := crypto.MustNewFromEnv()

	srv, err := server.New(ctx, st, cryptoSvc, cfg)
	if err != nil {
		logger.Error("failed to create mcp server", "error", err)
		os.Exit(1)
	}

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

	logger.Info("EmailMCP server starting", "addr", cfg.ListenAddr)
	logger.Info("Use EMAILMCP_MASTER_KEY for encryption (32-byte base64)")

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}

	logger.Info("server stopped")
}
