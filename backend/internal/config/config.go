package config

import (
	"errors"
	"net"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL     string
	MigrationsPath  string
	Environment     string
	ListenAddr      string
	ShutdownTimeout time.Duration
}

func Load() (Config, error) {
	c := Config{
		Environment:     "development",
		ListenAddr:      "127.0.0.1:8080",
		ShutdownTimeout: 10 * time.Second,
		MigrationsPath:  "./migrations",
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
	return c, nil
}
