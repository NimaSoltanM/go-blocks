# Auth block contract

## Status

This document defines the accepted contract implemented by `blocks/auth`. Contract
changes should be made here before expanding the block's behavior. The initial
implementation began only after this boundary was accepted.

The block is intentionally narrow. It authenticates an Iranian mobile number by
SMS code, creates the durable user on first successful verification, and manages
an opaque login session. It is not a general identity platform.

## Product decision

The only login factor is possession of an Iranian mobile number:

1. The client submits a phone number.
2. The server sends a six-digit, single-use SMS code.
3. The client submits the same phone number and code.
4. The server creates or finds the user and starts a session.

There is no email, password, username, registration form, or separate sign-up
flow. First successful verification is registration. Later successful
verifications are login.

SMS is convenient, but it is vulnerable to SIM swapping, number recycling,
carrier interception, and phishing. This block is appropriate for ordinary
consumer authentication; applications protecting high-value financial or
administrative actions must add step-up authentication outside this block.

## Repository constraints

- The block is source copied and owned by the receiving application. It must not
  import `goblocks.local/dev`.
- `blocks/server` is the HTTP foundation. Auth depends structurally on its stable
  public-error envelope and request context without importing a repository runtime.
- HTTP code uses Fiber v3. Service and storage code use `context.Context` and
  explicit deadlines.
- PostgreSQL access is handwritten, parameterized SQL through an application-owned
  `*pgxpool.Pool`. There is no ORM.
- Redis access uses an application-owned `*redis.Client`. Auth never closes it.
- There are no globals, dependency-injection containers, hidden route
  registration, or background goroutines.
- Development and tests remain free of Docker, Compose, and container-dependent
  tooling. Integration tests use explicit `TEST_DATABASE_URL` and
  `TEST_REDIS_URL` values.

## Scope

Version one includes:

- Iran-only mobile-number normalization and validation;
- request, resend, and verify-code behavior;
- configurable abuse and cost limits;
- automatic user creation after proof of number possession;
- cookie sessions for browser applications or bearer sessions for native/API
  clients;
- CSRF protection in cookie mode;
- current-session logout;
- an authenticated identity middleware and `/auth/me` handler;
- PostgreSQL migrations, Redis state, documentation, unit tests, opt-in real
  PostgreSQL/Redis tests, and a source-copy example.

Version one explicitly excludes:

- passwords, email, usernames, social login, and magic links;
- passkeys, TOTP, backup codes, and step-up authentication;
- roles, permissions, profiles, organizations, and admin policy;
- phone-number changes, account merging, recovery, deletion, or suspension;
- logout from every device and a user-visible session/device list;
- refresh/access JWT pairs;
- multiple countries or automatic carrier lookup;
- SMS-provider-specific adapters in the core block;
- Redis Cluster support.

Those concerns may become separate blocks or later contract revisions. They must
not be smuggled into the first implementation.

## Upstream baseline and native capability decision

The design and implementation were verified on 2026-09-04. Module source reported no newer direct
dependency than the repository pins.

| Component | Repository/target | Latest stable checked | Decision |
| --- | --- | --- | --- |
| Go | `go 1.26.0`, toolchain `1.26.4` | `1.26.4` installed | Keep the current toolchain declaration. |
| Fiber | `github.com/gofiber/fiber/v3 v3.5.0` | `v3.5.0` | Use native session and CSRF middleware. |
| Fiber Redis storage | audited, not pinned | `github.com/gofiber/storage/redis/v3 v3.6.0` | Do not import it: its published module requires container-based test helpers, and it lacks key prefixing and operation timeouts. Implement the small `fiber.Storage` surface directly over the existing go-redis client. |
| pgx | `github.com/jackc/pgx/v5 v5.10.0` | `v5.10.0` | Use `pgxpool` and handwritten SQL. |
| go-redis | `github.com/redis/go-redis/v9 v9.22.0` | `v9.22.0` | Use `redis.NewScript` for atomic OTP operations. |
| UUID | transitive `github.com/google/uuid v1.6.0` | `v1.6.0` | Make it a direct auth dependency and generate user IDs in Go. |
| SQL migration runner | not currently pinned | `github.com/golang-migrate/migrate/v4 v4.19.1` | Provide plain paired up/down SQL verified with this runner; no runtime import. |
| PostgreSQL server | application-owned | `18.6` current release checked; supported lines are 14.24, 15.19, 16.15, 17.11, and 18.6 | Target PostgreSQL 15+ using long-established SQL features and require a current minor release. |
| Redis server | application-owned | Redis Open Source `8.10.1` security release checked | Target standalone or Sentinel Redis 7.2+; do not support Cluster in v1. |

The complete Fiber v3 middleware catalog and the current configuration, source,
and tests for `session`, `csrf`, `keyauth`, `basicauth`, `encryptcookie`,
`limiter`, and `idempotency` were reviewed.

| Native capability | Decision |
| --- | --- |
| `session` | Use it. It already supplies opaque secure IDs, Redis-backed state, cookie/header extraction, rotation, destruction, idle expiry, and absolute expiry. Its default `utils.SecureToken` has 32 random bytes of entropy. |
| `csrf` | Use session-backed synchronizer tokens in cookie mode. Keep the CSRF cookie HTTP-only and return the token from a same-origin JSON endpoint. Do not enable it in bearer mode. |
| `keyauth` | Do not use it. It validates extracted keys but does not own the session lifecycle; adding it would duplicate the Redis session lookup. |
| `basicauth` | Do not use it. The product has no username/password credential. |
| `encryptcookie` | Do not use it. The cookie contains only a random opaque session ID; encrypting it adds key management without protecting additional data. |
| `limiter` | Do not use it for OTP policy. OTP needs one atomic decision over phone, client, global cost, cooldown, challenge replacement, and attempt state. Fiber limiter's generic window and storage contract do not provide that operation. |
| `idempotency` | Do not use it for sending SMS. Replaying an HTTP response cannot make an external SMS side effect atomic, and its default lock is process-local. Atomic Redis admission plus resend cooldown defines the operation instead. |

Fiber Redis storage's `NewFromConnection` deliberately takes connection settings
from the supplied client and only reads its `Reset` config field; it has no
key-prefix or per-operation timeout setting. Its v3.6.0 published `go.mod` also
requires Fiber's Redis test helper, which brings Testcontainers/Docker modules
into every receiving application's dependency graph. That conflicts with this
repository's no-Docker constraint even when no container is started.

Auth therefore implements the narrow `fiber.Storage` session surface directly
over the already-required go-redis client. It prefixes every non-empty
get/set/delete key, derives the configured timeout, treats Redis `Nil` as a cache
miss, never closes the borrowed client, and returns a named unsupported-operation
error from both database-wide reset methods. This custom code exists only to
avoid the unwanted dependency graph, namespace shared Redis safely, and make a
flush impossible. It has direct contract and real-Redis integration coverage.

Future substantial changes must repeat this upstream check and update the dated
evidence.

## Public Go surface

The exact field spelling may improve during implementation, but the public
responsibilities must remain this small:

```go
type SMSSender interface {
	SendCode(context.Context, SMSCode) error
}

type SMSCode struct {
	Phone          string
	Code           string
	ExpiresAt      time.Time
	IdempotencyKey string
}

type User struct {
	ID    uuid.UUID `json:"id"`
	Phone string    `json:"phone"`
}

type Dependencies struct {
	DB     *pgxpool.Pool
	Redis  *redis.Client
	SMS    SMSSender
	Logger *slog.Logger
}

func DefaultConfig() Config
func (Config) Validate() error
func New(Config, Dependencies) (*Block, error)

func (*Block) SessionMiddleware() (fiber.Handler, error)
func (*Block) CSRFMiddleware() (fiber.Handler, error)
func (*Block) CSRFToken(fiber.Ctx) error
func (*Block) RequestCode(fiber.Ctx) error
func (*Block) VerifyCode(fiber.Ctx) error
func (*Block) Me(fiber.Ctx) error
func (*Block) Logout(fiber.Ctx) error
func (*Block) RequireUser(fiber.Ctx) error

func UserFromContext(any) (User, bool)
```

`Config` groups policy instead of exposing an unstructured list of durations and
integers. Its contract includes:

| Configuration | Requirement/default |
| --- | --- |
| Transport | `cookie` by default; the only other value is `bearer`. |
| OTP secret | Required base64 input decoding to at least 32 bytes; no default. |
| Redis key prefix | `gb:auth:v1:`; non-empty, versioned, and configurable for shared Redis databases and tests. |
| OTP policy | 6 digits, 2-minute lifetime, 60-second resend delay, 5 attempts. Code length is intentionally not configurable in v1. |
| Send/verify limits | The defaults in the abuse-control table below; all positive and validated. |
| Session policy | 7-day idle and 30-day absolute expiry; both positive, with absolute greater than or equal to idle. |
| Operation deadlines | Redis 1 second, PostgreSQL 2 seconds, SMS 5 seconds; each positive. |
| Phone normalizer | The dated Iran implementation by default; replaceable through a small consumer-owned interface. |
| Client key | Direct peer IP by default; replaceable by a synchronous function that returns a copied string. |
| Cookie policy | Secure `__Host-` names by default. Insecure local HTTP requires an explicit opt-in and non-`__Host-` development names. |

There is no silent environment detection. The example reads a mandatory
`AUTH_PEPPER` secret and constructs `Config`; receiving applications may map
their own environment/configuration system onto the same fields. Empty secrets,
zero limits, unknown transports, unsafe production-style cookie combinations,
and invalid timeout relationships fail in `Validate`/`New` before routes are
served.

`SMSCode` values are valid only for the synchronous `SendCode` call. An adapter
that retains them must copy the strings. The auth block does not close the SMS
sender, database pool, or Redis client and starts no worker, so it needs no
shutdown method.

Routes remain application-owned. The block exposes handlers instead of
registering paths in `New`:

```go
sessionMW, err := authBlock.SessionMiddleware() // cookie mode only
if err != nil {
	return err
}
csrfMW, err := authBlock.CSRFMiddleware()
if err != nil {
	return err
}
app.Use(sessionMW)
app.Use(csrfMW)

routes := app.Group("/auth")
routes.Get("/csrf", authBlock.CSRFToken) // cookie mode only
routes.Post("/otp/request", authBlock.RequestCode)
routes.Post("/otp/verify", authBlock.VerifyCode)
routes.Get("/me", authBlock.RequireUser, authBlock.Me)
routes.Post("/logout", authBlock.Logout)
```

In bearer mode the caller does not install `SessionMiddleware`, `CSRFMiddleware`,
or `CSRFToken`. The public auth handlers are registered directly. `VerifyCode`,
`RequireUser`, and `Logout` use Fiber's explicit session-store pattern so missing
credentials do not create and save unreachable anonymous sessions. The
cookie-only middleware accessors return a configuration error on a
bearer-configured instance so an application cannot accidentally combine
transports.

## Middleware order

The required order is:

1. `server.New` installs request ID, logging, recovery, and request deadline.
2. The application registers liveness/readiness endpoints that should not create
   sessions.
3. In cookie mode, auth session middleware runs.
4. In cookie mode, auth CSRF middleware runs. Authentication POST routes are not
   exempt: login CSRF matters too.
5. The application registers auth routes and other session-aware routes.
6. `RequireUser` protects only routes that need a logged-in identity. Logout is
   intentionally idempotent and does not require one.

Other public routes may be registered before auth middleware if they do not need
sessions or CSRF. A browser client first calls `GET /auth/csrf` with credentials
enabled, then echoes the returned token in `X-CSRF-Token` on every unsafe request.

`RequireUser` stores a copied `User` value with `fiber.StoreInContext`; downstream
handlers and service calls can retrieve it through `UserFromContext(c)` or
`UserFromContext(c.Context())`. No Fiber context or borrowed byte slice is retained.

## Session transport

`Config` selects exactly one transport for the whole application.

### Cookie mode (default)

- Session cookie: `__Host-gb_session`.
- CSRF cookie: `__Host-gb_csrf`.
- Both cookies use `Secure`, `Path=/`, no `Domain`, and `SameSite=Lax`.
- The session cookie is `HttpOnly`.
- The CSRF cookie is also `HttpOnly`; JavaScript receives its value only from
  `GET /auth/csrf` and sends it in `X-CSRF-Token`.
- Cross-site deployments must explicitly redesign origin, CORS, `SameSite`, and
  credential behavior; weakening cookie defaults is not an implicit option.

Local plain-HTTP development cannot use `__Host-` secure cookies. Config permits
an explicit insecure-cookie opt-in only with different development names. Secure
cookies and `__Host-` names remain the default, and there is no environment-based
silent downgrade.

### Bearer mode

- The client sends `Authorization: Bearer <opaque-session-token>`.
- A successful verification response returns `session_token` once.
- The token is never accepted in a URL, query parameter, or request body.
- The auth block never writes a session cookie and does not install CSRF.
- Public bearer endpoints do not install session middleware. Successful verify
  explicitly creates and saves a Fiber session; `RequireUser` loads a supplied
  token without saving newly generated state when the token is absent or invalid.
- Native/mobile clients store the token in platform-protected credential storage,
  not logs or ordinary preferences. Browser clients should use cookie mode rather
  than local storage.

Supporting both extractors simultaneously is out of scope because precedence and
logout behavior become ambiguous and can accidentally expose a browser session as
an API token.

Native Fiber session middleware uses the shared Redis store, a 7-day idle timeout,
and a 30-day absolute timeout by default. Both are configurable, the absolute
timeout must not be shorter than idle timeout, and successful login regenerates
the session ID before identity is stored. Logout destroys the current session and
expires its cookie when applicable; it must not reset into a newly persisted
anonymous session.

Redis eviction or loss logs users out. It never deletes the durable user. Redis
unavailability fails session loading and saving closed with a `503` response.

## HTTP contract

Every auth response, including errors, carries `Cache-Control: no-store` and the
server request ID. Request bodies are JSON, size-bounded by the server, and reject
malformed or non-UTF-8 JSON, trailing data, unknown or duplicate fields, missing
fields, non-string values, and `null`.

### `GET /auth/csrf` (cookie mode only)

Response `200`:

```json
{"csrf_token":"opaque-token"}
```

This endpoint creates the anonymous session needed by the synchronizer-token
pattern. It does not exist in bearer mode.

### `POST /auth/otp/request`

Request:

```json
{"phone":"09121234567"}
```

Response `202` after the SMS adapter accepts the send:

```json
{"status":"code_sent","expires_in_seconds":120,"resend_after_seconds":60}
```

The two durations report the conservative time remaining after Redis admission
and SMS-provider latency, rounded up to whole seconds. A provider that returns
success only after the challenge lifetime has elapsed does not produce a false
login-success response.

The response does not reveal whether the phone already belongs to a user. A valid
phone never causes a pre-send database lookup.

### `POST /auth/otp/verify`

Request:

```json
{"phone":"09121234567","code":"012345"}
```

Cookie-mode response `200`:

```json
{"user":{"id":"9c1fcb97-942c-4f8a-94f7-dc165c737cc6","phone":"+989121234567"}}
```

Bearer-mode response `200`:

```json
{"user":{"id":"9c1fcb97-942c-4f8a-94f7-dc165c737cc6","phone":"+989121234567"},"session_token":"opaque-token"}
```

The session token is never returned in cookie mode.

### `GET /auth/me`

Authenticated response `200`:

```json
{"user":{"id":"9c1fcb97-942c-4f8a-94f7-dc165c737cc6","phone":"+989121234567"}}
```

### `POST /auth/logout`

Response `204` with no body whether a session existed or not. If supplied, the
current session is invalidated. Logging out other sessions is not implied. Cookie
mode still requires a valid CSRF token because logout changes authentication
state.

### Error semantics

Errors use the server envelope. Auth adds only stable, safe codes; internal errors
are logged but never returned.

| Status | Code | Meaning |
| --- | --- | --- |
| `400` | `invalid_request` | Malformed JSON, unknown fields, or invalid body shape. |
| `400` | `invalid_phone` | The value cannot normalize to a currently assigned Iranian mobile range. |
| `400` | `invalid_code_format` | The code is not exactly six digits after digit normalization. |
| `401` | `invalid_code` | The code is wrong, expired, superseded, already used, or exhausted. These states are deliberately indistinguishable. |
| `401` | `authentication_required` | No valid authenticated session is present. |
| `403` | `csrf_failed` | Cookie-mode CSRF validation failed. |
| `429` | `rate_limited` | A resend, phone, client, verification, or global limit was reached. |
| `503` | `sms_unavailable` | The SMS adapter did not accept the send before its deadline. |
| `503` | `service_unavailable` | PostgreSQL, Redis, or session persistence is unavailable. |
| `504` | `request_timeout` | The existing server request deadline expired. |

A `429` response includes a whole-second `Retry-After` header and
`retry_after_seconds` in the error detail. This requires the following backwards-
compatible server-foundation extension before auth implementation:

- an exported typed public error carrying HTTP status, stable code, safe message,
  optional retry-after duration, and an internal wrapped cause;
- the server error handler recognizes it, preserves only its explicitly allowed
  metadata across the response reset, and keeps the current envelope for every
  existing `fiber.Error` and ordinary error;
- the optional JSON field uses `omitempty`, so existing response shapes do not
  change when no retry delay exists.

The auth block depends on that smallest server API. It must not duplicate the
server error envelope.

## Iranian phone-number contract

The canonical value is E.164: `+98` followed by a ten-digit national significant
number. Input accepts:

- domestic `09xxxxxxxxx`;
- national-significant `9xxxxxxxxx`;
- international `+989xxxxxxxxx`;
- international-prefix `00989xxxxxxxxx`;
- ASCII, Persian (`۰`-`۹`), or Arabic-Indic (`٠`-`٩`) digits;
- surrounding/interior Unicode whitespace, common hyphens, and parentheses.

Other characters, misplaced plus signs, extensions, and non-Iranian country codes
are rejected. OTP input accepts the same three digit scripts but no separators and
normalizes to exactly six ASCII digits. Leading zeroes are significant.

Validation is not the overly broad `+989` plus nine digits. The default normalizer
uses the Iranian mobile NDC ranges published by the ITU on 2026-06-12:

`900`-`905`, `91`, `920`-`923`, `93`, `990`-`994`, `99510`, `99550`, `996`,
`9981`, `9982`, `99830`-`99832`, `99888`, `99900`-`99903`, `9991`, `99921`,
`99930`-`99934`, `9995`, `99969`, `99977`, `9998`, and `9999`.

The range table is dated policy and must be easy to update. Config permits a
replacement normalizer for a receiving application, but the Iran default remains
strict and has table-driven tests. The database check enforces only canonical
structure; application validation owns the evolving mobile assignment list.

## OTP lifecycle

- Code length is always six digits. Five-digit mode is not offered.
- Generation uses `crypto/rand.Int` over `[0, 1_000_000)` and zero-padded decimal
  formatting; leading zeroes are allowed and there is no modulo bias.
- One phone has at most one active challenge.
- Challenge lifetime is 2 minutes.
- Resend cooldown is 60 seconds.
- A new admitted send replaces and invalidates the previous code.
- A challenge permits 5 incorrect verification attempts.
- Successful comparison atomically deletes the challenge before PostgreSQL work.
- Concurrent verification of the same code can succeed only once.
- The code, session token, and full phone number are never logged.

Redis stores an HMAC-SHA-256 code verifier, never the six-digit code. A simple
unkeyed hash is insufficient because the code has only one million possibilities.
The required `AUTH_PEPPER` is at least 32 random bytes supplied as base64; it is
separate from SMS-provider credentials. It also derives fixed-length tags for
Redis keys so raw phone numbers and client identifiers do not appear in key names.

Rotating the pepper intentionally invalidates active OTPs and resets their short-
lived counters; existing sessions are unaffected. Deployment documentation must
call out that temporary rate-limit reset.

## Abuse and cost controls

The default policy is conservative and configurable:

| Control | Default |
| --- | --- |
| Per-phone resend cooldown | 1 send per 60 seconds |
| Per-phone send window | 5 per hour |
| Per-phone daily window | 10 per 24 hours |
| Per-client send window | 30 per 10 minutes |
| Global send budget | 300 per minute |
| Attempts per active challenge | 5 |
| Per-client verify window | 30 per 10 minutes |

Windows are fixed TTL windows beginning with the first admitted event for that
key, not wall-clock buckets. Send counters increment only when the complete atomic
admission succeeds; rejected resends do not consume additional send quota.
Per-client verification counts every well-formed verification attempt, while the
five-attempt challenge counter decrements only for an existing challenge with an
incorrect verifier. If several controls reject one request, `Retry-After` reports
the longest remaining delay so all of them can admit the next attempt.

The application supplies a client-key function. Its default is the direct peer IP
reported by Fiber. Forwarded headers are considered only when the application has
explicitly and correctly configured trusted proxies in Fiber. Carrier NAT makes
IP limits imperfect, so phone and global limits remain independent.

A single Redis script atomically evaluates all send limits, records their counters,
sets the cooldown, and writes the active challenge. A separate script atomically
applies the verify-client limit, compares the verifier, decrements attempts, and
consumes success. `redis.NewScript` provides `EVALSHA` with `EVAL` fallback.

The multi-key atomic decision is why v1 supports standalone Redis and Redis
Sentinel but not Redis Cluster: arbitrary phone, client, and global keys cannot be
guaranteed to occupy one cluster hash slot without a poor global hot spot. Cluster
support requires a revised rate-limit contract, not a silent loss of atomicity.

## External SMS effect

The core block knows no vendor SDK, template ID, or Persian message wording. The
application supplies `SMSSender`. `SendCode` receives a child context with a
5-second default deadline and an opaque idempotency key for providers that support
one. Auth itself does not retry because provider timeout can mean “accepted but
the response was lost.”

Redis admission and challenge creation happen before `SendCode`. If sending
returns an error or times out, the challenge, cooldown, and cost counters remain.
This is deliberate:

- a provider may still deliver after an ambiguous failure, in which case the code
  should work;
- deleting the state would permit cheap retry loops around provider failures;
- the user can request another code after the displayed cooldown.

The endpoint returns `503 sms_unavailable` for that failed call. Provider-specific
adapters own safe retry rules, delivery receipts, credentials, message templates,
and provider monitoring.

## State ownership

### PostgreSQL

Auth owns one durable table:

```sql
CREATE TABLE auth_users (
    id uuid PRIMARY KEY,
    phone_e164 text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL,
    last_verified_at timestamptz NOT NULL,
    CONSTRAINT auth_users_phone_e164_format
        CHECK (phone_e164 ~ '^[+]98[0-9]{10}$')
);
```

The final migration should use database-side UTC timestamps or pass one explicit
UTC timestamp consistently; tests must not depend on the database session time
zone. Verification performs one parameterized upsert with `RETURNING` and keeps
`last_verified_at` monotonic when application clocks are skewed or requests
finish out of order. User IDs are UUID v4 values generated in Go.

On first verification the row is inserted. On later verification only
`last_verified_at` changes. The name is deliberate: PostgreSQL can record a
successful code verification even if the following Redis session save fails.
Phone ownership has already been proved, so the response need not conceal whether
insertion occurred, but the API still returns a single uniform success shape.

Migration files are paired plain SQL compatible with `golang-migrate`. Copying
instructions tell applications to rename their sequence/timestamp to fit the
existing migration history before applying it. Adding the block never runs a
migration automatically. The down migration drops only `auth_users` and is
documented as destructive.

### Redis

Redis owns only expiring or replaceable state:

- Fiber session records;
- session-backed CSRF data;
- active OTP verifier and attempts;
- resend cooldown;
- phone, client, verification, and global counters.

All auth keys have a versioned, configurable prefix and a TTL. Session records
(including their CSRF state) and OTP/rate-limit keys use distinct sub-prefixes.
The session storage adapter prevents bare session IDs from becoming Redis key
names. Phone and client segments are truncated HMAC tags, not raw identifiers.
Scripts never call `FLUSHDB`, and auth's session storage rejects database-wide
reset.

Session data contains only a schema version, user UUID string, canonical phone,
and verification timestamp. `RequireUser` validates those values and returns the
copied session identity without a PostgreSQL query. That is consistent with v1's
explicit exclusion of user suspension/deletion and keeps Redis-backed session
checks inexpensive; a later account-state feature must revise the invalidation
contract rather than assume sessions notice database changes.

### Cross-system boundary

Redis challenge consumption, the PostgreSQL upsert, and Fiber session persistence
cannot share a transaction. The chosen failure behavior is fail-closed:

1. Atomically consume the valid OTP in Redis.
2. Upsert/find the user in PostgreSQL.
3. Regenerate and populate the Fiber session.
4. In cookie mode, let Fiber middleware persist it before the response completes;
   in bearer mode, explicitly save it before writing the success response.

If PostgreSQL or session persistence fails after step 1, the OTP stays consumed.
The user requests a new code. A successful user insert may remain even if session
save fails; that is harmless because it grants no session, and the next verified
attempt finds the same user. The API must never claim a login succeeded before
session persistence succeeds.

## Deadlines, retries, and outage behavior

Every operation derives a child deadline from `c.Context()` without extending the
server's request deadline:

| Operation | Default child deadline | Retry in core block |
| --- | --- | --- |
| Redis OTP/rate decision | 1 second | none |
| PostgreSQL user upsert/read | 2 seconds | none |
| SMS provider send | 5 seconds | none |
| Redis session load/save | bounded by request context and client timeouts | none |

- Redis outage: request/verify/session operations fail closed with `503`.
- PostgreSQL outage: code request can still send because it does not query users;
  successful code verification is consumed and returns `503`.
- SMS outage: admitted challenge and limits remain; request returns `503`.
- Redis eviction/restart: active challenges, counters, CSRF state, and sessions may
  disappear. Codes stop working, limits may temporarily reset, and users log in
  again. Durable users remain.
- PostgreSQL uniqueness resolves concurrent first logins for one number.

Readiness should check PostgreSQL and Redis with short application-owned probes.
It should not send an SMS or call the provider on every readiness request; provider
health belongs in operational monitoring.

## Logging and privacy

Structured events may include request ID, operation, outcome, duration, provider
class, and the same irreversible phone/client tag used for Redis correlation.
They must not include:

- OTP code or verifier;
- session or CSRF token;
- SMS-provider credential or raw response body;
- full phone number;
- request body or authorization header.

Expected events include code admitted, code rate-limited, provider failure,
verification failure, login success, and logout. Routine invalid codes are not
error-level server faults. Internal dependency failures retain their wrapped cause
for server-side logs.

## Verification plan

Default tests need no external services and cover:

- every accepted number form and digit script;
- every ITU mobile prefix family plus representative rejected fixed/service
  prefixes;
- six-digit generation, leading zeroes, HMAC verification, and secret redaction;
- config validation and production cookie safeguards;
- route JSON contracts and error mapping with fake service dependencies;
- cookie versus bearer behavior, session rotation, logout, and identity context;
- CSRF required on unsafe cookie requests and absent in bearer mode;
- provider failure semantics and no accidental retry;
- no phone, code, or token leakage in logs.

Opt-in integration tests use real PostgreSQL and Redis from
`TEST_DATABASE_URL`/`TEST_REDIS_URL` and isolated schema/key prefixes. They cover:

- migration up/down and the phone uniqueness constraint;
- concurrent first login and `INSERT ... ON CONFLICT` behavior;
- atomic send admission, expiry, resend replacement, attempt exhaustion, and
  single-use verification under concurrency;
- Fiber session persistence, regeneration, idle/absolute expiry, and Redis outage
  behavior.

No integration result is claimed until those services are explicitly configured
and the tests actually run. A fake SMS sender is sufficient for repository checks;
real provider tests are opt-in and live outside the default suite.

The maintained usage example is the independent
`examples/phone-auth-api` module, not a mutation of the server-only
`examples/basic-api`. It contains source copies of both required blocks, is tested
with `GOWORK=off`, and is covered by copy-consistency tests.

## Implementation record

The initial implementation followed these reviewable slices:

1. Add and test the backwards-compatible typed public error to `blocks/server`,
   then refresh its maintained basic example copy.
2. Add auth config, phone/code normalization, value types, and SMS interface.
3. Add paired SQL migration and pgx user queries.
4. Add Redis key derivation and the two atomic scripts with unit and integration
   tests.
5. Add the service flow and explicit cross-system failure tests.
6. Wire Fiber session/CSRF middleware, handlers, identity context, and transport
   modes.
7. Add the independent example, source-copy checks, block README, and dated
   upstream evidence.
8. Run `./scripts/check.ps1`; run integration checks only when explicit service
   URLs are present, and report the two results separately.

The public server error extension, auth source, migrations, tests, independent
example, and copy-consistency checks now correspond to this contract. Default
checks remain service-free; real PostgreSQL and Redis results are reported only
when their explicit test URLs are supplied.

## Authoritative references

- [Iran national numbering plan, ITU, posted 2026-06-12](https://www.itu.int/oth/T0202000066/en)
- [Iran E.164 numbering-plan PDF](https://www.itu.int/dms_pub/itu-t/oth/02/02/T02020000660034PDFE.pdf)
- [ITU-T E.164 recommendation](https://www.itu.int/rec/T-REC-E.164/en)
- [Fiber v3 middleware catalog](https://docs.gofiber.io/category/-middleware/)
- [Fiber v3 session middleware](https://docs.gofiber.io/middleware/session/)
- [Fiber v3 CSRF middleware](https://docs.gofiber.io/middleware/csrf/)
- [Fiber v3 limiter middleware](https://docs.gofiber.io/middleware/limiter/)
- [Fiber v3 idempotency middleware](https://docs.gofiber.io/middleware/idempotency/)
- [Fiber v3.5.0 release](https://github.com/gofiber/fiber/releases/tag/v3.5.0)
- [Fiber Redis storage](https://github.com/gofiber/storage/tree/main/redis)
- [pgx changelog](https://github.com/jackc/pgx/blob/master/CHANGELOG.md)
- [go-redis guide](https://redis.io/docs/latest/develop/clients/go/)
- [Redis scripting guarantees](https://redis.io/docs/latest/develop/programmability/eval-intro/)
- [Redis Open Source 8.10 release notes](https://redis.io/docs/latest/operate/oss_and_stack/stack-with-enterprise/release-notes/redisce/redisos-8.10-release-notes/)
- [PostgreSQL `INSERT`](https://www.postgresql.org/docs/current/sql-insert.html)
- [PostgreSQL versioning policy](https://www.postgresql.org/support/versioning/)
- [PostgreSQL 18.6 release notes](https://www.postgresql.org/docs/release/18.6/)
- [golang-migrate](https://github.com/golang-migrate/migrate)
- [NIST SP 800-63B authenticator requirements](https://pages.nist.gov/800-63-4/sp800-63b/authenticators/)
- [NIST SP 800-63B session requirements](https://pages.nist.gov/800-63-4/sp800-63b/session/)
- [OWASP Multifactor Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Multifactor_Authentication_Cheat_Sheet.html)
- [OWASP Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)
- [OWASP Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
