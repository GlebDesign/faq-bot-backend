package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"faqbot/backend/internal/config"
	"faqbot/backend/internal/migration"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(); err != nil {
		logger.Error("migration stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("migrations are up to date")
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return migration.Apply(ctx, cfg.DatabaseURL, cfg.MigrationsPath)
}
