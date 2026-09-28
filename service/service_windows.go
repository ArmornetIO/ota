package service

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

// The Windows task scheduler, NOT the service control manager.
//
// `sc create` is the obvious choice and it does not work here. A real Windows
// service has to connect back to the SCM and answer control messages within
// about 30 seconds of starting; a process that does not is killed with "the
// service did not respond to the start or control request in a timely
// fashion". A plain console program registered with `sc` is a service that
// visibly fails to start — the worst of the options, because it looks
// installed.
//
// A scheduled task with a logon/boot trigger runs an ordinary executable, needs
// no cooperation from the binary, and restarts it on failure. An SCM backend
// would need golang.org/x/sys/windows/svc and a binary that answers the SCM's
// control messages; until a caller needs that, this file is the one place that
// would change.

// schtasks is invoked by absolute path under the system root rather than
// through PATH, so a caller's environment cannot substitute the program that
// registers a persistent task.
func schtasks(args ...string) *exec.Cmd {
	root := os.Getenv("SystemRoot")
	if root == "" || !filepath.IsAbs(root) {
		root = `C:\Windows`
	}
	return exec.Command(filepath.Join(root, "System32", "schtasks.exe"), args...)
}

// cmdUnsafe are the characters cmd.exe still interprets inside a double-quoted
// string: a quote ends the quoting, and %VAR% is expanded. Either lets an
// argument or path rewrite the command line the log wrapper builds.
const cmdUnsafe = `"%`

// taskName is the scheduler's identifier. The leading backslash puts it in the
// root folder rather than wherever the invoking process happens to be rooted.
func taskName(name string) string { return `\` + name }

// taskXML renders a Task Scheduler 1.2 definition.
//
// Generated as a file and handed to `schtasks /create /xml` rather than built
// from command-line flags, because the two settings that make this a daemon
// have no flag: ExecutionTimeLimit (a task is killed after 72 hours by default)
// and RestartOnFailure. Those are the systemd Restart=always and the launchd
// KeepAlive of this implementation.
func taskXML(s Spec) (string, error) {
	command := s.BinaryPath
	// EscapeArg applies the CommandLineToArgvW rules the program's runtime uses
	// to split its command line, so an argument with a space stays one argument.
	quotedArgs := make([]string, len(s.Args))
	for i, a := range s.Args {
		quotedArgs[i] = syscall.EscapeArg(a)
	}
	argLine := strings.Join(quotedArgs, " ")

	// The scheduler has no stdout redirection, so logging means going through
	// cmd.exe. Only when a log is actually wanted — the extra process is a real
	// cost (it is what the scheduler then watches for restarts).
	if s.LogPath != "" {
		parts := append([]string{s.BinaryPath, s.LogPath}, s.Args...)
		for _, p := range parts {
			if strings.ContainsAny(p, cmdUnsafe) {
				return "", fmt.Errorf("service: %q contains %q, which cmd.exe interprets; not allowed with a log path", p, cmdUnsafe)
			}
		}
		quoted := make([]string, 0, len(s.Args)+1)
		for _, a := range append([]string{s.BinaryPath}, s.Args...) {
			quoted = append(quoted, `"`+a+`"`)
		}
		command = "cmd.exe"
		argLine = `/c "` + strings.Join(quoted, " ") + ` >> "` + s.LogPath + `" 2>&1"`
	}

	esc := func(v string) (string, error) {
		var b strings.Builder
		if err := xml.EscapeText(&b, []byte(v)); err != nil {
			return "", err
		}
		return b.String(), nil
	}

	desc, err := esc(s.Description)
	if err != nil {
		return "", err
	}
	cmdText, err := esc(command)
	if err != nil {
		return "", err
	}
	argText, err := esc(argLine)
	if err != nil {
		return "", err
	}
	workDir := ""
	if s.WorkingDir != "" {
		wd, err := esc(s.WorkingDir)
		if err != nil {
			return "", err
		}
		workDir = fmt.Sprintf("      <WorkingDirectory>%s</WorkingDirectory>\n", wd)
	}

	// A system task starts at boot with no session; a user task starts at logon
	// with the user's own token and privileges.
	trigger := "<LogonTrigger><Enabled>true</Enabled></LogonTrigger>"
	principal := "<LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel>"
	if s.Scope == ScopeSystem {
		trigger = "<BootTrigger><Enabled>true</Enabled></BootTrigger>"
		principal = "<UserId>S-1-5-18</UserId><RunLevel>HighestAvailable</RunLevel>"
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>%s</Description>
  </RegistrationInfo>
  <Triggers>%s</Triggers>
  <Principals>
    <Principal id="Author">%s</Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
%s    </Exec>
  </Actions>
</Task>
`, desc, trigger, principal, cmdText, argText, workDir), nil
}

// utf16LE encodes the definition as UTF-16 little-endian with a byte-order
// mark.
//
// Not cosmetic: schtasks rejects a UTF-8 task definition with "The task XML
// contains a value which is incorrectly formatted or is out of range", an error
// that says nothing about encoding and sends you looking through the XML for a
// bad field.
func utf16LE(s string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFE}) // BOM
	for _, v := range utf16.Encode([]rune(s)) {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	return b.Bytes()
}

// Install registers the task and starts it. Idempotent — /f overwrites a task
// of the same name.
func Install(s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	s = s.withDefaults()

	doc, err := taskXML(s)
	if err != nil {
		return fmt.Errorf("service: render task definition: %w", err)
	}

	// schtasks reads the definition from a path, so it has to exist as a file.
	// In the temp dir and removed straight after: it carries no secret (the
	// agent's credentials are in the config it points at) but it is not
	// something to leave lying around either.
	f, err := os.CreateTemp("", s.Name+"-*.xml")
	if err != nil {
		return fmt.Errorf("service: create temp task definition: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(utf16LE(doc)); err != nil {
		f.Close()
		return fmt.Errorf("service: write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("service: close %s: %w", tmp, err)
	}

	out, err := schtasks("/create", "/tn", taskName(s.Name), "/xml", filepath.Clean(tmp), "/f").CombinedOutput()
	if err != nil {
		return fmt.Errorf("service: schtasks /create: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// The trigger is logon or boot, so nothing starts it in the session that
	// installed it. Without this the user is told it is installed and sees no
	// agent until the next reboot.
	if out, err := schtasks("/run", "/tn", taskName(s.Name)).CombinedOutput(); err != nil {
		return fmt.Errorf("service: schtasks /run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall ends the task and deletes it. Absent is not an error.
func Uninstall(s Spec) error {
	s = s.withDefaults()

	_ = schtasks("/end", "/tn", taskName(s.Name)).Run()

	out, err := schtasks("/delete", "/tn", taskName(s.Name), "/f").CombinedOutput()
	if err != nil {
		// The only failure worth reporting is one that is not "no such task".
		if strings.Contains(string(out), "cannot find the file specified") ||
			strings.Contains(string(out), "does not exist") {
			return nil
		}
		return fmt.Errorf("service: schtasks /delete: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Query reports whether the task is registered and currently executing.
func Query(s Spec) (Status, error) {
	s = s.withDefaults()

	out, err := schtasks("/query", "/tn", taskName(s.Name), "/fo", "list").CombinedOutput()
	if err != nil {
		return Status{}, ErrNotInstalled
	}

	// "Running" is the scheduler's own word for a task whose action is currently
	// executing; "Ready" means registered and waiting for its trigger.
	text := string(out)
	running := strings.Contains(text, "Running")
	detail := "ready"
	if running {
		detail = "running"
	}
	return Status{Installed: true, Running: running, Detail: detail}, nil
}

// Start runs the task now rather than waiting for its logon or boot trigger.
func Start(s Spec) error { return taskVerb(s, "/run") }

// Stop ends the running action. The task stays registered, so its trigger
// still fires at the next logon or boot.
func Stop(s Spec) error { return taskVerb(s, "/end") }

// Restart ends and re-runs the task. There is no single scheduler verb for it,
// and /run on a task that is still running is ignored under IgnoreNew.
func Restart(s Spec) error {
	if err := Stop(s); err != nil {
		return err
	}
	return Start(s)
}

func taskVerb(s Spec, verb string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	s = s.withDefaults()
	if out, err := schtasks(verb, "/tn", taskName(s.Name)).CombinedOutput(); err != nil {
		return fmt.Errorf("service: schtasks %s: %w: %s", verb, err, strings.TrimSpace(string(out)))
	}
	return nil
}
