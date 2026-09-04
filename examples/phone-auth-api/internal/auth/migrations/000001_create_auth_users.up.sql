CREATE TABLE auth_users (
    id uuid PRIMARY KEY,
    phone_e164 text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL,
    last_verified_at timestamptz NOT NULL,
    CONSTRAINT auth_users_phone_e164_format
        CHECK (phone_e164 ~ '^\+989[0-9]{9}$')
);
