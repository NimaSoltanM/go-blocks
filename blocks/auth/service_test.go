package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type typedNilDependencyError struct{ detail string }
type panickingDependencyError struct{}

func (e *typedNilDependencyError) Error() string { return e.detail }
func (*panickingDependencyError) Error() string  { panic("dependency Error panic") }

func TestRequestCodeAdmitsBeforeSMSAndDoesNotQueryUsers(t *testing.T) {
	fakes := newFakes()
	var logs bytes.Buffer
	b := newBlock(validTestConfig(), fakes.otp, fakes.users, fakes.sms,
		slog.New(slog.NewJSONHandler(&logs, nil)), newMemoryStorage())
	b.code = func() (string, error) { return "012345", nil }
	b.now = func() time.Time { return time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC) }

	result, err := b.requestCode(context.Background(), "request-1", "+989121234567", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if result.expiresIn != 2*time.Minute || result.resendAfter != time.Minute {
		t.Fatalf("unexpected policy result: %+v", result)
	}
	if fakes.otp.admitCalls != 1 || fakes.sms.calls != 1 || fakes.users.calls != 0 {
		t.Fatalf("calls: admit=%d sms=%d users=%d", fakes.otp.admitCalls, fakes.sms.calls, fakes.users.calls)
	}
	if fakes.sms.code.Phone != "+989121234567" || fakes.sms.code.Code != "012345" ||
		!fakes.sms.code.ExpiresAt.Equal(b.now().Add(2*time.Minute)) || fakes.sms.code.IdempotencyKey == "" {
		t.Fatalf("SMS payload: %+v", fakes.sms.code)
	}
	if fakes.otp.phoneTag == "+989121234567" || fakes.otp.clientTag == "192.0.2.1" || fakes.otp.verifier == "012345" {
		t.Fatal("raw secret material was sent to Redis keys/state")
	}
	logText := logs.String()
	for _, secret := range []string{"+989121234567", "012345", "192.0.2.1"} {
		if strings.Contains(logText, secret) {
			t.Fatalf("log leaked %q: %s", secret, logText)
		}
	}
}

func TestRequestCodeSMSExpiryNeverOutlivesAdmittedChallenge(t *testing.T) {
	fakes := newFakes()
	startedAt := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	now := startedAt
	fakes.otp.admitHook = func() { now = now.Add(15 * time.Second) }
	b := newBlock(validTestConfig(), fakes.otp, fakes.users, fakes.sms,
		testLogger(), newMemoryStorage())
	b.code = func() (string, error) { return "012345", nil }
	b.now = func() time.Time { return now }

	result, err := b.requestCode(context.Background(), "request-1", "+989121234567", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	want := startedAt.Add(b.cfg.OTP.Lifetime)
	if !fakes.sms.code.ExpiresAt.Equal(want) {
		t.Fatalf("SMS expiry = %v, want conservative challenge expiry %v", fakes.sms.code.ExpiresAt, want)
	}
	if result.expiresIn != b.cfg.OTP.Lifetime-15*time.Second || result.resendAfter != b.cfg.OTP.ResendDelay-15*time.Second {
		t.Fatalf("remaining policy durations = %+v", result)
	}
}

func TestRequestCodeDoesNotReportSuccessAfterChallengeLifetime(t *testing.T) {
	fakes := newFakes()
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	fakes.otp.admitHook = func() { now = now.Add(3 * time.Minute) }
	b := newBlock(validTestConfig(), fakes.otp, fakes.users, fakes.sms,
		testLogger(), newMemoryStorage())
	b.code = func() (string, error) { return "012345", nil }
	b.now = func() time.Time { return now }

	_, err := b.requestCode(context.Background(), "request-1", "+989121234567", "192.0.2.1")
	var unavailable *smsUnavailableError
	if !errors.As(err, &unavailable) || fakes.otp.admitCalls != 1 || fakes.sms.calls != 1 {
		t.Fatalf("expired delivery result=%v admit=%d SMS=%d", err, fakes.otp.admitCalls, fakes.sms.calls)
	}
}

func TestRequestCodeStopsAtAdmissionAndRetainsAdmittedStateOnSMSFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		admitErr  error
		smsErr    error
		wantSMS   int
		wantRate  bool
		wantSMSUn bool
	}{
		{"rate limited", &rateLimitError{retryAfter: time.Second}, nil, 0, true, false},
		{"Redis failed", errTestDependency, nil, 0, false, false},
		{"SMS failed after admission", nil, errors.New("raw provider response SECRET"), 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakes := newFakes()
			fakes.otp.admitErr, fakes.sms.err = tc.admitErr, tc.smsErr
			var logs bytes.Buffer
			b := newBlock(validTestConfig(), fakes.otp, fakes.users, fakes.sms,
				slog.New(slog.NewJSONHandler(&logs, nil)), newMemoryStorage())
			b.code = func() (string, error) { return "123456", nil }
			_, err := b.requestCode(context.Background(), "r", "+989121234567", "client")
			if err == nil || fakes.sms.calls != tc.wantSMS || fakes.otp.admitCalls != 1 {
				t.Fatalf("err=%v calls admit=%d SMS=%d", err, fakes.otp.admitCalls, fakes.sms.calls)
			}
			var rate *rateLimitError
			var sms *smsUnavailableError
			if errors.As(err, &rate) != tc.wantRate || errors.As(err, &sms) != tc.wantSMSUn {
				t.Fatalf("unexpected mapped error %T: %v", err, err)
			}
			if strings.Contains(logs.String(), "SECRET") {
				t.Fatalf("provider response leaked to logs: %s", logs.String())
			}
		})
	}
}

func TestRequestCodeContainsSMSProviderPanicWithoutLeakingIt(t *testing.T) {
	fakes := newFakes()
	fakes.sms.panicValue = "raw provider response SECRET"
	var logs bytes.Buffer
	b := newBlock(validTestConfig(), fakes.otp, fakes.users, fakes.sms,
		slog.New(slog.NewJSONHandler(&logs, nil)), newMemoryStorage())
	b.code = func() (string, error) { return "123456", nil }

	_, err := b.requestCode(context.Background(), "r", "+989121234567", "client")
	var unavailable *smsUnavailableError
	if !errors.As(err, &unavailable) || fakes.otp.admitCalls != 1 || fakes.sms.calls != 1 {
		t.Fatalf("panic result=%v admit=%d SMS=%d", err, fakes.otp.admitCalls, fakes.sms.calls)
	}
	if strings.Contains(logs.String(), "SECRET") {
		t.Fatalf("provider panic leaked to logs: %s", logs.String())
	}
}

func TestRequestCodeDoesNotAdmitWhenMessageIdentityGenerationFails(t *testing.T) {
	fakes := newFakes()
	b := newBlock(validTestConfig(), fakes.otp, fakes.users, fakes.sms, testLogger(), newMemoryStorage())
	b.code = func() (string, error) { return "123456", nil }
	b.idempotencyKey = func() (string, error) { return "", errTestDependency }
	if _, err := b.requestCode(context.Background(), "r", "+989121234567", "client"); !errors.Is(err, errTestDependency) {
		t.Fatalf("idempotency generation error = %v", err)
	}
	if fakes.otp.admitCalls != 0 || fakes.sms.calls != 0 {
		t.Fatal("challenge admitted without an SMS idempotency identity")
	}
}

func TestVerifyConsumesChallengeBeforeUserUpsert(t *testing.T) {
	fakes := newFakes()
	b := newBlock(validTestConfig(), fakes.otp, fakes.users, fakes.sms, testLogger(), newMemoryStorage())
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.FixedZone("IRST", 3*60*60+30*60))
	b.now = func() time.Time { return now }
	user, err := b.verifyCode(context.Background(), "r", "+989121234567", "123456", "client")
	if err != nil || user != fakes.users.user {
		t.Fatalf("verify result = %+v, %v", user, err)
	}
	if fakes.otp.verifyCalls != 1 || fakes.users.calls != 1 || fakes.users.phone != "+989121234567" {
		t.Fatalf("calls/state: otp=%d users=%d phone=%q", fakes.otp.verifyCalls, fakes.users.calls, fakes.users.phone)
	}
	if fakes.users.verified.Location() != time.UTC || !fakes.users.verified.Equal(now) {
		t.Fatalf("verified time not UTC: %v", fakes.users.verified)
	}

	fakes.otp.verifyErr = ErrInvalidCode
	if _, err := b.verifyCode(context.Background(), "r", "+989121234567", "000000", "client"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("invalid verification error = %v", err)
	}
	if fakes.users.calls != 1 {
		t.Fatal("user store called after invalid verifier")
	}
}

func TestTypedNilDependencyErrorCannotPanicErrorMapping(t *testing.T) {
	fakes := newFakes()
	var typedNil *typedNilDependencyError
	fakes.otp.admitErr = typedNil
	b := newBlock(validTestConfig(), fakes.otp, fakes.users, fakes.sms, testLogger(), newMemoryStorage())
	b.code = func() (string, error) { return "123456", nil }
	_, err := b.requestCode(context.Background(), "r", "+989121234567", "client")
	if !errors.Is(err, errTypedNilDependency) {
		t.Fatalf("typed nil error = %v", err)
	}
	mapped := mapServiceError(context.Background(), typedNil)
	var public *publicError
	if !errors.As(mapped, &public) || public.status != 503 || public.code != "service_unavailable" {
		t.Fatalf("mapped typed nil = %#v", mapped)
	}
}

func TestPanickingDependencyErrorCannotPanicLoggingOrPublicMapping(t *testing.T) {
	fakes := newFakes()
	fakes.otp.admitErr = &panickingDependencyError{}
	var logs bytes.Buffer
	b := newBlock(validTestConfig(), fakes.otp, fakes.users, fakes.sms,
		slog.New(slog.NewJSONHandler(&logs, nil)), newMemoryStorage())
	b.code = func() (string, error) { return "123456", nil }
	_, err := b.requestCode(context.Background(), "r", "+989121234567", "client")
	if err == nil || !strings.Contains(logs.String(), "dependency error string panicked") {
		t.Fatalf("error=%v logs=%s", safeErrorText(err), logs.String())
	}
	mapped := mapServiceError(context.Background(), err)
	if text := mapped.Error(); text != "dependency error string panicked" {
		t.Fatalf("safe public error text = %q", text)
	}
}

func TestServiceErrorMappingPreservesParentRequestDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := mapServiceError(ctx, errTestDependency); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mapped deadline = %v", err)
	}
	if err := authServiceErrorForContext(ctx, errTestDependency); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("session deadline = %v", err)
	}
	childTimeout := mapServiceError(context.Background(), context.DeadlineExceeded)
	var public *publicError
	if !errors.As(childTimeout, &public) || public.status != 503 {
		t.Fatalf("child dependency timeout = %#v", childTimeout)
	}
}
