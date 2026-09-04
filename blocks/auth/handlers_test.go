package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type testErrorEnvelope struct {
	Error struct {
		Code              string `json:"code"`
		Message           string `json:"message"`
		RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
	} `json:"error"`
}

func authTestApp() *fiber.App {
	return fiber.New(fiber.Config{ErrorHandler: func(c fiber.Ctx, err error) error {
		status, code, message, retry := 500, "internal_server_error", "Internal Server Error", int64(0)
		var public interface {
			error
			HTTPStatus() int
			PublicCode() string
			PublicMessage() string
			RetryAfter() time.Duration
		}
		if errors.As(err, &public) {
			status, code, message = public.HTTPStatus(), public.PublicCode(), public.PublicMessage()
			retry = durationSeconds(public.RetryAfter())
		}
		c.Response().Reset()
		c.Set(fiber.HeaderCacheControl, "no-store")
		if retry > 0 {
			c.Set(fiber.HeaderRetryAfter, strconv.FormatInt(retry, 10))
		}
		return c.Status(status).JSON(fiber.Map{"error": fiber.Map{
			"code": code, "message": message, "retry_after_seconds": retry,
		}})
	}})
}

func doRequest(t *testing.T, app *fiber.App, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var envelope testErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode error response %q: %v", body, err)
	}
	return envelope.Error.Code
}

func TestRequestCodeStrictJSONAndErrorMapping(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		status      int
		code        string
	}{
		{"success", "application/json", `{"phone":"09121234567"}`, 202, ""},
		{"media type parameters", "application/json; charset=utf-8", `{"phone":"09121234567"}`, 202, ""},
		{"missing content type", "", `{"phone":"09121234567"}`, 400, "invalid_request"},
		{"wrong content type", "text/plain", `{"phone":"09121234567"}`, 400, "invalid_request"},
		{"empty", "application/json", ``, 400, "invalid_request"},
		{"array", "application/json", `[]`, 400, "invalid_request"},
		{"missing field", "application/json", `{}`, 400, "invalid_request"},
		{"unknown field", "application/json", `{"phone":"09121234567","email":"x"}`, 400, "invalid_request"},
		{"case variant", "application/json", `{"Phone":"09121234567"}`, 400, "invalid_request"},
		{"duplicate", "application/json", `{"phone":"09121234567","phone":"09120000000"}`, 400, "invalid_request"},
		{"trailing object", "application/json", `{"phone":"09121234567"}{}`, 400, "invalid_request"},
		{"wrong type", "application/json", `{"phone":123}`, 400, "invalid_request"},
		{"null is not a string", "application/json", `{"phone":null}`, 400, "invalid_request"},
		{"invalid UTF-8", "application/json", "{\"phone\":\"\xff\"}", 400, "invalid_request"},
		{"invalid phone", "application/json", `{"phone":"02112345678"}`, 400, "invalid_phone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakes := newFakes()
			cfg := validTestConfig()
			cfg.Transport = TransportBearer
			b := newBlock(cfg, fakes.otp, fakes.users, fakes.sms, testLogger(), newMemoryStorage())
			b.code = func() (string, error) { return "123456", nil }
			app := authTestApp()
			app.Post("/auth/otp/request", b.RequestCode)
			req := newRequest("POST", "/auth/otp/request", tc.body)
			if tc.contentType == "" {
				req.Header.Del("Content-Type")
			} else {
				req.Header.Set("Content-Type", tc.contentType)
			}
			resp, body := doRequest(t, app, req)
			if resp.StatusCode != tc.status || resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("response = %d %q headers=%v", resp.StatusCode, body, resp.Header)
			}
			if tc.code != "" && errorCode(t, body) != tc.code {
				t.Fatalf("body = %s", body)
			}
			wantCalls := 0
			if tc.status == 202 {
				wantCalls = 1
				if string(body) != `{"expires_in_seconds":120,"resend_after_seconds":60,"status":"code_sent"}` {
					t.Fatalf("success body = %s", body)
				}
			}
			if fakes.otp.admitCalls != wantCalls || fakes.sms.calls != wantCalls {
				t.Fatalf("side effects occurred for invalid request: admit=%d SMS=%d", fakes.otp.admitCalls, fakes.sms.calls)
			}
		})
	}
}

func TestRequestCodeRejectsAmbiguousContentType(t *testing.T) {
	fakes := newFakes()
	cfg := validTestConfig()
	cfg.Transport = TransportBearer
	b := newBlock(cfg, fakes.otp, fakes.users, fakes.sms, testLogger(), newMemoryStorage())
	app := authTestApp()
	app.Post("/request", b.RequestCode)
	req := newRequest("POST", "/request", `{"phone":"09121234567"}`)
	req.Header.Add("Content-Type", "text/plain")
	resp, body := doRequest(t, app, req)
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_request" || fakes.sms.calls != 0 {
		t.Fatalf("ambiguous Content-Type = %d %s SMS=%d", resp.StatusCode, body, fakes.sms.calls)
	}
}

func TestVerifyCodeValidationAndMappedFailures(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		verifyErr  error
		userErr    error
		status     int
		code       string
		retryAfter string
	}{
		{"bad code format", `{"phone":"09121234567","code":"12345"}`, nil, nil, 400, "invalid_code_format", ""},
		{"null code", `{"phone":"09121234567","code":null}`, nil, nil, 400, "invalid_request", ""},
		{"invalid code", `{"phone":"09121234567","code":"123456"}`, ErrInvalidCode, nil, 401, "invalid_code", ""},
		{"rate limit", `{"phone":"09121234567","code":"123456"}`, &rateLimitError{retryAfter: 1201 * time.Millisecond}, nil, 429, "rate_limited", "2"},
		{"Redis failure", `{"phone":"09121234567","code":"123456"}`, errTestDependency, nil, 503, "service_unavailable", ""},
		{"PostgreSQL failure", `{"phone":"09121234567","code":"123456"}`, nil, errTestDependency, 503, "service_unavailable", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakes := newFakes()
			fakes.otp.verifyErr, fakes.users.err = tc.verifyErr, tc.userErr
			cfg := validTestConfig()
			cfg.Transport = TransportBearer
			b := newBlock(cfg, fakes.otp, fakes.users, fakes.sms, testLogger(), newMemoryStorage())
			app := authTestApp()
			app.Post("/auth/otp/verify", b.VerifyCode)
			resp, body := doRequest(t, app, newRequest("POST", "/auth/otp/verify", tc.body))
			if resp.StatusCode != tc.status || errorCode(t, body) != tc.code || resp.Header.Get("Retry-After") != tc.retryAfter {
				t.Fatalf("response = %d %s Retry-After=%q", resp.StatusCode, body, resp.Header.Get("Retry-After"))
			}
		})
	}
}

func TestCookieHandlersFailClosedWhenSecurityMiddlewareIsMissing(t *testing.T) {
	fakes := newFakes()
	b := newBlock(validTestConfig(), fakes.otp, fakes.users, fakes.sms, testLogger(), newMemoryStorage())
	app := authTestApp()
	app.Post("/request", b.RequestCode)
	app.Post("/verify", b.VerifyCode)
	app.Post("/logout", b.Logout)
	for path, body := range map[string]string{
		"/request": `{"phone":"09121234567"}`,
		"/verify":  `{"phone":"09121234567","code":"123456"}`,
		"/logout":  "",
	} {
		resp, responseBody := doRequest(t, app, newRequest("POST", path, body))
		if resp.StatusCode != 503 || errorCode(t, responseBody) != "service_unavailable" {
			t.Errorf("%s = %d %s", path, resp.StatusCode, responseBody)
		}
	}
	if fakes.otp.admitCalls != 0 || fakes.otp.verifyCalls != 0 || fakes.sms.calls != 0 || fakes.users.calls != 0 {
		t.Fatal("an auth side effect occurred without the cookie security stack")
	}
}

func TestBearerLoginIdentityRotationAndLogout(t *testing.T) {
	fakes := newFakes()
	cfg := validTestConfig()
	cfg.Transport = TransportBearer
	storage := newMemoryStorage()
	b := newBlock(cfg, fakes.otp, fakes.users, fakes.sms, testLogger(), storage)
	app := authTestApp()
	app.Post("/auth/otp/verify", b.VerifyCode)
	app.Get("/auth/me", b.RequireUser, b.Me)
	app.Post("/auth/logout", b.Logout)

	verify := func(token string) string {
		req := newRequest("POST", "/auth/otp/verify", `{"phone":"09121234567","code":"123456"}`)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, body := doRequest(t, app, req)
		if resp.StatusCode != 200 || len(resp.Cookies()) != 0 {
			t.Fatalf("verify = %d %s cookies=%v", resp.StatusCode, body, resp.Cookies())
		}
		var value struct {
			User         User   `json:"user"`
			SessionToken string `json:"session_token"`
		}
		if err := json.Unmarshal(body, &value); err != nil || value.User != fakes.users.user || !validSessionToken(value.SessionToken) {
			t.Fatalf("verify body = %s, err=%v", body, err)
		}
		return value.SessionToken
	}

	first := verify("")
	req := newRequest("GET", "/auth/me", "")
	req.Header.Set("Authorization", "Bearer "+first)
	resp, body := doRequest(t, app, req)
	if resp.StatusCode != 200 || !strings.Contains(string(body), fakes.users.user.ID.String()) {
		t.Fatalf("me = %d %s", resp.StatusCode, body)
	}

	second := verify(first)
	if second == first {
		t.Fatal("successful login did not rotate bearer session")
	}
	req = newRequest("GET", "/auth/me", "")
	req.Header.Set("Authorization", "Bearer "+first)
	resp, body = doRequest(t, app, req)
	if resp.StatusCode != 401 || errorCode(t, body) != "authentication_required" {
		t.Fatalf("old token still valid: %d %s", resp.StatusCode, body)
	}

	req = newRequest("POST", "/auth/logout", "")
	req.Header.Set("Authorization", "Bearer "+second)
	resp, body = doRequest(t, app, req)
	if resp.StatusCode != 204 || len(body) != 0 {
		t.Fatalf("logout = %d %q", resp.StatusCode, body)
	}
	req = newRequest("GET", "/auth/me", "")
	req.Header.Set("Authorization", "Bearer "+second)
	resp, body = doRequest(t, app, req)
	if resp.StatusCode != 401 {
		t.Fatalf("logged-out token valid: %d %s", resp.StatusCode, body)
	}

	for _, header := range []string{"", "Basic abc", "Bearer malformed", "Bearer  one-space"} {
		req = newRequest("POST", "/auth/logout", "")
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, body = doRequest(t, app, req)
		if resp.StatusCode != 204 || len(body) != 0 {
			t.Errorf("idempotent logout %q = %d %q", header, resp.StatusCode, body)
		}
	}
}

func TestBearerAbsoluteExpiryIsNotExtendedByIdleRefresh(t *testing.T) {
	fakes := newFakes()
	cfg := validTestConfig()
	cfg.Transport = TransportBearer
	cfg.Session.IdleTimeout = 500 * time.Millisecond
	cfg.Session.AbsoluteTimeout = 700 * time.Millisecond
	b := newBlock(cfg, fakes.otp, fakes.users, fakes.sms, testLogger(), newMemoryStorage())
	app := authTestApp()
	app.Post("/verify", b.VerifyCode)
	app.Get("/me", b.RequireUser, b.Me)

	resp, body := doRequest(t, app, newRequest("POST", "/verify", `{"phone":"09121234567","code":"123456"}`))
	if resp.StatusCode != 200 {
		t.Fatalf("login = %d %s", resp.StatusCode, body)
	}
	var login struct {
		SessionToken string `json:"session_token"`
	}
	if err := json.Unmarshal(body, &login); err != nil {
		t.Fatal(err)
	}
	requestMe := func() (*http.Response, []byte) {
		req := newRequest("GET", "/me", "")
		req.Header.Set("Authorization", "Bearer "+login.SessionToken)
		return doRequest(t, app, req)
	}
	for range 2 {
		time.Sleep(200 * time.Millisecond)
		resp, body = requestMe()
		if resp.StatusCode != 200 {
			t.Fatalf("idle refresh = %d %s", resp.StatusCode, body)
		}
	}
	time.Sleep(350 * time.Millisecond)
	resp, body = requestMe()
	if resp.StatusCode != 401 || errorCode(t, body) != "authentication_required" {
		t.Fatalf("absolute expiry was extended: %d %s", resp.StatusCode, body)
	}
}

func TestCookieCSRFLoginIdentityAndLogout(t *testing.T) {
	fakes := newFakes()
	cfg := validTestConfig()
	cfg.Cookie.Secure = false
	cfg.Cookie.AllowInsecure = true
	cfg.Cookie.SessionName = "gb_session_dev"
	cfg.Cookie.CSRFName = "gb_csrf_dev"
	storage := newMemoryStorage()
	b := newBlock(cfg, fakes.otp, fakes.users, fakes.sms, testLogger(), storage)
	sessionMW, _ := b.SessionMiddleware()
	csrfMW, _ := b.CSRFMiddleware()
	app := authTestApp()
	app.Use(sessionMW, csrfMW)
	app.Get("/auth/csrf", b.CSRFToken)
	app.Post("/auth/otp/verify", b.VerifyCode)
	app.Get("/auth/me", b.RequireUser, b.Me)
	app.Post("/auth/logout", b.Logout)

	cookies := map[string]*http.Cookie{}
	updateCookies := func(resp *http.Response) {
		for _, cookie := range resp.Cookies() {
			cookies[cookie.Name] = cookie
		}
	}
	addCookies := func(req *http.Request) {
		for _, cookie := range cookies {
			if cookie.MaxAge >= 0 {
				req.AddCookie(cookie)
			}
		}
	}

	resp, body := doRequest(t, app, newRequest("GET", "/auth/csrf", ""))
	if resp.StatusCode != 200 {
		t.Fatalf("csrf = %d %s", resp.StatusCode, body)
	}
	updateCookies(resp)
	var csrfBody struct {
		Token string `json:"csrf_token"`
	}
	if err := json.Unmarshal(body, &csrfBody); err != nil || csrfBody.Token == "" {
		t.Fatalf("csrf body = %s, %v", body, err)
	}
	if cookies[cfg.Cookie.SessionName] == nil || cookies[cfg.Cookie.CSRFName] == nil ||
		!cookies[cfg.Cookie.SessionName].HttpOnly || !cookies[cfg.Cookie.CSRFName].HttpOnly {
		t.Fatalf("cookie flags/names = %#v", cookies)
	}

	withoutCSRF := newRequest("POST", "/auth/otp/verify", `{"phone":"09121234567","code":"123456"}`)
	addCookies(withoutCSRF)
	resp, body = doRequest(t, app, withoutCSRF)
	if resp.StatusCode != 403 || errorCode(t, body) != "csrf_failed" || fakes.otp.verifyCalls != 0 {
		t.Fatalf("missing CSRF = %d %s verifyCalls=%d", resp.StatusCode, body, fakes.otp.verifyCalls)
	}

	oldSession := cookies[cfg.Cookie.SessionName].Value
	verifyReq := newRequest("POST", "/auth/otp/verify", `{"phone":"09121234567","code":"123456"}`)
	addCookies(verifyReq)
	verifyReq.Header.Set("X-CSRF-Token", csrfBody.Token)
	resp, body = doRequest(t, app, verifyReq)
	if resp.StatusCode != 200 || strings.Contains(string(body), "session_token") {
		t.Fatalf("cookie verify = %d %s cause=%q token=%q csrf_cookie=%#v session_cookie=%#v", resp.StatusCode, body, resp.Header.Get("X-Test-Error"), csrfBody.Token, cookies[cfg.Cookie.CSRFName], cookies[cfg.Cookie.SessionName])
	}
	updateCookies(resp)
	if cookies[cfg.Cookie.SessionName].Value == oldSession {
		t.Fatal("successful login did not rotate cookie session")
	}

	meReq := newRequest("GET", "/auth/me", "")
	addCookies(meReq)
	resp, body = doRequest(t, app, meReq)
	if resp.StatusCode != 200 || !strings.Contains(string(body), fakes.users.user.ID.String()) {
		t.Fatalf("me = %d %s", resp.StatusCode, body)
	}

	logoutReq := newRequest("POST", "/auth/logout", "")
	addCookies(logoutReq)
	logoutReq.Header.Set("X-CSRF-Token", csrfBody.Token)
	resp, body = doRequest(t, app, logoutReq)
	if resp.StatusCode != 204 || len(body) != 0 {
		t.Fatalf("logout = %d %q", resp.StatusCode, body)
	}
	updateCookies(resp)
	if cookie := cookies[cfg.Cookie.SessionName]; cookie == nil || cookie.MaxAge >= 0 {
		t.Fatalf("session cookie was not expired: %#v", cookie)
	}
}

func TestIdentityRejectsCorruptSessionValues(t *testing.T) {
	valid := map[any]any{
		sessionSchemaKey: sessionSchemaV1, sessionUserIDKey: "9c1fcb97-942c-4f8a-94f7-dc165c737cc6",
		sessionPhoneKey: "+989121234567", sessionVerifiedKey: "2026-09-03T10:00:00Z",
	}
	getter := func(values map[any]any) func(any) any { return func(key any) any { return values[key] } }
	if _, ok := identityFromGetter(getter(valid)); !ok {
		t.Fatal("valid identity rejected")
	}
	withNilID := make(map[any]any, len(valid))
	for key, value := range valid {
		withNilID[key] = value
	}
	withNilID[sessionUserIDKey] = uuid.Nil.String()
	if _, ok := identityFromGetter(getter(withNilID)); ok {
		t.Fatal("nil user UUID was accepted")
	}
	for _, key := range []any{sessionSchemaKey, sessionUserIDKey, sessionPhoneKey, sessionVerifiedKey} {
		copy := make(map[any]any, len(valid))
		for k, v := range valid {
			copy[k] = v
		}
		copy[key] = fmt.Sprintf("bad-%v", key)
		if _, ok := identityFromGetter(getter(copy)); ok {
			t.Errorf("corrupt %v accepted", key)
		}
	}
}
