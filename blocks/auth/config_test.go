package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type nilNormalizer struct{}

func (*nilNormalizer) Normalize(string) (string, error) { return "", nil }

type nilSMS struct{}

func (*nilSMS) SendCode(context.Context, SMSCode) error { return nil }

func validTestConfig() Config {
	cfg := DefaultConfig()
	cfg.Pepper = []byte(strings.Repeat("p", 32))
	return cfg
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestDefaultConfigValidWithPepper(t *testing.T) {
	cfg := validTestConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigValidationRejectsUnsafeAndPanicProneValues(t *testing.T) {
	tests := map[string]func(*Config){
		"unknown transport":           func(c *Config) { c.Transport = "both" },
		"short pepper":                func(c *Config) { c.Pepper = []byte("short") },
		"control in key prefix":       func(c *Config) { c.KeyPrefix = "auth:\n" },
		"missing key suffix":          func(c *Config) { c.KeyPrefix = "auth" },
		"sub-millisecond Redis TTL":   func(c *Config) { c.Limits.GlobalSend.Window = time.Nanosecond },
		"counter exceeds Lua integer": func(c *Config) { c.Limits.GlobalSend.Max = 1 << 40 },
		"resend exceeds lifetime":     func(c *Config) { c.OTP.ResendDelay = c.OTP.Lifetime + time.Second },
		"absolute below idle":         func(c *Config) { c.Session.AbsoluteTimeout = c.Session.IdleTimeout - time.Second },
		"typed nil normalizer":        func(c *Config) { var n *nilNormalizer; c.PhoneNormalizer = n },
		"insecure without opt-in":     func(c *Config) { c.Cookie.Secure = false },
		"insecure host cookie":        func(c *Config) { c.Cookie.Secure = false; c.Cookie.AllowInsecure = true },
		"same cookies":                func(c *Config) { c.Cookie.CSRFName = c.Cookie.SessionName },
		"invalid trusted origin":      func(c *Config) { c.Cookie.TrustedOrigins = []string{"https://example.com/path"} },
		"origin credentials":          func(c *Config) { c.Cookie.TrustedOrigins = []string{"https://u:p@example.com"} },
		"origin wildcard in hostname": func(c *Config) { c.Cookie.TrustedOrigins = []string{"https://foo.*.example.com"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validTestConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestConfigAcceptsFiberCSRFOriginForms(t *testing.T) {
	for _, origin := range []string{"https://example.com", "https://example.com/", "https://*.example.com", "http://localhost:8080"} {
		cfg := validTestConfig()
		cfg.Cookie.TrustedOrigins = []string{origin}
		if err := cfg.Validate(); err != nil {
			t.Errorf("origin %q rejected: %v", origin, err)
		}
	}
}

func TestCookieAndBearerMiddlewareAccessors(t *testing.T) {
	storage := newMemoryStorage()
	fakes := newFakes()
	cookie := newBlock(validTestConfig(), fakes.otp, fakes.users, fakes.sms, testLogger(), storage)
	if _, err := cookie.SessionMiddleware(); err != nil {
		t.Fatal(err)
	}
	if _, err := cookie.CSRFMiddleware(); err != nil {
		t.Fatal(err)
	}

	cfg := validTestConfig()
	cfg.Transport = TransportBearer
	bearer := newBlock(cfg, fakes.otp, fakes.users, fakes.sms, testLogger(), newMemoryStorage())
	if _, err := bearer.SessionMiddleware(); err != ErrCookieTransportRequired {
		t.Fatalf("session accessor error = %v", err)
	}
	if _, err := bearer.CSRFMiddleware(); err != ErrCookieTransportRequired {
		t.Fatalf("CSRF accessor error = %v", err)
	}
}

func TestBlockCopiesMutableConfiguration(t *testing.T) {
	cfg := validTestConfig()
	cfg.Cookie.TrustedOrigins = []string{"https://example.com"}
	fakes := newFakes()
	b := newBlock(cfg, fakes.otp, fakes.users, fakes.sms, testLogger(), newMemoryStorage())
	cfg.Pepper[0] = 'x'
	cfg.Cookie.TrustedOrigins[0] = "https://changed.example"
	if b.cfg.Pepper[0] == 'x' || b.cfg.Cookie.TrustedOrigins[0] != "https://example.com" {
		t.Fatal("block retained mutable configuration slices")
	}
}

type badOutputNormalizer struct{}

func (badOutputNormalizer) Normalize(string) (string, error) { return "not-an-e164-phone", nil }

func TestCustomNormalizerMustReturnCanonicalIranianE164(t *testing.T) {
	cfg := validTestConfig()
	cfg.PhoneNormalizer = badOutputNormalizer{}
	fakes := newFakes()
	b := newBlock(cfg, fakes.otp, fakes.users, fakes.sms, testLogger(), newMemoryStorage())
	if _, err := b.normalizePhone("anything"); !errors.Is(err, ErrInvalidPhone) {
		t.Fatalf("invalid custom normalizer output accepted: %v", err)
	}
}

func TestClientKeyUsesDirectPeerAndRejectsEmptyOverride(t *testing.T) {
	app := fiber.New()
	app.Get("/", func(c fiber.Ctx) error {
		value, err := clientKey(c, nil)
		if err != nil || value == "" {
			t.Fatalf("default client key = %q, %v", value, err)
		}
		if _, err := clientKey(c, func(fiber.Ctx) string { return "" }); err == nil {
			t.Fatal("empty custom client key accepted")
		}
		return c.SendStatus(204)
	})
	req := newRequest("GET", "/", "")
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsMissingAndTypedNilDependencies(t *testing.T) {
	cfg := validTestConfig()
	if _, err := New(cfg, Dependencies{}); err == nil {
		t.Fatal("missing dependencies accepted")
	}
	var sender *nilSMS
	if _, err := New(cfg, Dependencies{SMS: sender}); err == nil {
		t.Fatal("typed nil SMS sender accepted")
	}
	if _, err := New(cfg, Dependencies{
		DB: &pgxpool.Pool{}, Redis: &redis.Client{}, SMS: &fakeSMSSender{},
	}); err == nil {
		t.Fatal("nil logger accepted")
	}
}
