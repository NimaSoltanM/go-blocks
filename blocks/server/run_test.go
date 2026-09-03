package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

type observedListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (ln *observedListener) Close() error {
	err := ln.Listener.Close()
	ln.once.Do(func() { close(ln.closed) })
	return err
}

type testRun struct {
	url    string
	cancel context.CancelFunc
	done   chan struct{}
	closed chan struct{}
	err    error
}

func await(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server lifecycle event")
	}
}

func startTestRun(t *testing.T, app *fiber.App, cfg Config) *testRun {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &testRun{url: "http://" + ln.Addr().String(), cancel: cancel, done: make(chan struct{}), closed: make(chan struct{})}
	go func() {
		run.err = serve(ctx, app, &observedListener{Listener: ln, closed: run.closed}, cfg, quietLogger())
		close(run.done)
	}()
	t.Cleanup(func() { cancel(); await(t, run.done) })
	return run
}

func TestShutdownWaitsForInflightRequest(t *testing.T) {
	cfg := DefaultConfig()
	app := testApp(t, cfg)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	app.Get("/work", func(c fiber.Ctx) error {
		close(entered)
		<-release
		return c.SendString("finished")
	})
	run := startTestRun(t, app, cfg)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	requestDone := make(chan struct{})
	var responseBody string
	var requestErr error
	go func() {
		defer close(requestDone)
		client := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := client.Get(run.url + "/work")
		if err != nil {
			requestErr = err
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		requestErr, responseBody = err, string(body)
	}()
	await(t, entered)
	run.cancel()
	await(t, run.closed)
	select {
	case <-run.done:
		t.Fatal("server returned before the active request completed")
	default:
	}
	once.Do(func() { close(release) })
	await(t, requestDone)
	await(t, run.done)
	if run.err != nil || requestErr != nil || responseBody != "finished" {
		t.Fatalf("drain failed: server=%v request=%v body=%q", run.err, requestErr, responseBody)
	}
}

func TestShutdownTimeoutIsReturned(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ShutdownTimeout = 20 * time.Millisecond
	app := testApp(t, cfg)
	entered, release, requestDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	app.Get("/work", func(c fiber.Ctx) error {
		close(entered)
		<-release
		return c.SendString("finished")
	})
	run := startTestRun(t, app, cfg)
	t.Cleanup(func() { once.Do(func() { close(release) }); await(t, requestDone) })
	go func() {
		defer close(requestDone)
		client := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		if resp, err := client.Get(run.url + "/work"); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	await(t, entered)
	run.cancel()
	await(t, run.done)
	if !errors.Is(run.err, context.DeadlineExceeded) {
		t.Fatalf("expected drain deadline, got %v", run.err)
	}
	once.Do(func() { close(release) })
	await(t, requestDone)
}

func TestCancellationDuringStartupClosesListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	app := testApp(t, DefaultConfig())
	done := make(chan struct{})
	var serveErr error
	go func() {
		serveErr = serve(ctx, app, ln, DefaultConfig(), quietLogger())
		close(done)
	}()
	await(t, done)
	if serveErr != nil {
		t.Fatal(serveErr)
	}
	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listener leaked during startup: %v", err)
	}
	_ = rebound.Close()
}

func TestRunReportsBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := DefaultConfig()
	cfg.Address = ln.Addr().String()
	if err := Run(context.Background(), testApp(t, cfg), cfg, quietLogger()); err == nil {
		t.Fatal("binding to occupied port should fail")
	}
}

func TestBodyLimitOnNetwork(t *testing.T) {
	// app.Test rejects an oversized request before invoking Fiber's protocol
	// error handler, so exercise this guarantee through an actual TCP listener.
	cfg := DefaultConfig()
	cfg.BodyLimit = 128
	app := testApp(t, cfg)
	app.Post("/body", func(c fiber.Ctx) error { return c.SendStatus(204) })
	run := startTestRun(t, app, cfg)
	client := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Post(run.url+"/body", "text/plain", strings.NewReader(strings.Repeat("x", 129)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	expectError(t, resp, body, 413, "request_entity_too_large")
}
