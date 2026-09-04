package auth

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/gofiber/fiber/v3"
)

type Transport string

const (
	TransportCookie Transport = "cookie"
	TransportBearer Transport = "bearer"
)

type WindowLimit struct {
	Max    int
	Window time.Duration
}

type OTPConfig struct {
	Lifetime    time.Duration
	ResendDelay time.Duration
	Attempts    int
}

type LimitConfig struct {
	PhoneHour    WindowLimit
	PhoneDay     WindowLimit
	ClientSend   WindowLimit
	GlobalSend   WindowLimit
	ClientVerify WindowLimit
}

type SessionConfig struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
}

type TimeoutConfig struct {
	Redis      time.Duration
	PostgreSQL time.Duration
	SMS        time.Duration
}

type CookieConfig struct {
	SessionName    string
	CSRFName       string
	Secure         bool
	AllowInsecure  bool
	TrustedOrigins []string
}

type Config struct {
	Transport       Transport
	Pepper          []byte
	KeyPrefix       string
	OTP             OTPConfig
	Limits          LimitConfig
	Session         SessionConfig
	Timeouts        TimeoutConfig
	Cookie          CookieConfig
	PhoneNormalizer PhoneNormalizer
	ClientKey       func(fiber.Ctx) string
}

func DefaultConfig() Config {
	return Config{
		Transport: TransportCookie,
		KeyPrefix: "gb:auth:v1:",
		OTP: OTPConfig{
			Lifetime: 2 * time.Minute, ResendDelay: time.Minute, Attempts: 5,
		},
		Limits: LimitConfig{
			PhoneHour:    WindowLimit{Max: 5, Window: time.Hour},
			PhoneDay:     WindowLimit{Max: 10, Window: 24 * time.Hour},
			ClientSend:   WindowLimit{Max: 30, Window: 10 * time.Minute},
			GlobalSend:   WindowLimit{Max: 300, Window: time.Minute},
			ClientVerify: WindowLimit{Max: 30, Window: 10 * time.Minute},
		},
		Session:  SessionConfig{IdleTimeout: 7 * 24 * time.Hour, AbsoluteTimeout: 30 * 24 * time.Hour},
		Timeouts: TimeoutConfig{Redis: time.Second, PostgreSQL: 2 * time.Second, SMS: 5 * time.Second},
		Cookie: CookieConfig{
			SessionName: "__Host-gb_session", CSRFName: "__Host-gb_csrf", Secure: true,
		},
		PhoneNormalizer: IranPhoneNormalizer{},
	}
}

func (cfg Config) Validate() error {
	if cfg.Transport != TransportCookie && cfg.Transport != TransportBearer {
		return errors.New("auth transport must be cookie or bearer")
	}
	if len(cfg.Pepper) < 32 {
		return errors.New("auth pepper must contain at least 32 bytes")
	}
	if len(cfg.KeyPrefix) == 0 || len(cfg.KeyPrefix) > 128 || !strings.HasSuffix(cfg.KeyPrefix, ":") ||
		strings.IndexFunc(cfg.KeyPrefix, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return errors.New("auth Redis key prefix must be 1-128 non-space, non-control characters ending in ':'")
	}
	if cfg.OTP.Lifetime < time.Millisecond || cfg.OTP.ResendDelay < time.Millisecond || cfg.OTP.ResendDelay > cfg.OTP.Lifetime {
		return errors.New("auth OTP lifetime and resend delay must be positive, with resend delay no longer than lifetime")
	}
	if cfg.OTP.Attempts <= 0 {
		return errors.New("auth OTP attempts must be positive")
	}
	if cfg.OTP.Attempts > 1<<31-1 {
		return errors.New("auth OTP attempts must not exceed 2147483647")
	}
	for name, limit := range map[string]WindowLimit{
		"phone hour": cfg.Limits.PhoneHour, "phone day": cfg.Limits.PhoneDay,
		"client send": cfg.Limits.ClientSend, "global send": cfg.Limits.GlobalSend,
		"client verify": cfg.Limits.ClientVerify,
	} {
		if limit.Max <= 0 || limit.Max > 1<<31-1 || limit.Window < time.Millisecond {
			return fmt.Errorf("auth %s limit must be 1-2147483647 and its window must be at least 1ms", name)
		}
	}
	if cfg.Session.IdleTimeout < time.Millisecond || cfg.Session.AbsoluteTimeout < cfg.Session.IdleTimeout {
		return errors.New("auth session idle timeout must be positive and no longer than absolute timeout")
	}
	if cfg.Timeouts.Redis <= 0 || cfg.Timeouts.PostgreSQL <= 0 || cfg.Timeouts.SMS <= 0 {
		return errors.New("auth Redis, PostgreSQL, and SMS timeouts must be positive")
	}
	if isNilInterface(cfg.PhoneNormalizer) {
		return errors.New("auth phone normalizer is required")
	}
	if cfg.Transport == TransportCookie {
		if !validCookieName(cfg.Cookie.SessionName) || !validCookieName(cfg.Cookie.CSRFName) ||
			cfg.Cookie.SessionName == cfg.Cookie.CSRFName {
			return errors.New("auth session and CSRF cookie names must be distinct valid cookie names")
		}
		if cfg.Cookie.Secure {
			if !strings.HasPrefix(cfg.Cookie.SessionName, "__Host-") || !strings.HasPrefix(cfg.Cookie.CSRFName, "__Host-") {
				return errors.New("secure auth cookie names must use the __Host- prefix")
			}
		} else if !cfg.Cookie.AllowInsecure {
			return errors.New("insecure auth cookies require explicit AllowInsecure opt-in")
		} else if strings.HasPrefix(cfg.Cookie.SessionName, "__Host-") || strings.HasPrefix(cfg.Cookie.CSRFName, "__Host-") {
			return errors.New("insecure auth cookies cannot use the __Host- prefix")
		}
		for _, origin := range cfg.Cookie.TrustedOrigins {
			if !validTrustedOrigin(origin) {
				return fmt.Errorf("auth trusted origin %q must be an HTTP(S) origin without credentials, path, query, or fragment", origin)
			}
		}
	}
	return nil
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// validTrustedOrigin mirrors the accepted Fiber v3 CSRF origin grammar so bad
// application configuration is returned as an error instead of panicking.
func validTrustedOrigin(value string) bool {
	origin := strings.TrimSpace(value)
	if i := strings.Index(origin, "://*."); i >= 0 {
		origin = origin[:i+3] + origin[i+5:]
	}
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.User == nil && !strings.ContainsRune(u.Host, '*') &&
		(u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == ""
}

func validCookieName(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c <= 0x20 || c >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", rune(c)) {
			return false
		}
	}
	return true
}
