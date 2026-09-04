package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type requestCodeResult struct {
	expiresIn   time.Duration
	resendAfter time.Duration
}

func (b *Block) requestCode(ctx context.Context, requestID, phone, client string) (requestCodeResult, error) {
	requestedAt := b.now()
	code, err := b.code()
	err = safeDependencyError(err)
	if err != nil {
		b.logFailure(ctx, "otp_request", requestID, phone, err)
		return requestCodeResult{}, err
	}
	idempotencyKey, err := b.idempotencyKey()
	err = safeDependencyError(err)
	if err != nil {
		b.logFailure(ctx, "otp_request", requestID, phone, err)
		return requestCodeResult{}, err
	}
	phoneTag := valueTag(b.cfg.Pepper, "phone", phone)
	clientTag := valueTag(b.cfg.Pepper, "client", client)
	if err := safeDependencyError(b.otp.Admit(ctx, phoneTag, clientTag, codeVerifier(b.cfg.Pepper, phone, code))); err != nil {
		if !isRateLimited(err) {
			b.logFailure(ctx, "otp_request", requestID, phone, err)
		}
		return requestCodeResult{}, err
	}

	smsCtx, cancel := context.WithTimeout(ctx, b.cfg.Timeouts.SMS)
	defer cancel()
	err = sendSMSCode(b.sms, smsCtx, SMSCode{
		Phone: phone, Code: code, ExpiresAt: requestedAt.UTC().Add(b.cfg.OTP.Lifetime),
		IdempotencyKey: idempotencyKey,
	})
	if err == nil {
		err = smsCtx.Err()
	}
	err = safeDependencyError(err)
	if err != nil {
		err = &smsUnavailableError{cause: err}
		b.logFailure(ctx, "otp_request", requestID, phone, err)
		return requestCodeResult{}, err
	}
	elapsed := b.now().Sub(requestedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	expiresIn := max(b.cfg.OTP.Lifetime-elapsed, 0)
	if expiresIn == 0 {
		err = &smsUnavailableError{cause: errors.New("SMS provider accepted an expired verification code")}
		b.logFailure(ctx, "otp_request", requestID, phone, err)
		return requestCodeResult{}, err
	}
	b.logger.InfoContext(ctx, "auth_otp_sent", "request_id", requestID, "phone_tag", phoneTag)
	return requestCodeResult{
		expiresIn: expiresIn, resendAfter: max(b.cfg.OTP.ResendDelay-elapsed, 0),
	}, nil
}

func sendSMSCode(sender SMSSender, ctx context.Context, code SMSCode) (err error) {
	defer func() {
		if recover() != nil {
			err = errSMSProviderPanicked
		}
	}()
	return sender.SendCode(ctx, code)
}

func (b *Block) verifyCode(ctx context.Context, requestID, phone, code, client string) (User, error) {
	phoneTag := valueTag(b.cfg.Pepper, "phone", phone)
	clientTag := valueTag(b.cfg.Pepper, "client", client)
	if err := safeDependencyError(b.otp.Verify(ctx, phoneTag, clientTag, codeVerifier(b.cfg.Pepper, phone, code))); err != nil {
		if safeErrorIs(err, ErrInvalidCode) {
			b.logger.InfoContext(ctx, "auth_otp_rejected", "request_id", requestID, "phone_tag", phoneTag)
		} else if !isRateLimited(err) {
			b.logFailure(ctx, "otp_verify", requestID, phone, err)
		}
		return User{}, err
	}
	user, err := b.users.UpsertVerified(ctx, phone, b.now().UTC())
	err = safeDependencyError(err)
	if err != nil {
		b.logFailure(ctx, "otp_verify", requestID, phone, err)
		return User{}, err
	}
	b.logger.InfoContext(ctx, "auth_login_succeeded", "request_id", requestID, "phone_tag", phoneTag, "user_id", user.ID.String())
	return user, nil
}

func (b *Block) logFailure(ctx context.Context, operation, requestID, phone string, err error) {
	b.logger.ErrorContext(ctx, "auth_dependency_failed",
		slog.String("operation", operation), slog.String("request_id", requestID),
		slog.String("phone_tag", valueTag(b.cfg.Pepper, "phone", phone)), slog.String("error", safeErrorText(err)))
}

func isRateLimited(err error) bool {
	if isNilInterface(err) {
		return false
	}
	var target *rateLimitError
	return safeErrorAs(err, &target)
}
