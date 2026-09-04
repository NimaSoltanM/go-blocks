package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/csrf"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Dependencies struct {
	DB     *pgxpool.Pool
	Redis  *redis.Client
	SMS    SMSSender
	Logger *slog.Logger
}

type Block struct {
	cfg            Config
	otp            otpRepository
	users          userRepository
	sms            SMSSender
	logger         *slog.Logger
	sessions       *session.Store
	sessionMW      fiber.Handler
	csrfMW         fiber.Handler
	code           func() (string, error)
	idempotencyKey func() (string, error)
	now            func() time.Time
	bearerToken    extractors.Extractor
}

func New(cfg Config, deps Dependencies) (*Block, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if deps.DB == nil {
		return nil, errors.New("auth PostgreSQL pool is required")
	}
	if deps.Redis == nil {
		return nil, errors.New("auth Redis client is required")
	}
	if isNilInterface(deps.SMS) {
		return nil, errors.New("auth SMS sender is required")
	}
	if deps.Logger == nil {
		return nil, errors.New("auth logger is required")
	}

	storage := newRedisSessionStorage(deps.Redis, cfg.KeyPrefix+"session:", cfg.Timeouts.Redis)
	b := newBlock(cfg, newRedisOTPStore(deps.Redis, cfg), &postgresUserStore{
		pool: deps.DB, timeout: cfg.Timeouts.PostgreSQL,
	}, deps.SMS, deps.Logger, storage)
	return b, nil
}

func newBlock(
	cfg Config,
	otp otpRepository,
	users userRepository,
	sms SMSSender,
	logger *slog.Logger,
	storage fiber.Storage,
) *Block {
	cfg.Pepper = append([]byte(nil), cfg.Pepper...)
	cfg.Cookie.TrustedOrigins = append([]string(nil), cfg.Cookie.TrustedOrigins...)
	b := &Block{
		cfg: cfg, otp: otp, users: users, sms: sms, logger: logger,
		code: generateCode, idempotencyKey: generateIdempotencyKey,
		now: time.Now, bearerToken: extractors.FromAuthHeader("Bearer"),
	}

	extractor := extractors.FromCookie(cfg.Cookie.SessionName)
	if cfg.Transport == TransportBearer {
		extractor = b.bearerToken
	}
	sessionConfig := session.Config{
		Storage: storage, Extractor: extractor,
		IdleTimeout: cfg.Session.IdleTimeout, AbsoluteTimeout: cfg.Session.AbsoluteTimeout,
		CookiePath: "/", CookieSameSite: "Lax", CookieSecure: cfg.Cookie.Secure,
		CookieHTTPOnly: true,
	}
	sessionConfig.ErrorHandler = func(c fiber.Ctx, err error) {
		serviceErr := authServiceErrorForContext(c.Context(), err)
		c.Set(fiber.HeaderCacheControl, "no-store")
		if handlerErr := c.App().ErrorHandler(c, serviceErr); handlerErr != nil {
			logger.ErrorContext(c.Context(), "auth_session_error_handler_failed", "error", handlerErr)
		}
	}
	b.sessionMW, b.sessions = session.NewWithStore(sessionConfig)

	if cfg.Transport == TransportCookie {
		b.csrfMW = csrf.New(csrf.Config{
			Session: b.sessions, IdleTimeout: cfg.Session.IdleTimeout,
			CookieName: cfg.Cookie.CSRFName, CookiePath: "/", CookieSameSite: "Lax",
			CookieSecure: cfg.Cookie.Secure, CookieHTTPOnly: true,
			TrustedOrigins: append([]string(nil), cfg.Cookie.TrustedOrigins...),
			Extractor:      extractors.FromHeader(csrf.HeaderName),
			ErrorHandler: func(c fiber.Ctx, err error) error {
				c.Set(fiber.HeaderCacheControl, "no-store")
				return httpError(403, "csrf_failed", "CSRF validation failed", err)
			},
		})
	}
	return b
}

func (b *Block) SessionMiddleware() (fiber.Handler, error) {
	if b == nil || b.cfg.Transport != TransportCookie {
		return nil, ErrCookieTransportRequired
	}
	return b.sessionMW, nil
}

func (b *Block) CSRFMiddleware() (fiber.Handler, error) {
	if b == nil || b.cfg.Transport != TransportCookie {
		return nil, ErrCookieTransportRequired
	}
	return b.csrfMW, nil
}

func clientKey(c fiber.Ctx, configured func(fiber.Ctx) string) (string, error) {
	var value string
	if configured == nil {
		value = c.RequestCtx().RemoteIP().String()
	} else {
		value = configured(c)
	}
	value = strings.Clone(value)
	if value == "" {
		return "", errors.New("auth client key is empty")
	}
	return value, nil
}

func redisContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, timeout)
}
