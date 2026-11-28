package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultTrialRequests = 25
	DefaultTrialDays     = 14
)

type Config struct {
	AgentCursorKey        []byte
	AuthEnabled           bool
	SupabaseURL           string
	SupabaseKey           string
	AuthOrigin            string
	AuthAllowInsecureHTTP bool
	DatabaseURL           string
	MigrationsPath        string
	TrialRequests         int
	TrialDays             int
	Environment           string
	ListenAddr            string
	ShutdownTimeout       time.Duration
}

func Load() (Config, error) {
	c := Config{
		Environment:     "development",
		ListenAddr:      "127.0.0.1:8080",
		ShutdownTimeout: 10 * time.Second,
		MigrationsPath:  "./migrations",
		TrialRequests:   DefaultTrialRequests,
		TrialDays:       DefaultTrialDays,
	}
	if value, ok := os.LookupEnv("APP_ENV"); ok {
		c.Environment = value
	}
	if c.Environment != "development" && c.Environment != "staging" && c.Environment != "production" {
		return Config{}, errors.New("APP_ENV must be development, staging or production")
	}
	if value, ok := os.LookupEnv("HTTP_LISTEN_ADDR"); ok {
		c.ListenAddr = value
	}
	host, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return Config{}, errors.New("HTTP_LISTEN_ADDR must be host:port")
	}
	if host != "" && host != "localhost" && net.ParseIP(host) == nil {
		return Config{}, errors.New("HTTP_LISTEN_ADDR host must be an IP address or localhost")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Config{}, errors.New("HTTP_LISTEN_ADDR port must be 1..65535")
	}
	if value, ok := os.LookupEnv("HTTP_SHUTDOWN_TIMEOUT"); ok {
		c.ShutdownTimeout, err = time.ParseDuration(value)
		if err != nil || c.ShutdownTimeout <= 0 || c.ShutdownTimeout > time.Minute {
			return Config{}, errors.New("HTTP_SHUTDOWN_TIMEOUT must be a duration greater than 0 and at most 1m")
		}
	}
	if value, ok := os.LookupEnv("DATABASE_URL"); ok {
		if value == "" {
			return Config{}, errors.New("DATABASE_URL must not be empty when set")
		}
		c.DatabaseURL = value
	}
	if c.Environment != "development" && c.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required outside development")
	}
	if value, ok := os.LookupEnv("MIGRATIONS_PATH"); ok {
		if value == "" {
			return Config{}, errors.New("MIGRATIONS_PATH must not be empty")
		}
		c.MigrationsPath = value
	}
	for _, setting := range []struct {
		name string
		dest *int
	}{{"TRIAL_REQUESTS", &c.TrialRequests}, {"TRIAL_DAYS", &c.TrialDays}} {
		if value, ok := os.LookupEnv(setting.name); ok {
			n, err := strconv.ParseInt(value, 10, 32)
			if err != nil || n <= 0 {
				return Config{}, errors.New(setting.name + " must be a positive 32-bit integer")
			}
			*setting.dest = int(n)
		}
	}
	if c.TrialDays > 36500 {
		return Config{}, errors.New("TRIAL_DAYS must be at most 36500")
	}
	c.SupabaseURL = os.Getenv("SUPABASE_URL")
	c.SupabaseKey = os.Getenv("SUPABASE_KEY")
	c.AuthOrigin = os.Getenv("AUTH_ORIGIN")
	c.AgentCursorKey = []byte(os.Getenv("AGENT_CURSOR_KEY"))
	raw, insecureSet := os.LookupEnv("AUTH_ALLOW_INSECURE_HTTP")
	if insecureSet {
		c.AuthAllowInsecureHTTP, err = strconv.ParseBool(raw)
		if err != nil {
			return Config{}, errors.New("AUTH_ALLOW_INSECURE_HTTP must be a boolean")
		}
	}
	_, urlSet := os.LookupEnv("SUPABASE_URL")
	_, keySet := os.LookupEnv("SUPABASE_KEY")
	_, originSet := os.LookupEnv("AUTH_ORIGIN")
	c.AuthEnabled = urlSet || keySet || originSet || insecureSet
	if c.AuthEnabled {
		if len(c.AgentCursorKey) < 32 {
			return Config{}, errors.New("AGENT_CURSOR_KEY must contain at least 32 bytes when Auth is enabled")
		}
		if c.SupabaseURL == "" || strings.TrimSpace(c.SupabaseKey) == "" || c.AuthOrigin == "" || c.DatabaseURL == "" {
			return Config{}, errors.New("SUPABASE_URL, SUPABASE_KEY, AUTH_ORIGIN and DATABASE_URL must be configured together")
		}
		if c.AuthAllowInsecureHTTP && c.Environment != "development" {
			return Config{}, errors.New("AUTH_ALLOW_INSECURE_HTTP is only allowed in development")
		}
		for _, item := range []struct {
			key, value string
			origin     bool
		}{{"SUPABASE_URL", c.SupabaseURL, false}, {"AUTH_ORIGIN", c.AuthOrigin, true}} {
			u, parseErr := url.Parse(item.value)
			if parseErr != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (item.origin && u.Path != "") {
				return Config{}, errors.New(item.key + " must be an origin URL without credentials, path, query or fragment")
			}
			if u.Scheme != "https" {
				ip := net.ParseIP(u.Hostname())
				if !c.AuthAllowInsecureHTTP || u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
					return Config{}, errors.New(item.key + " requires HTTPS; local HTTP requires explicit development configuration")
				}
			}
		}
	}
	return c, nil
}
