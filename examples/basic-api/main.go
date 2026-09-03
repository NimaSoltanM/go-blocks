package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"example.local/basic-api/internal/server"
	"github.com/gofiber/fiber/v3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := server.LoadConfig()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	app, err := newApp(cfg, logger)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx, app, cfg, logger)
}

func newApp(cfg server.Config, logger *slog.Logger) (*fiber.App, error) {
	app, err := server.New(cfg, logger)
	if err != nil {
		return nil, err
	}
	// Supply a readiness callback here when the application owns real clients.
	server.RegisterHealth(app, nil)
	app.Get("/api/hello", func(c fiber.Ctx) error {
		message, err := greeting(c.Context())
		if err != nil {
			return err
		}
		return c.JSON(fiber.Map{"message": message})
	})
	return app, nil
}

func greeting(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "Hello from Go Blocks", nil
}
