// Package client checks for, verifies and applies updates to the running
// program. Call Boot first thing in main, then CheckAndUpdate whenever the
// host decides to look for an update; ota schedules nothing itself.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/health"
	"github.com/ArmornetIO/ota/internal/stage"
	"github.com/ArmornetIO/ota/internal/swap"
	"github.com/ArmornetIO/ota/internal/testkit"
	"github.com/ArmornetIO/ota/lifecycle"
	"github.com/ArmornetIO/ota/restart"
	"github.com/ArmornetIO/ota/verify"
)

// Options configures a Client. Program, ClientID, CurrentVersion, Transport
// and Verifier are required.
type Options struct {
	Program string
	// Channel defaults to "stable".
	Channel string
	// ClientID is the host's stable identifier for this installation; ota
	// never invents one.
	ClientID string
	// CurrentVersion is the version compiled into this binary.
	CurrentVersion string
	// BinaryPath defaults to os.Executable with symlinks resolved.
	BinaryPath string
	// StateDir defaults to <dir of BinaryPath>/.ota/<binary name>. Set it when
	// the binary's directory changes between versions (side-by-side installs),
	// so every version shares one replay floor and lock.
	StateDir  string
	Transport Transport
	// Verifier is required and has no default, so nothing ships unverified by
	// omission.
	Verifier verify.Verifier
	// Restarter defaults to restart.SelfExec, or restart.Child when the
	// process was started by the resident launcher.
	Restarter restart.Restarter
	// Health defaults to health.CommitOnly: only an explicit Commit keeps the
	// new version.
	Health health.Source
	Hooks  Hooks
	// OnEvent receives every stage transition.
	OnEvent func(ota.Event)
	// Reporter, if set, delivers progress reports instead of the Transport,
	// for a host that sends them over its own channel. Undelivered reports are
	// kept and retried either way.
	Reporter func(ctx context.Context, rs []ota.Report) error
	Logger   *slog.Logger
	// MirrorClient fetches artifact mirror URLs; defaults to http.DefaultClient.
	MirrorClient *http.Client

	ClockTolerance time.Duration // default 5m
	SteadyWindow   time.Duration // default 30s
	CommitDeadline time.Duration // default 5m
	// MaxAttempts is how many starts an uncommitted version gets before it is
	// treated as crash-looping and rolled back. Default 3.
	MaxAttempts int
	// HealthPoll is how often Health is asked. Default 1s.
	HealthPoll time.Duration
	// EditChain, if set, may insert, remove or reorder the named steps of an
	// update (drain, swap, restart, and whatever the restarter adds) before
	// they run.
	EditChain func(*lifecycle.Manager)
	Now       func() time.Time
}

// Hooks let the host take part in an update. ota draws no UI; a host that
// wants a person to approve asks them inside BeforeApply.
type Hooks struct {
	// BeforeApply decides whether a verified offer is applied now, later, or never.
	BeforeApply func(ctx context.Context, o ota.Offer) ota.Decision
	// Drain shuts the program down gracefully before the swap. ctx carries the
	// deadline; overrunning it abandons the attempt without swapping.
	Drain func(ctx context.Context) error
	// DrainTimeout bounds Drain. Default 30s.
	DrainTimeout time.Duration
	// AfterApply runs when a new version commits.
	AfterApply func(ctx context.Context, r Result)
	// OnRollback runs when a new version is rolled back.
	OnRollback func(ctx context.Context, r Result)
}

// Result describes the outcome of CheckAndUpdate or of a health gate.
type Result struct {
	Stage    ota.Stage
	Version  string
	UpToDate bool
	Reason   string
}

// BootResult reports what Boot found.
type BootResult struct {
	// Gate delivers the health gate's outcome when this start is an
	// uncommitted new version; nil otherwise.
	Gate <-chan Result
}

// Client applies updates to one installed binary.
type Client struct {
	opts    Options
	layout  swap.Layout
	current ota.Version
	log     *slog.Logger
	// verifier starts as Options.Verifier and changes only by an accepted,
	// verified trust-root rotation.
	verifier verify.Verifier

	commitOnce sync.Once
	committed  chan struct{}
}

// New validates opts and returns a Client.
func New(opts Options) (*Client, error) {
	switch {
	case opts.Program == "":
		return nil, errors.New("client: Program is required")
	case opts.ClientID == "":
		return nil, errors.New("client: ClientID is required")
	case opts.Transport == nil:
		return nil, errors.New("client: Transport is required")
	case opts.Verifier == nil:
		return nil, errors.New("client: Verifier is required")
	}
	cur, err := ota.ParseVersion(opts.CurrentVersion)
	if err != nil {
		return nil, fmt.Errorf("client: CurrentVersion: %w", err)
	}
	if opts.BinaryPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("client: locate executable: %w", err)
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return nil, fmt.Errorf("client: resolve executable: %w", err)
		}
		opts.BinaryPath = exe
	}
	if !filepath.IsAbs(opts.BinaryPath) {
		return nil, errors.New("client: BinaryPath must be absolute")
	}
	if opts.Channel == "" {
		opts.Channel = "stable"
	}
	if opts.Restarter == nil {
		opts.Restarter = restart.SelfExec()
		if restart.UnderLauncher() {
			opts.Restarter = restart.Child()
		}
	}
	if opts.Health == nil {
		opts.Health = health.CommitOnly()
	}
	if opts.ClockTolerance == 0 {
		opts.ClockTolerance = 5 * time.Minute
	}
	if opts.SteadyWindow == 0 {
		opts.SteadyWindow = 30 * time.Second
	}
	if opts.CommitDeadline == 0 {
		opts.CommitDeadline = 5 * time.Minute
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 3
	}
	if opts.Hooks.DrainTimeout == 0 {
		opts.Hooks.DrainTimeout = 30 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MirrorClient == nil {
		opts.MirrorClient = http.DefaultClient
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	layout := swap.For(opts.BinaryPath)
	if opts.StateDir != "" {
		layout = swap.ForState(opts.BinaryPath, opts.StateDir)
	}
	c := &Client{
		opts:      opts,
		layout:    layout,
		current:   cur,
		log:       log.With("component", "ota", "program", opts.Program),
		committed: make(chan struct{}),
		verifier:  opts.Verifier,
	}
	if err := c.loadRotatedVerifier(); err != nil {
		return nil, err
	}
	return c, nil
}

// Commit confirms the running version is healthy. With health.CommitOnly it
// is the only thing that keeps a new version; with other sources it commits
// early. Calling it when nothing is pending does nothing.
func (c *Client) Commit() {
	c.commitOnce.Do(func() { close(c.committed) })
}

// Boot finishes whatever an earlier run left: it repairs an interrupted swap,
// rolls back a version that crash-looped or missed its deadline, starts the
// health gate for a version on its first starts, and sweeps temp files from
// crashed runs. Call it before the program does real work.
//
// If Boot rolls back, it relaunches the previous binary and does not return
// on success.
func (c *Client) Boot(ctx context.Context) (BootResult, error) {
	lock, err := stage.Acquire(c.layout.StateDir)
	if errors.Is(err, ota.ErrLocked) {
		// Another updater is mid-operation; it finishes what it started.
		c.log.InfoContext(ctx, "ota: boot maintenance skipped, lock held elsewhere")
		return BootResult{}, nil
	}
	if err != nil {
		return BootResult{}, err
	}
	release := sync.OnceFunc(lock.Release)
	defer release()
	c.flushOutbox(ctx)

	if repaired, err := swap.Repair(c.layout); err != nil {
		return BootResult{}, err
	} else if repaired {
		c.emit(ctx, ota.StageRolledBack, c.current.String(), 0, errors.New("interrupted swap repaired"))
	}

	p, err := swap.LoadPending(c.layout)
	if err != nil {
		return BootResult{}, err
	}
	if p == nil {
		swap.Clean(c.layout)
		_ = stage.Sweep(c.layout.StateDir, stage.Partial(c.layout.StateDir))
		return BootResult{}, c.recordRunning()
	}

	if p.Version != c.current.String() {
		// The record names a version this binary is not: the swap was undone
		// by another path. Nothing to gate.
		_ = swap.Commit(c.layout)
		return BootResult{}, c.recordRunning()
	}

	if p.Expired(c.opts.Now()) || p.Attempt >= c.opts.MaxAttempts {
		reason := fmt.Sprintf("version %s did not commit within %d starts or before its deadline", p.Version, p.Attempt)
		release()
		return BootResult{}, c.rollback(ctx, *p, reason)
	}

	p.Attempt++
	if p.Deadline == 0 {
		p.Deadline = c.opts.Now().Add(c.opts.CommitDeadline).UnixMilli()
	}
	if err := swap.SavePending(c.layout, *p); err != nil {
		return BootResult{}, err
	}
	release()

	out := make(chan Result, 1)
	go c.runGate(ctx, *p, out)
	return BootResult{Gate: out}, nil
}

func (c *Client) runGate(ctx context.Context, p swap.Pending, out chan<- Result) {
	testkit.KillAt("in-gate")
	g := health.Gate{
		Source:       c.opts.Health,
		SteadyWindow: c.opts.SteadyWindow,
		Deadline:     time.UnixMilli(p.Deadline),
		Poll:         c.opts.HealthPoll,
		Committed:    c.committed,
		Now:          c.opts.Now,
	}
	o := g.Run(ctx)
	lock, err := c.waitLock(ctx)
	if err != nil {
		out <- Result{Stage: ota.StageFailed, Version: p.Version, Reason: err.Error()}
		return
	}
	if !o.Commit {
		lock.Release()
		err := c.rollback(ctx, p, o.Reason)
		out <- Result{Stage: ota.StageRolledBack, Version: p.Version, Reason: fmt.Sprint(err)}
		return
	}
	defer lock.Release()
	testkit.KillAt("before-commit")
	if err := c.endAttempt(p, p.Version); err != nil {
		out <- Result{Stage: ota.StageFailed, Version: p.Version, Reason: err.Error()}
		return
	}
	if err := swap.Commit(c.layout); err != nil {
		out <- Result{Stage: ota.StageFailed, Version: p.Version, Reason: err.Error()}
		return
	}
	c.emit(ctx, ota.StageCommitted, p.Version, p.OfferIssuedAt, nil)
	r := Result{Stage: ota.StageCommitted, Version: p.Version, Reason: o.Reason}
	if c.opts.Hooks.AfterApply != nil {
		c.opts.Hooks.AfterApply(ctx, r)
	}
	out <- r
}

// rollback restores the previous binary and relaunches it.
func (c *Client) rollback(ctx context.Context, p swap.Pending, reason string) error {
	lock, err := c.waitLock(ctx)
	if err != nil {
		return err
	}
	// State first: a crash after it but before the restore leaves a pending
	// record that the next Boot rolls back again, with the floor already set.
	if err := c.endAttempt(p, p.PrevVersion); err != nil {
		lock.Release()
		return fmt.Errorf("client: rollback: %w", err)
	}
	if err := swap.Restore(c.layout); err != nil {
		lock.Release()
		return fmt.Errorf("client: rollback: %w", err)
	}
	lock.Release()

	err = fmt.Errorf("%w: %s", ota.ErrRolledBack, reason)
	c.emit(ctx, ota.StageRolledBack, p.Version, p.OfferIssuedAt, err)
	if c.opts.Hooks.OnRollback != nil {
		c.opts.Hooks.OnRollback(ctx, Result{Stage: ota.StageRolledBack, Version: p.Version, Reason: reason})
	}
	if rerr := c.opts.Restarter.Relaunch(c.layout.Binary); rerr != nil {
		return errors.Join(err, rerr)
	}
	return err
}

// waitLock takes the lock, waiting briefly: at the end of a gate the only
// other holder is a short maintenance step.
func (c *Client) waitLock(ctx context.Context) (*stage.Lock, error) {
	for i := 0; ; i++ {
		l, err := stage.Acquire(c.layout.StateDir)
		if !errors.Is(err, ota.ErrLocked) || i >= 100 {
			return l, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// endAttempt records the end of an update attempt: the offer becomes the
// replay floor whichever way it ended, since a failed version must not be
// retried from the same offer, and any deferral of it is spent.
func (c *Client) endAttempt(p swap.Pending, running string) error {
	st, err := swap.LoadState(c.layout)
	if err != nil {
		return err
	}
	if p.OfferIssuedAt > st.LastOfferIssuedAt {
		st.LastOfferIssuedAt = p.OfferIssuedAt
	}
	st.RunningVersion = running
	st.DeferredUntil, st.DeferredOfferIssuedAt = 0, 0
	return swap.SaveState(c.layout, st)
}

func (c *Client) recordRunning() error {
	st, err := swap.LoadState(c.layout)
	if err != nil {
		return err
	}
	if st.RunningVersion == c.current.String() {
		return nil
	}
	st.RunningVersion = c.current.String()
	return swap.SaveState(c.layout, st)
}

// CheckAndUpdate asks for an offer and, if one verifies and is accepted,
// downloads, verifies, swaps and restarts. With the default SelfExec
// restarter it does not return when the update is applied: the process is
// replaced by the new version, whose Boot runs the health gate.
//
// ctx is the only deadline: give it one, or a server that stops sending holds
// the update lock until the process exits.
func (c *Client) CheckAndUpdate(ctx context.Context) (Result, error) {
	lock, err := stage.Acquire(c.layout.StateDir)
	if err != nil {
		return Result{}, err
	}
	release := sync.OnceFunc(lock.Release)
	defer release()
	c.flushOutbox(ctx)

	if p, err := swap.LoadPending(c.layout); err != nil {
		return Result{}, err
	} else if p != nil {
		return Result{}, fmt.Errorf("%w: version %s is awaiting its health gate", ota.ErrLocked, p.Version)
	}

	st, err := swap.LoadState(c.layout)
	if err != nil {
		return Result{}, err
	}
	so, err := c.opts.Transport.Check(ctx, ota.CheckRequest{
		ClientID:          c.opts.ClientID,
		Program:           c.opts.Program,
		OS:                runtime.GOOS,
		Arch:              runtime.GOARCH,
		Channel:           c.opts.Channel,
		RunningVersion:    c.current.String(),
		LastOfferIssuedAt: st.LastOfferIssuedAt,
	})
	if err != nil {
		return Result{}, err
	}
	if so == nil {
		return Result{UpToDate: true, Version: c.current.String()}, nil
	}

	o, err := c.verify(ctx, *so)
	if err != nil {
		c.emit(ctx, ota.StageRefused, "", 0, err)
		return Result{Stage: ota.StageRefused, Reason: err.Error()}, err
	}
	c.emit(ctx, ota.StageOfferSeen, o.Version, o.IssuedAt, nil)

	v, err := c.acceptance(st).accept(o)
	if err == nil {
		err = c.rotate(ctx, o)
	}
	if err != nil {
		c.emit(ctx, ota.StageRefused, o.Version, o.IssuedAt, err)
		return Result{Stage: ota.StageRefused, Version: o.Version, Reason: err.Error()}, err
	}
	if v.upToDate {
		return Result{UpToDate: true, Version: c.current.String()}, nil
	}

	if r, err := c.decide(ctx, o, &st); err != nil {
		return r, err
	}

	c.emit(ctx, ota.StageDownloading, o.Version, o.IssuedAt, nil)
	path, err := c.download(ctx, v.artifact, o)
	if err != nil {
		c.emit(ctx, ota.StageFailed, o.Version, o.IssuedAt, err)
		return Result{Stage: ota.StageFailed, Version: o.Version, Reason: err.Error()}, err
	}
	c.emit(ctx, ota.StageVerified, o.Version, o.IssuedAt, nil)

	// The replay floor moves when the attempt ends (commit or rollback), not
	// here: a crash before the swap lands must leave this offer acceptable, or
	// the machine would refuse its own interrupted update as a replay. While
	// the attempt is pending, CheckAndUpdate refuses to start, so nothing can
	// be replayed in between.
	pending := swap.Pending{
		Version:       o.SemVer().String(),
		PrevVersion:   c.current.String(),
		OfferIssuedAt: o.IssuedAt,
		SHA256:        v.artifact.SHA256,
		Deadline:      c.opts.Now().Add(c.opts.CommitDeadline).UnixMilli(),
	}
	// installed decides whether a failed chain is a rollback of a real swap or
	// a failure (such as a drain timeout) that changed nothing on disk.
	installed := false
	plan := restart.Plan{
		Binary:   c.layout.Binary,
		StateDir: c.layout.StateDir,
		Version:  pending.Version,
		Install: func() error {
			if err := swap.Install(c.layout, path, pending); err != nil {
				return err
			}
			installed = true
			c.emit(ctx, ota.StageInstalled, pending.Version, pending.OfferIssuedAt, nil)
			return nil
		},
		Restore: func() error { return swap.Restore(c.layout) },
		BeforeRestart: func() {
			testkit.KillAt("before-restart")
			release()
		},
		DeferGate: func() error {
			pending.Deadline = 0
			return swap.SavePending(c.layout, pending)
		},
		Staged: path,
		Commit: func() error {
			if err := c.endAttempt(pending, pending.Version); err != nil {
				return err
			}
			c.emit(ctx, ota.StageCommitted, pending.Version, pending.OfferIssuedAt, nil)
			return nil
		},
	}
	var chain lifecycle.Manager
	if c.opts.Hooks.Drain != nil {
		chain.Append(lifecycle.Hook{Name: StepDrain, OnStart: c.drain})
	}
	for _, h := range c.opts.Restarter.Chain(plan) {
		chain.Append(h)
	}
	if c.opts.EditChain != nil {
		c.opts.EditChain(&chain)
	}
	if err := chain.Start(ctx); err != nil {
		if !installed {
			_ = stage.Remove(path)
			c.emit(ctx, ota.StageFailed, pending.Version, pending.OfferIssuedAt, err)
			return Result{Stage: ota.StageFailed, Version: pending.Version, Reason: err.Error()}, err
		}
		err = fmt.Errorf("%w: %w", ota.ErrRolledBack, err)
		c.emit(ctx, ota.StageRolledBack, pending.Version, pending.OfferIssuedAt, err)
		return Result{Stage: ota.StageRolledBack, Version: pending.Version, Reason: err.Error()}, err
	}
	return Result{Stage: ota.StageInstalled, Version: pending.Version}, nil
}

// StepDrain names the host's drain step in the update chain.
const StepDrain = "drain"

// verify checks the signature over the exact bytes, then parses them. The
// order matters: nothing from the offer is read before it is trusted.
func (c *Client) verify(ctx context.Context, so ota.SignedOffer) (ota.Offer, error) {
	if so.Kind != c.verifier.Kind() {
		return ota.Offer{}, fmt.Errorf("%w: offer signed as %q, this client verifies %q", ota.ErrRefused, so.Kind, c.verifier.Kind())
	}
	id, err := c.verifier.Verify(ctx, so.Offer, so.Signature)
	if err != nil {
		return ota.Offer{}, fmt.Errorf("%w: signature: %w", ota.ErrRefused, err)
	}
	o, err := ota.ParseOffer(so.Offer)
	if err != nil {
		return ota.Offer{}, fmt.Errorf("%w: %w", ota.ErrRefused, err)
	}
	c.log.InfoContext(ctx, "ota: offer verified", "version", o.Version, "signer", id.Subject)
	return o, nil
}

// decide runs BeforeApply, honouring a deferral persisted by an earlier run so
// a one-shot invocation does not re-ask before the deferral ends.
func (c *Client) decide(ctx context.Context, o ota.Offer, st *swap.State) (Result, error) {
	now := c.opts.Now()
	if st.DeferredOfferIssuedAt == o.IssuedAt && now.UnixMilli() < st.DeferredUntil {
		err := fmt.Errorf("%w: until %s", ota.ErrDeferred, time.UnixMilli(st.DeferredUntil).UTC().Format(time.RFC3339))
		return Result{Stage: ota.StageDeferred, Version: o.Version, Reason: err.Error()}, err
	}
	if c.opts.Hooks.BeforeApply == nil {
		return Result{}, nil
	}
	d := c.opts.Hooks.BeforeApply(ctx, o)
	switch d.Action {
	case ota.Apply:
		return Result{}, nil
	case ota.Defer:
		st.DeferredUntil, st.DeferredOfferIssuedAt = d.Until.UnixMilli(), o.IssuedAt
		if err := swap.SaveState(c.layout, *st); err != nil {
			return Result{}, err
		}
		err := fmt.Errorf("%w: %s", ota.ErrDeferred, d.Reason)
		c.emit(ctx, ota.StageDeferred, o.Version, o.IssuedAt, err)
		return Result{Stage: ota.StageDeferred, Version: o.Version, Reason: d.Reason}, err
	default:
		err := fmt.Errorf("%w: refused by host: %s", ota.ErrDeferred, d.Reason)
		c.emit(ctx, ota.StageRefused, o.Version, o.IssuedAt, err)
		return Result{Stage: ota.StageRefused, Version: o.Version, Reason: d.Reason}, err
	}
}

func (c *Client) drain(ctx context.Context) error {
	if c.opts.Hooks.Drain == nil {
		return nil
	}
	dctx, cancel := context.WithTimeout(ctx, c.opts.Hooks.DrainTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.opts.Hooks.Drain(dctx) }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("client: drain: %w", err)
		}
		return nil
	case <-dctx.Done():
		return ota.ErrDrainTimeout
	}
}

// download tries the server first, then each mirror in the offer. Every source
// is held to the same size and hash, so a mirror can only fail, never corrupt.
func (c *Client) download(ctx context.Context, a ota.Artifact, o ota.Offer) (string, error) {
	progress := func(done, total int64) {
		c.emitEvent(ota.Event{Stage: ota.StageDownloading, Program: c.opts.Program, Version: o.Version, Bytes: done, Total: total})
	}
	sources := []stage.Fetcher{func(ctx context.Context, off int64) (io.ReadCloser, int64, error) {
		return c.opts.Transport.Fetch(ctx, a, off)
	}}
	for _, u := range a.URLs {
		sources = append(sources, func(ctx context.Context, off int64) (io.ReadCloser, int64, error) {
			return rangeGet(ctx, c.opts.MirrorClient.Do, u, off)
		})
	}
	var errs []error
	for _, src := range sources {
		path, err := stage.Download(ctx, c.layout.StateDir, a, src, progress)
		if err == nil {
			testkit.KillAt("after-download")
			return path, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return "", errors.Join(errs...)
}

func (c *Client) emit(ctx context.Context, s ota.Stage, version string, issuedAt int64, err error) {
	e := ota.Event{Stage: s, Program: c.opts.Program, Version: version, Err: err, At: c.opts.Now()}
	c.emitEvent(e)
	if err != nil {
		c.log.WarnContext(ctx, "ota: "+string(s), "version", version, "err", err)
	} else {
		c.log.InfoContext(ctx, "ota: "+string(s), "version", version)
	}
	if !s.Reported() {
		return
	}
	r := ota.Report{
		ClientID: c.opts.ClientID, Program: c.opts.Program, Version: version,
		OfferIssuedAt: issuedAt, Stage: s, At: e.At.UnixMilli(),
	}
	if err != nil {
		r.Error = err.Error()
	}
	c.deliver(ctx, []ota.Report{r})
}

func (c *Client) emitEvent(e ota.Event) {
	if e.At.IsZero() {
		e.At = c.opts.Now()
	}
	if c.opts.OnEvent != nil {
		c.opts.OnEvent(e)
	}
}
