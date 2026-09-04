package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/phone-auth-api/internal/auth"
)

func TestDecodePepper(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	if value, err := decodePepper(valid); err != nil || len(value) != 32 {
		t.Fatalf("valid pepper = %d bytes, %v", len(value), err)
	}
	for _, value := range []string{"", "not-base64", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := decodePepper(value); err == nil {
			t.Errorf("invalid pepper %q accepted", value)
		}
	}
}

func TestWebhookURLPolicy(t *testing.T) {
	for _, value := range []string{
		"", "http://example.com/send", "https://user:pass@example.com/send",
		"https://example.com/send?token=secret", "file:///tmp/code",
	} {
		if _, err := newWebhookSMS(value, ""); err == nil {
			t.Errorf("unsafe webhook URL %q accepted", value)
		}
	}
	for _, value := range []string{"https://example.com/send", "http://localhost:8080/send", "http://127.0.0.1/send"} {
		if _, err := newWebhookSMS(value, ""); err != nil {
			t.Errorf("safe webhook URL %q rejected: %v", value, err)
		}
	}
}

func TestWebhookSMSSendsBoundedRequestWithoutFollowingRedirects(t *testing.T) {
	var received struct {
		Phone     string    `json:"phone"`
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer provider-token" || r.Header.Get("Idempotency-Key") != "message-id" {
			t.Errorf("headers = %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Errorf("payload %q: %v", body, err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	sender, err := newWebhookSMS(server.URL, "provider-token")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	if err := sender.SendCode(context.Background(), auth.SMSCode{
		Phone: "+989121234567", Code: "012345", ExpiresAt: expires, IdempotencyKey: "message-id",
	}); err != nil {
		t.Fatal(err)
	}
	if received.Phone != "+989121234567" || received.Code != "012345" || !received.ExpiresAt.Equal(expires) {
		t.Fatalf("received = %+v", received)
	}

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", server.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = w.Write([]byte("private provider body"))
	}))
	defer redirect.Close()
	sender, err = newWebhookSMS(redirect.URL, "provider-token")
	if err != nil {
		t.Fatal(err)
	}
	err = sender.SendCode(context.Background(), auth.SMSCode{})
	if err == nil || strings.Contains(err.Error(), "private provider body") {
		t.Fatalf("redirect error = %v", err)
	}
}
