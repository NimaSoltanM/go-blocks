# Auth

A copyable passwordless authentication block for Iranian mobile numbers. It
normalizes current Iranian mobile ranges to E.164, issues six-digit SMS codes,
stores one-time challenges and abuse limits atomically in Redis, creates users in
PostgreSQL after successful verification, and manages Fiber v3 sessions.

The block supports one transport per application: secure, CSRF-protected cookies
(the default) or opaque bearer session tokens. It does not provide email,
passwords, a user profile, authorization roles, account recovery, or an SMS
vendor adapter.

## Requirements

- Go 1.26+
- the current `blocks/server` public-error contract
- Fiber `v3.5.0`
- pgx `v5.10.0` and go-redis `v9.22.0`
- PostgreSQL 15 or newer on a supported current minor release
- standalone or Sentinel Redis 7.2 or newer; Redis Cluster is not supported in v1
- an application-owned `SMSSender`, PostgreSQL pool, Redis client, and logger

The application owns and closes all dependencies. Auth starts no worker and does
not close them.

## Copy and migrate

1. Copy the non-test `.go` files into a package such as `internal/auth`.
2. Copy both files in `migrations/` into your application's migration history,
   resolving version-number collisions explicitly.
3. Copy the current server block too, including `public_error.go`. Auth implements
   that structural error contract without importing a repository runtime.
4. Pin the dependencies listed above and run `go mod tidy` and your application
   tests.
5. Apply the up migration using your existing migration process. The block never
   runs migrations automatically.

The migration creates only `auth_users`. UUIDs are generated in Go, so no
PostgreSQL extension is required. The down migration drops only that table.

## Provide an SMS adapter

```go
type SMSProvider struct { /* provider client */ }

func (p *SMSProvider) SendCode(ctx context.Context, message auth.SMSCode) error {
    // Submit message.Phone and message.Code synchronously and honor ctx.
    // Use message.IdempotencyKey if the provider supports idempotent requests.
    return nil
}
```

Returning nil means the provider accepted the send. A timeout can be ambiguous,
so auth does not retry automatically. The admitted challenge, cooldown, and
budgets remain when the adapter returns an error. Never retain `SMSCode` fields
without copying them and never log the code, full phone, provider credentials, or
raw provider response.

## Construct and register: cookie mode

Decode the application secret from configuration. Use at least 32 random bytes;
there is intentionally no default.

```go
cfg := auth.DefaultConfig()
cfg.Pepper = decodedSecret

authBlock, err := auth.New(cfg, auth.Dependencies{
    DB: db, Redis: redisClient, SMS: smsProvider, Logger: logger,
})
if err != nil {
    return err
}
sessionMW, err := authBlock.SessionMiddleware()
if err != nil {
    return err
}
csrfMW, err := authBlock.CSRFMiddleware()
if err != nil {
    return err
}

// Register liveness/readiness before these application-wide middlewares.
app.Use(sessionMW)
app.Use(csrfMW)

routes := app.Group("/auth")
routes.Get("/csrf", authBlock.CSRFToken)
routes.Post("/otp/request", authBlock.RequestCode)
routes.Post("/otp/verify", authBlock.VerifyCode)
routes.Get("/me", authBlock.RequireUser, authBlock.Me)
routes.Post("/logout", authBlock.Logout)
```

The browser first requests `GET /auth/csrf` with credentials enabled and sends
the returned value in `X-CSRF-Token` on every POST. Login routes are deliberately
not exempt from CSRF. The default `__Host-` cookies require HTTPS, use `Path=/`,
`SameSite=Lax`, and are HTTP-only. Local plain HTTP requires an explicit
`AllowInsecure` opt-in and different, non-`__Host-` cookie names.

Cookie handlers verify that both middlewares ran and fail with 503 before an auth
side effect if the stack was omitted. Keep the order shown above.

## Construct and register: bearer mode

```go
cfg := auth.DefaultConfig()
cfg.Transport = auth.TransportBearer
cfg.Pepper = decodedSecret

authBlock, err := auth.New(cfg, dependencies)
if err != nil {
    return err
}
routes := app.Group("/auth")
routes.Post("/otp/request", authBlock.RequestCode)
routes.Post("/otp/verify", authBlock.VerifyCode)
routes.Get("/me", authBlock.RequireUser, authBlock.Me)
routes.Post("/logout", authBlock.Logout)
```

Do not install the cookie session or CSRF middleware in bearer mode. Verification
returns `session_token` once; clients send it only as `Authorization: Bearer ...`.
Missing and malformed credentials do not create anonymous sessions. Successful
authenticated requests refresh the idle TTL. Logout is idempotent and invalidates
only the supplied session. Browser applications should prefer cookie mode rather
than storing bearer tokens in web storage.

## HTTP contract

All auth responses use `Cache-Control: no-store`. Errors use the server envelope
and stable codes documented in the [auth contract](../../docs/auth-contract.md).
Request bodies must be
`application/json` objects with exactly the documented fields; malformed,
non-UTF-8, trailing, unknown, duplicate, missing, `null`, and incorrectly typed
fields are rejected.

The normal flow is:

- `POST /auth/otp/request` with `{"phone":"09121234567"}` returns 202 only after
  the SMS adapter accepts the send. Its expiry and resend durations are the
  conservative time remaining after provider latency, rounded up to seconds.
- `POST /auth/otp/verify` with the phone and a six-digit code returns the user and
  starts a rotated session.
- `GET /auth/me` returns the authenticated user.
- `POST /auth/logout` returns 204 whether or not a bearer session existed.

`RequireUser` stores a copied `User` value for downstream handlers:

```go
user, ok := auth.UserFromContext(c)
// auth.UserFromContext(c.Context()) works too.
```

## Security and state behavior

- Codes use `crypto/rand`, preserve leading zeroes, expire after two minutes, and
  are single-use with five attempts by default.
- Redis stores HMAC-derived phone/client tags and code verifiers, never raw codes
  or phone numbers. One Lua admission transition checks every send budget before
  incrementing any of them. A second transition rate-limits verification,
  compares the verifier, decrements attempts, and consumes a correct code.
- Sessions contain only schema version, UUID, canonical phone, and verification
  timestamp. Login rotates the session identifier.
- Redis loss expires challenges, resets budgets, and logs users out; it never
  deletes durable users. Redis, PostgreSQL, SMS, and session failures fail closed.
- A correct code is consumed before the PostgreSQL upsert. If PostgreSQL or
  session persistence then fails, the client must request another code. This
  avoids replaying a proved code across partial failures.
- PostgreSQL preserves the greatest `last_verified_at` value if application
  clocks are skewed or concurrent login requests finish out of order.
- The default client limiter uses the direct peer IP. Deployments behind a trusted
  proxy should provide `Config.ClientKey` using their validated proxy policy.

The block deliberately does not import Fiber Redis storage. Its audited v3.6.0
module brings container test-helper dependencies into consumer module graphs and
does not provide key prefixing or operation deadlines. The included narrow
`fiber.Storage` implementation uses the existing go-redis client, cannot reset a
shared database, and never closes the caller-owned client.

See the [auth contract](../../docs/auth-contract.md) for exact defaults, responses, rate limits, failure
semantics, and the dated Iranian numbering-policy boundary.

## Verify

Default tests need no services:

```sh
go test -race ./blocks/auth
```

They cover phone and digit forms, strict JSON, error mapping, secret-safe logs,
cookie/CSRF and bearer flows, session rotation/logout, corrupt session data,
configuration, storage namespacing/deadlines, and service ordering.

Real-service checks are opt-in and use isolated Redis keys and a temporary
PostgreSQL schema. They write and then remove test data; use dedicated test
services, never production:

```sh
TEST_DATABASE_URL='postgres://...' TEST_REDIS_URL='redis://...' \
go test -tags=integration -count=1 -v ./blocks/auth
```

No Docker, Compose, or container-based test harness is included.
