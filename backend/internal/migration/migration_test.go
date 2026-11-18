package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"faqbot/backend/internal/db"
)

func database(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL integration")
	}
	admin, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("migration_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	pool, err := db.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
		if err := admin.Close(); err != nil {
			t.Error(err)
		}
	})
	return u.String(), pool
}

func files(t *testing.T, second string) string {
	t.Helper()
	dir := t.TempDir()
	for name, sql := range map[string]string{
		"000001_create.up.sql": "BEGIN; SELECT pg_sleep(0.1); CREATE TABLE probe (id integer PRIMARY KEY); COMMIT;",
		"000002_insert.up.sql": second,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(sql), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestApplyRepeatAndConcurrent(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprint(concurrent), func(t *testing.T) {
			dsn, pool := database(t)
			dir := files(t, "BEGIN; INSERT INTO probe VALUES (1); COMMIT;")
			if concurrent {
				results := make(chan error, 2)
				start := make(chan struct{})
				for range 2 {
					go func() { <-start; results <- Apply(context.Background(), dsn, dir) }()
				}
				close(start)
				for range 2 {
					if err := <-results; err != nil {
						t.Fatal(err)
					}
				}
			} else if err := Apply(context.Background(), dsn, dir); err != nil {
				t.Fatal(err)
			}
			if err := Apply(context.Background(), dsn, dir); err != nil {
				t.Fatal(err)
			}
			var version, count int
			var dirty bool
			if err := pool.QueryRow("SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
				t.Fatal(err)
			}
			if version != 2 || dirty {
				t.Fatalf("version=%d dirty=%v", version, dirty)
			}
			if err := pool.QueryRow("SELECT count(*) FROM probe").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatal("migration applied more than once")
			}
		})
	}
}

func TestDirtyMigration(t *testing.T) {
	dsn, pool := database(t)
	dir := files(t, "BEGIN; INSERT INTO probe VALUES (1); SELECT 1/0; COMMIT;")
	for range 2 {
		if err := Apply(context.Background(), dsn, dir); err == nil {
			t.Fatal("dirty migration silently accepted")
		}
	}
	var version, count int
	var dirty bool
	if err := pool.QueryRow("SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 2 || !dirty {
		t.Fatalf("version=%d dirty=%v", version, dirty)
	}
	if err := pool.QueryRow("SELECT count(*) FROM probe").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration partially committed")
	}
}

func TestEmptyAndMissingSource(t *testing.T) {
	if err := Apply(context.Background(), "", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing path accepted")
	}
	dsn, pool := database(t)
	if err := Apply(context.Background(), dsn, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("empty source fabricated a schema version")
	}
}

func TestMissingAppliedFiles(t *testing.T) {
	dsn, _ := database(t)
	if err := Apply(context.Background(), dsn, files(t, "INSERT INTO probe VALUES (1);")); err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), dsn, t.TempDir()); err == nil {
		t.Fatal("missing history accepted for versioned database")
	}
}

func TestMissingPathPreservesCause(t *testing.T) {
	err := Apply(context.Background(), "", filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("filesystem cause lost: %v", err)
	}
}
