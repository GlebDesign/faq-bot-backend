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

	"faqbot/backend/internal/config"
	"faqbot/backend/internal/db"
	"faqbot/backend/internal/handlers"
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
				err = errors.Join(err, fmt.Errorf("close API database: %w", closeErr))
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
	server := &http.Server{
		Handler:           handlers.NewHandler(health),
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
