//go:build !otatest

package testkit

// KillAt is a no-op outside otatest builds.
func KillAt(string) {}
