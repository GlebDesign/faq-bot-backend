package migration

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"faqbot/backend/internal/db"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func Apply(ctx context.Context, dsn, path string) (err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return db.WrapError("invalid migrations path", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return db.WrapError("read migrations directory", err)
	}
	if !info.IsDir() {
		return errors.New("migrations path is not a directory")
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return db.WrapError("connect migration database", err)
	}
	defer func() { err = errors.Join(err, db.WrapError("close migration pool", pool.Close())) }()
	conn, err := pool.Conn(ctx)
	if err != nil {
		return db.WrapError("cannot acquire migration connection", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "SET lock_timeout = '5s'; SET statement_timeout = '30s'"); err != nil {
		return db.WrapError("cannot configure migration session", err)
	}
	driver, err := postgres.WithConnection(ctx, conn, &postgres.Config{StatementTimeout: 30 * time.Second})
	if err != nil {
		return db.WrapError("cannot initialize migration version table", err)
	}
	source := (&url.URL{Scheme: "file", Path: abs}).String()
	runner, err := migrate.NewWithDatabaseInstance(source, "postgres", driver)
	if err != nil {
		return db.WrapError("cannot initialize migration source", errors.Join(err, driver.Close()))
	}
	defer func() {
		sourceErr, dbErr := runner.Close()
		err = errors.Join(err, db.WrapError("close migration runner", errors.Join(sourceErr, dbErr)))
	}()
	runner.LockTimeout = 5 * time.Second

	_, _, versionErr := runner.Version()
	if versionErr != nil && !errors.Is(versionErr, migrate.ErrNilVersion) {
		return db.WrapError("read migration version", versionErr)
	}
	if errors.Is(versionErr, migrate.ErrNilVersion) {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return db.WrapError("cannot read migrations directory", err)
		}
		hasUp := false
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
				hasUp = true
				break
			}
		}
		if !hasUp {
			return ctx.Err()
		}
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			select {
			case runner.GracefulStop <- true:
			case <-done:
			}
		case <-done:
		}
	}()
	if err := runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return db.WrapError("migration failed: check schema_migrations and repair dirty state before retry", err)
	}
	return ctx.Err()
}
