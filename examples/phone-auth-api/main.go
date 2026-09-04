package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"example.com/phone-auth-api/internal/auth"
	"example.com/phone-auth-api/internal/server"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func main() {
	if err := run(); err != nil {
		slog.Error("application_stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	serverConfig, err := server.LoadConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: serverConfig.LogLevel}))
	app, err := server.New(serverConfig, logger)
	if err != nil {
		return err
	}

	databaseURL, err := requiredEnv("DATABASE_URL")
	if err != nil {
		return err
	}
	db, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		return errors.New("DATABASE_URL is invalid")
	}
	defer db.Close()
	redisURL, err := requiredEnv("REDIS_URL")
	if err != nil {
		return err
	}
	redisOptions, err := redis.ParseURL(redisURL)
	if err != nil {
		return errors.New("REDIS_URL is invalid")
	}
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()

	pepper, err := decodePepper(os.Getenv("AUTH_PEPPER"))
	if err != nil {
		return err
	}
	sms, err := newWebhookSMS(os.Getenv("SMS_WEBHOOK_URL"), os.Getenv("SMS_WEBHOOK_TOKEN"))
	if err != nil {
		return err
	}
	authConfig := auth.DefaultConfig()
	authConfig.Transport = auth.TransportBearer
	authConfig.Pepper = pepper
	authBlock, err := auth.New(authConfig, auth.Dependencies{
		DB: db, Redis: redisClient, SMS: sms, Logger: logger,
	})
	if err != nil {
		return err
	}

	server.RegisterHealth(app, func(ctx context.Context) bool {
		checkCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := db.Ping(checkCtx); err != nil {
			logger.ErrorContext(ctx, "readiness_failed", "dependency", "postgresql")
			return false
		}
		if err := redisClient.Ping(checkCtx).Err(); err != nil {
			logger.ErrorContext(ctx, "readiness_failed", "dependency", "redis")
			return false
		}
		return true
	})
	routes := app.Group("/auth")
	routes.Post("/otp/request", authBlock.RequestCode)
	routes.Post("/otp/verify", authBlock.VerifyCode)
	routes.Get("/me", authBlock.RequireUser, authBlock.Me)
	routes.Post("/logout", authBlock.Logout)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx, app, serverConfig, logger)
}

func requiredEnv(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func decodePepper(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("AUTH_PEPPER is required")
	}
	pepper, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(pepper) < 32 {
		return nil, errors.New("AUTH_PEPPER must be standard base64 encoding of at least 32 random bytes")
	}
	return pepper, nil
}

type webhookSMS struct {
	endpoint string
	token    string
	client   *http.Client
}

func newWebhookSMS(endpoint, token string) (*webhookSMS, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("SMS_WEBHOOK_URL must be an absolute URL without credentials, query, or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("SMS_WEBHOOK_URL must use HTTPS, except for a loopback development endpoint")
	}
	return &webhookSMS{endpoint: parsed.String(), token: token, client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func (s *webhookSMS) SendCode(ctx context.Context, message auth.SMSCode) error {
	payload, err := json.Marshal(struct {
		Phone     string    `json:"phone"`
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}{Phone: message.Phone, Code: message.Code, ExpiresAt: message.ExpiresAt})
	if err != nil {
		return errors.New("encode SMS webhook request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return errors.New("create SMS webhook request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", message.IdempotencyKey)
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return errors.New("SMS webhook request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("SMS webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

var _ auth.SMSSender = (*webhookSMS)(nil)
