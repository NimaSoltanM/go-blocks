package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"example.local/basic-api/internal/server"
)

func TestApplicationRoutes(t *testing.T) {
	app, err := newApp(server.DefaultConfig(), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		status int
		key    string
		value  string
	}{
		{"/livez", 200, "status", "OK"},
		{"/readyz", 200, "status", "OK"},
		{"/api/hello", 200, "message", "Hello from Go Blocks"},
		{"/missing", 404, "", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := app.Test(httptest.NewRequest("GET", tc.path, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.status || resp.Header.Get("X-Request-ID") == "" {
				t.Fatalf("status=%d headers=%v", resp.StatusCode, resp.Header)
			}
			if tc.key != "" && body[tc.key] != tc.value {
				t.Fatalf("unexpected body: %v", body)
			}
			if tc.status == 404 {
				detail, ok := body["error"].(map[string]any)
				if !ok || detail["code"] != "not_found" || detail["request_id"] != resp.Header.Get("X-Request-ID") {
					t.Fatalf("unexpected error: %v", body)
				}
			}
		})
	}
}
