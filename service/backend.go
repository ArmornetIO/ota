package service

// Backend is the set of service-manager operations, as an interface so code
// that drives a service (ota's restart strategies) can be tested without a
// real service manager.
type Backend interface {
	Install(Spec) error
	Uninstall(Spec) error
	Query(Spec) (Status, error)
	Start(Spec) error
	Stop(Spec) error
	Restart(Spec) error
}

// OS returns the Backend for this machine's service manager.
func OS() Backend { return osBackend{} }

type osBackend struct{}

func (osBackend) Install(s Spec) error         { return Install(s) }
func (osBackend) Uninstall(s Spec) error       { return Uninstall(s) }
func (osBackend) Query(s Spec) (Status, error) { return Query(s) }
func (osBackend) Start(s Spec) error           { return Start(s) }
func (osBackend) Stop(s Spec) error            { return Stop(s) }
func (osBackend) Restart(s Spec) error         { return Restart(s) }
