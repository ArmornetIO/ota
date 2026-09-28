package lifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Hook is a startup/shutdown pair. Either field may be nil.
// Name is optional — when set it appears in all lifecycle log output for this hook.
type Hook struct {
	Name    string
	OnStart func(ctx context.Context) error
	OnStop  func(ctx context.Context) error
}

// Lifecycle collects ordered hooks. Agents and servers register hooks in their
// Create/New calls; the engine or runner drives Start/Stop.
type Lifecycle interface {
	Append(Hook)
}

// Aware is an optional extension for tools or components that need startup/shutdown.
// The caller checks via type assertion and registers the hook automatically.
type Aware interface {
	OnStart(ctx context.Context) error
	OnStop(ctx context.Context) error
}

// Manager is the concrete, engine-owned implementation of Lifecycle.
// Hooks start in append order and stop in reverse order.
// Every hook transition is logged automatically; callers do not need their own logging.
type Manager struct {
	hooks []Hook
}

// Append adds h to the end of the chain: it starts last and stops first.
func (m *Manager) Append(h Hook) {
	m.hooks = append(m.hooks, h)
}

// Hooks returns a copy of the chain in start order, so a caller can inspect a
// default chain before deciding what to change without mutating it by accident.
func (m *Manager) Hooks() []Hook {
	return append([]Hook(nil), m.hooks...)
}

// Insert places h at index i, clamped to the ends of the chain. Chains built by
// a library are edited by adopters (a step before the swap, a step after the
// commit), and Append alone can only ever add to the end.
func (m *Manager) Insert(i int, h Hook) {
	i = max(0, min(i, len(m.hooks)))
	m.hooks = append(m.hooks[:i], append([]Hook{h}, m.hooks[i:]...)...)
}

// Index returns the position of the first hook with this name, or -1. Names
// are how adopters address steps; positions shift as other steps are added.
func (m *Manager) Index(name string) int {
	for i, h := range m.hooks {
		if h.Name == name {
			return i
		}
	}
	return -1
}

// Remove drops the first hook with this name and reports whether one existed.
func (m *Manager) Remove(name string) bool {
	i := m.Index(name)
	if i < 0 {
		return false
	}
	m.hooks = append(m.hooks[:i], m.hooks[i+1:]...)
	return true
}

// Start runs all OnStart hooks in registration order.
// On the first failure it rolls back already-started hooks in reverse and returns the error.
func (m *Manager) Start(ctx context.Context) error {
	for i, h := range m.hooks {
		if h.OnStart == nil {
			continue
		}
		name := hookLabel(h, i)
		slog.InfoContext(ctx, "lifecycle: starting", "hook", name)
		t := time.Now()
		if err := h.OnStart(ctx); err != nil {
			slog.ErrorContext(ctx, "lifecycle: start failed", "hook", name, "err", err, "elapsed", time.Since(t))
			_ = m.stopUpTo(ctx, i-1)
			return fmt.Errorf("lifecycle start %q: %w", name, err)
		}
		slog.InfoContext(ctx, "lifecycle: started", "hook", name, "elapsed", time.Since(t))
	}
	return nil
}

// Stop runs all OnStop hooks in reverse order.
// All hooks run regardless of errors; errors are combined and returned.
func (m *Manager) Stop(ctx context.Context) error {
	return m.stopUpTo(ctx, len(m.hooks)-1)
}

func (m *Manager) stopUpTo(ctx context.Context, last int) error {
	var errs []error
	for i := last; i >= 0; i-- {
		h := m.hooks[i]
		if h.OnStop == nil {
			continue
		}
		name := hookLabel(h, i)
		slog.InfoContext(ctx, "lifecycle: stopping", "hook", name)
		t := time.Now()
		if err := h.OnStop(ctx); err != nil {
			slog.ErrorContext(ctx, "lifecycle: stop failed", "hook", name, "err", err, "elapsed", time.Since(t))
			errs = append(errs, fmt.Errorf("lifecycle stop %q: %w", name, err))
			continue
		}
		slog.InfoContext(ctx, "lifecycle: stopped", "hook", name, "elapsed", time.Since(t))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%v", errs)
}

func hookLabel(h Hook, i int) string {
	if h.Name != "" {
		return h.Name
	}
	return fmt.Sprintf("hook[%d]", i)
}
