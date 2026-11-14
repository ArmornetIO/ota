# CLAUDE.md

Over-the-air updates for Go programs. README.md explains the design; these
are the rules that cause real bugs when broken.

## Rules

- **The core takes no third-party dependencies.** The root module, `lifecycle`
  and `service` use only the standard library; `deps_test.go` enforces it.
  Anything that needs a library (Sigstore, OpenTelemetry) is its own module.
- **The server holds no key.** Offers are signed in the publisher's pipeline.
  Rollouts and pins only choose among signed offers; nothing server-side may
  create or alter one.
- **Verify before parsing.** A signature covers an offer's exact bytes; read
  nothing from an offer until it has verified.
- **Fail closed.** A verifier error, a stored trust root that can't be loaded,
  a hash mismatch: each refuses. There is no warn-and-continue path.
- **The replay floor moves when an attempt ends** (commit or rollback), never
  before the swap: a crash mid-swap must leave the offer acceptable.
- **The health gate runs in the new process's `Boot`** for every restart
  strategy except blue/green, which gates in its own chain.
- **Kill hooks exist only under the `otatest` build tag.** No shipped binary
  may be made to exit by an environment variable.

## Testing

- Each module has its own `go.mod`: test `.`, `lifecycle`, `service`,
  `verify/sigstore`, `server/source/github`, `otel` and `cmd/ota`
  separately.
- Tests open localhost listeners; a sandbox that blocks them fails `httptest`.
- `client/faults_test.go` and `cli/cli_test.go` build `testdata/sampleapp` and
  run real binaries; `-short` skips them.
- Run `-race` on anything with a goroutine.

## Style

Comments say why, never what. Errors wrap once with context
(`fmt.Errorf("client: rollback: %w", err)`); callers branch on the sentinels in
`errors.go` with `errors.Is`, never on message text.
