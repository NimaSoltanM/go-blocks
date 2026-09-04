package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestIranPhoneNormalizerAcceptedForms(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"09121234567",
		"9121234567",
		"+989121234567",
		"0098 912 123 4567",
		"۰۹۱۲ ۱۲۳ ۴۵۶۷",
		"٠٩١٢-١٢٣-٤٥٦٧",
		"(+98) 912‑123‑4567",
	} {
		t.Run(input, func(t *testing.T) {
			got, err := (IranPhoneNormalizer{}).Normalize(input)
			if err != nil || got != "+989121234567" {
				t.Fatalf("Normalize(%q) = %q, %v", input, got, err)
			}
		})
	}
}

func TestIranPhoneNormalizerRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"", "0912123456", "091212345678", "+989121234567x", "+0989121234567",
		"+989121234567+", "02112345678", "09871234567", "0912/123/4567",
		string(make([]byte, 257)),
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := (IranPhoneNormalizer{}).Normalize(input); !errors.Is(err, ErrInvalidPhone) {
				t.Fatalf("Normalize(%q) error = %v", input, err)
			}
		})
	}
}

func TestIranPhoneNormalizerAcceptsEveryConfiguredPrefixFamily(t *testing.T) {
	t.Parallel()
	for _, prefix := range iranMobilePrefixes {
		national := prefix + strings.Repeat("0", 10-len(prefix))
		want := "+98" + national
		if got, err := (IranPhoneNormalizer{}).Normalize("0" + national); err != nil || got != want {
			t.Errorf("prefix %s normalized to %q, %v", prefix, got, err)
		}
	}
}

func TestNormalizeCode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input string
		want  string
		ok    bool
	}{
		{"012345", "012345", true},
		{"۰۱۲۳۴۵", "012345", true},
		{"٠١٢٣٤٥", "012345", true},
		{"12345", "", false},
		{"1234567", "", false},
		{"12 3456", "", false},
		{"１２３４５６", "", false},
	} {
		got, err := normalizeCode(tc.input)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("normalizeCode(%q) = %q, %v", tc.input, got, err)
		}
	}
}
