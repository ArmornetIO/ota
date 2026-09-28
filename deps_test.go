package ota_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The core module, and the lifecycle and service modules it requires, may use
// only the standard library and each other. Written as an allowlist, not a
// denylist: any third-party import anywhere in the graph fails, so the
// promise that adopters take on no dependencies cannot erode one
// helper at a time. Sigstore and OpenTelemetry live in their own modules for
// exactly this reason.
func TestCoreDependsOnlyOnTheStandardLibrary(t *testing.T) {
	for _, dir := range []string{".", "lifecycle", "service"} {
		cmd := exec.Command("go", "list", "-deps", "-test", "./...")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list in %s: %v\n%s", dir, err, out)
		}
		for dep := range strings.FieldsSeq(string(out)) {
			// -test lists variants as "pkg [pkg.test]"; the bracketed half names
			// the same package.
			dep = strings.Trim(dep, "[]")
			first, _, _ := strings.Cut(dep, "/")
			stdlib := !strings.Contains(first, ".")
			if stdlib || strings.HasPrefix(dep, "github.com/ArmornetIO/ota") {
				continue
			}
			t.Errorf("%s depends on %s; the core modules take no third-party dependencies", dir, dep)
		}
	}
}
