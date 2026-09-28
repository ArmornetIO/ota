package service

import (
	"strings"
	"testing"
)

func TestUnitFileQuotesExecStartAndKeepsRestartAlways(t *testing.T) {
	s := validSpec()
	s.BinaryPath = "/opt/my app/bin/demo"
	s = s.withDefaults()

	unit := unitFile(s)
	if !strings.Contains(unit, `ExecStart="/opt/my app/bin/demo" "agent" "serve"`) {
		t.Fatalf("ExecStart not quoted:\n%s", unit)
	}
	if !strings.Contains(unit, "Restart=always") {
		t.Fatalf("unit lost Restart=always:\n%s", unit)
	}
}

// systemd expands %-specifiers, $VARIABLES and C escapes inside ExecStart
// quotes; each must reach the program literally.
func TestShJoinEscapesSystemdExpansion(t *testing.T) {
	got := shJoin([]string{`/bin/demo`, `--token=%h$HOME\n"x"`})
	want := `"/bin/demo" "--token=%%h$$HOME\\n\"x\""`
	if got != want {
		t.Fatalf("shJoin = %s\nwant    %s", got, want)
	}
}
