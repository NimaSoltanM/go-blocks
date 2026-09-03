# Architecture

## Purpose

Go Blocks is a collection of source code that can be copied into Go applications.
The application owns the resulting code, routes, schema, configuration, and
dependency versions. The repository is a development and verification workspace.

## Stack decisions

- **HTTP:** Fiber v3 for handlers and middleware.
  Follow the version and middleware review process in `fiber-v3.md`.
- **Database:** PostgreSQL with handwritten, parameterized SQL through pgx v5.
  Applications create a `pgxpool.Pool` at startup and own its shutdown.
- **Redis:** go-redis v9. Applications own the client and pass it to features
  that need it. Blocks that do not need Redis do not require a Redis client.
- **Schema:** versioned SQL migrations, kept with the feature that owns them.
  A migration tool will be chosen when a database block needs one.
- **Logging:** standard-library `log/slog` unless a concrete feature requires more.
- **Development:** Go and Git, with native or externally hosted test services.
  No Docker, Compose, container images, or container-dependent test harnesses.

Dependency versions are pinned by the root Go module. They are the verified
baseline for development, not a requirement to import a Go Blocks runtime.

## Feature boundaries

Keep related handlers, behavior, persistence, tests, migrations, and documentation
together. Use only as many files and layers as the feature needs. A small
middleware block may be one implementation file and one test file.

Fiber handlers parse and validate HTTP inputs, call behavior, and write HTTP
responses. Service and storage functions receive `context.Context` and ordinary
Go values. Do not retain `fiber.Ctx` or references to its reused buffers after a
request. Copy values before sending them to asynchronous work.

Derive operation timeouts from `c.Context()` explicitly. Fiber's context is
reused, and client disconnection does not automatically cancel database work.
Durable background work belongs in a worker with its own lifetime, not an
untracked goroutine launched by a request.

Construct dependencies in the application and pass them to features. Introduce
small consumer-owned interfaces where substitution is useful. Prefer a concrete
pgx or Redis client to a generic repository or storage framework.

For operations that must be atomic across features, the application owns the
transaction and passes the transaction-capable query dependency through the
operation. Features must not silently create unrelated nested transactions.

## State ownership

Use PostgreSQL for durable application records. Use Redis for cache entries,
rate-limit state, and intentionally expiring data. Document eviction, expiration,
and outage behavior for every Redis-backed feature.

Idempotency must be specified per operation. A Redis response cache cannot make
a PostgreSQL write atomic with recording its result. Database write deduplication
should commit alongside the write when those guarantees are needed. External
effects need their own retry and deduplication strategy.

Each feature owns its schema objects and migration files. Integrations decide
how those migrations enter the application's migration history; collisions and
ordering must be explicit. Adding a source block must not automatically execute
migrations against a database.

## Distribution

Initially, use manual copying and explicit wiring instructions. Future automation
may copy files, adjust imports, record source versions, and show changes for
review. It must preserve local edits and report dependency conflicts.

Avoid mandatory imports from `goblocks.local/dev`, hidden globals, automatic
route registration, and a universal application object shared by every block.

## Verification

Default checks run without PostgreSQL or Redis. Opt-in integration checks require
explicit test connection URLs. Future SQL and Redis behavior tests should use
real test services and isolated data rather than assume that mocks prove
database constraints, transaction behavior, or atomicity.

The smoke tests validate dependency compatibility and optional connectivity.
`blocks/server` also has behavior tests, including actual loopback TCP requests
and shutdown checks. Its source is copied into `examples/basic-api`, an independent
Go module verified by the same scripts. `tests/copy` prevents that maintained
example from drifting from the source block. Authentication is not implemented.

## References

- [Repository Fiber v3 baseline](fiber-v3.md)
- [Fiber v3 context behavior](https://docs.gofiber.io/guide/go-context/)
- [pgx PostgreSQL driver and toolkit](https://github.com/jackc/pgx)
- [go-redis connection guide](https://redis.io/docs/latest/develop/clients/go/connect/)
