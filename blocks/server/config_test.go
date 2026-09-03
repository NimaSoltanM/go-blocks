package server

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func configFrom(values map[string]string) (Config, error) {
	return loadConfig(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
}

func TestConfigDefaultsAndOverrides(t *testing.T) {
	cfg, err := configFrom(nil)
	if err != nil || cfg != DefaultConfig() {
		t.Fatalf("default configuration: %+v, %v", cfg, err)
	}
	cfg, err = configFrom(map[string]string{
		"HTTP_ADDR": "[::1]:9000", "LOG_LEVEL": "DEBUG", "HTTP_BODY_LIMIT": "512",
		"HTTP_READ_TIMEOUT": "2s", "HTTP_WRITE_TIMEOUT": "3s", "HTTP_IDLE_TIMEOUT": "4s",
		"HTTP_REQUEST_TIMEOUT": "50ms", "HTTP_SHUTDOWN_TIMEOUT": "5s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != "[::1]:9000" || cfg.LogLevel != slog.LevelDebug || cfg.BodyLimit != 512 ||
		cfg.ReadTimeout != 2*time.Second || cfg.WriteTimeout != 3*time.Second ||
		cfg.IdleTimeout != 4*time.Second || cfg.RequestTimeout != 50*time.Millisecond ||
		cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("overrides lost: %+v", cfg)
	}
}

func TestConfigRejectsInvalidEnvironment(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"HTTP_ADDR", ""}, {"HTTP_ADDR", "localhost"}, {"HTTP_ADDR", ":65536"},
		{"HTTP_ADDR", "bad host:3000"}, {"HTTP_ADDR", ":-1"},
		{"LOG_LEVEL", ""}, {"LOG_LEVEL", "verbose"},
		{"HTTP_BODY_LIMIT", "-1"}, {"HTTP_BODY_LIMIT", "one"},
		{"HTTP_READ_TIMEOUT", ""}, {"HTTP_READ_TIMEOUT", "0s"},
		{"HTTP_WRITE_TIMEOUT", "-1s"}, {"HTTP_WRITE_TIMEOUT", "5s"},
		{"HTTP_IDLE_TIMEOUT", "0"}, {"HTTP_REQUEST_TIMEOUT", "forever"},
		{"HTTP_SHUTDOWN_TIMEOUT", "-5s"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			_, err := configFrom(map[string]string{tc.key: tc.value})
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("expected actionable %s error, got %v", tc.key, err)
			}
		})
	}
}
