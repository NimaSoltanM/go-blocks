package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRedisSessionStorageKeyScopeAndUnsupportedReset(t *testing.T) {
	storage := &redisSessionStorage{prefix: "gb:auth:v1:session:", timeout: time.Second}
	if got := storage.key("abc"); got != "gb:auth:v1:session:abc" {
		t.Fatalf("scoped key = %q", got)
	}
	if got := storage.key(""); got != "" {
		t.Fatalf("empty key changed to %q", got)
	}
	if err := storage.Reset(); !errors.Is(err, ErrStorageResetUnsupported) {
		t.Fatalf("Reset error = %v", err)
	}
	if err := storage.ResetWithContext(context.Background()); !errors.Is(err, ErrStorageResetUnsupported) {
		t.Fatalf("ResetWithContext error = %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("borrowed client close = %v", err)
	}
}

func TestScriptPairRejectsUnexpectedRedisResults(t *testing.T) {
	for _, value := range []any{nil, "bad", []any{int64(1)}, []any{"1", int64(0)}, []any{int64(1), "0"}} {
		if _, _, err := scriptPair(value); err == nil {
			t.Fatalf("scriptPair(%#v) accepted", value)
		}
	}
	first, second, err := scriptPair([]any{int64(1), int64(250)})
	if err != nil || first != 1 || second != 250 {
		t.Fatalf("valid pair = %d, %d, %v", first, second, err)
	}
}

func TestRetryDurationIsPositiveAndCannotOverflow(t *testing.T) {
	if got, err := retryDuration(0); err != nil || got != time.Millisecond {
		t.Fatalf("zero-millisecond boundary = %s, %v", got, err)
	}
	const maxDurationMilliseconds = (1<<63 - 1) / int64(time.Millisecond)
	if got, err := retryDuration(maxDurationMilliseconds); err != nil || got <= 0 {
		t.Fatalf("largest retry duration = %s, %v", got, err)
	}
	for _, invalid := range []int64{-1, maxDurationMilliseconds + 1} {
		if _, err := retryDuration(invalid); err == nil {
			t.Fatalf("retryDuration(%d) accepted", invalid)
		}
	}
}
