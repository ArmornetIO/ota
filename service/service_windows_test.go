package service

import (
	"strings"
	"testing"
)

// A quote ends cmd.exe's quoting and %VAR% is expanded even inside quotes, so
// either in anything the log wrapper interpolates is command injection.
func TestTaskXMLRejectsCmdMetacharactersWithALogPath(t *testing.T) {
	for _, arg := range []string{`x" & calc & "`, `%COMSPEC%`} {
		s := validSpec()
		s.LogPath = `C:\logs\agent.log`
		s.Args = []string{"serve", arg}
		if _, err := taskXML(s.withDefaults()); err == nil {
			t.Errorf("argument %q was accepted with a log path", arg)
		}
	}
	s := validSpec()
	s.LogPath = `C:\logs\"x.log`
	if _, err := taskXML(s.withDefaults()); err == nil {
		t.Error("a log path containing a quote was accepted")
	}
}

func TestTaskXMLKeepsArgumentsWithSpacesWhole(t *testing.T) {
	s := validSpec()
	s.Args = []string{"serve", "-config", `C:\Users\ada lovelace\agent.yaml`}
	doc, err := taskXML(s.withDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, `serve -config &#34;C:\Users\ada lovelace\agent.yaml&#34;`) {
		t.Fatalf("argument with a space was not quoted:\n%s", doc)
	}
}

func TestSchtasksIsInvokedByAbsolutePath(t *testing.T) {
	t.Setenv("SystemRoot", `C:\Windows`)
	if got := schtasks("/query").Path; got != `C:\Windows\System32\schtasks.exe` {
		t.Fatalf("schtasks path = %q", got)
	}
}
