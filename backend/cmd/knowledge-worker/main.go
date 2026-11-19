package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/knowledge"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		slog.Error("database unavailable")
		os.Exit(1)
	}
	defer pool.Close()
	store := knowledge.Store{DB: pool}
	for ctx.Err() == nil {
		broker, err := knowledge.Dial(os.Getenv("AI_AMQP_URL"))
		if err != nil {
			slog.Warn("broker unavailable")
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		for ctx.Err() == nil {
			if err = store.Dispatch(ctx, broker); err != nil {
				break
			}
			var handled bool
			handled, err = store.ConsumeResult(ctx, broker)
			if err != nil {
				break
			}
			if !handled {
				select {
				case <-ctx.Done():
				case <-time.After(200 * time.Millisecond):
				}
			}
		}
		broker.Close()
		if ctx.Err() == nil {
			slog.Warn("knowledge coordinator reconnecting")
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
}
