# Go Blocks

This repository develops reusable source blocks for Fiber v3, PostgreSQL with
handwritten SQL through pgx, and Redis through go-redis. Users copy the source
into their own projects and own the result.

## Mandatory upstream verification gate

This gate applies to every agent and every change that designs, creates, or
substantially modifies production code. It is a prerequisite, not an optional
research step.

1. Identify every affected upstream component, including the framework, client
   library, server technology, and relevant extension or middleware package.
2. Query the authoritative package source for the latest stable release and
   compare it with the repository pin. For Go modules, run `go list -m <module>@latest`
   and `go list -m -u -f '{{if .Update}}{{.Path}} {{.Version}} -> {{.Update.Version}}{{end}}' @direct`.
   Treat the database or Redis server version separately from its Go client.
3. Open and read the current official documentation, release notes, and migration
   notes for the affected version. Search results, blog posts, examples copied
   from other projects, and model memory are not sufficient evidence.
4. Inventory the current built-in capabilities related to the task before
   designing custom code. For Fiber work, review the complete official v3
   middleware catalog and inspect every middleware relevant to the concern,
   including its current `Config`, defaults, storage behavior, context behavior,
   response contract, and tests. Repeat this discovery step even if a prior agent
   already knows the catalog because upstream capabilities change.
5. Inspect the exact installed package source, exported API, examples, and tests
   in the Go module cache when documentation leaves behavior unclear. Code must
   target the pinned API, after deliberately upgrading the pin when needed.
6. Record the dated evidence and design decision in the relevant technology note
   or block README: checked version, official pages consulted, applicable native
   capabilities, which capability is used, and the concrete reason for each
   custom replacement. Update `docs/fiber-v3.md` for Fiber-wide findings.
7. Run repository checks after implementation and report the version comparison,
   documentation reviewed, native-capability decision, and test result in the
   final response. Never claim code is current without this evidence.

If authoritative current documentation or the module source cannot be accessed,
the agent must say that freshness is unverified and must not present remembered
APIs as current. “Latest” means the latest stable release verified on the date of
the change; it does not mean silently floating dependencies at build time.

## Working agreements

- Keep the project free of Docker, Compose, container-based development, and
  container-dependent tests. This is an explicit user preference.
- Keep work scoped to the user's current request. Repository preparation does
  not authorize implementing feature blocks.
- Read `docs/architecture.md` before making structural decisions and
  `docs/block-contract.md` before adding a block. Read `docs/fiber-v3.md` before
  changing Fiber code.
- Fiber v3 knowledge is a primary project requirement. Complete the mandatory
  upstream verification gate above before changing Fiber code. Never rely on a
  v2 example or memory of an older API. Prefer suitable native v3 middleware and
  record intentional exceptions in `docs/fiber-v3.md`.
- Use ordinary Go packages and explicit dependencies. Avoid a mandatory shared
  runtime, global database clients, dependency injection containers, and ORM
  layers.
- Keep Fiber context in HTTP handlers and middleware. Service and storage code
  uses `context.Context`, typed inputs, and explicit operation deadlines.
- Keep block code independent of the repository's tooling module
  (`goblocks.local/dev`) and document any dependencies between blocks.
- Never put credentials in committed files. External service checks use explicit
  `TEST_DATABASE_URL` and `TEST_REDIS_URL` environment variables.
- Check ordinary changes with `./scripts/check.ps1` on Windows or
  `bash scripts/check.sh` on Linux/macOS. Integration checks are separate and
  require test services; do not claim they passed when they were not run.
- Prefer small, reviewable changes and tests of meaningful behavior. Do not add
  a block installer, registry server, or extra framework without a concrete need.

## Current phase

The first block is `blocks/server`. `examples/basic-api` is an independent Go
module containing a verbatim source copy in `internal/server`. When changing the
block, refresh that maintained copy; `tests/copy` checks consistency. Verification
scripts test every repository Go module independently with `GOWORK=off`.
