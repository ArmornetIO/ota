package server

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/ArmornetIO/ota"
)

// MemStore keeps everything in memory. FileStore persists it.
type MemStore struct {
	mu   sync.Mutex
	data memData
	// onChange runs after every mutation, under the lock; FileStore saves here.
	onChange func(memData) error
}

type memData struct {
	Releases []Release               `json:"releases"`
	Rollouts []Rollout               `json:"rollouts"`
	Pins     []Pin                   `json:"pins"`
	Clients  map[string]ClientRecord `json:"clients"`
	Reports  []ota.Report            `json:"reports"`
}

// maxReports bounds memory; outcomes are computed from what is kept.
const maxReports = 100_000

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore { return &MemStore{data: memData{Clients: map[string]ClientRecord{}}} }

func (m *MemStore) changed() error {
	if m.onChange != nil {
		return m.onChange(m.data)
	}
	return nil
}

// PutOffer ingests a signed offer directly, without verifying it. It is for
// tests and for hosts that verify elsewhere; servers ingest through Server.
func (m *MemStore) PutOffer(so ota.SignedOffer) error {
	o, err := ota.ParseOffer(so.Offer)
	if err != nil {
		return err
	}
	return m.PutRelease(context.Background(), Release{
		Program: o.Program, Version: o.SemVer().String(), Status: ReleaseIngested, IngestedAt: time.Now(),
		Offers: []StoredOffer{storedOffer(so, o)},
	})
}

// PutRelease implements Store, merging offers into an existing release.
func (m *MemStore) PutRelease(_ context.Context, r Release) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, cur := range m.data.Releases {
		if cur.Program == r.Program && cur.Version == r.Version {
			for _, o := range r.Offers {
				if !slices.ContainsFunc(cur.Offers, func(c StoredOffer) bool { return c.Digest == o.Digest }) {
					cur.Offers = append(cur.Offers, o)
				}
			}
			if r.Status == ReleaseIngested || cur.Status != ReleaseIngested {
				cur.Status, cur.Reason = r.Status, r.Reason
			}
			cur.Tag, cur.Source = firstNonEmpty(cur.Tag, r.Tag), firstNonEmpty(cur.Source, r.Source)
			m.data.Releases[i] = cur
			return m.changed()
		}
	}
	m.data.Releases = append(m.data.Releases, r)
	return m.changed()
}

// Releases implements Store.
func (m *MemStore) Releases(_ context.Context, program string) ([]Release, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Release
	for _, r := range m.data.Releases {
		if program == "" || r.Program == program {
			out = append(out, r)
		}
	}
	return out, nil
}

// PutRollout implements Store; one rollout per program and channel.
func (m *MemStore) PutRollout(_ context.Context, r Rollout) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, cur := range m.data.Rollouts {
		if cur.Program == r.Program && cur.Channel == r.Channel {
			m.data.Rollouts[i] = r
			return m.changed()
		}
	}
	m.data.Rollouts = append(m.data.Rollouts, r)
	return m.changed()
}

// Rollouts implements Store.
func (m *MemStore) Rollouts(_ context.Context, program string) ([]Rollout, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Rollout
	for _, r := range m.data.Rollouts {
		if program == "" || r.Program == program {
			out = append(out, r)
		}
	}
	return out, nil
}

// PutPin implements Store.
func (m *MemStore) PutPin(_ context.Context, p Pin) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Pins = slices.DeleteFunc(m.data.Pins, func(c Pin) bool { return c.Program == p.Program && c.ClientID == p.ClientID })
	m.data.Pins = append(m.data.Pins, p)
	return m.changed()
}

// DeletePin implements Store.
func (m *MemStore) DeletePin(_ context.Context, program, clientID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Pins = slices.DeleteFunc(m.data.Pins, func(c Pin) bool { return c.Program == program && c.ClientID == clientID })
	return m.changed()
}

// GetPin implements Store.
func (m *MemStore) GetPin(_ context.Context, program, clientID string) (*Pin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.data.Pins {
		if p.Program == program && p.ClientID == clientID {
			return &p, nil
		}
	}
	return nil, nil
}

func clientKey(program, id string) string { return program + "\x00" + id }

// TouchClient implements Store.
func (m *MemStore) TouchClient(_ context.Context, c ClientRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := clientKey(c.Program, c.ClientID)
	if cur, ok := m.data.Clients[k]; ok && c.LastReport == nil {
		c.LastReport = cur.LastReport
	}
	m.data.Clients[k] = c
	return m.changed()
}

// Clients implements Store.
func (m *MemStore) Clients(_ context.Context, program string) ([]ClientRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ClientRecord
	for _, c := range m.data.Clients {
		if program == "" || c.Program == program {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b ClientRecord) int {
		if a.ClientID < b.ClientID {
			return -1
		}
		if a.ClientID > b.ClientID {
			return 1
		}
		return 0
	})
	return out, nil
}

// AddReports implements Store.
func (m *MemStore) AddReports(_ context.Context, rs []ota.Report) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rs {
		dup := slices.ContainsFunc(m.data.Reports, func(c ota.Report) bool {
			return c.ClientID == r.ClientID && c.Program == r.Program && c.Version == r.Version &&
				c.OfferIssuedAt == r.OfferIssuedAt && c.Stage == r.Stage
		})
		if dup {
			continue
		}
		m.data.Reports = append(m.data.Reports, r)
		k := clientKey(r.Program, r.ClientID)
		c := m.data.Clients[k]
		c.ClientID, c.Program = r.ClientID, r.Program
		rc := r
		c.LastReport = &rc
		if r.Stage == ota.StageCommitted {
			c.RunningVersion = r.Version
		}
		m.data.Clients[k] = c
	}
	if n := len(m.data.Reports); n > maxReports {
		m.data.Reports = m.data.Reports[n-maxReports:]
	}
	return m.changed()
}

// Outcomes implements Store.
func (m *MemStore) Outcomes(_ context.Context, program, version string) (Outcomes, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return outcomesOf(m.data.Reports, program, version), nil
}

// outcomesOf counts distinct clients: attempted means it installed or failed
// trying; failed means it failed or rolled back.
func outcomesOf(rs []ota.Report, program, version string) Outcomes {
	attempted, failed := map[string]bool{}, map[string]bool{}
	for _, r := range rs {
		if r.Program != program || r.Version != version {
			continue
		}
		switch r.Stage {
		case ota.StageInstalled:
			attempted[r.ClientID] = true
		case ota.StageFailed, ota.StageRolledBack:
			attempted[r.ClientID] = true
			failed[r.ClientID] = true
		}
	}
	return Outcomes{Attempted: len(attempted), Failed: len(failed)}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
