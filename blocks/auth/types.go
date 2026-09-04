// Package auth provides copyable Iranian phone-number authentication for Fiber.
package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID    uuid.UUID `json:"id"`
	Phone string    `json:"phone"`
}

type SMSCode struct {
	Phone          string
	Code           string
	ExpiresAt      time.Time
	IdempotencyKey string
}

type SMSSender interface {
	SendCode(context.Context, SMSCode) error
}

type PhoneNormalizer interface {
	Normalize(string) (string, error)
}

var (
	ErrInvalidPhone            = errors.New("invalid Iranian mobile number")
	ErrInvalidCode             = errors.New("invalid verification code")
	ErrStorageResetUnsupported = errors.New("auth session storage reset is not supported")
	ErrCookieTransportRequired = errors.New("cookie transport is required for this middleware")
	errTypedNilDependency      = errors.New("authentication dependency returned a typed nil error")
	errSMSProviderPanicked     = errors.New("SMS provider panicked")
)

func safeDependencyError(err error) error {
	if err != nil && isNilInterface(err) {
		return errTypedNilDependency
	}
	return err
}

func safeErrorAs(err error, target any) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return errors.As(err, target)
}

func safeErrorIs(err, target error) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return errors.Is(err, target)
}

func safeErrorText(err error) (message string) {
	if isNilInterface(err) {
		return "typed nil dependency error"
	}
	defer func() {
		if recover() != nil {
			message = "dependency error string panicked"
		}
	}()
	return err.Error()
}

type rateLimitError struct {
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string { return "authentication rate limit exceeded" }

type smsUnavailableError struct {
	cause error
}

func (e *smsUnavailableError) Error() string { return "SMS provider did not accept the code" }
func (e *smsUnavailableError) Unwrap() error { return e.cause }

type publicError struct {
	status     int
	code       string
	message    string
	retryAfter time.Duration
	cause      error
}

func (e *publicError) Error() string {
	if e.cause != nil {
		return safeErrorText(e.cause)
	}
	return e.code
}

func (e *publicError) Unwrap() error             { return e.cause }
func (e *publicError) HTTPStatus() int           { return e.status }
func (e *publicError) PublicCode() string        { return e.code }
func (e *publicError) PublicMessage() string     { return e.message }
func (e *publicError) RetryAfter() time.Duration { return e.retryAfter }

func httpError(status int, code, message string, cause error) error {
	return &publicError{status: status, code: code, message: message, cause: cause}
}

func rateHTTPError(retryAfter time.Duration, cause error) error {
	return &publicError{
		status: 429, code: "rate_limited", message: "Too many attempts; try again later",
		retryAfter: retryAfter, cause: cause,
	}
}
