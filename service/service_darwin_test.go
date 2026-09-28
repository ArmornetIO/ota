package service

import (
	"strings"
	"testing"
)

func TestPlistCarriesTheCallersLabel(t *testing.T) {
	s := validSpec()
	s.LabelPrefix = "io.armornet."
	s = s.withDefaults()

	if got := label(s); got != "io.armornet.armornet-agent" {
		t.Fatalf("label = %q", got)
	}
	doc := plistFile(s)
	if !strings.Contains(doc, "<string>io.armornet.armornet-agent</string>") {
		t.Fatalf("plist does not carry the label:\n%s", doc)
	}
	path, err := plistPath(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "/io.armornet.armornet-agent.plist") {
		t.Fatalf("plist path = %q", path)
	}
}

func TestLabelWithoutPrefixIsTheName(t *testing.T) {
	if got := label(validSpec()); got != "armornet-agent" {
		t.Fatalf("label = %q", got)
	}
}
