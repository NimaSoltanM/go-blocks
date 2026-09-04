package auth

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisSessionStorage is the narrow Fiber storage contract sessions need. It
// borrows the application client, namespaces every non-empty key, and cannot
// flush the shared Redis database.
type redisSessionStorage struct {
	client  *redis.Client
	prefix  string
	timeout time.Duration
}

func newRedisSessionStorage(client *redis.Client, prefix string, timeout time.Duration) *redisSessionStorage {
	return &redisSessionStorage{client: client, prefix: prefix, timeout: timeout}
}

func (s *redisSessionStorage) key(key string) string {
	if key == "" {
		return ""
	}
	return s.prefix + key
}

func (s *redisSessionStorage) context(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.timeout)
}

func (s *redisSessionStorage) GetWithContext(ctx context.Context, key string) ([]byte, error) {
	if key == "" {
		return nil, nil
	}
	ctx, cancel := s.context(ctx)
	defer cancel()
	value, err := s.client.Get(ctx, s.key(key)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	return value, err
}

func (s *redisSessionStorage) Get(key string) ([]byte, error) {
	return s.GetWithContext(context.Background(), key)
}

func (s *redisSessionStorage) SetWithContext(ctx context.Context, key string, value []byte, expiration time.Duration) error {
	if key == "" || len(value) == 0 {
		return nil
	}
	if expiration < 0 {
		expiration = 0
	}
	ctx, cancel := s.context(ctx)
	defer cancel()
	return s.client.Set(ctx, s.key(key), value, expiration).Err()
}

func (s *redisSessionStorage) Set(key string, value []byte, expiration time.Duration) error {
	return s.SetWithContext(context.Background(), key, value, expiration)
}

func (s *redisSessionStorage) DeleteWithContext(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	ctx, cancel := s.context(ctx)
	defer cancel()
	return s.client.Del(ctx, s.key(key)).Err()
}

func (s *redisSessionStorage) Delete(key string) error {
	return s.DeleteWithContext(context.Background(), key)
}

func (*redisSessionStorage) ResetWithContext(context.Context) error {
	return ErrStorageResetUnsupported
}

func (*redisSessionStorage) Reset() error { return ErrStorageResetUnsupported }

// Close is intentionally a no-op: the application owns the Redis client.
func (*redisSessionStorage) Close() error { return nil }
