package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/healthcheck"
	loggermw "github.com/gofiber/fiber/v3/middleware/logger"
	recovermw "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
)

// New creates a normal Fiber app with the base middleware installed. The caller
// registers every route, including health routes, before calling Run.
func New(cfg Config, logger *slog.Logger) (*fiber.App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		return nil, errors.New("server logger is required")
	}
	handleError := errorHandler(logger)
	app := fiber.New(fiber.Config{
		BodyLimit:           cfg.BodyLimit,
		ReadTimeout:         cfg.ReadTimeout,
		WriteTimeout:        cfg.WriteTimeout,
		IdleTimeout:         cfg.IdleTimeout,
		PassLocalsToContext: true,
		ErrorHandler:        handleError,
	})
	app.Use(requestid.New())
	app.Use(loggermw.New(loggermw.Config{
		// Fiber's v3 logger renders errors through the app's ErrorHandler before
		// calling LoggerFunc, so the response status is final here.
		Format: "${latency}",
		LoggerFunc: func(c fiber.Ctx, data *loggermw.Data, _ *loggermw.Config) error {
			level := slog.LevelInfo
			if c.Response().StatusCode() >= 500 {
				level = slog.LevelError
			}
			logger.LogAttrs(c.Context(), level, "http_request",
				slog.String("request_id", requestid.FromContext(c)),
				slog.String("method", c.Method()),
				slog.String("route", c.Route().Path),
				slog.Int("status", c.Response().StatusCode()),
				slog.Duration("duration", data.Stop.Sub(data.Start)),
			)
			return nil
		},
	}))
	app.Use(recovermw.New(recovermw.Config{
		PanicHandler: func(_ fiber.Ctx, value any) error {
			// A panic must remain a 500 even when its value is a fiber.Error.
			return fmt.Errorf("panic recovered: %v", value)
		},
	}))
	app.Use(func(c fiber.Ctx) error {
		parent := c.Context()
		ctx, cancel := context.WithTimeout(parent, cfg.RequestTimeout)
		c.SetContext(ctx)
		defer cancel()
		defer c.SetContext(parent)
		err := c.Next()
		// Preserve an explicit error response such as healthcheck's 503. Replace
		// only a late success with the common request-timeout contract.
		if err == nil && ctx.Err() != nil && c.Response().StatusCode() < 400 {
			return ctx.Err()
		}
		return err
	})
	return app, nil
}

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	RequestID         string `json:"request_id"`
	RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
}

func errorHandler(logger *slog.Logger) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		status := http.StatusInternalServerError
		var retryAfter int64
		var publicErr PublicError
		var httpErr *fiber.Error
		typedNil := isNilError(err)
		deadlineExceeded := !typedNil && safeErrorsIs(err, context.DeadlineExceeded)
		metadata, publicOK := publicErrorMetadata{}, false
		if !typedNil && safeErrorsAs(err, &publicErr) {
			metadata, publicOK = readPublicError(publicErr)
		}
		if publicOK {
			status = metadata.status
			retryAfter = retryAfterSeconds(metadata.retryAfter)
		} else if !typedNil && safeErrorsAs(err, &httpErr) && httpErr != nil && httpErr.Code >= 400 && httpErr.Code <= 599 {
			status = httpErr.Code
		} else if deadlineExceeded {
			status = http.StatusGatewayTimeout
		}
		message := http.StatusText(status)
		if message == "" {
			status, message = http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)
		}
		code := strings.ReplaceAll(strings.ToLower(message), " ", "_")
		if publicOK {
			code, message = metadata.code, metadata.message
		} else if status == http.StatusGatewayTimeout && deadlineExceeded {
			code, message = "request_timeout", "Request timed out"
		}
		id := requestid.FromContext(c)
		if id == "" {
			// Parser/body-limit errors can happen before request middleware runs.
			id = rand.Text()
		}
		if status >= 500 {
			internalMessage := safeErrorMessage(err)
			logger.ErrorContext(c.Context(), "request_failed", "request_id", id, "error", internalMessage)
		}
		// Discard any partial success body and headers, including cookies. Preserve
		// a protocol-level connection close requested by the HTTP server.
		closeConnection := c.Response().ConnectionClose()
		c.Response().Reset()
		if closeConnection {
			c.Response().SetConnectionClose()
		}
		c.Set("X-Request-ID", id)
		c.Set("Cache-Control", "no-store")
		if retryAfter > 0 {
			c.Set("Retry-After", strconv.FormatInt(retryAfter, 10))
		}
		return c.Status(status).JSON(errorEnvelope{Error: errorDetail{
			Code: code, Message: message, RequestID: id, RetryAfterSeconds: retryAfter,
		}})
	}
}

func safeErrorsAs(err error, target any) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return errors.As(err, target)
}

func safeErrorsIs(err, target error) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return errors.Is(err, target)
}

func safeErrorMessage(err error) (message string) {
	if isNilError(err) {
		return "typed nil error"
	}
	defer func() {
		if recover() != nil {
			message = "error string panicked"
		}
	}()
	return err.Error()
}

// RegisterHealth explicitly adds Fiber v3's conventional GET/HEAD /livez and
// /readyz handlers. A nil probe checks HTTP readiness only. The caller owns any
// checked dependencies and logs their failure details before returning false.
func RegisterHealth(app fiber.Router, ready func(context.Context) bool) {
	noStore := func(c fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		return c.Next()
	}
	app.Get(healthcheck.LivenessEndpoint, noStore, healthcheck.New(healthcheck.Config{
		ResponseFormat: healthcheck.FormatJSON,
	}))
	app.Get(healthcheck.ReadinessEndpoint, noStore, healthcheck.New(healthcheck.Config{
		ResponseFormat: healthcheck.FormatJSON,
		Probe: func(c fiber.Ctx) bool {
			return ready == nil || ready(c.Context())
		},
	}))
}
