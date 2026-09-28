//go:build otatest

// Package testkit holds hooks the fault-injection suite needs inside production
// code paths. They compile to nothing without the otatest build tag, so no
// shipped binary can be made to exit by an environment variable.
package testkit

import "os"

// KillAt exits the process abruptly, without running defers, when
// OTA_TEST_KILL_AT names this step. It stands in for a power cut or kill -9.
func KillAt(step string) {
	if os.Getenv("OTA_TEST_KILL_AT") == step {
		os.Exit(86)
	}
}
