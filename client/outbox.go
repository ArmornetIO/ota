package client

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sync"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/internal/stage"
)

// outboxCap bounds undelivered reports. Only the latest per (version, stage)
// is kept, which is all the server needs to know where each attempt ended.
const outboxCap = 64

// outboxMu serialises outbox rewrites within the process; across processes the
// file is replaced atomically, so the worst case is a lost duplicate.
var outboxMu sync.Mutex

func (c *Client) outboxPath() string { return filepath.Join(c.layout.StateDir, "outbox.json") }

// deliver sends reports through the host's Reporter if set, else the
// Transport. Whatever cannot be delivered is kept for the next attempt.
func (c *Client) deliver(ctx context.Context, rs []ota.Report) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := c.send(rctx, rs); err != nil {
		c.log.DebugContext(ctx, "ota: report not delivered; kept for later", "err", err)
		c.keep(rs)
	}
}

func (c *Client) send(ctx context.Context, rs []ota.Report) error {
	if c.opts.Reporter != nil {
		return c.opts.Reporter(ctx, rs)
	}
	return c.opts.Transport.Report(ctx, rs)
}

func (c *Client) keep(rs []ota.Report) {
	outboxMu.Lock()
	defer outboxMu.Unlock()
	var box []ota.Report
	if err := stage.ReadJSON(c.outboxPath(), &box); err != nil && !errors.Is(err, fs.ErrNotExist) {
		box = nil
	}
	for _, r := range rs {
		box = upsert(box, r)
	}
	if len(box) > outboxCap {
		box = box[len(box)-outboxCap:]
	}
	if stage.EnsureDir(c.layout.StateDir) == nil {
		_ = stage.WriteJSON(c.outboxPath(), box)
	}
}

func upsert(box []ota.Report, r ota.Report) []ota.Report {
	for i, cur := range box {
		if cur.Version == r.Version && cur.Stage == r.Stage {
			box = append(box[:i], box[i+1:]...)
			break
		}
	}
	return append(box, r)
}

// flushOutbox retries undelivered reports. Called at the start of Boot and
// CheckAndUpdate, the two moments a client is known to be running.
func (c *Client) flushOutbox(ctx context.Context) {
	outboxMu.Lock()
	var box []ota.Report
	err := stage.ReadJSON(c.outboxPath(), &box)
	outboxMu.Unlock()
	if err != nil || len(box) == 0 {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := c.send(rctx, box); err != nil {
		return
	}
	outboxMu.Lock()
	defer outboxMu.Unlock()
	_ = stage.Remove(c.outboxPath())
}
