package client

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/internal/stage"
	"github.com/ArmornetIO/ota/internal/swap"
)

// AnyPlatform is the os and arch of an artifact that is the same everywhere:
// data, such as a model bundle, rather than a binary.
const AnyPlatform = "any"

// Fetched is a verified data artifact on disk.
type Fetched struct {
	// Dir is <StateDir>/data/<version>; it holds the artifact and its digest.
	Dir     string
	Path    string
	Version string
	// SHA256 is the digest the signed offer named and the download matched.
	SHA256 string
}

const (
	dataDir        = "data"
	dataFile       = "artifact"
	dataDigestFile = "artifact.sha256"
)

// Fetch is CheckAndUpdate for a data program: the offer's artifact is
// os/arch "any", and applying it means putting it on disk, with no swap,
// restart or health gate. It is held to the same rules as a binary: the
// offer's signature, the replay floor, the artifact's size and hash.
//
// A new version is written beside the old and only then made current, so a
// failed or interrupted fetch leaves the previous version in place. The
// version before the new one is kept as well; older ones are removed.
func (c *Client) Fetch(ctx context.Context) (Fetched, error) {
	lock, err := stage.Acquire(c.layout.StateDir)
	if err != nil {
		return Fetched{}, err
	}
	defer lock.Release()
	c.flushOutbox(ctx)

	st, err := swap.LoadState(c.layout)
	if err != nil {
		return Fetched{}, err
	}
	running := c.current
	if st.RunningVersion != "" {
		if running, err = ota.ParseVersion(st.RunningVersion); err != nil {
			return Fetched{}, fmt.Errorf("client: state: %w", err)
		}
	}
	current := func() (Fetched, error) {
		f, err := c.fetched(running.String())
		if errors.Is(err, fs.ErrNotExist) {
			return Fetched{}, nil
		}
		return f, err
	}

	so, err := c.opts.Transport.Check(ctx, ota.CheckRequest{
		ClientID: c.opts.ClientID, Program: c.opts.Program,
		OS: AnyPlatform, Arch: AnyPlatform, Channel: c.opts.Channel,
		RunningVersion: running.String(), LastOfferIssuedAt: st.LastOfferIssuedAt,
	})
	if err != nil {
		return Fetched{}, err
	}
	if so == nil {
		return current()
	}
	o, err := c.verify(ctx, *so)
	if err != nil {
		c.emit(ctx, ota.StageRefused, "", 0, err)
		return Fetched{}, err
	}
	c.emit(ctx, ota.StageOfferSeen, o.Version, o.IssuedAt, nil)

	acc := c.acceptance(st)
	acc.goos, acc.goarch, acc.running = AnyPlatform, AnyPlatform, running
	v, err := acc.accept(o)
	if err == nil {
		err = c.rotate(ctx, o)
	}
	if err != nil {
		c.emit(ctx, ota.StageRefused, o.Version, o.IssuedAt, err)
		return Fetched{}, err
	}
	if v.upToDate {
		return current()
	}
	if _, err := c.decide(ctx, o, &st); err != nil {
		return Fetched{}, err
	}

	c.emit(ctx, ota.StageDownloading, o.Version, o.IssuedAt, nil)
	staged, err := c.download(ctx, v.artifact, o)
	if err != nil {
		c.emit(ctx, ota.StageFailed, o.Version, o.IssuedAt, err)
		return Fetched{}, err
	}
	c.emit(ctx, ota.StageVerified, o.Version, o.IssuedAt, nil)

	version := o.SemVer().String()
	f, err := c.place(staged, version, v.artifact.SHA256)
	if err != nil {
		_ = stage.Remove(staged)
		c.emit(ctx, ota.StageFailed, version, o.IssuedAt, err)
		return Fetched{}, err
	}
	prev := running.String()
	st.LastOfferIssuedAt, st.RunningVersion = o.IssuedAt, version
	st.DeferredUntil, st.DeferredOfferIssuedAt = 0, 0
	if err := swap.SaveState(c.layout, st); err != nil {
		return Fetched{}, err
	}
	c.prune(version, prev)
	c.emit(ctx, ota.StageCommitted, version, o.IssuedAt, nil)
	return f, nil
}

// place moves a verified download into data/<version>/ by renaming a
// complete directory into place, so no reader sees half a version.
func (c *Client) place(staged, version, sha string) (Fetched, error) {
	root := filepath.Join(c.layout.StateDir, dataDir)
	dest := filepath.Join(root, version)
	tmp := dest + ".partial"
	if err := os.RemoveAll(tmp); err != nil {
		return Fetched{}, err
	}
	if err := os.MkdirAll(tmp, 0o750); err != nil {
		return Fetched{}, err
	}
	if err := os.Rename(staged, filepath.Join(tmp, dataFile)); err != nil {
		return Fetched{}, err
	}
	if err := os.WriteFile(filepath.Join(tmp, dataDigestFile), []byte(sha+"\n"), 0o600); err != nil {
		return Fetched{}, err
	}
	// Only a downgrade re-offers a version already on disk.
	if err := os.RemoveAll(dest); err != nil {
		return Fetched{}, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return Fetched{}, err
	}
	return c.fetched(version)
}

func (c *Client) fetched(version string) (Fetched, error) {
	dir := filepath.Join(c.layout.StateDir, dataDir, version)
	b, err := os.ReadFile(filepath.Join(dir, dataDigestFile))
	if err != nil {
		return Fetched{}, err
	}
	sha := string(b[:min(len(b), 64)])
	if !ota.ValidSHA256(sha) {
		return Fetched{}, fmt.Errorf("client: %s: invalid digest", filepath.Join(dir, dataDigestFile))
	}
	return Fetched{Dir: dir, Path: filepath.Join(dir, dataFile), Version: version, SHA256: sha}, nil
}

// prune keeps the new version and the one it replaced; a reader that opened
// the previous one before the switch still finds it.
func (c *Client) prune(keep ...string) {
	root := filepath.Join(c.layout.StateDir, dataDir)
	es, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range es {
		name := e.Name()
		kept := false
		for _, k := range keep {
			kept = kept || name == k
		}
		if !kept {
			_ = os.RemoveAll(filepath.Join(root, name))
		}
	}
}

// DataStateDir is where a data program fetched beside binary keeps its state
// and versions: next to the binary's own .ota directory, so one install holds
// both.
func DataStateDir(binary, program string) string {
	return filepath.Join(filepath.Dir(binary), ".ota", program)
}

// Current returns the version an earlier Fetch made current in stateDir,
// without a network call: what a reader loads at startup. fs.ErrNotExist
// means nothing has been fetched yet.
func Current(stateDir string) (Fetched, error) {
	st, err := swap.LoadState(swap.ForState("", stateDir))
	if err != nil {
		return Fetched{}, err
	}
	if st.RunningVersion == "" {
		return Fetched{}, fmt.Errorf("client: %s: %w", stateDir, fs.ErrNotExist)
	}
	return (&Client{layout: swap.ForState("", stateDir)}).fetched(st.RunningVersion)
}
