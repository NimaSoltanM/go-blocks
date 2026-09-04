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

type testPublicError struct {
	status     int
	code       string
	message    string
	retryAfter time.Duration
	cause      error
}

type statefulPublicError struct {
	statusCalls, codeCalls, messageCalls, retryCalls int
}

type nilSlicePublicError []string

type wrappedBrokenError struct{ cause error }
type panickingPublicError struct{}

func (e nilSlicePublicError) Error() string           { return e[0] }
func (nilSlicePublicError) HTTPStatus() int           { return 400 }
func (nilSlicePublicError) PublicCode() string        { return "bad" }
func (nilSlicePublicError) PublicMessage() string     { return "Bad" }
func (nilSlicePublicError) RetryAfter() time.Duration { return 0 }

func (*wrappedBrokenError) Error() string   { return "wrapped broken error" }
func (e *wrappedBrokenError) Unwrap() error { return e.cause }

func (*panickingPublicError) Error() string             { return "panicking metadata" }
func (*panickingPublicError) HTTPStatus() int           { panic("metadata panic") }
func (*panickingPublicError) PublicCode() string        { return "bad" }
func (*panickingPublicError) PublicMessage() string     { return "Bad" }
func (*panickingPublicError) RetryAfter() time.Duration { return 0 }

func (*statefulPublicError) Error() string { return "internal stateful error" }
func (e *statefulPublicError) HTTPStatus() int {
	e.statusCalls++
	if e.statusCalls == 1 {
		return 429
	}
	return 200
}
func (e *statefulPublicError) PublicCode() string {
	e.codeCalls++
	return "rate_limited"
}
func (e *statefulPublicError) PublicMessage() string {
	e.messageCalls++
	return "Try again later"
}
func (e *statefulPublicError) RetryAfter() time.Duration {
	e.retryCalls++
	return time.Second
}

func (e *testPublicError) Error() string             { return e.cause.Error() }
func (e *testPublicError) Unwrap() error             { return e.cause }
func (e *testPublicError) HTTPStatus() int           { return e.status }
func (e *testPublicError) PublicCode() string        { return e.code }
func (e *testPublicError) PublicMessage() string     { return e.message }
func (e *testPublicError) RetryAfter() time.Duration { return e.retryAfter }

func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func testApp(t *testing.T, cfg Config) *fiber.App {
	t.Helper()
	app, err := New(cfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestPublicErrorMetadataAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		status    int
		code      string
		retry     string
		retryJSON int64
	}{
		{
			name: "valid wrapped rate limit",
			err: fmt.Errorf("request code: %w", &testPublicError{
				status: 429, code: "rate_limited", message: "Try again later",
				retryAfter: 1201 * time.Millisecond, cause: errors.New("private limit key"),
			}),
			status: 429, code: "rate_limited", retry: "2", retryJSON: 2,
		},
		{
			name: "invalid code falls back safely",
			err: &testPublicError{
				status: 400, code: "Bad-Code", message: "unsafe", cause: errors.New("private"),
			},
			status: 500, code: "internal_server_error",
		},
		{
			name: "retry metadata restricted by status",
			err: &testPublicError{
				status: 400, code: "invalid_request", message: "Invalid request",
				retryAfter: time.Second, cause: errors.New("private"),
			},
			status: 500, code: "internal_server_error",
		},
		{
			name:   "typed nil public error falls back safely",
			err:    (*testPublicError)(nil),
			status: 500, code: "internal_server_error",
		},
		{
			name:   "typed nil slice error falls back safely",
			err:    nilSlicePublicError(nil),
			status: 500, code: "internal_server_error",
		},
		{
			name:   "wrapped typed nil chain falls back safely",
			err:    &wrappedBrokenError{cause: nilSlicePublicError(nil)},
			status: 500, code: "internal_server_error",
		},
		{
			name:   "panicking public metadata falls back safely",
			err:    &panickingPublicError{},
			status: 500, code: "internal_server_error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := testApp(t, DefaultConfig())
			app.Get("/fail", func(fiber.Ctx) error { return tc.err })
			resp, body := request(t, app, "GET", "/fail", "")
			expectError(t, resp, body, tc.status, tc.code)
			if got := resp.Header.Get("Retry-After"); got != tc.retry {
				t.Fatalf("Retry-After=%q, want %q", got, tc.retry)
			}
			var envelope errorEnvelope
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.RetryAfterSeconds != tc.retryJSON {
				t.Fatalf("retry_after_seconds=%d, want %d", envelope.Error.RetryAfterSeconds, tc.retryJSON)
			}
			if strings.Contains(string(body), "private") {
				t.Fatalf("private cause leaked: %s", body)
			}
		})
	}
}

func TestPublicErrorMetadataIsSnapshottedOnce(t *testing.T) {
	publicErr := &statefulPublicError{}
	app := testApp(t, DefaultConfig())
	app.Get("/fail", func(fiber.Ctx) error { return publicErr })
	resp, body := request(t, app, "GET", "/fail", "")
	expectError(t, resp, body, 429, "rate_limited")
	if publicErr.statusCalls != 1 || publicErr.codeCalls != 1 || publicErr.messageCalls != 1 || publicErr.retryCalls != 1 {
		t.Fatalf("public metadata calls: status=%d code=%d message=%d retry=%d",
			publicErr.statusCalls, publicErr.codeCalls, publicErr.messageCalls, publicErr.retryCalls)
	}
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
