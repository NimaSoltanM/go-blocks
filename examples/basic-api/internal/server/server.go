package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func errorHandler(logger *slog.Logger) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		status := http.StatusInternalServerError
		var httpErr *fiber.Error
		if errors.As(err, &httpErr) && httpErr != nil && httpErr.Code >= 400 && httpErr.Code <= 599 {
			status = httpErr.Code
		} else if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		message := http.StatusText(status)
		if message == "" {
			status, message = http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)
		}
		code := strings.ReplaceAll(strings.ToLower(message), " ", "_")
		if status == http.StatusGatewayTimeout && errors.Is(err, context.DeadlineExceeded) {
			code, message = "request_timeout", "Request timed out"
		}
		id := requestid.FromContext(c)
		if id == "" {
			// Parser/body-limit errors can happen before request middleware runs.
			id = rand.Text()
		}
		if status >= 500 {
			logger.ErrorContext(c.Context(), "request_failed", "request_id", id, "error", err.Error())
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
		return c.Status(status).JSON(errorEnvelope{Error: errorDetail{
			Code: code, Message: message, RequestID: id,
		}})
	}
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
