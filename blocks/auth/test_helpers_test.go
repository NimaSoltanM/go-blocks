package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type memoryItem struct {
	value     []byte
	expiresAt time.Time
}

type memoryStorage struct {
	mu        sync.Mutex
	items     map[string]memoryItem
	lastKey   string
	operation string
	err       error
}

func newMemoryStorage() *memoryStorage {
	return &memoryStorage{items: make(map[string]memoryItem)}
}

func (s *memoryStorage) GetWithContext(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastKey, s.operation = strings.Clone(key), "get"
	if s.err != nil {
		return nil, s.err
	}
	item, ok := s.items[key]
	if !ok || (!item.expiresAt.IsZero() && !time.Now().Before(item.expiresAt)) {
		delete(s.items, key)
		return nil, nil
	}
	return append([]byte(nil), item.value...), nil
}

func (s *memoryStorage) Get(key string) ([]byte, error) {
	return s.GetWithContext(context.Background(), key)
}

func (s *memoryStorage) SetWithContext(ctx context.Context, key string, value []byte, expiration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key = strings.Clone(key)
	s.lastKey, s.operation = key, "set"
	if s.err != nil {
		return s.err
	}
	item := memoryItem{value: append([]byte(nil), value...)}
	if expiration > 0 {
		item.expiresAt = time.Now().Add(expiration)
	}
	s.items[key] = item
	return nil
}

func (s *memoryStorage) Set(key string, value []byte, expiration time.Duration) error {
	return s.SetWithContext(context.Background(), key, value, expiration)
}

func (s *memoryStorage) DeleteWithContext(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastKey, s.operation = strings.Clone(key), "delete"
	if s.err != nil {
		return s.err
	}
	delete(s.items, key)
	return nil
}

func (s *memoryStorage) Delete(key string) error {
	return s.DeleteWithContext(context.Background(), key)
}

func (s *memoryStorage) ResetWithContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.items)
	return nil
}

func (s *memoryStorage) Reset() error { return s.ResetWithContext(context.Background()) }
func (*memoryStorage) Close() error   { return nil }

type fakeOTPRepository struct {
	mu             sync.Mutex
	admitErr       error
	admitHook      func()
	verifyErr      error
	admitCalls     int
	verifyCalls    int
	phoneTag       string
	clientTag      string
	verifier       string
	verifyVerifier string
}

func (f *fakeOTPRepository) Admit(_ context.Context, phoneTag, clientTag, verifier string) error {
	f.mu.Lock()
	f.admitCalls++
	f.phoneTag, f.clientTag, f.verifier = phoneTag, clientTag, verifier
	err, hook := f.admitErr, f.admitHook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (f *fakeOTPRepository) Verify(_ context.Context, phoneTag, clientTag, verifier string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifyCalls++
	f.phoneTag, f.clientTag, f.verifyVerifier = phoneTag, clientTag, verifier
	return f.verifyErr
}

type fakeUserRepository struct {
	mu       sync.Mutex
	err      error
	calls    int
	phone    string
	verified time.Time
	user     User
}

func (f *fakeUserRepository) UpsertVerified(_ context.Context, phone string, verified time.Time) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.phone, f.verified = phone, verified
	return f.user, f.err
}

type fakeSMSSender struct {
	mu         sync.Mutex
	err        error
	panicValue any
	calls      int
	code       SMSCode
}

func (f *fakeSMSSender) SendCode(ctx context.Context, code SMSCode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.code = code
	if f.panicValue != nil {
		panic(f.panicValue)
	}
	if f.err != nil {
		return f.err
	}
	return ctx.Err()
}

type fakeSet struct {
	otp   *fakeOTPRepository
	users *fakeUserRepository
	sms   *fakeSMSSender
}

func newFakes() fakeSet {
	return fakeSet{
		otp:   &fakeOTPRepository{},
		users: &fakeUserRepository{user: User{ID: uuid.MustParse("9c1fcb97-942c-4f8a-94f7-dc165c737cc6"), Phone: "+989121234567"}},
		sms:   &fakeSMSSender{},
	}
}

func newRequest(method, path, body string) *http.Request {
	req, err := http.NewRequest(method, path, strings.NewReader(body))
	if err != nil {
		panic(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

var errTestDependency = errors.New("test dependency unavailable")
