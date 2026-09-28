package ota

import "testing"

func TestVersionCompare(t *testing.T) {
	// Ascending order, taken from the semver 2.0 precedence example.
	order := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0",
	}
	for i := range order {
		for j := range order {
			a, b := MustParseVersion(order[i]), MustParseVersion(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.Compare(b); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestVersionBuildMetadataIgnored(t *testing.T) {
	if MustParseVersion("1.2.3+a").Compare(MustParseVersion("v1.2.3+b")) != 0 {
		t.Fatal("build metadata affected precedence")
	}
}

func TestParseVersionRejects(t *testing.T) {
	for _, s := range []string{"", "1", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "1.2.3+", "1.2.3-01", "1.2.x", "1.2.3-a..b", "1.2.3-a_b"} {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("ParseVersion(%q) accepted", s)
		}
	}
}

func TestVersionString(t *testing.T) {
	if got := MustParseVersion("v1.9.0-rc.1+sha").String(); got != "1.9.0-rc.1+sha" {
		t.Fatalf("String() = %q", got)
	}
}
