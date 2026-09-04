package auth

import (
	"regexp"
	"strings"
	"testing"
)

func TestGenerateCodeAlwaysSixDigits(t *testing.T) {
	t.Parallel()
	pattern := regexp.MustCompile(`^[0-9]{6}$`)
	for range 1000 {
		code, err := generateCode()
		if err != nil {
			t.Fatal(err)
		}
		if !pattern.MatchString(code) {
			t.Fatalf("invalid generated code %q", code)
		}
	}
}

func TestFormatCodePreservesLeadingZeroes(t *testing.T) {
	t.Parallel()
	for value, want := range map[int64]string{0: "000000", 1: "000001", 42: "000042", 999999: "999999"} {
		if got := formatCode(value); got != want {
			t.Errorf("formatCode(%d) = %q, want %q", value, got, want)
		}
	}
}

func TestGenerateIdempotencyKeysAreOpaqueAndUnique(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		value, err := generateIdempotencyKey()
		if err != nil {
			t.Fatal(err)
		}
		if len(value) != 22 {
			t.Fatalf("idempotency key length = %d", len(value))
		}
		if _, exists := seen[value]; exists {
			t.Fatalf("duplicate idempotency key %q", value)
		}
		seen[value] = struct{}{}
	}
}

func TestKeyedValuesAreStableSeparatedAndOpaque(t *testing.T) {
	t.Parallel()
	pepper := []byte(strings.Repeat("x", 32))
	a := valueTag(pepper, "phone", "+989121234567")
	if a != valueTag(pepper, "phone", "+989121234567") {
		t.Fatal("tag is not deterministic")
	}
	if a == valueTag(pepper, "client", "+989121234567") {
		t.Fatal("domain separation failed")
	}
	if strings.Contains(a, "9121234567") || len(a) != 22 {
		t.Fatalf("tag is not a fixed opaque value: %q", a)
	}
	if got := codeVerifier(pepper, "+989121234567", "123456"); got == "123456" || len(got) != 43 {
		t.Fatalf("unexpected verifier %q", got)
	}
	if codeVerifier(pepper, "+989121234567", "123456") == codeVerifier(pepper, "+989121234567", "123457") ||
		codeVerifier(pepper, "+989121234567", "123456") == codeVerifier(pepper, "+989351234567", "123456") {
		t.Fatal("OTP verifier did not bind both phone and code")
	}
}
