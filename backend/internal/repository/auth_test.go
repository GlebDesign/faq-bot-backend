package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"faqbot/backend/internal/db"
	"faqbot/backend/internal/service"
)

func testAuth(t *testing.T) *Auth {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	pool, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := pool
	t.Cleanup(func() {
		if err := admin.Close(); err != nil {
			t.Error(err)
		}
	})
	schema := "auth_test_" + uuid()
	schema = "\"" + schema + "\""
	if _, err := pool.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema[1:len(schema)-1])
	u.RawQuery = q.Encode()
	scoped, err := db.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := scoped.Close(); err != nil {
			t.Error(err)
		}
	})
	pool = scoped
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000001_identity.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	return NewAuth(pool)
}

func TestBootstrapAndSessionIsolation(t *testing.T) {
	a := testAuth(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			identity, err := a.Bootstrap(ctx, "subject-a")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- identity.AccountID
		}()
	}
	wg.Wait()
	close(ids)
	var accountID string
	for id := range ids {
		if accountID != "" && id != accountID {
			t.Fatal("duplicate tenant")
		}
		accountID = id
	}
	var count int
	if err := a.pool.QueryRow(`SELECT count(*) FROM accounts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("accounts=%d err=%v", count, err)
	}
	first, err := a.Bootstrap(ctx, "subject-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Bootstrap(ctx, "subject-b")
	if err != nil {
		t.Fatal(err)
	}
	if first.UserID == "subject-a" || first.AccountID == second.AccountID || first.Role != "owner" || first.AuditRead {
		t.Fatal("invalid identity")
	}
	now := time.Now().UTC()
	hash := sha256.Sum256([]byte("session"))
	if err := a.CreateSession(ctx, hash[:], "csrf", &first, now, now.Add(8*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s, err := a.GetSession(ctx, hash[:], now)
	if err != nil || s.Identity.AccountID != first.AccountID {
		t.Fatalf("session %v %v", s, err)
	}
	if _, err := a.pool.Exec(`UPDATE sessions SET account_id=$1 WHERE token_hash=$2`, second.AccountID, hash[:]); err == nil {
		t.Fatal("foreign membership session accepted by SQL")
	}
	if _, err := a.pool.Exec(`DELETE FROM account_members WHERE user_id=$1`, first.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GetSession(ctx, hash[:], now); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatal("removed membership accepted", err)
	}
	if _, err := a.Bootstrap(ctx, "subject-a"); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatal("membership restored", err)
	}
	if err := a.pool.QueryRow(`SELECT count(*) FROM accounts`).Scan(&count); err != nil || count != 2 {
		t.Fatal("bootstrap created another tenant", err)
	}
}

func TestSessionExpiryRevocationAndCleanup(t *testing.T) {
	a := testAuth(t)
	ctx := context.Background()
	now := time.Now().UTC()
	identity, err := a.Bootstrap(ctx, "subject")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		offset time.Duration
		revoke bool
	}{{"idle", 31 * time.Minute, false}, {"absolute", 9 * time.Hour, false}, {"revoked", time.Second, true}} {
		hash := sha256.Sum256([]byte(tc.name))
		if err := a.CreateSession(ctx, hash[:], "csrf", &identity, now, now.Add(8*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if tc.revoke {
			if err := a.RevokeSession(ctx, hash[:], now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := a.GetSession(ctx, hash[:], now.Add(tc.offset)); !errors.Is(err, service.ErrUnauthorized) {
			t.Fatal(tc.name, err)
		}
	}
	if err := a.CleanupSessions(ctx, now.Add(9*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := a.pool.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal(count)
	}
}

func TestSessionSlidingExpiry(t *testing.T) {
	a := testAuth(t)
	ctx := context.Background()
	identity, err := a.Bootstrap(ctx, "sliding-subject")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	expiry := now.Add(time.Hour)
	hash := sha256.Sum256([]byte("sliding-session"))
	if err := a.CreateSession(ctx, hash[:], "csrf", &identity, now, expiry); err != nil {
		t.Fatal(err)
	}
	for _, elapsed := range []time.Duration{20 * time.Minute, 40 * time.Minute, 59 * time.Minute} {
		s, err := a.GetSession(ctx, hash[:], now.Add(elapsed))
		if err != nil || !s.ExpiresAt.Equal(expiry) {
			t.Fatalf("elapsed=%v expiry=%v err=%v", elapsed, s.ExpiresAt, err)
		}
	}
	var idle time.Time
	if err := a.pool.QueryRow(`SELECT idle_expires_at FROM sessions WHERE token_hash=$1`, hash[:]).Scan(&idle); err != nil {
		t.Fatal(err)
	}
	if !idle.Equal(expiry) {
		t.Fatalf("idle expiry=%v want=%v", idle, expiry)
	}
	if _, err := a.GetSession(ctx, hash[:], expiry); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatal("absolute expiry extended", err)
	}
	if err := a.RevokeSession(ctx, hash[:], expiry); !errors.Is(err, service.ErrUnauthorized) {
		t.Fatal("expired session consumed", err)
	}
}
