package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/csrf"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/google/uuid"
)

const (
	sessionSchemaKey   = "auth_schema"
	sessionUserIDKey   = "auth_user_id"
	sessionPhoneKey    = "auth_phone"
	sessionVerifiedKey = "auth_verified_at"
	sessionSchemaV1    = "1"
)

type userContextKey struct{}

type requestCodeBody struct {
	Phone string `json:"phone"`
}

type verifyCodeBody struct {
	Phone string `json:"phone"`
	Code  string `json:"code"`
}

func UserFromContext(ctx any) (User, bool) {
	return fiber.ValueFromContext[User](ctx, userContextKey{})
}

func (b *Block) CSRFToken(c fiber.Ctx) error {
	noStore(c)
	if b == nil || b.cfg.Transport != TransportCookie {
		return ErrCookieTransportRequired
	}
	token := csrf.TokenFromContext(c)
	if token == "" {
		return authServiceError(errors.New("CSRF middleware is not installed"))
	}
	return c.JSON(fiber.Map{"csrf_token": token})
}

func (b *Block) RequestCode(c fiber.Ctx) error {
	noStore(c)
	if err := b.requireCookieSecurity(c); err != nil {
		return err
	}
	var body requestCodeBody
	if err := decodeStrictJSON(c, &body, "phone"); err != nil {
		return invalidRequest(err)
	}
	phone, err := b.normalizePhone(body.Phone)
	if err != nil {
		return httpError(400, "invalid_phone", "Enter a valid Iranian mobile number", err)
	}
	client, err := clientKey(c, b.cfg.ClientKey)
	if err != nil {
		return authServiceErrorForContext(c.Context(), err)
	}
	result, err := b.requestCode(c.Context(), requestid.FromContext(c), phone, client)
	if err != nil {
		return mapServiceError(c.Context(), err)
	}
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"status": "code_sent", "expires_in_seconds": durationSeconds(result.expiresIn),
		"resend_after_seconds": durationSeconds(result.resendAfter),
	})
}

func (b *Block) VerifyCode(c fiber.Ctx) error {
	noStore(c)
	if err := b.requireCookieSecurity(c); err != nil {
		return err
	}
	var body verifyCodeBody
	if err := decodeStrictJSON(c, &body, "phone", "code"); err != nil {
		return invalidRequest(err)
	}
	phone, err := b.normalizePhone(body.Phone)
	if err != nil {
		return httpError(400, "invalid_phone", "Enter a valid Iranian mobile number", err)
	}
	code, err := normalizeCode(body.Code)
	if err != nil {
		return httpError(400, "invalid_code_format", "Enter the six-digit verification code", err)
	}
	client, err := clientKey(c, b.cfg.ClientKey)
	if err != nil {
		return authServiceErrorForContext(c.Context(), err)
	}
	user, err := b.verifyCode(c.Context(), requestid.FromContext(c), phone, code, client)
	if err != nil {
		return mapServiceError(c.Context(), err)
	}

	if b.cfg.Transport == TransportCookie {
		m := session.FromContext(c)
		if m == nil {
			return authServiceError(errors.New("session middleware is not installed"))
		}
		ctx, cancel := redisContext(c.Context(), b.cfg.Timeouts.Redis)
		defer cancel()
		if err := m.RegenerateWithContext(ctx); err != nil {
			return authServiceErrorForContext(c.Context(), fmt.Errorf("rotate auth session: %w", err))
		}
		setMiddlewareIdentity(m, user, b.now().UTC())
		return c.JSON(fiber.Map{"user": user})
	}

	sess, err := b.sessions.Get(c)
	if err != nil {
		return authServiceErrorForContext(c.Context(), fmt.Errorf("create bearer session: %w", err))
	}
	defer sess.Release()
	ctx, cancel := redisContext(c.Context(), b.cfg.Timeouts.Redis)
	if err := sess.RegenerateWithContext(ctx); err != nil {
		cancel()
		return authServiceErrorForContext(c.Context(), fmt.Errorf("rotate bearer session: %w", err))
	}
	cancel()
	setSessionIdentity(sess, user, b.now().UTC())
	ctx, cancel = redisContext(c.Context(), b.cfg.Timeouts.Redis)
	defer cancel()
	if err := sess.SaveWithContext(ctx); err != nil {
		return authServiceErrorForContext(c.Context(), fmt.Errorf("save bearer session: %w", err))
	}
	return c.JSON(fiber.Map{"user": user, "session_token": sess.ID()})
}

func (b *Block) RequireUser(c fiber.Ctx) error {
	noStore(c)
	var user User
	var ok bool
	if b.cfg.Transport == TransportCookie {
		m := session.FromContext(c)
		if m == nil {
			return authServiceError(errors.New("session middleware is not installed"))
		}
		user, ok = identityFromGetter(m.Get)
	} else {
		token, err := b.bearerToken.Extract(c)
		if err != nil || !validSessionToken(token) {
			return authenticationRequired()
		}
		ctx, cancel := redisContext(c.Context(), b.cfg.Timeouts.Redis)
		sess, err := b.sessions.GetByID(ctx, token)
		cancel()
		if safeErrorIs(err, session.ErrSessionIDNotFoundInStore) {
			return authenticationRequired()
		}
		if err != nil {
			return authServiceErrorForContext(c.Context(), fmt.Errorf("load bearer session: %w", err))
		}
		defer sess.Release()
		user, ok = identityFromGetter(sess.Get)
		if ok {
			ctx, cancel = redisContext(c.Context(), b.cfg.Timeouts.Redis)
			defer cancel()
			if err := sess.SaveWithContext(ctx); err != nil {
				return authServiceErrorForContext(c.Context(), fmt.Errorf("refresh bearer session: %w", err))
			}
		}
	}
	if !ok {
		return authenticationRequired()
	}
	fiber.StoreInContext(c, userContextKey{}, user)
	return c.Next()
}

func (b *Block) Me(c fiber.Ctx) error {
	noStore(c)
	user, ok := UserFromContext(c)
	if !ok {
		return authenticationRequired()
	}
	return c.JSON(fiber.Map{"user": user})
}

func (b *Block) Logout(c fiber.Ctx) error {
	noStore(c)
	if err := b.requireCookieSecurity(c); err != nil {
		return err
	}
	if b.cfg.Transport == TransportCookie {
		m := session.FromContext(c)
		if m == nil {
			return authServiceError(errors.New("session middleware is not installed"))
		}
		ctx, cancel := redisContext(c.Context(), b.cfg.Timeouts.Redis)
		defer cancel()
		if err := m.DestroyWithContext(ctx); err != nil {
			return authServiceErrorForContext(c.Context(), fmt.Errorf("destroy auth session: %w", err))
		}
	} else {
		token, err := b.bearerToken.Extract(c)
		if err == nil && validSessionToken(token) {
			ctx, cancel := redisContext(c.Context(), b.cfg.Timeouts.Redis)
			defer cancel()
			if err := b.sessions.Delete(ctx, token); err != nil {
				return authServiceErrorForContext(c.Context(), fmt.Errorf("delete bearer session: %w", err))
			}
		}
	}
	b.logger.InfoContext(c.Context(), "auth_logout", "request_id", requestid.FromContext(c))
	return c.SendStatus(fiber.StatusNoContent)
}

func setMiddlewareIdentity(m *session.Middleware, user User, verifiedAt time.Time) {
	m.Set(sessionSchemaKey, sessionSchemaV1)
	m.Set(sessionUserIDKey, user.ID.String())
	m.Set(sessionPhoneKey, user.Phone)
	m.Set(sessionVerifiedKey, verifiedAt.Format(time.RFC3339Nano))
}

func setSessionIdentity(sess *session.Session, user User, verifiedAt time.Time) {
	sess.Set(sessionSchemaKey, sessionSchemaV1)
	sess.Set(sessionUserIDKey, user.ID.String())
	sess.Set(sessionPhoneKey, user.Phone)
	sess.Set(sessionVerifiedKey, verifiedAt.Format(time.RFC3339Nano))
}

func identityFromGetter(get func(any) any) (User, bool) {
	schema, schemaOK := get(sessionSchemaKey).(string)
	idValue, idOK := get(sessionUserIDKey).(string)
	phone, phoneOK := get(sessionPhoneKey).(string)
	verified, verifiedOK := get(sessionVerifiedKey).(string)
	id, idErr := uuid.Parse(idValue)
	_, timeErr := time.Parse(time.RFC3339Nano, verified)
	if !schemaOK || schema != sessionSchemaV1 || !idOK || idErr != nil || id == uuid.Nil ||
		!phoneOK || !validCanonicalPhone(phone) || !verifiedOK || timeErr != nil {
		return User{}, false
	}
	return User{ID: id, Phone: strings.Clone(phone)}, true
}

func validCanonicalPhone(phone string) bool {
	normalized, err := (IranPhoneNormalizer{}).Normalize(phone)
	return err == nil && normalized == phone
}

func (b *Block) normalizePhone(input string) (string, error) {
	phone, err := b.cfg.PhoneNormalizer.Normalize(input)
	if err != nil || !validCanonicalPhone(phone) {
		return "", ErrInvalidPhone
	}
	return strings.Clone(phone), nil
}

func (b *Block) requireCookieSecurity(c fiber.Ctx) error {
	if b.cfg.Transport != TransportCookie {
		return nil
	}
	if session.FromContext(c) == nil || csrf.TokenFromContext(c) == "" {
		return authServiceError(errors.New("cookie session and CSRF middleware are not installed"))
	}
	return nil
}

func validSessionToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32
}

func decodeStrictJSON(c fiber.Ctx, destination any, allowedFields ...string) error {
	if len(c.Request().Header.PeekAll(fiber.HeaderContentType)) != 1 {
		return errors.New("exactly one Content-Type header is required")
	}
	contentType := c.Get(fiber.HeaderContentType)
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != fiber.MIMEApplicationJSON {
		return errors.New("Content-Type must be application/json")
	}
	body := c.Body()
	if len(body) == 0 {
		return errors.New("request body is empty")
	}
	if !utf8.Valid(body) {
		return errors.New("request body must be valid UTF-8 JSON")
	}
	allowed := make(map[string]struct{}, len(allowedFields))
	for _, field := range allowedFields {
		allowed[field] = struct{}{}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("request body must be a JSON object")
	}
	seen := make(map[string]struct{}, len(allowedFields))
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return errors.New("malformed JSON object")
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("malformed JSON object key")
		}
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown field %q", key)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = struct{}{}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return errors.New("malformed JSON field value")
		}
		value := bytes.TrimSpace(raw)
		if len(value) == 0 || value[0] != '"' {
			return fmt.Errorf("field %q must be a string", key)
		}
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') {
		return errors.New("malformed JSON object")
	}
	if len(seen) != len(allowed) {
		return errors.New("request body is missing a required field")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request body contains trailing data")
	}
	dec = json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(destination); err != nil {
		return errors.New("request fields have invalid types")
	}
	return nil
}

func noStore(c fiber.Ctx) {
	c.Set(fiber.HeaderCacheControl, "no-store")
}

func durationSeconds(value time.Duration) int64 {
	seconds := value / time.Second
	if value%time.Second != 0 {
		seconds++
	}
	return int64(seconds)
}

func invalidRequest(cause error) error {
	return httpError(400, "invalid_request", "The request body is invalid", cause)
}

func authenticationRequired() error {
	return httpError(401, "authentication_required", "Authentication is required", nil)
}

func authServiceError(cause error) error {
	return httpError(503, "service_unavailable", "Authentication service is unavailable", cause)
}

func authServiceErrorForContext(ctx context.Context, cause error) error {
	if ctx != nil && safeErrorIs(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return authServiceError(cause)
}

func mapServiceError(ctx context.Context, err error) error {
	if ctx != nil && safeErrorIs(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	err = safeDependencyError(err)
	var rate *rateLimitError
	var sms *smsUnavailableError
	switch {
	case safeErrorAs(err, &rate):
		return rateHTTPError(rate.retryAfter, err)
	case safeErrorIs(err, ErrInvalidCode):
		return httpError(401, "invalid_code", "The verification code is invalid or expired", err)
	case safeErrorAs(err, &sms):
		return httpError(503, "sms_unavailable", "SMS delivery is temporarily unavailable", err)
	default:
		return authServiceError(err)
	}
}
