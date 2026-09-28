// Package service registers a program with the host's init system so it starts
// again after a reboot and is restarted when it exits, and starts, stops and
// restarts it there.
//
// A detached child process is correct for the session it was started in and
// gone the next time the machine comes up, and nothing reports that. Callers
// that need a program to stay running register it here instead, and fall back
// to a detached process only on ErrUnsupported.
//
// Three implementations, one per init system: systemd on Linux, launchd on
// macOS, the task scheduler on Windows. Each shells out to the platform's own
// tool rather than linking a library: these are the commands an administrator
// would run by hand, so a failure is diagnosable by running the printed
// command, and no dependency ships for a code path that only executes on one
// GOOS.
//
// # Scope
//
// The default is a USER service, and that is load-bearing rather than timid.
// An installer that runs as whoever launched it and asks for no privileges
// would break that promise at the last step if registering the service
// suddenly needed root. ScopeSystem exists for a host genuinely being
// provisioned as a server, and the caller has to ask for it.
package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrUnsupported reports that this GOOS has no implementation. Callers treat it
// as "leave the process detached" rather than as a failed install — a program
// running without boot persistence is worse than one with it, and much better
// than none.
var ErrUnsupported = errors.New("service: no service manager for this platform")

// ErrNotInstalled reports that no service by this name is registered.
var ErrNotInstalled = errors.New("service: service is not installed")

// Scope selects which init-system domain the service is registered in.
type Scope string

const (
	// ScopeUser registers the service for the invoking user and starts it at
	// login: `systemctl --user`, ~/Library/LaunchAgents, a task that runs ONLOGON.
	ScopeUser Scope = "user"
	// ScopeSystem registers a machine-wide daemon that starts at boot with no
	// user logged in. Requires root/Administrator.
	ScopeSystem Scope = "system"
)

// Spec describes the daemon to register. The same value identifies a service
// for Install, Uninstall and Query, so a caller that can install can always
// name what it installed.
type Spec struct {
	// Name is the service identifier. It appears in a systemd unit filename, a
	// launchd label and a scheduled task name, so it is constrained to what all
	// three accept: see Validate.
	Name string
	// Description is the human-readable label shown by the service manager.
	// Empty means Name.
	Description string
	// LabelPrefix is prepended to Name to form the launchd label, which is
	// reverse-DNS by convention (e.g. "io.example."). Empty means the label is
	// Name alone. Ignored by the other service managers.
	LabelPrefix string
	// BinaryPath is the absolute path to the executable. Absolute
	// because an init system starts the service with a working directory and an
	// environment that are not the installer's — a relative path resolves
	// against something the caller never saw.
	BinaryPath string
	// Args are passed to BinaryPath, e.g. {"serve", "-config", path}.
	Args []string
	// WorkingDir is the service's working directory. Empty means the platform
	// default.
	WorkingDir string
	// LogPath receives the service's stdout and stderr. Empty means the
	// platform's own journal.
	LogPath string
	// Scope selects user or system registration. Empty means ScopeUser.
	Scope Scope
}

// Status reports what the service manager currently knows about the service.
type Status struct {
	// Installed is whether a unit/plist/task by this name is registered.
	Installed bool
	// Running is whether the service manager currently has it up. A service can
	// be installed and not running — that is a crash loop, and reporting it as
	// simply "installed" is how the old detached-process check lied.
	Running bool
	// Detail carries the service manager's own words, for surfacing a failure
	// the caller cannot interpret.
	Detail string
}

// withDefaults fills the optional fields so each platform implementation reads
// one settled value rather than repeating the same fallbacks.
func (s Spec) withDefaults() Spec {
	if s.Description == "" {
		s.Description = s.Name
	}
	if s.Scope == "" {
		s.Scope = ScopeUser
	}
	return s
}

// Validate rejects a Spec before any of it reaches exec or a generated unit
// file.
//
// Name is the field that matters: it is interpolated into a filename, into a
// systemd unit, into an XML plist and into a schtasks argument. A name carrying
// a path separator escapes the directory the unit was meant to land in, and one
// carrying a quote or an angle bracket breaks out of the plist it is written
// into. Constraining it to one conservative alphabet is cheaper than four
// correct context-specific escapes, and does not cost anything real — these are
// service names.
func (s Spec) Validate() error {
	s = s.withDefaults()

	if s.Name == "" {
		return errors.New("service: service name is empty")
	}
	if len(s.Name) > 64 {
		return fmt.Errorf("service: service name %q is longer than 64 characters", s.Name)
	}
	for _, r := range s.Name {
		ok := r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("service: service name %q contains %q; allowed are letters, digits, dot, dash and underscore", s.Name, r)
		}
	}
	// Rejected separately from the alphabet so the message names the real
	// problem: "." and ".." pass the character check and are still not names.
	if s.Name == "." || s.Name == ".." {
		return fmt.Errorf("service: %q is not a service name", s.Name)
	}
	// Same alphabet as Name for the same reason: it lands in the plist filename
	// and the label launchctl is addressed by.
	for _, r := range s.LabelPrefix {
		ok := r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("service: label prefix %q contains %q; allowed are letters, digits, dot, dash and underscore", s.LabelPrefix, r)
		}
	}
	if len(s.LabelPrefix)+len(s.Name) > 128 {
		return fmt.Errorf("service: label %q is longer than 128 characters", s.LabelPrefix+s.Name)
	}

	if s.BinaryPath == "" {
		return errors.New("service: binary path is empty")
	}
	if !filepath.IsAbs(s.BinaryPath) {
		return fmt.Errorf("service: binary path %q is not absolute; an init system does not share the installer's working directory", s.BinaryPath)
	}
	if s.LogPath != "" && !filepath.IsAbs(s.LogPath) {
		return fmt.Errorf("service: log path %q is not absolute", s.LogPath)
	}
	if s.WorkingDir != "" && !filepath.IsAbs(s.WorkingDir) {
		return fmt.Errorf("service: working directory %q is not absolute", s.WorkingDir)
	}

	switch s.Scope {
	case ScopeUser, ScopeSystem:
	default:
		return fmt.Errorf("service: unknown scope %q", s.Scope)
	}

	// Newlines are the one thing that survives the checks above (they are not in
	// Name, but Args and the paths are free-form) and still rewrites a generated
	// unit file, where a line break starts a new directive.
	for _, a := range s.Args {
		if strings.ContainsAny(a, "\n\r") {
			return fmt.Errorf("service: argument %q contains a line break", a)
		}
	}
	if strings.ContainsAny(s.BinaryPath+s.LogPath+s.WorkingDir, "\n\r") {
		return errors.New("service: a path contains a line break")
	}
	// Description is written verbatim into the systemd unit, where a line break
	// would start a new directive such as ExecStartPost.
	if strings.ContainsAny(s.Description, "\n\r") {
		return errors.New("service: description contains a line break")
	}

	return nil
}
