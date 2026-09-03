# Basic API

A standalone Go module using a real copy of the server block at
`internal/server`. It only imports Fiber and its own local package. There are no
`replace` directives, Go workspace links, or imports of `goblocks.local/dev`.

## Run from the repository root

Windows PowerShell:

```powershell
go -C examples/basic-api build -o ../../bin/basic-api.exe .
.\bin\basic-api.exe
```

Linux/macOS:

```sh
go -C examples/basic-api build -o ../../bin/basic-api .
./bin/basic-api
```

The default address is `127.0.0.1:8080`. The startup JSON log reports the actual
address. Ctrl+C stops the listener and waits for active requests. Running the
built binary directly makes signal handling straightforward.

In another terminal, open or request:

- `http://127.0.0.1:8080/livez` -> `{"status":"OK"}`
- `http://127.0.0.1:8080/readyz` -> `{"status":"OK"}`
- `http://127.0.0.1:8080/api/hello` -> `{"message":"Hello from Go Blocks"}`
- `http://127.0.0.1:8080/missing` -> 404 with the standard JSON error envelope

There are no PostgreSQL or Redis clients in this example, so readiness checks
HTTP only. Add clients in `run()`, pass them to the relevant feature, provide a
readiness callback, and close them after `server.Run` returns.

## Configure

Set environment variables before starting the binary, for example:

```powershell
$env:HTTP_ADDR = '127.0.0.1:9000'
$env:LOG_LEVEL = 'debug'
.\bin\basic-api.exe
```

See the server block README for all configuration values and deadline semantics.
Environment files are not automatically loaded.

## Verify independently

```sh
go -C examples/basic-api mod verify
go -C examples/basic-api vet ./...
go -C examples/basic-api test -race ./...
```

Repository check scripts also test this module, with `GOWORK=off`. You can copy
this whole directory outside the repository and build it without the root module.

`internal/server/config.go`, `server.go`, and `run.go` are copied verbatim from
`blocks/server` in the same repository snapshot. The root source-copy test checks
that those files remain in sync while the example is maintained here. End-user
copies are free to diverge. The block behavior tests remain under `blocks/server`;
this example tests the application's route wiring.
