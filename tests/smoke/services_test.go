//go:build integration

package smoke

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const serviceTimeout = 5 * time.Second

// These are opt-in, read-only connectivity checks. Configuration errors omit
// the original error because driver errors can contain connection credentials.
func TestPostgresConnection(t *testing.T) {
	connectionURL := requireEnv(t, "TEST_DATABASE_URL")
	config, err := pgxpool.ParseConfig(connectionURL)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL; check its format")
	}
	config.MaxConns = 1
	config.MinConns = 0
	config.ConnConfig.ConnectTimeout = serviceTimeout

	ctx, cancel := context.WithTimeout(context.Background(), serviceTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("could not create PostgreSQL pool; check test configuration")
	}
	defer pool.Close()

	var result int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&result); err != nil {
		t.Fatal("PostgreSQL SELECT 1 failed; check test credentials, TLS, and connectivity")
	}
	if result != 1 {
		t.Fatalf("PostgreSQL SELECT 1 returned %d", result)
	}
}

func TestRedisConnection(t *testing.T) {
	connectionURL := requireEnv(t, "TEST_REDIS_URL")
	options, err := redis.ParseURL(connectionURL)
	if err != nil {
		t.Fatal("invalid TEST_REDIS_URL; check its format")
	}
	options.DialTimeout = serviceTimeout
	options.ReadTimeout = serviceTimeout
	options.WriteTimeout = serviceTimeout
	options.ContextTimeoutEnabled = true
	options.MaxRetries = -1
	client := redis.NewClient(options)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), serviceTimeout)
	defer cancel()
	result, err := client.Ping(ctx).Result()
	if err != nil {
		t.Fatal("Redis PING failed; check test credentials, TLS, and connectivity")
	}
	if result != "PONG" {
		t.Fatalf("Redis PING returned %q", result)
	}
}

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is required for explicit integration checks", key)
	}
	return value
}
