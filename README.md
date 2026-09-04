# Go Blocks

Reusable backend source blocks for **Fiber v3, PostgreSQL, and Redis**.
Copy a feature into your application, wire its dependencies, and adapt the code.

Available blocks are [server](blocks/server/README.md) and
[Iranian phone auth](blocks/auth/README.md). The repository includes independent
[basic server](examples/basic-api/README.md) and
[phone-auth](examples/phone-auth-api/README.md) examples with maintained source copies.

## Stack

| Component | Choice |
| --- | --- |
| Go | 1.26, with 1.26.4 as the preferred toolchain |
| HTTP | Fiber v3 |
| PostgreSQL | Handwritten, parameterized SQL through pgx v5 and pgxpool |
| Redis | go-redis v9 |
| Schema changes | Paired plain SQL files, verified with `golang-migrate` |

Exact Go dependency versions are recorded in `go.mod` and `go.sum`. The local
module name `goblocks.local/dev` identifies this development repository; it is
not a published import path and need not appear in copied blocks.

## Get ready

Install Go 1.26.4 or newer and Git, then run from the repository root:

```sh
go mod download
```

On Windows PowerShell:

```powershell
.\scripts\check.ps1
```

On Linux or macOS:

```sh
bash scripts/check.sh
```

Both scripts check Go formatting, dependency consistency and checksums, `go vet`,
and tests. They also compile the optional integration tests without connecting to
services. Every Go module, including examples, is checked independently with
`GOWORK=off`. They work from any current directory. Formatting errors are reported
without changing files; run `gofmt -w` on the reported files to fix them.

To enable Go's race detector, use `./scripts/check.ps1 -Race` or
`bash scripts/check.sh --race`. This requires a compatible C compiler and CGO.

The underlying commands, if you prefer to run them directly, are:

```sh
go mod tidy -diff
go mod verify
go vet -tags=integration ./...
go test -count=1 ./...
go test -tags=integration -run='^$' ./tests/smoke
```

These are the root module's commands. Repeat tidy, verify, vet, and test inside
each example module to check it independently, or use the scripts above to check
all modules automatically.

No external service installation is part of these checks. Tests cover server and
auth behavior, example wiring, and source-copy consistency. Lifecycle and
body-limit tests briefly open ephemeral loopback TCP listeners; PostgreSQL and
Redis are not needed. The dependency smoke test also runs in-process.

## Run the server example

From the repository root on Windows:

```powershell
go -C examples/basic-api build -o ../../bin/basic-api.exe .
.\bin\basic-api.exe
```

On Linux/macOS, build to `../../bin/basic-api` and run `./bin/basic-api`.
Request `http://127.0.0.1:8080/api/hello`, `/livez`, or `/readyz`. Press Ctrl+C to
stop gracefully. The example's readiness checks HTTP only because it owns no
database clients. See its README for configuration and copying instructions.

## Optional database connection checks

Use existing PostgreSQL and Redis test instances, whether installed natively or
hosted elsewhere. There is no Docker or container setup in this repository.

Set both variables explicitly. These are placeholder URLs, not working credentials:

```powershell
$env:TEST_DATABASE_URL = 'postgres://USER:PASSWORD@HOST:5432/TEST_DATABASE?sslmode=require'
$env:TEST_REDIS_URL = 'rediss://default:PASSWORD@HOST:6379/0'
go test -tags=integration -count=1 -v ./tests/smoke
```

For Bash or Zsh:

```sh
export TEST_DATABASE_URL='postgres://USER:PASSWORD@HOST:5432/TEST_DATABASE?sslmode=require'
export TEST_REDIS_URL='rediss://default:PASSWORD@HOST:6379/0'
go test -tags=integration -count=1 -v ./tests/smoke
```

Use the TLS settings supplied by your service. Plain local Redis uses `redis://`;
`rediss://` enables TLS. The checks only run `SELECT 1` and Redis `PING`; they do
not migrate a schema or write application data. Missing variables fail an
explicit integration run rather than silently skipping it. `.env` files are
ignored by Git and are not automatically loaded.

The auth block has separate opt-in behavior checks that create isolated Redis
keys and a temporary PostgreSQL schema, then remove them. Run them only against
dedicated test services, never production:

```powershell
go test -tags=integration -count=1 -v ./blocks/auth
```

## Repository layout

```text
blocks/server/          Copyable server source, tests, and integration instructions
blocks/auth/            Iranian SMS auth source, migration, tests, and contract
examples/basic-api/     Independent Go module containing a real server source copy
examples/phone-auth-api/ Independent module containing copies of server and auth
docs/architecture.md    Boundaries and stack decisions
docs/block-contract.md  Contents and integration rules for future blocks
docs/fiber-v3.md        Version checks, middleware inventory, and current decisions
scripts/                Local verification commands
tests/smoke/            Dependency and optional service connectivity checks
tests/copy/             Maintained example/source consistency checks
.github/workflows/     Linux and Windows verification
```

GitHub Actions runs the same checks without services on Linux and Windows, plus
the race detector on Linux. It does not provision external services or run the
optional integration checks. The workflow will run once this repository is
connected to GitHub and pushed.

## Next milestone

Use both blocks in a real project and adapt the example webhook to its SMS
provider. Additional identity features should be separate contract revisions or
blocks rather than expanding this intentionally narrow login flow implicitly.
