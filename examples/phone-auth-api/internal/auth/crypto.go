package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
)

func generateCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("generate OTP: %w", err)
	}
	return formatCode(n.Int64()), nil
}

func formatCode(value int64) string { return fmt.Sprintf("%06d", value) }

func generateIdempotencyKey() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate SMS idempotency key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func keyedDigest(pepper []byte, purpose, value string) []byte {
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte("go-blocks-auth-v1\x00"))
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{'\x00'})
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func valueTag(pepper []byte, purpose, value string) string {
	digest := keyedDigest(pepper, purpose, value)
	return base64.RawURLEncoding.EncodeToString(digest[:16])
}

func codeVerifier(pepper []byte, phone, code string) string {
	digest := keyedDigest(pepper, "otp", phone+"\x00"+code)
	return base64.RawURLEncoding.EncodeToString(digest)
}
