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
	"faqbot/backend/internal/models"
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
	var agents *handlers.AgentHandler
	var chat *handlers.ChatHandler
	if cfg.AuthEnabled {
		authStore := repository.NewAuthWithTrial(pool, cfg.TrialRequests, cfg.TrialDays)
		authService := service.New(authStore, authprovider.NewSupabase(cfg.SupabaseURL, cfg.SupabaseKey))
		auth = handlers.NewAuthHandler(authService, cfg.AuthOrigin, cfg.AuthAllowInsecureHTTP)
		agentStore := repository.NewAgents(pool)
		agents = handlers.NewAgentHandler(authService, service.NewAgents(pool, agentStore, service.NewAudit(repository.NewAudit()), agentStore, cfg.AgentCursorKey), cfg.AuthOrigin)
		chat = handlers.NewChatHandler(authService, service.NewChat(pool, repository.NewChat(pool), cfg.AgentCursorKey), cfg.AuthOrigin)
		recoveryCtx, stopRecovery := context.WithCancel(ctx)
		recoveryDone := make(chan struct{})
		go func() {
			defer close(recoveryDone)
			recoverQuota(recoveryCtx, repository.NewQuota(pool), logger)
		}()
		defer func() {
			stopRecovery()
			<-recoveryDone
		}()
	}
	server := &http.Server{
		Handler:           handlers.NewHandler(health, auth, agents, chat),
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

func recoverQuota(ctx context.Context, quota *repository.Quota, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var after *models.ReservationKey
	var horizon time.Time
	for ctx.Err() == nil {
		batchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var err error
		if horizon.IsZero() {
			horizon, err = quota.RecoveryHorizon(batchCtx)
		}
		if err == nil {
			after, err = recoverQuotaBatch(batchCtx, quota, after, horizon)
		}
		if after == nil {
			horizon = time.Time{}
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Error("quota recovery failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func recoverQuotaBatch(ctx context.Context, quota *repository.Quota, after *models.ReservationKey, horizon time.Time) (*models.ReservationKey, error) {
	const batchSize = 32
	keys, err := quota.ExpiredReservations(ctx, batchSize, after, horizon)
	if err != nil {
		return after, err
	}
	for _, key := range keys {
		if ctx.Err() != nil {
			return after, err
		}
		err = errors.Join(err, quota.RecoverReservation(ctx, key))
		after = &key
	}
	if len(keys) < batchSize {
		after = nil
	}
	return after, err
}
