package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOpenFailure(t *testing.T) {
	for _, dsn := range []string{"", "postgres://user:DO_NOT_LOG@host:invalid/faq", "postgres://user:DO_NOT_LOG@127.0.0.1:1/faq?sslmode=disable"} {
		pool, err := Open(context.Background(), dsn)
		if pool != nil || err == nil {
			t.Fatal("expected connection error")
		}
		if strings.Contains(err.Error(), "DO_NOT_LOG") {
			t.Fatal("connection error leaked credentials")
		}
	}
}

func TestTransactions(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL integration")
	}
	pool, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	}()
	if pool.Stats().MaxOpenConnections != 25 {
		t.Fatal("pool limit not set")
	}
	table := fmt.Sprintf("tx_probe_%d", time.Now().UnixNano())
	if _, err := pool.Exec("CREATE TABLE " + table + " (value integer NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pool.Exec("DROP TABLE " + table); err != nil {
			t.Error(err)
		}
	}()
	insert := func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1)")
		return err
	}
	if err := WithTx(context.Background(), pool, insert); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("rollback")
	err = WithTx(context.Background(), pool, func(ctx context.Context, tx *sql.Tx) error {
		if err := insert(ctx, tx); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal("callback error lost")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("callback panic swallowed")
			}
		}()
		_ = WithTx(context.Background(), pool, func(ctx context.Context, tx *sql.Tx) error {
			if err := insert(ctx, tx); err != nil {
				t.Fatal(err)
			}
			panic("rollback panic")
		})
	}()
	var count int
	if err := pool.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rollback failed, rows=%d", count)
	}
	var timeout string
	if err := pool.QueryRow("SHOW statement_timeout").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != "5s" {
		t.Fatalf("statement timeout=%s", timeout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := pool.ExecContext(ctx, "SELECT pg_sleep(1)"); err == nil {
		t.Fatal("query ignored cancellation")
	}
}

func TestOpenPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(ctx, "postgres://fixture:DO_NOT_LOG@127.0.0.1:1/faq?sslmode=disable")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
	if strings.Contains(err.Error(), "DO_NOT_LOG") {
		t.Fatal("credentials exposed")
	}
}
