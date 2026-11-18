package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	for _, key := range []string{"APP_ENV", "HTTP_LISTEN_ADDR", "HTTP_SHUTDOWN_TIMEOUT", "DATABASE_URL", "MIGRATIONS_PATH"} {
		value, exists := os.LookupEnv(key)
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
		_ = os.Unsetenv(key)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Environment != "development" || c.ListenAddr != "127.0.0.1:8080" || c.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestConfig(t *testing.T) {
	for _, c := range []struct{ key, value string }{
		{"APP_ENV", ""}, {"APP_ENV", "invalid"}, {"HTTP_LISTEN_ADDR", ""},
		{"HTTP_LISTEN_ADDR", "localhost"}, {"HTTP_LISTEN_ADDR", ":0"},
		{"HTTP_LISTEN_ADDR", ":65536"}, {"HTTP_LISTEN_ADDR", "bad.invalid:8080"},
		{"HTTP_SHUTDOWN_TIMEOUT", ""}, {"HTTP_SHUTDOWN_TIMEOUT", "wrong"},
		{"HTTP_SHUTDOWN_TIMEOUT", "0s"}, {"HTTP_SHUTDOWN_TIMEOUT", "-1s"}, {"HTTP_SHUTDOWN_TIMEOUT", "2m"},
	} {
		t.Run(c.key+"/"+c.value, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv("HTTP_LISTEN_ADDR", "127.0.0.1:8080")
			t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "10s")
			t.Setenv(c.key, c.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.key) {
				t.Fatalf("expected configuration error for %s", c.key)
			}
		})
	}
	t.Run("explicit", func(t *testing.T) {
		t.Setenv("APP_ENV", "production")
		t.Setenv("DATABASE_URL", "postgres://fixture.invalid/faq")
		t.Setenv("HTTP_LISTEN_ADDR", "[::1]:9090")
		t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "2s")
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.Environment != "production" || c.ListenAddr != "[::1]:9090" || c.ShutdownTimeout != 2*time.Second {
			t.Fatal("explicit config ignored")
		}
	})
}

func TestDatabaseConfig(t *testing.T) {
	for _, key := range []string{"DATABASE_URL", "MIGRATIONS_PATH"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			t.Setenv(key, "")
			if _, err := Load(); err == nil {
				t.Fatalf("empty %s accepted", key)
			}
		})
	}
	t.Run("production_requires_database", func(t *testing.T) {
		old, exists := os.LookupEnv("DATABASE_URL")
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv("DATABASE_URL", old)
			} else {
				_ = os.Unsetenv("DATABASE_URL")
			}
		})
		_ = os.Unsetenv("DATABASE_URL")
		t.Setenv("APP_ENV", "production")
		if _, err := Load(); err == nil {
			t.Fatal("production started without database")
		}
	})
}
