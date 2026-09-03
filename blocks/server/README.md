# Server

A copyable Fiber v3 server foundation. It provides validated environment
configuration, structured request logs, request IDs, panic recovery, JSON errors,
cooperative request deadlines, explicit health routes, and graceful shutdown.

Requires Go 1.26+ and `github.com/gofiber/fiber/v3 v3.5.0` (the verified version).
There are no other block dependencies, database clients, schema objects,
migrations, background workers, or external services required by this block.

## Copy it

1. Create a new package such as `internal/server` in your application's Go module.
2. Copy `config.go`, `server.go`, and `run.go` into it. Also copy the three
   `*_test.go` files if you want to retain the block's behavior checks.
3. Add the dependency with `go get github.com/gofiber/fiber/v3@v3.5.0`, then run
   `go mod tidy`. In an existing application, review dependency changes and run
   its tests before accepting an upgrade.
4. Import `YOUR_MODULE/internal/server` in your application. Source files inside
   the block contain no repository-specific imports to rewrite.
5. Create the logger and app, register routes, and call `Run` as shown below.

The separate module in `examples/basic-api` contains a real copy of these three
files. It has its own `go.mod`/`go.sum` and no replacement or import pointing at
the development repository. The repository checks verify that its copied source
matches this block and test both modules independently with `GOWORK=off`.

## Wire it

```go
cfg, err := server.LoadConfig()
if err != nil {
    return err
}
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level: cfg.LogLevel,
}))
app, err := server.New(cfg, logger)
if err != nil {
    return err
}

server.RegisterHealth(app, nil)
app.Get("/api/hello", func(c fiber.Ctx) error {
    return c.JSON(fiber.Map{"message": "hello"})
})

ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
return server.Run(ctx, app, cfg, logger)
```

Use the complete `main.go` in the example for imports and top-level error handling.
`New` returns an ordinary `*fiber.App`; all route registration is explicit and
must finish before the app starts serving. Pass the same config to `New` and
`Run`. A single app supports one `Run` call.

The application constructs, owns, and closes its PostgreSQL pool and Redis client.
Pass those clients into feature handlers/services. The server does not create or
close them. When those clients exist, pass a readiness callback that calls their
health checks using the provided `context.Context`. The callback must honor its
deadline. Returning an error makes readiness return 503; liveness stays 200.

## Configuration

`LoadConfig` reads these variables. Unset variables use defaults; explicitly empty
or invalid values fail startup. It does not automatically read a `.env` file.

| Variable | Default | Meaning |
| --- | --- | --- |
| `HTTP_ADDR` | `127.0.0.1:8080` | TCP listen address; `:8080` binds all interfaces; port `0` selects an available port |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`, used when the caller constructs its logger |
| `HTTP_BODY_LIMIT` | `1048576` | Maximum request body size, in bytes |
| `HTTP_READ_TIMEOUT` | `5s` | Time allowed to read a request |
| `HTTP_WRITE_TIMEOUT` | `15s` | Response write deadline; must exceed the request timeout |
| `HTTP_IDLE_TIMEOUT` | `60s` | Idle keep-alive timeout |
| `HTTP_REQUEST_TIMEOUT` | `10s` | Cooperative handler/service deadline |
| `HTTP_SHUTDOWN_TIMEOUT` | `15s` | Maximum time spent waiting for active connections to drain |

All limits and durations must be positive. `DefaultConfig()` and `Config.Validate()`
also support explicit configuration without environment variables.

## HTTP behavior

`RegisterHealth` explicitly installs:

| Route | Success | Failure |
| --- | --- | --- |
| `GET /livez` | `200 {"status":"OK"}` | Independent of database readiness |
| `GET /readyz` | `200 {"status":"OK"}` | `503 {"status":"Service Unavailable"}` if the supplied probe returns false |

Both routes use Fiber v3's `healthcheck` middleware and conventional endpoint
constants, support `HEAD`, and set `Cache-Control: no-store`. A nil readiness
probe checks HTTP readiness only; it makes no claim about PostgreSQL or Redis.
The probe returns a bool because that is Fiber's current API. Log a dependency
error inside the application's probe before returning false; health responses do
not expose dependency details.

Application errors use one shape, including 404s, panics, oversized bodies, and deadlines:

```json
{
  "error": {
    "code": "request_timeout",
    "message": "Request timed out",
    "request_id": "correlation-id"
  }
}
```

Wrapped `fiber.Error` values retain valid HTTP error status codes. Messages use
standard HTTP status text, rather than the error's raw message. Other errors and
all panics become 500. A returned/wrapped `context.DeadlineExceeded` becomes 504
with code `request_timeout`. The native healthcheck middleware owns the compact
operational response shown above.
An explicit non-success response such as readiness 503 is preserved if its probe
finishes as the request deadline expires; only a late success is replaced by 504.
Error responses discard partial bodies and headers (including cookies), set
`Cache-Control: no-store`, and include the request ID in `X-Request-ID` and JSON.
Customize `errorHandler` if your application needs specific public validation errors.

Middleware order is Fiber v3 request ID, Fiber v3 logger with a `slog` adapter,
Fiber v3 recovery, then
request deadline. Request logs contain the final status, method, matched route
pattern, duration, and request ID. They omit query strings, request bodies, and
headers. Server error details are logged in `request_failed` for diagnosis; those
internal logs can contain information from application errors. Protocol errors
that occur before middleware receive JSON errors and IDs but no middleware access log.

Valid inbound `X-Request-ID` values are preserved by Fiber's request-ID middleware;
missing or invalid values are generated. They are correlation data, not trusted
identity. Use `requestid.FromContext(c.Context())` in service logging if needed.

## Deadlines and shutdown

Pass **`c.Context()`** to queries and external calls. The block derives a deadline
from the existing context, preserving any earlier deadline, and cancels it when
the handler returns. A late successful response is replaced with a 504. Explicit
handler errors retain their normal mapping.

Deadlines are cooperative: a handler that ignores its context can keep running.
This block does not launch a second goroutine to interrupt a handler or claim to
roll back work already committed. Socket timeouts do not cancel arbitrary Go work.
Streaming/SSE/WebSocket endpoints need a separate lifetime/deadline policy before
being added to this base stack. Do not retain `fiber.Ctx` or references to reused
request buffers in asynchronous work.

Canceling the context passed to `Run` closes the listener, then waits for in-flight
requests. It uses a fresh shutdown deadline, so canceling the lifecycle context
does not immediately abort the drain. A completed drain returns nil; a drain
deadline or server failure returns an error. Deadline expiry stops waiting but
does not forcibly kill handler goroutines. Keep app-owned clients alive until
`Run` returns and handle a shutdown error explicitly.

The caller installs signal handling. The example handles Ctrl+C and SIGTERM and
exits nonzero on failure. `Run` serves plain HTTP; configure a reverse proxy or
adapt the listener for TLS in your deployment. Bind failures are returned to the caller.

## Customize and verify

Edit the copied package to fit your service's routes, body limits, error contract,
logger, and timeout policy. It is source you own. Record the source commit when
copying from a committed version; updates are reviewed manually.

From this repository, run `go test -race ./blocks/server`. Tests cover invalid
configuration, request metadata, error/log consistency, panic recovery, deadline
handling, liveness/readiness, TCP body limits, startup cancellation, and graceful
shutdown including a drain timeout. They use ephemeral loopback listeners and
require no database or container services.
