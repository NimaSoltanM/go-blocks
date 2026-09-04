package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type userRepository interface {
	UpsertVerified(context.Context, string, time.Time) (User, error)
}

type postgresUserStore struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

func (s *postgresUserStore) UpsertVerified(ctx context.Context, phone string, verifiedAt time.Time) (User, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	newID, err := uuid.NewRandom()
	if err != nil {
		return User{}, fmt.Errorf("generate auth user UUID: %w", err)
	}
	var user User
	err = s.pool.QueryRow(ctx, `
INSERT INTO auth_users (id, phone_e164, created_at, last_verified_at)
VALUES ($1, $2, $3, $3)
ON CONFLICT (phone_e164) DO UPDATE
SET last_verified_at = GREATEST(auth_users.last_verified_at, EXCLUDED.last_verified_at)
RETURNING id, phone_e164`, newID, phone, verifiedAt.UTC()).Scan(&user.ID, &user.Phone)
	if err != nil {
		return User{}, fmt.Errorf("upsert verified auth user: %w", err)
	}
	return user, nil
}
