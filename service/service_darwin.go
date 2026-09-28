package service

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// launchd on macOS. A LaunchAgent in ~/Library/LaunchAgents by default, which
// runs as the user at login; a LaunchDaemon in /Library/LaunchDaemons for
// ScopeSystem, which runs as root at boot.

// plistDir is where the job definition for this scope belongs.
func plistDir(scope Scope) (string, error) {
	if scope == ScopeSystem {
		return "/Library/LaunchDaemons", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("service: resolve home: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents"), nil
}

// launchctl is invoked by absolute path: it always lives here, and resolving it
// through PATH would let whoever controls the caller's environment substitute
// the program that registers a persistent job.
const launchctlPath = "/bin/launchctl"

func launchctl(args ...string) *exec.Cmd { return exec.Command(launchctlPath, args...) } // #nosec G204 -- absolute path constant; args are the service spec, not user input

// label is launchd's identifier for the job. launchctl addresses the job by
// this string, not by the filename.
func label(s Spec) string { return s.LabelPrefix + s.Name }

// target is the job as launchctl's modern subcommands address it.
func target(s Spec) string { return domain(s.Scope) + "/" + label(s) }

// plistPath is the job definition's location on disk.
func plistPath(s Spec) (string, error) {
	dir, err := plistDir(s.Scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, label(s)+".plist"), nil
}

// xmlText escapes a value for a plist <string> body.
//
// Escaped properly rather than by rejecting the offending characters: unlike a
// service name, these are real filesystem paths, and a user whose home
// directory contains an ampersand is entitled to install the agent. Spec
// validation already removed line breaks; this handles the rest.
func xmlText(v string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(v)); err != nil {
		// EscapeText only fails if the writer fails, and strings.Builder does not.
		return ""
	}
	return b.String()
}

// plistFile renders the launchd job definition.
//
// KeepAlive rather than RunAtLoad alone: RunAtLoad starts it once, KeepAlive is
// what brings it back when it exits. Both are set because they answer different
// questions — start now, and start again.
func plistFile(s Spec) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")

	fmt.Fprintf(&b, "  <key>Label</key>\n  <string>%s</string>\n", xmlText(label(s)))

	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, a := range append([]string{s.BinaryPath}, s.Args...) {
		fmt.Fprintf(&b, "    <string>%s</string>\n", xmlText(a))
	}
	b.WriteString("  </array>\n")

	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	b.WriteString("  <key>KeepAlive</key>\n  <true/>\n")

	if s.WorkingDir != "" {
		fmt.Fprintf(&b, "  <key>WorkingDirectory</key>\n  <string>%s</string>\n", xmlText(s.WorkingDir))
	}
	if s.LogPath != "" {
		fmt.Fprintf(&b, "  <key>StandardOutPath</key>\n  <string>%s</string>\n", xmlText(s.LogPath))
		fmt.Fprintf(&b, "  <key>StandardErrorPath</key>\n  <string>%s</string>\n", xmlText(s.LogPath))
	}

	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// domain is launchctl's target for this scope: gui/<uid> for a LaunchAgent,
// system for a LaunchDaemon. The modern launchctl subcommands are all addressed
// by domain, and using the wrong one reports success against a job nobody asked
// about.
func domain(scope Scope) string {
	if scope == ScopeSystem {
		return "system"
	}
	return "gui/" + strconv.Itoa(os.Getuid())
}

// Install writes the plist and bootstraps the job into launchd. Idempotent: an
// already-loaded job is booted out first, because bootstrap on a loaded label
// fails rather than replacing it.
func Install(s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	s = s.withDefaults()

	path, err := plistPath(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- LaunchAgents dir; launchd must traverse it
		return fmt.Errorf("service: create %s: %w", filepath.Dir(path), err)
	}
	// 0644 — launchd refuses to load a plist it considers improperly permissive,
	// and needs to read it as its own user for a daemon.
	if err := os.WriteFile(path, []byte(plistFile(s)), 0o644); err != nil { // #nosec G306 -- see above
		return fmt.Errorf("service: write %s: %w", path, err)
	}

	// Ignored: it fails when the job was not loaded, which is the normal
	// first-install case.
	_ = launchctl("bootout", target(s)).Run()

	out, err := launchctl("bootstrap", domain(s.Scope), path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("service: launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall boots the job out of launchd and removes the plist. Absent is not
// an error.
func Uninstall(s Spec) error {
	s = s.withDefaults()

	path, err := plistPath(s)
	if err != nil {
		return err
	}
	_ = launchctl("bootout", target(s)).Run()

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("service: remove %s: %w", path, err)
	}
	return nil
}

// Query reports whether the job is registered and up.
func Query(s Spec) (Status, error) {
	s = s.withDefaults()

	path, err := plistPath(s)
	if err != nil {
		return Status{}, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return Status{}, ErrNotInstalled
	}

	// `launchctl print` exits non-zero when the label is not loaded — that is
	// installed-but-not-running, not an error, because the plist is on disk.
	out, err := launchctl("print", target(s)).CombinedOutput()
	if err != nil {
		return Status{Installed: true, Running: false, Detail: "not loaded"}, nil
	}

	// A loaded job with no PID is one launchd knows about but is not currently
	// running — between KeepAlive restarts, or crash-looping.
	running := strings.Contains(string(out), "pid = ")
	detail := "loaded"
	if !running {
		detail = "loaded, not running"
	}
	return Status{Installed: true, Running: running, Detail: detail}, nil
}

// Start runs a loaded job now, bootstrapping it first if it was booted out.
// kickstart alone fails on an unloaded label, and Stop leaves it unloaded.
func Start(s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	s = s.withDefaults()
	if launchctl("print", target(s)).Run() != nil {
		path, err := plistPath(s)
		if err != nil {
			return err
		}
		if out, err := launchctl("bootstrap", domain(s.Scope), path).CombinedOutput(); err != nil {
			return fmt.Errorf("service: launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if out, err := launchctl("kickstart", target(s)).CombinedOutput(); err != nil {
		return fmt.Errorf("service: launchctl kickstart: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Stop boots the job out. Killing it is not enough: KeepAlive would restart
// it straight away. The plist stays on disk, so it still loads at next login.
func Stop(s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	s = s.withDefaults()
	if out, err := launchctl("bootout", target(s)).CombinedOutput(); err != nil {
		return fmt.Errorf("service: launchctl bootout: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Restart kills the running instance and starts it again in one step (-k),
// executing whatever file is at BinaryPath now.
func Restart(s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	s = s.withDefaults()
	if out, err := launchctl("kickstart", "-k", target(s)).CombinedOutput(); err != nil {
		return fmt.Errorf("service: launchctl kickstart -k: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
