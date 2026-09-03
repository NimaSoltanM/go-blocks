package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/requestid"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func testApp(t *testing.T, cfg Config) *fiber.App {
	t.Helper()
	app, err := New(cfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func request(t *testing.T, app *fiber.App, method, path, body string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Request-ID", "test-request-id")
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func expectError(t *testing.T, resp *http.Response, body []byte, status int, code string) {
	t.Helper()
	var envelope errorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("non-JSON error: %s (%v)", body, err)
	}
	if resp.StatusCode != status || envelope.Error.Code != code {
		t.Fatalf("status=%d body=%s; want %d %s", resp.StatusCode, body, status, code)
	}
	if envelope.Error.RequestID == "" || envelope.Error.RequestID != resp.Header.Get("X-Request-ID") {
		t.Fatalf("missing/mismatched request ID: %s", body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("error must not be cached")
	}
}

func TestRequestMetadataAndContextLifetime(t *testing.T) {
	app := testApp(t, DefaultConfig())
	var captured context.Context
	app.Get("/ok", func(c fiber.Ctx) error {
		captured = c.Context()
		if _, ok := captured.Deadline(); !ok || requestid.FromContext(captured) != "test-request-id" {
			return errors.New("deadline or request ID missing from service context")
		}
		return c.SendString("ok")
	})
	resp, body := request(t, app, "GET", "/ok", "")
	if resp.StatusCode != 200 || string(body) != "ok" || resp.Header.Get("X-Request-ID") != "test-request-id" {
		t.Fatalf("unexpected response: %d %s", resp.StatusCode, body)
	}
	if !errors.Is(captured.Err(), context.Canceled) {
		t.Fatalf("request context not released: %v", captured.Err())
	}
}

func TestErrorsAndPanicsAreJSONAndDiscardPartialResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		panic  bool
		status int
		code   string
	}{
		{"internal", errors.New("private-error-detail"), false, 500, "internal_server_error"},
		{"bad_request", fiber.NewError(400, "private-error-detail"), false, 400, "bad_request"},
		{"panic", errors.New("private-error-detail"), true, 500, "internal_server_error"},
		{"http_error_panic", fiber.ErrBadRequest, true, 500, "internal_server_error"},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), false, 504, "request_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			app, err := New(DefaultConfig(), slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			app.Get("/fail", func(c fiber.Ctx) error {
				c.Set("X-Partial", "private-partial")
				c.Cookie(&fiber.Cookie{Name: "session", Value: "private-cookie"})
				_ = c.SendString("private-body")
				if tc.panic {
					panic(tc.err)
				}
				return tc.err
			})
			resp, body := request(t, app, "GET", "/fail?token=private-query", "")
			expectError(t, resp, body, tc.status, tc.code)
			if strings.Contains(string(body), "private") || resp.Header.Get("X-Partial") != "" || len(resp.Cookies()) != 0 {
				t.Fatalf("partial data leaked: %s %v", body, resp.Header)
			}
			var entry map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					t.Fatal(err)
				}
			}
			if entry["msg"] != "http_request" || entry["status"] != float64(tc.status) || entry["request_id"] != "test-request-id" {
				t.Fatalf("incorrect final request log: %s", logs.String())
			}
			if strings.Contains(logs.String(), "private-query") {
				t.Fatal("query string leaked into access log")
			}
		})
	}
}

func TestDeadlineReplacesLateSuccessAndDoesNotAffectNextRequest(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RequestTimeout = 10 * time.Millisecond
	app := testApp(t, cfg)
	app.Get("/slow", func(c fiber.Ctx) error {
		<-c.Context().Done()
		return c.SendString("private-late-success")
	})
	app.Get("/fast", func(c fiber.Ctx) error { return c.SendString("ok") })
	resp, body := request(t, app, "GET", "/slow", "")
	expectError(t, resp, body, 504, "request_timeout")
	resp, body = request(t, app, "GET", "/fast", "")
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("next request inherited expired context: %d %s", resp.StatusCode, body)
	}
}

func TestNotFoundAndHealth(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BodyLimit = 128
	cfg.RequestTimeout = 10 * time.Millisecond
	app := testApp(t, cfg)
	resp, body := request(t, app, "GET", "/livez", "")
	expectError(t, resp, body, 404, "not_found")
	// Route registration is normally completed before serving. Use a new app
	// after checking that New did not implicitly install health endpoints.
	app = testApp(t, cfg)
	RegisterHealth(app, func(ctx context.Context) bool {
		<-ctx.Done()
		return false
	})
	resp, body = request(t, app, "GET", "/livez", "")
	if resp.StatusCode != 200 || string(body) != `{"status":"OK"}` {
		t.Fatalf("liveness: %d %s", resp.StatusCode, body)
	}
	resp, body = request(t, app, "HEAD", "/livez", "")
	if resp.StatusCode != 200 || len(body) != 0 {
		t.Fatalf("HEAD health: %d %s", resp.StatusCode, body)
	}
	resp, body = request(t, app, "GET", "/readyz", "")
	if resp.StatusCode != 503 || string(body) != `{"status":"Service Unavailable"}` || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("readiness failure: %d %s", resp.StatusCode, body)
	}
	app = testApp(t, DefaultConfig())
	RegisterHealth(app, nil)
	resp, body = request(t, app, "GET", "/readyz", "")
	if resp.StatusCode != 200 || string(body) != `{"status":"OK"}` {
		t.Fatalf("HTTP-only readiness: %d %s", resp.StatusCode, body)
	}
}

func TestNewRejectsInvalidInputs(t *testing.T) {
	if _, err := New(Config{}, quietLogger()); err == nil {
		t.Fatal("invalid config accepted")
	}
	if _, err := New(DefaultConfig(), nil); err == nil {
		t.Fatal("nil logger accepted")
	}
}
