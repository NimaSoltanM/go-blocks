//go:build integration

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func TestRedisOTPScriptsIntegration(t *testing.T) {
	url := requireIntegrationEnv(t, "TEST_REDIS_URL")
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal("invalid TEST_REDIS_URL")
	}
	options.MaxRetries = -1
	options.ContextTimeoutEnabled = true
	client := redis.NewClient(options)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal("Redis is unavailable")
	}

	cfg := validTestConfig()
	cfg.KeyPrefix = "gb:auth:test:" + uuid.NewString() + ":"
	cfg.OTP.Lifetime = 10 * time.Second
	cfg.OTP.ResendDelay = 5 * time.Second
	cfg.OTP.Attempts = 2
	cfg.Limits.PhoneHour = WindowLimit{Max: 5, Window: time.Minute}
	cfg.Limits.PhoneDay = WindowLimit{Max: 5, Window: time.Minute}
	cfg.Limits.ClientSend = WindowLimit{Max: 5, Window: time.Minute}
	cfg.Limits.GlobalSend = WindowLimit{Max: 20, Window: time.Minute}
	cfg.Limits.ClientVerify = WindowLimit{Max: 20, Window: time.Minute}
	store := newRedisOTPStore(client, cfg)
	t.Cleanup(func() {
		cleanupRedisPrefix(client, cfg.KeyPrefix)
	})

	phoneTag, clientTag := "phone-tag", "client-tag"
	verifier := "opaque-verifier"
	if err := store.Admit(ctx, phoneTag, clientTag, verifier); err != nil {
		t.Fatal(err)
	}
	var rate *rateLimitError
	if err := store.Admit(ctx, phoneTag, clientTag, verifier); !errors.As(err, &rate) || rate.retryAfter <= 0 {
		t.Fatalf("cooldown did not reject atomically: %v", err)
	}
	if err := store.Verify(ctx, phoneTag, clientTag, "wrong"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("first wrong code = %v", err)
	}
	if err := store.Verify(ctx, phoneTag, clientTag, "wrong"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("second wrong code = %v", err)
	}
	if err := store.Verify(ctx, phoneTag, clientTag, verifier); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("exhausted challenge accepted: %v", err)
	}

	otherPhone := "other-phone-tag"
	if err := store.Admit(ctx, otherPhone, clientTag, verifier); err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(ctx, otherPhone, clientTag, verifier); err != nil {
		t.Fatalf("correct verifier rejected: %v", err)
	}
	if err := store.Verify(ctx, otherPhone, clientTag, verifier); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("one-time verifier was reusable: %v", err)
	}
	corruptPhone := "corrupt-phone-tag"
	corruptChallenge := cfg.KeyPrefix + "otp:challenge:" + corruptPhone
	if err := client.Set(ctx, corruptChallenge, "wrong-type", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(ctx, corruptPhone, "corrupt-client", verifier); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("corrupt challenge did not fail closed: %v", err)
	}
	if exists, err := client.Exists(ctx, corruptChallenge).Result(); err != nil || exists != 0 {
		t.Fatalf("corrupt challenge was not removed: exists=%d, %v", exists, err)
	}
	stuckCooldown := cfg.KeyPrefix + "otp:cooldown:stuck-phone"
	if err := client.Set(ctx, stuckCooldown, "1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.Admit(ctx, "stuck-phone", "stuck-client", verifier); !errors.As(err, &rate) {
		t.Fatalf("non-expiring cooldown bypassed: %v", err)
	}
	if ttl, err := client.PTTL(ctx, stuckCooldown).Result(); err != nil || ttl <= 0 {
		t.Fatalf("non-expiring cooldown was not repaired: %s, %v", ttl, err)
	}
	corruptCounterPhone := "corrupt-counter-phone"
	corruptCounter := cfg.KeyPrefix + "limit:send:phone_hour:" + corruptCounterPhone
	if err := client.Set(ctx, corruptCounter, "1e2", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.Admit(ctx, corruptCounterPhone, "corrupt-counter-client", verifier); err == nil || errors.As(err, &rate) {
		t.Fatalf("corrupt counter did not fail as a dependency error: %v", err)
	}
	for _, key := range []string{
		cfg.KeyPrefix + "otp:cooldown:" + corruptCounterPhone,
		cfg.KeyPrefix + "otp:challenge:" + corruptCounterPhone,
	} {
		if exists, err := client.Exists(ctx, key).Result(); err != nil || exists != 0 {
			t.Fatalf("corrupt counter partially admitted %q: exists=%d, %v", key, exists, err)
		}
	}

	// Concurrent admission must be all-or-nothing: the cooldown admits one
	// sender and every cost counter advances exactly once.
	concurrentPhone := "concurrent-phone-tag"
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			err := store.Admit(ctx, concurrentPhone, "concurrent-client", verifier)
			if err == nil {
				admitted.Add(1)
				return
			}
			var limited *rateLimitError
			if !errors.As(err, &limited) {
				t.Errorf("concurrent admission error = %v", err)
			}
		})
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("concurrent admissions accepted %d sends", admitted.Load())
	}
	for _, key := range []string{
		cfg.KeyPrefix + "limit:send:phone_hour:" + concurrentPhone,
		cfg.KeyPrefix + "limit:send:phone_day:" + concurrentPhone,
		cfg.KeyPrefix + "limit:send:client:concurrent-client",
	} {
		if count, err := client.Get(ctx, key).Int(); err != nil || count != 1 {
			t.Fatalf("atomic counter %q = %d, %v", key, count, err)
		}
	}

	// A correct verifier is consumable by exactly one concurrent request.
	oneTimePhone := "one-time-phone-tag"
	if err := store.Admit(ctx, oneTimePhone, "one-time-client", verifier); err != nil {
		t.Fatal(err)
	}
	var verified atomic.Int32
	for range 12 {
		wg.Go(func() {
			err := store.Verify(ctx, oneTimePhone, "one-time-verify-client", verifier)
			if err == nil {
				verified.Add(1)
			} else if !errors.Is(err, ErrInvalidCode) {
				t.Errorf("concurrent verification error = %v", err)
			}
		})
	}
	wg.Wait()
	if verified.Load() != 1 {
		t.Fatalf("one-time verifier succeeded %d times", verified.Load())
	}

	expiryCfg := cfg
	expiryCfg.KeyPrefix += "expiry:"
	expiryCfg.OTP.Lifetime = 40 * time.Millisecond
	expiryCfg.OTP.ResendDelay = 20 * time.Millisecond
	expiryStore := newRedisOTPStore(client, expiryCfg)
	if err := expiryStore.Admit(ctx, "expiry-phone", "expiry-client", verifier); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if err := expiryStore.Verify(ctx, "expiry-phone", "expiry-client", verifier); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expired verifier result = %v", err)
	}
	newVerifier := "replacement-verifier"
	if err := expiryStore.Admit(ctx, "expiry-phone", "expiry-client", newVerifier); err != nil {
		t.Fatalf("replacement admission = %v", err)
	}
	if err := expiryStore.Verify(ctx, "expiry-phone", "expiry-client", verifier); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("superseded verifier result = %v", err)
	}
	if err := expiryStore.Verify(ctx, "expiry-phone", "expiry-client", newVerifier); err != nil {
		t.Fatalf("replacement verifier result = %v", err)
	}
}

func TestRedisBearerSessionIntegration(t *testing.T) {
	url := requireIntegrationEnv(t, "TEST_REDIS_URL")
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal("invalid TEST_REDIS_URL")
	}
	options.MaxRetries = -1
	options.ContextTimeoutEnabled = true
	client := redis.NewClient(options)
	cleanupOptions := *options
	cleanupClient := redis.NewClient(&cleanupOptions)
	defer cleanupClient.Close()

	cfg := validTestConfig()
	cfg.Transport = TransportBearer
	cfg.KeyPrefix = "gb:auth:test:" + uuid.NewString() + ":"
	cfg.Session.IdleTimeout = 500 * time.Millisecond
	cfg.Session.AbsoluteTimeout = 2 * time.Second
	cfg.ClientKey = func(fiber.Ctx) string { return "integration-client" }
	t.Cleanup(func() { cleanupRedisPrefix(cleanupClient, cfg.KeyPrefix) })
	fakes := newFakes()
	b := newBlock(cfg, fakes.otp, fakes.users, fakes.sms, testLogger(),
		newRedisSessionStorage(client, cfg.KeyPrefix+"session:", cfg.Timeouts.Redis))
	app := authTestApp()
	app.Post("/verify", b.VerifyCode)
	app.Get("/me", b.RequireUser, b.Me)

	login := func() string {
		resp, body := doRequest(t, app, newRequest("POST", "/verify", `{"phone":"09121234567","code":"123456"}`))
		if resp.StatusCode != 200 {
			t.Fatalf("real Redis login = %d %s", resp.StatusCode, body)
		}
		var value struct {
			SessionToken string `json:"session_token"`
		}
		if err := json.Unmarshal(body, &value); err != nil || value.SessionToken == "" {
			t.Fatalf("real Redis login body = %s, %v", body, err)
		}
		return value.SessionToken
	}
	token := login()
	key := cfg.KeyPrefix + "session:" + token
	if exists, err := client.Exists(context.Background(), key).Result(); err != nil || exists != 1 {
		t.Fatalf("session key exists=%d, %v", exists, err)
	}
	time.Sleep(100 * time.Millisecond)
	req := newRequest("GET", "/me", "")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, body := doRequest(t, app, req)
	if resp.StatusCode != 200 {
		t.Fatalf("real Redis identity = %d %s", resp.StatusCode, body)
	}
	if ttl, err := client.PTTL(context.Background(), key).Result(); err != nil || ttl < 300*time.Millisecond {
		t.Fatalf("idle TTL was not refreshed: %s, %v", ttl, err)
	}
	time.Sleep(550 * time.Millisecond)
	req = newRequest("GET", "/me", "")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, body = doRequest(t, app, req)
	if resp.StatusCode != 401 || errorCode(t, body) != "authentication_required" {
		t.Fatalf("expired session = %d %s", resp.StatusCode, body)
	}

	token = login()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	req = newRequest("GET", "/me", "")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, body = doRequest(t, app, req)
	if resp.StatusCode != 503 || errorCode(t, body) != "service_unavailable" {
		t.Fatalf("Redis outage = %d %s", resp.StatusCode, body)
	}
}

func TestPostgresUserStoreIntegration(t *testing.T) {
	url := requireIntegrationEnv(t, "TEST_DATABASE_URL")
	adminConfig, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	adminConfig.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal("PostgreSQL is unavailable")
	}

	schema := "auth_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal("could not create isolated integration schema")
	}
	t.Cleanup(func() {
		defer admin.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE")
	})

	appConfig, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	appConfig.MaxConns = 4
	appConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+identifier)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, appConfig)
	if err != nil {
		t.Fatal("could not create isolated PostgreSQL pool")
	}
	defer pool.Close()
	migration, err := os.ReadFile("migrations/000001_create_auth_users.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal("auth migration failed")
	}

	store := &postgresUserStore{pool: pool, timeout: 3 * time.Second}
	firstTime := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	first, err := store.UpsertVerified(ctx, "+989121234567", firstTime)
	if err != nil {
		t.Fatal(err)
	}
	secondTime := firstTime.Add(time.Hour)
	second, err := store.UpsertVerified(ctx, "+989121234567", secondTime)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Phone != "+989121234567" {
		t.Fatalf("upsert identity changed: first=%+v second=%+v", first, second)
	}
	if _, err := store.UpsertVerified(ctx, "+989121234567", firstTime.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	var verified time.Time
	if err := pool.QueryRow(ctx, "SELECT count(*), max(last_verified_at) FROM auth_users").Scan(&count, &verified); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !verified.Equal(secondTime) {
		t.Fatalf("upsert state or monotonic verification time count=%d verified=%s", count, verified)
	}
	if _, err := store.UpsertVerified(ctx, "+982112345678", secondTime); err == nil {
		t.Fatal("database phone constraint accepted a landline")
	}

	concurrentPhone := "+989131234567"
	ids := make(chan uuid.UUID, 12)
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			user, err := store.UpsertVerified(ctx, concurrentPhone, secondTime)
			if err != nil {
				errs <- err
				return
			}
			ids <- user.ID
		})
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent upsert: %v", err)
	}
	var expected uuid.UUID
	for id := range ids {
		if expected == uuid.Nil {
			expected = id
		} else if id != expected {
			t.Fatalf("concurrent upsert returned different IDs: %s and %s", expected, id)
		}
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM auth_users WHERE phone_e164=$1", concurrentPhone).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent row count=%d, %v", count, err)
	}

	down, err := os.ReadFile("migrations/000001_create_auth_users.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal("auth down migration failed")
	}
	var relation *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('auth_users')::text").Scan(&relation); err != nil || relation != nil {
		t.Fatalf("down migration left auth_users: %v, %v", relation, err)
	}
}

func requireIntegrationEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is required for explicit auth integration checks", key)
	}
	return value
}

func cleanupRedisPrefix(client *redis.Client, prefix string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			return
		}
		if len(keys) > 0 {
			_ = client.Unlink(ctx, keys...).Err()
		}
		cursor = next
		if cursor == 0 {
			return
		}
	}
}
