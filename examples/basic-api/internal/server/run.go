package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"

	"github.com/gofiber/fiber/v3"
)

// Run owns the TCP listener and blocks until shutdown has finished. Cancel ctx
// to stop accepting connections and drain in-flight requests. Register all routes
// before calling it; use each app for one Run. The caller owns logger and clients.
func Run(ctx context.Context, app *fiber.App, cfg Config, logger *slog.Logger) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if ctx == nil || app == nil || logger == nil {
		return errors.New("server context, app, and logger are required")
	}
	if ctx.Err() != nil {
		return nil
	}
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return err
	}
	return serve(ctx, app, listener, cfg, logger)
}

func serve(ctx context.Context, app *fiber.App, listener net.Listener, cfg Config, logger *slog.Logger) error {
	defer listener.Close()
	ln := &startingListener{Listener: listener, ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
	}()

	// Wait until fasthttp has registered the listener before asking it to shut
	// down. A cancellation during startup must not leave an untracked listener.
	select {
	case <-ln.ready:
	case err := <-done:
		return err
	}
	logger.Info("server_started", "address", listener.Addr().String())

	var serveErr error
	finished := false
	select {
	case <-ctx.Done():
	case serveErr = <-done:
		finished = true
	}
	logger.Info("server_stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	shutdownErr := app.ShutdownWithContext(shutdownCtx)
	if !finished {
		serveErr = <-done
	}
	// Listener closure alone is not completion: ShutdownWithContext waits for
	// active connections or its deadline. Surface a drain timeout to the caller.
	err := errors.Join(serveErr, shutdownErr)
	if err == nil {
		logger.Info("server_stopped")
	} else {
		logger.Error("server_stop_failed", "error", err.Error())
	}
	return err
}

type startingListener struct {
	net.Listener
	ready chan struct{}
	once  sync.Once
}

func (ln *startingListener) Accept() (net.Conn, error) {
	ln.once.Do(func() { close(ln.ready) })
	return ln.Listener.Accept()
}
