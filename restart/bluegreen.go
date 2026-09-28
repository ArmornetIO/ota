package restart

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ArmornetIO/ota/health"
	"github.com/ArmornetIO/ota/lifecycle"
	"github.com/ArmornetIO/ota/service"
)

// Step names specific to side-by-side installs.
const (
	StepStageVersion = "stage-version"
	StepStartNew     = "start-new"
	StepGate         = "gate"
	StepSwitch       = "switch"
)

// BlueGreenOptions configures a side-by-side service update.
//
// Each version lives in <Root>/versions/<version>/<Binary> and runs as its own
// service. The new one starts next to the old, and the old is removed only once
// the new one has passed its health gate, so there is no moment with nothing
// serving. That needs the program to share its listening port with the old
// instance during the overlap (SO_REUSEPORT or equivalent); ota does not work
// around a program that cannot.
type BlueGreenOptions struct {
	Root   string
	Binary string
	// Spec returns the service for one installed version.
	Spec func(version, binaryPath string) service.Spec
	// Probe returns the health source for one running version, usually a
	// health.ProbeURL on the port it serves.
	Probe func(version string) health.Source
	// CurrentVersion is the version being replaced.
	CurrentVersion string
	SteadyWindow   time.Duration // default 30s
	Deadline       time.Duration // default 5m
	Poll           time.Duration // default 1s
	Backend        service.Backend
}

// ServiceBlueGreen returns the side-by-side strategy. Configure the client
// with StateDir under Root, so the download lands on the same filesystem and
// every version shares one replay floor.
func ServiceBlueGreen(o BlueGreenOptions) Restarter {
	if o.Backend == nil {
		o.Backend = service.OS()
	}
	if o.SteadyWindow == 0 {
		o.SteadyWindow = 30 * time.Second
	}
	if o.Deadline == 0 {
		o.Deadline = 5 * time.Minute
	}
	return blueGreen{o: o}
}

type blueGreen struct{ o BlueGreenOptions }

// VersionDir is where one version of a side-by-side install lives.
func VersionDir(root, version string) string { return filepath.Join(root, "versions", version) }

func (bg blueGreen) Chain(p Plan) []lifecycle.Hook {
	o := bg.o
	dir := VersionDir(o.Root, p.Version)
	bin := filepath.Join(dir, o.Binary)
	newSpec := o.Spec(p.Version, bin)
	oldSpec := o.Spec(o.CurrentVersion, filepath.Join(VersionDir(o.Root, o.CurrentVersion), o.Binary))

	return []lifecycle.Hook{
		{
			Name: StepStageVersion,
			OnStart: func(context.Context) error {
				if p.Staged == "" {
					return errors.New("restart: side-by-side install needs the staged download")
				}
				if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- versioned install dir; the binary in it must be world-executable
					return err
				}
				if err := os.Rename(p.Staged, bin); err != nil {
					return fmt.Errorf("restart: place version %s: %w", p.Version, err)
				}
				return os.Chmod(bin, 0o755) // #nosec G302 -- it is the program binary; 0755 is what an executable is
			},
			OnStop: func(context.Context) error { return os.RemoveAll(dir) },
		},
		{
			Name:    StepStartNew,
			OnStart: func(context.Context) error { return o.Backend.Install(newSpec) },
			OnStop:  func(context.Context) error { return o.Backend.Uninstall(newSpec) },
		},
		{
			Name: StepGate,
			OnStart: func(ctx context.Context) error {
				g := health.Gate{
					Source:       o.Probe(p.Version),
					SteadyWindow: o.SteadyWindow,
					Deadline:     time.Now().Add(o.Deadline),
					Poll:         o.Poll,
				}
				if out := g.Run(ctx); !out.Commit {
					return fmt.Errorf("restart: version %s failed its health gate: %s", p.Version, out.Reason)
				}
				return nil
			},
		},
		{
			Name: StepSwitch,
			OnStart: func(context.Context) error {
				if err := setCurrent(o.Root, p.Version); err != nil {
					return err
				}
				// The new version is serving and current; a failure to remove
				// the old service is reported but does not undo the switch.
				uninstallErr := o.Backend.Uninstall(oldSpec)
				if p.Commit != nil {
					if err := p.Commit(); err != nil {
						return err
					}
				}
				pruneVersions(o.Root, p.Version, o.CurrentVersion)
				return uninstallErr
			},
		},
	}
}

// Relaunch is unused: the old version is never stopped until the new one has
// already proved itself.
func (blueGreen) Relaunch(string) error { return nil }

// pruneVersions removes version directories other than the live one and the
// one it replaced, which is kept until the next successful switch.
func pruneVersions(root string, keep ...string) {
	entries, err := os.ReadDir(filepath.Join(root, "versions"))
	if err != nil {
		return
	}
	for _, e := range entries {
		skip := false
		for _, k := range keep {
			skip = skip || e.Name() == k
		}
		if !skip {
			_ = os.RemoveAll(filepath.Join(root, "versions", e.Name()))
		}
	}
}

// Current returns the version a side-by-side install points at.
func Current(root string) (string, error) { return readCurrent(root) }
