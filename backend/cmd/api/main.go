package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"faqbot/backend/internal/authprovider"
	"faqbot/backend/internal/config"
	"faqbot/backend/internal/db"
	"faqbot/backend/internal/handlers"
	"faqbot/backend/internal/repository"
	"faqbot/backend/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("API stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) (err error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.AuthEnabled && cfg.Environment != "development" {
		return errors.New("authentication configuration is required outside development")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var pool *sql.DB
	if cfg.DatabaseURL != "" {
		pool, err = db.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := pool.Close(); closeErr != nil {
				err = errors.Join(err, db.WrapError("close API database", closeErr))
			}
		}()
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = listener.Close() }()
	health := &handlers.Health{}
	if pool != nil {
		health.Check = pool.PingContext
	}
	var auth *handlers.AuthHandler
	if cfg.AuthEnabled {
		auth = handlers.NewAuthHandler(service.New(repository.NewAuth(pool), authprovider.NewSupabase(cfg.SupabaseURL, cfg.SupabaseKey)), cfg.AuthOrigin, cfg.AuthAllowInsecureHTTP)
	}
	server := &http.Server{
		Handler:           handlers.NewHandler(health, auth),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveErr := make(chan error, 1)
	health.Start()
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("API started", "address", listener.Addr().String(), "environment", cfg.Environment)
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	stop()
	health.Drain()
	logger.Info("API shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("API stopped")
	return nil
}
