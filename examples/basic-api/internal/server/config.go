// Package server provides a copyable Fiber HTTP server foundation.
package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address         string
	LogLevel        slog.Level
	BodyLimit       int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
}

func DefaultConfig() Config {
	return Config{
		Address:         "127.0.0.1:8080",
		LogLevel:        slog.LevelInfo,
		BodyLimit:       1 << 20,
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    15 * time.Second,
		IdleTimeout:     60 * time.Second,
		RequestTimeout:  10 * time.Second,
		ShutdownTimeout: 15 * time.Second,
	}
}

// LoadConfig reads environment variables. Unset variables use defaults; an
// explicitly empty or malformed value is an error. It does not load .env files.
func LoadConfig() (Config, error) {
	return loadConfig(os.LookupEnv)
}

func loadConfig(lookup func(string) (string, bool)) (Config, error) {
	cfg := DefaultConfig()
	if value, ok := lookup("HTTP_ADDR"); ok {
		cfg.Address = value
	}
	if value, ok := lookup("LOG_LEVEL"); ok {
		switch strings.ToLower(value) {
		case "debug":
			cfg.LogLevel = slog.LevelDebug
		case "info":
			cfg.LogLevel = slog.LevelInfo
		case "warn":
			cfg.LogLevel = slog.LevelWarn
		case "error":
			cfg.LogLevel = slog.LevelError
		default:
			return Config{}, errors.New("LOG_LEVEL must be debug, info, warn, or error")
		}
	}
	if value, ok := lookup("HTTP_BODY_LIMIT"); ok {
		n, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, errors.New("HTTP_BODY_LIMIT must be a positive integer in bytes")
		}
		cfg.BodyLimit = n
	}
	for _, setting := range []struct {
		name   string
		target *time.Duration
	}{
		{"HTTP_READ_TIMEOUT", &cfg.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", &cfg.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", &cfg.IdleTimeout},
		{"HTTP_REQUEST_TIMEOUT", &cfg.RequestTimeout},
		{"HTTP_SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout},
	} {
		if value, ok := lookup(setting.name); ok {
			duration, err := time.ParseDuration(value)
			if err != nil {
				return Config{}, fmt.Errorf("%s must be a positive Go duration, such as 5s", setting.name)
			}
			*setting.target = duration
		}
	}
	return cfg, cfg.Validate()
}

// Validate also protects applications that construct Config directly.
func (cfg Config) Validate() error {
	host, port, err := net.SplitHostPort(cfg.Address)
	if err != nil || strings.ContainsAny(host, " \t\r\n/") {
		return errors.New("HTTP_ADDR must be a TCP address such as 127.0.0.1:8080 or :8080")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return errors.New("HTTP_ADDR port must be between 0 and 65535")
	}
	if cfg.BodyLimit <= 0 {
		return errors.New("HTTP_BODY_LIMIT must be positive")
	}
	for _, setting := range []struct {
		name  string
		value time.Duration
	}{
		{"HTTP_READ_TIMEOUT", cfg.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", cfg.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", cfg.IdleTimeout},
		{"HTTP_REQUEST_TIMEOUT", cfg.RequestTimeout},
		{"HTTP_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout},
	} {
		if setting.value <= 0 {
			return fmt.Errorf("%s must be positive", setting.name)
		}
	}
	if cfg.WriteTimeout <= cfg.RequestTimeout {
		return errors.New("HTTP_WRITE_TIMEOUT must exceed HTTP_REQUEST_TIMEOUT to allow a timeout response")
	}
	return nil
}
