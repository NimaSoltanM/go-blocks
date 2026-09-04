package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type otpRepository interface {
	Admit(context.Context, string, string, string) error
	Verify(context.Context, string, string, string) error
}

type redisOTPStore struct {
	client       *redis.Client
	prefix       string
	timeout      time.Duration
	otp          OTPConfig
	limits       LimitConfig
	admitScript  *redis.Script
	verifyScript *redis.Script
}

const admitOTPScript = `
local retry = 0

local function remaining(key, fallback)
  local ttl = redis.call('PTTL', key)
  if ttl < 0 then
    redis.call('PEXPIRE', key, fallback)
    return fallback
  end
  return ttl
end

-- Validate every counter before creating the cooldown or challenge. Redis Lua
-- scripts do not roll back earlier writes when a later command errors, so an
-- INCR parsing failure must be impossible after admission starts mutating keys.
for i = 2, 5 do
  local raw = redis.call('GET', KEYS[i])
  if raw then
    local current = tonumber(raw)
    if not current or current < 0 or current > 2147483647 or raw ~= tostring(current) then
      return redis.error_reply('auth send counter is corrupt')
    end
  end
end

if redis.call('EXISTS', KEYS[1]) == 1 then
  local cooldown_ttl = remaining(KEYS[1], ARGV[1])
  if cooldown_ttl > retry then retry = cooldown_ttl end
end

for i = 2, 5 do
  local current = tonumber(redis.call('GET', KEYS[i]) or '0')
  local limit = tonumber(ARGV[(i - 2) * 2 + 2])
  local window = tonumber(ARGV[(i - 2) * 2 + 3])
  if current >= limit then
    local ttl = remaining(KEYS[i], window)
    if ttl > retry then retry = ttl end
  end
end

if retry > 0 then return {0, retry} end

redis.call('SET', KEYS[1], '1', 'PX', ARGV[1])
for i = 2, 5 do
  local window = tonumber(ARGV[(i - 2) * 2 + 3])
  local value = redis.call('INCR', KEYS[i])
  if value == 1 or redis.call('PTTL', KEYS[i]) < 0 then
    redis.call('PEXPIRE', KEYS[i], window)
  end
end
redis.call('DEL', KEYS[6])
redis.call('HSET', KEYS[6], 'verifier', ARGV[10], 'attempts', ARGV[11])
redis.call('PEXPIRE', KEYS[6], ARGV[12])
return {1, 0}
`

const verifyOTPScript = `
local challenge_type = redis.call('TYPE', KEYS[1])['ok']
if challenge_type ~= 'none' and challenge_type ~= 'hash' then
  redis.call('DEL', KEYS[1])
  return {0, 0}
end

local count = redis.call('INCR', KEYS[2])
if count == 1 or redis.call('PTTL', KEYS[2]) < 0 then
  redis.call('PEXPIRE', KEYS[2], ARGV[3])
end
if count > tonumber(ARGV[2]) then
  local ttl = redis.call('PTTL', KEYS[2])
  if ttl < 0 then ttl = tonumber(ARGV[3]) end
  return {2, ttl}
end

local state = redis.call('HMGET', KEYS[1], 'verifier', 'attempts')
local expected = state[1]
local attempts = tonumber(state[2])
if not expected or not attempts or attempts <= 0 then
  redis.call('DEL', KEYS[1])
  return {0, 0}
end
if expected == ARGV[1] then
  redis.call('DEL', KEYS[1])
  return {1, 0}
end

attempts = attempts - 1
if attempts <= 0 then
  redis.call('DEL', KEYS[1])
else
  redis.call('HSET', KEYS[1], 'attempts', attempts)
end
return {0, 0}
`

func newRedisOTPStore(client *redis.Client, cfg Config) *redisOTPStore {
	return &redisOTPStore{
		client: client, prefix: cfg.KeyPrefix, timeout: cfg.Timeouts.Redis,
		otp: cfg.OTP, limits: cfg.Limits,
		admitScript: redis.NewScript(admitOTPScript), verifyScript: redis.NewScript(verifyOTPScript),
	}
}

func (s *redisOTPStore) Admit(ctx context.Context, phoneTag, clientTag, verifier string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	keys := []string{
		s.prefix + "otp:cooldown:" + phoneTag,
		s.prefix + "limit:send:phone_hour:" + phoneTag,
		s.prefix + "limit:send:phone_day:" + phoneTag,
		s.prefix + "limit:send:client:" + clientTag,
		s.prefix + "limit:send:global",
		s.prefix + "otp:challenge:" + phoneTag,
	}
	result, err := s.admitScript.Run(ctx, s.client, keys,
		milliseconds(s.otp.ResendDelay),
		s.limits.PhoneHour.Max, milliseconds(s.limits.PhoneHour.Window),
		s.limits.PhoneDay.Max, milliseconds(s.limits.PhoneDay.Window),
		s.limits.ClientSend.Max, milliseconds(s.limits.ClientSend.Window),
		s.limits.GlobalSend.Max, milliseconds(s.limits.GlobalSend.Window),
		verifier, s.otp.Attempts, milliseconds(s.otp.Lifetime),
	).Result()
	if err != nil {
		return fmt.Errorf("admit OTP in Redis: %w", err)
	}
	allowed, retry, err := scriptPair(result)
	if err != nil {
		return fmt.Errorf("decode OTP admission result: %w", err)
	}
	switch allowed {
	case 0:
		retryAfter, err := retryDuration(retry)
		if err != nil {
			return fmt.Errorf("decode OTP admission retry duration: %w", err)
		}
		return &rateLimitError{retryAfter: retryAfter}
	case 1:
		if retry != 0 {
			return errors.New("OTP admission returned a retry duration for an allowed request")
		}
		return nil
	default:
		return fmt.Errorf("unknown OTP admission outcome %d", allowed)
	}
}

func (s *redisOTPStore) Verify(ctx context.Context, phoneTag, clientTag, verifier string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	keys := []string{
		s.prefix + "otp:challenge:" + phoneTag,
		s.prefix + "limit:verify:client:" + clientTag,
	}
	result, err := s.verifyScript.Run(ctx, s.client, keys,
		verifier, s.limits.ClientVerify.Max, milliseconds(s.limits.ClientVerify.Window),
	).Result()
	if err != nil {
		return fmt.Errorf("verify OTP in Redis: %w", err)
	}
	outcome, retry, err := scriptPair(result)
	if err != nil {
		return fmt.Errorf("decode OTP verification result: %w", err)
	}
	switch outcome {
	case 0:
		if retry != 0 {
			return errors.New("invalid OTP verification returned a retry duration")
		}
		return ErrInvalidCode
	case 1:
		if retry != 0 {
			return errors.New("successful OTP verification returned a retry duration")
		}
		return nil
	case 2:
		retryAfter, err := retryDuration(retry)
		if err != nil {
			return fmt.Errorf("decode OTP verification retry duration: %w", err)
		}
		return &rateLimitError{retryAfter: retryAfter}
	default:
		return fmt.Errorf("unknown OTP verification outcome %d", outcome)
	}
}

func retryDuration(milliseconds int64) (time.Duration, error) {
	const maxDurationMilliseconds = (1<<63 - 1) / int64(time.Millisecond)
	if milliseconds < 0 || milliseconds > maxDurationMilliseconds {
		return 0, errors.New("Redis script returned an invalid retry duration")
	}
	if milliseconds == 0 {
		// A key may have less than one millisecond remaining. Keep the 429
		// response contract useful after rounding to whole HTTP seconds.
		return time.Millisecond, nil
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func milliseconds(value time.Duration) int64 {
	return value.Milliseconds()
}

func scriptPair(value any) (int64, int64, error) {
	items, ok := value.([]any)
	if !ok || len(items) != 2 {
		return 0, 0, errors.New("Redis script returned a non-pair")
	}
	first, ok := items[0].(int64)
	if !ok {
		return 0, 0, errors.New("Redis script returned a non-integer outcome")
	}
	second, ok := items[1].(int64)
	if !ok {
		return 0, 0, errors.New("Redis script returned a non-integer retry duration")
	}
	return first, second, nil
}
