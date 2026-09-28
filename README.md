# ota

Over-the-air updates for Go programs. Your release pipeline signs the update,
the machine streams and verifies it, swaps the binary atomically, and keeps the
new version only after it proves it is healthy. If anything fails, including a
power cut halfway through, the machine is left running the version it had
before.

```
offer ─▶ verify signature ─▶ download (resumable, hashed) ─▶ swap ─▶ restart ─▶ health gate ─▶ commit
                                                                                         └──▶ roll back
```

- **The server holds no signing key.** The publisher signs offers in CI. The
  server only chooses which signed offer each machine sees, so a compromised
  server cannot install anything the publisher did not sign.
- **One binary.** Updating needs no second program to ship or manage: the
  updater lives inside the program it updates.
- **No dependencies.** The core modules use only the standard library, and
  pure Go (no CGo) on Linux, macOS and Windows.

## What it does

Tested end to end by suites that run real binaries: a fault-injection run
kills the updater at every step, and the launcher and CLI suites exercise
every hand-off.

- **Client:** check, verify, resumable streamed download, atomic swap with
  crash repair, health-gated commit and rollback, and a durable outbox for
  reports.
- **Signatures:** Sigstore/cosign (keyless, verified offline against a
  pinned root and signer identity) or pinned ed25519 keys. The trust root can
  be rotated through a signed offer.
- **Publishing:** `ota offer` builds the offer from goreleaser output. See
  [docs/goreleaser.md](docs/goreleaser.md).
- **Server:**
  - Intake from GitHub Releases or a static index; offers are verified at
    intake too.
  - Channels, staged rollouts with stable buckets, per-client pins, and a
    host `Targeter`.
  - Automatic halt on a failure rate.
  - An admin API that exists only when you give it an authorizer.
- **Restarts:** re-exec, in-place service restart, side-by-side (blue/green)
  services, or a resident launcher (`run`) that supervises the program and
  moves itself onto the new version without restarting it.
- **Your binary's CLI:** `update` (with stable exit codes) and `run`, mounted
  as subcommands.
- **Host hooks:** approve, defer or refuse an offer; drain before the swap;
  reorder the update's steps.
- **OpenTelemetry:** metrics and traces through an optional add-on module.

## Modules

Each is its own Go module, so you take on only the dependencies you use.

| Module | What it is | Third-party deps |
|---|---|---|
| `github.com/ArmornetIO/ota` | client, server, restart strategies, health gate | none |
| [`github.com/ArmornetIO/ota/lifecycle`](lifecycle/) | ordered start/stop hooks with rollback | none |
| [`github.com/ArmornetIO/ota/service`](service/) | register a program with systemd, launchd or Windows Task Scheduler | none |
| `github.com/ArmornetIO/ota/verify/sigstore` | Sigstore verification | sigstore-go |
| `github.com/ArmornetIO/ota/otel` | OpenTelemetry metrics and traces | OpenTelemetry |
| `github.com/ArmornetIO/ota/cmd/ota` | standalone server (`ota serve`) and publisher CLI (`ota offer`) | the above |

The core, `lifecycle` and `service` need Go 1.24 or newer; `verify/sigstore`,
`otel` and `cmd/ota` follow their dependencies (Go 1.25). A test holds the core
modules to the standard library.

## Using it

### In your program

Call `Boot` first thing in `main`. It finishes anything a previous run left
behind: it repairs an interrupted swap, rolls back a version that kept
crashing, and starts the health gate for a version on its first run. Call
`CheckAndUpdate` whenever you decide to look for an update. `ota` never
schedules anything itself.

```go
import (
	"github.com/ArmornetIO/ota/client"
	"github.com/ArmornetIO/ota/health"
	"github.com/ArmornetIO/ota/verify/ed25519"
)

verifier, err := ed25519.New(publisherKey) // pinned in the binary
if err != nil {
	log.Fatal(err)
}

c, err := client.New(client.Options{
	Program:        "myapp",
	ClientID:       installID, // your stable id for this installation
	CurrentVersion: version,   // set with -ldflags at build time
	Transport:      &client.HTTPTransport{BaseURL: "https://updates.example.com"},
	Verifier:       verifier,
	Health:         health.ProbeURL("http://127.0.0.1:8080/healthz"),
})
if err != nil {
	log.Fatal(err)
}

// First thing in main.
if _, err := c.Boot(ctx); err != nil {
	log.Fatal(err)
}

// Whenever you decide to look for an update. With the default restarter this
// does not return when an update is applied: the process becomes the new
// version, whose Boot runs the health gate.
res, err := c.CheckAndUpdate(ctx)
```

`Verifier` is required and has no default, so a program can't ship unverified
by omission.

### Proving the new version is healthy

A new version is kept only after the health gate commits it:

- **In-process status:** `health.StatusFunc(func() string { ... })` returning
  one of the OpenTelemetry Collector status values (`Starting`, `OK`,
  `RecoverableError`, `PermanentError`, `FatalError`, `Stopping`, `Stopped`).
- **A local URL:** `health.ProbeURL(url)`. A 2xx means OK and a 503 means
  Starting. A JSON body with a `status` field in the vocabulary above takes
  precedence over the status code.
- **Explicitly:** `health.CommitOnly()` (the default) plus a call to
  `c.Commit()` once your program knows it is working.

The gate commits after `SteadyWindow` (default 30s) of continuous OK. It rolls
back on `FatalError`, on a missed `CommitDeadline` (default 5m), or after
`MaxAttempts` starts that never committed (default 3), which catches crash
loops.

### Letting the host decide when

`Hooks.BeforeApply` answers each verified offer with `ota.ApplyNow()`,
`ota.DeferUntil(t, reason)` or `ota.RefuseOffer(reason)`. If you want a
person to approve, ask them inside the hook; `ota` never draws a UI.
`Hooks.Drain` runs before the swap with a deadline so your program can shut
down gracefully. If it overruns, the attempt is abandoned rather than forced.

### From the command line, in your own binary

```go
import "github.com/ArmornetIO/ota/cli"

switch os.Args[1] {
case "update", "run", "offer":
	os.Exit(cli.Commands(loadConfig)[os.Args[1]](os.Args[2:]))
}
```

- `myapp update` checks and applies once. It never restarts itself. If a
  launcher runs the program, it asks the launcher to restart onto the new
  version. Otherwise, if the program is a registered service, it restarts the
  service. Otherwise it leaves the update installed for the next start and
  exits 60. It then waits for the new version's verdict.
- `myapp run -- serve` runs `myapp serve` as a child and keeps it running.
  Crashes are restarted with backoff unless a service manager already does
  that. Updates restart the child, and after a commit the launcher re-execs
  itself onto the new version while keeping the child running.

| Exit | Meaning |
|---|---|
| 0 | up to date, or updated and committed |
| 10 | deferred or refused by your `BeforeApply` hook |
| 11 | your `Drain` hook overran its deadline; nothing swapped |
| 20 | offer refused (signature, replay, expiry, channel, downgrade) |
| 30 | download failed |
| 40 | installed, then rolled back |
| 50 | another update is in progress |
| 60 | installed; applies on the program's next start |

### Choosing how the new version starts

`client.Options.Restarter`:

- `restart.SelfExec()`: the default. Replaces the process in place.
- `restart.ServiceInPlace(spec, nil)`: swaps under a registered service and
  restarts it.
- `restart.ServiceBlueGreen(...)`: installs each version side by side as its
  own service and switches only after the new one is healthy. The program must
  allow port sharing (`SO_REUSEPORT`) for zero downtime.
- Under `run`, the child strategy is chosen automatically.

Every strategy is a chain of named lifecycle steps. `Options.EditChain` lets
you insert, remove or reorder them.

### Serving updates

```go
import (
	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/server"
	"github.com/ArmornetIO/ota/verify/ed25519"
)

store := server.NewMemStore()
if err := store.PutOffer(ota.SignedOffer{Offer: offerJSON, Signature: sig, Kind: ed25519.Kind}); err != nil {
	log.Fatal(err)
}

srv, err := server.New(server.Options{
	Store:     store,
	Blobs:     server.FSBlobStore{Dir: "/var/lib/ota/blobs"},
	Authorize: checkBearerToken, // nil only behind a host that already authenticates
})
if err != nil {
	log.Fatal(err)
}
http.Handle("/", srv.Handler())
```

The handler is a plain `http.Handler`: mount it on your own router, or behind
`http.StripPrefix`. It serves `POST /v1/check`, `GET /v1/artifacts/{sha256}`
(with resumable Range requests, and gzip when the whole file is requested) and
`POST /v1/reports`.

**Rollouts:** nothing is offered until a rollout, a pin or your `Targeter`
chooses a version, and each can only choose among offers the publisher signed.
With `AuthorizeAdmin` set, `/v1/admin/*` lists releases, triggers intake,
accepts a signed offer upload, sets rollouts and pins, and shows what every
client runs.

**Client IDs:** if your `Authorize` returns a client id, it replaces the id in
the request body, so one client cannot check or report as another.

**Standalone:** `ota serve` runs the same server on its own (see `ota serve -h`). If you run your own `http.Server`, set
`ReadHeaderTimeout` but not `ReadTimeout`/`WriteTimeout`: artifact downloads
are long-lived streams.

## The offer

An offer is a small JSON document, signed by the publisher over its **exact
bytes**. The client verifies it before parsing anything, so there is no
canonical form to get wrong.

```json
{
  "schema": "ota.offer/v1",
  "program": "myapp",
  "version": "1.8.0",
  "issued_at": 1789776000000,
  "channels": ["stable"],
  "artifacts": [
    { "os": "linux", "arch": "amd64", "sha256": "9f2c…", "size": 48213504,
      "urls": ["https://mirror.example.com/myapp_linux_amd64"] }
  ]
}
```

A client refuses an offer when any of these hold:

- The signature fails.
- It is for another program.
- `issued_at` is not newer than the last offer it acted on (replay).
- `issued_at` is too far in the future (5-minute tolerance).
- It has expired (`expires_at`, optional).
- It excludes the client's channel.
- It names an older version without `"downgrade": true`.
- It has no artifact for the client's OS and architecture.

A deliberate rollback is a new signed offer naming the older version.

## Security model

- **The publisher signs offers; nothing else can.** A compromised release
  server can withhold offers, but it cannot forge one, and it cannot replay an
  old one: each machine remembers the newest offer it acted on.
- **Accepted risk:** with `expires_at` unset (the default), a compromised
  server can hide newer releases and keep machines on an older signed version.
  Set an expiry and re-sign on a schedule if that matters to you.
- **Downloads are checked against the signed size and SHA-256** as they stream
  and again over the whole file, including after a resume. So a mirror, a
  proxy or a tampered partial file can only make an update fail, never install
  the wrong bytes.
- **Mirror URLs must be `https`.** Plain `http` is allowed only for localhost.

## On disk

State lives next to the binary, in `<dir>/.ota/<binary-name>/` (mode 0700).
Keeping it on the same filesystem makes the swap a single atomic rename, and
two programs installed in the same directory never share state.

| File | Purpose |
|---|---|
| `lock` | Single-updater lock; the OS releases it if the process dies |
| `state.json` | Last accepted offer (the replay floor), running version |
| `pending.json` | An update awaiting its health gate |
| `prev` | The previous binary, kept until commit |
| `.ota-*.part`, `download.json` | A resumable partial download |

Temp files are created exclusively with mode 0600. Leftovers from crashed runs
are removed at the next `Boot`, but only while holding the lock.

## Platforms

Linux, macOS and Windows, pure Go. On Windows a running `.exe` cannot be
overwritten, so the swap renames it aside first. The displaced file is removed
at the next start, and a crash between the two renames is repaired at boot.

## Development

```sh
go test ./...                 # needs to open localhost listeners
go test ./... -short          # skips the suites that build binaries
```

The fault-injection suite (`client/faults_test.go`) builds `testdata/sampleapp`
twice, as v1 and v2, and kills the updater at every step using the `otatest`
build tag. Without that tag the kill hooks compile to nothing, so no shipped
binary can be made to exit by an environment variable.

## License

Apache-2.0. See [LICENSE](LICENSE).
