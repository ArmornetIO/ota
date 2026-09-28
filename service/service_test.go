package service

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// validSpec is the shape every test starts from, so a case shows only the field
// it is actually about.
func validSpec() Spec {
	bin := "/usr/local/bin/armornet"
	if runtime.GOOS == "windows" {
		bin = `C:\Program Files\armornet\armornet.exe`
	}
	return Spec{
		Name:       "armornet-agent",
		BinaryPath: bin,
		Args:       []string{"agent", "serve"},
	}
}

func TestValidateAcceptsADefaultSpec(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("a default spec was rejected: %v", err)
	}
}

// The name reaches a unit filename, a plist path, and a schtasks argument. A
// separator in it escapes the directory the definition was meant to land in.
func TestValidateRejectsNameEscapingItsDirectory(t *testing.T) {
	for _, name := range []string{
		"../../etc/systemd/system/sshd",
		"armornet/agent",
		`armornet\agent`,
		"..",
		".",
	} {
		s := validSpec()
		s.Name = name
		if err := s.Validate(); err == nil {
			t.Errorf("service name %q was accepted", name)
		}
	}
}

// A quote or an angle bracket in the name would break out of the plist and the
// task XML it is written into.
func TestValidateRejectsNameBreakingAGeneratedDefinition(t *testing.T) {
	for _, name := range []string{
		`armornet"/><key>RunAtLoad</key><false`,
		"armornet<script>",
		"armornet agent",
		"armornet;reboot",
		"armornet$(id)",
		"armornet\nExecStart=/bin/sh",
	} {
		s := validSpec()
		s.Name = name
		if err := s.Validate(); err == nil {
			t.Errorf("service name %q was accepted", name)
		}
	}
}

// An init system starts the service with a working directory that is not the
// installer's, so a relative program path resolves against something the caller
// never saw — and usually silently fails to start.
func TestValidateRequiresAnAbsoluteBinaryPath(t *testing.T) {
	s := validSpec()
	s.BinaryPath = "armornet"
	if err := s.Validate(); err == nil {
		t.Fatal("a relative binary path was accepted")
	}

	s = validSpec()
	s.BinaryPath = ""
	if err := s.Validate(); err == nil {
		t.Fatal("an empty binary path was accepted")
	}
}

func TestValidateRequiresAbsoluteOptionalPaths(t *testing.T) {
	s := validSpec()
	s.LogPath = "agent.log"
	if err := s.Validate(); err == nil {
		t.Error("a relative log path was accepted")
	}

	s = validSpec()
	s.WorkingDir = "state"
	if err := s.Validate(); err == nil {
		t.Error("a relative working directory was accepted")
	}
}

// A unit file is line-oriented: a break in an argument starts a new directive.
func TestValidateRejectsLineBreaksInArgs(t *testing.T) {
	s := validSpec()
	s.Args = []string{"agent", "serve\nExecStartPost=/bin/sh -c id"}
	if err := s.Validate(); err == nil {
		t.Fatal("an argument containing a line break was accepted")
	}
}

func TestValidateRejectsUnknownScope(t *testing.T) {
	s := validSpec()
	s.Scope = "root"
	if err := s.Validate(); err == nil {
		t.Fatal("an unknown scope was accepted")
	}
}

func TestWithDefaultsFillsDescriptionAndScope(t *testing.T) {
	got := Spec{Name: "demo", BinaryPath: "/usr/local/bin/demo"}.withDefaults()
	if got.Scope != ScopeUser {
		t.Errorf("Scope = %q, want %q (an unprivileged default is deliberate)", got.Scope, ScopeUser)
	}
	if got.Description != "demo" {
		t.Errorf("Description = %q, want the name", got.Description)
	}
}

// There is no default name: a library cannot know what the caller's program is
// called, and guessing registers someone else's service.
func TestValidateRequiresAName(t *testing.T) {
	s := validSpec()
	s.Name = ""
	if err := s.Validate(); err == nil {
		t.Fatal("an empty name was accepted")
	}
}

func TestValidateRejectsLineBreakInDescription(t *testing.T) {
	s := validSpec()
	s.Description = "demo\nExecStartPost=/bin/sh -c id"
	if err := s.Validate(); err == nil {
		t.Fatal("a description with a line break was accepted")
	}
}

func TestValidateLabelPrefix(t *testing.T) {
	s := validSpec()
	s.LabelPrefix = "io.armornet."
	if err := s.Validate(); err != nil {
		t.Fatalf("a reverse-DNS prefix was rejected: %v", err)
	}
	for _, p := range []string{"io.armornet/", `io"x`, "io <x>", "io\n"} {
		s.LabelPrefix = p
		if err := s.Validate(); err == nil {
			t.Errorf("label prefix %q was accepted", p)
		}
	}
}

// An empty Spec must not validate — the zero value is not a service.
func TestValidateRejectsZeroSpec(t *testing.T) {
	if err := (Spec{}).Validate(); err == nil {
		t.Fatal("the zero Spec was accepted")
	}
}

func TestValidateRejectsAnOverlongName(t *testing.T) {
	s := validSpec()
	s.Name = strings.Repeat("a", 65)
	if err := s.Validate(); err == nil {
		t.Fatal("a 65-character name was accepted")
	}
}

// A home directory with a space in it is ordinary on macOS and Windows, and
// must not be mistaken for an injection.
func TestValidateAcceptsOrdinaryAwkwardPaths(t *testing.T) {
	s := validSpec()
	s.BinaryPath = filepath.Join(string(filepath.Separator), "Users", "ada lovelace", "bin", "armornet")
	s.LogPath = filepath.Join(string(filepath.Separator), "Users", "ada & co", "agent.log")
	if err := s.Validate(); err != nil {
		t.Fatalf("an ordinary path with a space and an ampersand was rejected: %v", err)
	}
}
