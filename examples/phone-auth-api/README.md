# Phone auth API example

This independent module contains verbatim source copies of `blocks/server` and
`blocks/auth`, wired in bearer-token mode. It also includes a small generic SMS
webhook adapter; replace that adapter with your vendor integration in a real
application.

Apply `internal/auth/migrations/000001_create_auth_users.up.sql` to a dedicated
development database first. Then configure:

```powershell
$env:DATABASE_URL = 'postgres://USER:PASSWORD@HOST:5432/DB?sslmode=require'
$env:REDIS_URL = 'rediss://default:PASSWORD@HOST:6379/0'
$env:AUTH_PEPPER = '<standard base64 for at least 32 random bytes>'
$env:SMS_WEBHOOK_URL = 'https://your-development-sms-adapter.example/send-code'
$env:SMS_WEBHOOK_TOKEN = '<optional bearer credential>'
go run .
```

Only an `http://localhost`, `127.0.0.0/8`, or `::1` webhook may use plain HTTP.
The webhook receives JSON containing `phone`, `code`, and `expires_at`, plus an
`Idempotency-Key` header. It must return any 2xx status after accepting the send.
The example never logs the code, phone, token, webhook response, or credentials.

The API exposes `/auth/otp/request`, `/auth/otp/verify`, `/auth/me`, and
`/auth/logout`, plus `/livez` and `/readyz`. Successful verification returns an
opaque `session_token`; send it as `Authorization: Bearer <token>`.

The repository copy test keeps both internal packages and the migration files in
sync with their source blocks. This module does not import `goblocks.local/dev`
and is tested independently with `GOWORK=off`.
