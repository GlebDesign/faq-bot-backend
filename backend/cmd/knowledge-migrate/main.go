package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/knowledge"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"))
	if err == nil {
		defer pool.Close()
		err = (knowledge.Store{DB: pool}).Migrate(ctx)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "knowledge migration failed:", err)
		os.Exit(1)
	}
	fmt.Println("Knowledge schema ready")
}
