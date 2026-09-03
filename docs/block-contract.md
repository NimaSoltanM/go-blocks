# Block contract

This describes source blocks and their integration contract, not a registry API.

## What a block contains

Each block lives in `blocks/<name>/` and includes:

1. Ordinary Go source files with an understandable, explicit public API.
2. Tests for its important behavior and failure cases.
3. A README with required Go dependencies, configuration, installation steps,
   wiring, expected behavior, and limitations.
4. SQL migrations when it owns schema objects.
5. A small usage example, either inline or in `examples/`.

Keep optional files optional. A rate-limit helper should not inherit the folder
structure of a complete authentication feature.

## Required integration documentation

Document:

- What is copied and where it can live in the destination application.
- Which imports or package names need adjustment.
- Which Go dependency versions were verified and any other required blocks.
- Constructor inputs, client ownership, shutdown, and background workers.
- Environment variables and defaults without real credentials.
- Routes, request/response shapes, middleware order, and authenticated identity.
- Schema objects, migration ordering, and how to incorporate migrations into an
  existing application's history without renumbering already applied migrations.
- Timeouts, retry behavior, concurrency limits, and behavior during service outages.
- What the developer is expected to customize.

## Independence

Copying a block must not require importing the repository's development module.
Application clients, loggers, and functions are passed in explicitly. If a block
depends on another block, document that dependency and the smallest required API.

Shared infrastructure such as a database pool is created once by the application.
The receiving feature does not close clients it did not create.

## Before calling a block reusable

- The documented example works with the pinned dependency baseline.
- Relevant tests cover the feature's guarantees, including real database or
  Redis checks when those guarantees depend on server behavior.
- The feature can be copied into a second Go module without depending on this
  repository at runtime.
- Required wiring and application-specific decisions are visible in the README.

An update mechanism is not implemented. Record a source Git commit as provenance
when copying committed code, and review updates manually. The maintained
`examples/basic-api` copy is verified against the current source by tests.
