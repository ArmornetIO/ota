# ota/service

Register a program with the host's service manager so it comes back after a
reboot and is restarted when it exits. Then start, stop, restart and query it.

| Platform | Backend | User scope | System scope |
|---|---|---|---|
| Linux | systemd | `systemctl --user` | `/etc/systemd/system` (root) |
| macOS | launchd | `~/Library/LaunchAgents` | `/Library/LaunchDaemons` (root) |
| Windows | Task Scheduler | logon trigger | boot trigger as SYSTEM (Administrator) |

```go
import "github.com/ArmornetIO/ota/service"

spec := service.Spec{
	Name:        "myapp",
	LabelPrefix: "com.example.", // launchd label becomes com.example.myapp
	BinaryPath:  "/opt/myapp/bin/myapp",
	Args:        []string{"serve", "-config", "/etc/myapp.yaml"},
}
if err := service.Install(spec); errors.Is(err, service.ErrUnsupported) {
	// No service manager here: fall back to running detached.
}
st, _ := service.Query(spec) // Installed, Running, Detail (the manager's own words)
_ = service.Restart(spec)    // runs whatever file is at BinaryPath now
```

- **User scope is the default.** Registering needs no privileges unless you ask
  for `ScopeSystem`.
- **It shells out to the platform's own tool.** A failure can be reproduced by
  running the same command by hand, and no library ships for code that only
  runs on one OS. `launchctl` and `schtasks` are invoked by absolute path.
- **`Spec` is validated before anything reaches `exec` or a generated file.**
  Names are limited to a conservative alphabet, paths must be absolute, and
  line breaks are rejected anywhere. systemd `%`, `$` and `\` expansion is
  escaped, and cmd.exe metacharacters are refused when a log path is set.
- **Windows uses Task Scheduler, not the Service Control Manager.** An SCM
  service must answer the SCM's control messages, and an ordinary console
  program registered with `sc` fails to start.

Zero dependencies, Go 1.24+. Part of [ota](../README.md).
