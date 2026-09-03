# Fiber v3 baseline

Fiber is the HTTP foundation for Go Blocks. Using a current module version is
only one part of the requirement: every block design must account for the current
v3 API, built-in middleware, storage behavior, lifecycle, and context semantics.

## Verified baseline

The dependency baseline was checked on 2026-09-03:

| Component | Pinned stable version |
| --- | --- |
| Fiber | `github.com/gofiber/fiber/v3 v3.5.0` |
| Go | `1.26.0`, preferred toolchain `1.26.4` |

`go.mod` and `go.sum` are the reproducible record. “Latest” is time-sensitive, so
run these commands before starting or substantially changing a Fiber-based block:

```sh
go list -m -u -f '{{if .Update}}{{.Path}} {{.Version}} -> {{.Update.Version}}{{end}}' @direct
go list -m github.com/gofiber/fiber/v3@latest
```

An empty first command means Go's module source reports no newer direct dependency.
The second reports the latest stable Fiber version independently of the pin. Also
review the official Fiber v3 release notes, v3 “What's New” guide, middleware
catalog, and the installed package source/tests in `$(go env GOMODCACHE)`. Upgrade
the pin deliberately, run the full checks, and review behavior changes. Do not
silently float dependency versions during a build.

This process establishes what was current when a change was written. A commit
cannot guarantee it stays current after a later upstream release; the check must
be repeated for new work.

## Required agent audit record

`AGENTS.md` makes upstream verification mandatory for every agent. Each Fiber
change must leave enough evidence for the next reviewer to reproduce the check:

- verification date, pinned Fiber version, and `@latest` result;
- official v3 documentation, release notes, and migration notes consulted;
- the current middleware packages relevant to the requested concern;
- installed source or tests inspected to resolve behavior not explicit in docs;
- native middleware selected, or the exact missing contract that requires custom
  code;
- repository checks that passed after the change.

Agents must rediscover applicable middleware from the current official catalog;
this file is a dated baseline and must not be treated as a permanently complete
list. A new native capability can make an existing custom implementation
unnecessary even when the pinned API still compiles.

## Current middleware catalog

Fiber v3.5.0 includes these middleware packages in the main Fiber module:

| Area | Middleware to consider before custom code |
| --- | --- |
| Identity and credentials | `basicauth`, `keyauth`, `session`, `csrf`, `encryptcookie` |
| Request security and policy | `cors`, `earlydata`, `helmet`, `hostauthorization` |
| Reliability and state | `healthcheck`, `idempotency`, `limiter`, `recover`, `timeout` |
| Caching and representation | `cache`, `compress`, `etag` |
| Observability and diagnostics | `logger`, `requestid`, `responsetime`, `expvar`, `pprof` |
| Routing and delivery | `favicon`, `paginate`, `proxy`, `redirect`, `rewrite`, `skip`, `sse`, `static` |
| Interoperability and configuration | `adaptor`, `envvar` |

The list is an inventory, not a recommendation to install everything. For each
future block, inspect the relevant middleware's current `Config`, defaults,
storage interface, response behavior, and tests. Then either use it or record a
specific reason for a small custom implementation.

Important examples for planned work:

- Rate limiting and idempotency default to process-local memory. Before using
  either across application instances, provide and test suitable shared storage.
  HTTP response deduplication also does not make a PostgreSQL write transactional.
- Cache and session behavior depend on the selected `fiber.Storage`; inspect the
  current Redis storage implementation and failure semantics when those blocks
  are designed.
- Authentication work must evaluate `session`, `keyauth`, `csrf`,
  `encryptcookie`, and `basicauth` before creating overlapping middleware.
- Server-sent events have a dedicated `sse` middleware/package path. Streaming
  routes require their own timeout and shutdown policy.
- `adaptor` can bridge `net/http` code, but native Fiber handlers keep full
  `fiber.Ctx` access and avoid compatibility overhead.

## v3 behavior that affects block design

- `fiber.Ctx` is an interface value and is recycled after the handler returns.
  Do not retain it or borrowed request values in asynchronous work.
- `fiber.Ctx` satisfies `context.Context` for values, but its own cancellation
  methods are no-ops. Use `c.Context()` for a context that can be wrapped with a
  deadline and used beyond the handler. Fiber does not automatically cancel work
  when an individual client disconnects.
- `fiber.StoreInContext` and middleware accessors such as
  `requestid.FromContext` are the current way to share middleware values.
  `PassLocalsToContext` controls propagation into the user context.
- Registering a GET route automatically registers the corresponding HEAD route
  unless `DisableHeadAutoRegister` is set.
- Listen-time settings such as startup output, listener network, prefork, and TLS
  belong to `fiber.ListenConfig` in v3. Shutdown has pre/post hooks and
  context/timeout-aware APIs.
- Route registration accepts native Fiber handlers plus supported Express-style,
  `net/http`, and `fasthttp` shapes. Go Blocks uses native `fiber.Handler` unless
  interoperability is the actual requirement.
- Trusting proxy headers requires explicit `TrustProxy` and proxy configuration.
  Blocks must not infer client identity from forwarded headers without that
  application-level deployment decision.

## Current server decisions

The server foundation was re-audited against Fiber v3.5.0:

| Concern | Current implementation |
| --- | --- |
| Request correlation | Fiber `requestid`, including its current secure-token default and context accessor |
| Request logging | Fiber `logger` with a `log/slog` `LoggerFunc`, preserving the repository's structured fields |
| Panic recovery | Fiber `recover`, with panics forced to the JSON 500 contract |
| Health | Fiber `healthcheck`, JSON format, and its `/livez` and `/readyz` constants |
| Listener and shutdown | Fiber v3 `Listener`, `ListenConfig`, and `ShutdownWithContext`, wrapped to expose completion/errors |
| Errors | A small custom `ErrorHandler` because the project requires one stable JSON envelope and removal of partial output |
| Request deadline | A small cooperative context wrapper; it derives from `c.Context()` and restores/cancels it on return |

Fiber v3's native `timeout` middleware is useful for endpoints that require an
early response: it executes the handler in a goroutine, races it against a
deadline, and uses Fiber's abandonment/reclamation mechanism so the handler can
finish safely. The base server uses a cooperative context deadline because it
keeps handler execution synchronous and makes no claim that timed-out database
effects were stopped. Evaluate the native timeout middleware per endpoint when
early return is required, rather than enabling it globally without that tradeoff.

The custom error and lifecycle wrappers should be removed when Fiber provides the
same required contract directly. Recheck this table after every Fiber upgrade.

## Primary references

- [Fiber v3: What's New](https://docs.gofiber.io/whats_new/)
- [Fiber v3 middleware catalog](https://docs.gofiber.io/category/-middleware/)
- [Fiber v3 context guide](https://docs.gofiber.io/guide/go-context/)
- [Fiber v3.5.0 release](https://github.com/gofiber/fiber/releases/tag/v3.5.0)
