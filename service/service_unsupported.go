//go:build !linux && !darwin && !windows

package service

// Everywhere without an init system we generate for. The package still builds,
// so a caller does not need its own build tags to compile — it handles
// ErrUnsupported at runtime and leaves the program as a detached process.

// Install reports ErrUnsupported.
func Install(s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	return ErrUnsupported
}

// Uninstall reports ErrUnsupported.
func Uninstall(s Spec) error { return ErrUnsupported }

// Query reports ErrUnsupported.
func Query(s Spec) (Status, error) { return Status{}, ErrUnsupported }

// Start reports ErrUnsupported.
func Start(s Spec) error { return ErrUnsupported }

// Stop reports ErrUnsupported.
func Stop(s Spec) error { return ErrUnsupported }

// Restart reports ErrUnsupported.
func Restart(s Spec) error { return ErrUnsupported }
