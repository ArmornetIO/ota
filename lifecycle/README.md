# ota/lifecycle

Ordered start/stop hooks with rollback. It is the chain `ota` runs an update
through, and it works for any program that starts and stops components in a
fixed order.

```go
import "github.com/ArmornetIO/ota/lifecycle"

var m lifecycle.Manager
m.Append(lifecycle.Hook{Name: "db", OnStart: openDB, OnStop: closeDB})
m.Append(lifecycle.Hook{Name: "http", OnStart: listen, OnStop: shutdown})

if err := m.Start(ctx); err != nil { // "db" is already stopped again if "http" failed
	return err
}
defer m.Stop(ctx) // stops "http", then "db"
```

- Hooks start in order and stop in reverse.
- If a hook fails to start, the hooks already started are stopped in reverse
  and the error is returned.
- `Stop` runs every hook even if some fail, and returns the combined error.
- Every transition is logged through `log/slog`.
- `Hooks`, `Index`, `Insert` and `Remove` let a caller edit a chain built by a
  library, addressing steps by name. That is how adopters add a step before
  `ota`'s swap or drop one they do not need.

Zero dependencies, Go 1.27+. Part of [ota](../README.md).
