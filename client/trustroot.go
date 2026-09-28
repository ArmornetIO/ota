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
	"github.com/ArmornetIO/ota/verify"
)

func (c *Client) trustRootPath() string { return filepath.Join(c.layout.StateDir, "trust-root.json") }

// loadRotatedVerifier replaces the compiled-in verifier with one built from a
// rotation this installation already accepted, so a restart does not fall
// back to keys the publisher has retired.
func (c *Client) loadRotatedVerifier() error {
	root, err := os.ReadFile(c.trustRootPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	r, ok := c.verifier.(verify.Rotatable)
	if !ok {
		return nil
	}
	v, err := r.Rotate(root)
	if err != nil {
		return fmt.Errorf("client: stored trust root: %w", err)
	}
	c.verifier = v
	return nil
}

// rotate adopts the trust root carried by an accepted offer. The
// offer has verified under the current root and passed every acceptance rule,
// and only a newer root version is taken, so an old offer cannot roll trust
// back.
func (c *Client) rotate(ctx context.Context, o ota.Offer) error {
	u := o.TrustRootUpdate
	if u == nil {
		return nil
	}
	st, err := swap.LoadState(c.layout)
	if err != nil {
		return err
	}
	if u.Version <= st.TrustRootVersion {
		return nil
	}
	if u.Kind != c.verifier.Kind() {
		return fmt.Errorf("%w: trust root update is for %q, this client verifies %q", ota.ErrRefused, u.Kind, c.verifier.Kind())
	}
	r, ok := c.verifier.(verify.Rotatable)
	if !ok {
		return fmt.Errorf("%w: verifier %q cannot rotate its trust root", ota.ErrRefused, u.Kind)
	}
	v, err := r.Rotate(u.Root)
	if err != nil {
		return fmt.Errorf("%w: %w", ota.ErrRefused, err)
	}
	// Root first, then the version that says it is in force: a crash between
	// the two re-applies the same rotation from the next offer.
	if err := stage.EnsureDir(c.layout.StateDir); err != nil {
		return err
	}
	f, err := stage.CreateTemp(c.layout.StateDir, ".json")
	if err != nil {
		return err
	}
	if _, err := f.Write(u.Root); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), c.trustRootPath()); err != nil {
		return err
	}
	st.TrustRootVersion = u.Version
	if err := swap.SaveState(c.layout, st); err != nil {
		return err
	}
	c.verifier = v
	c.log.InfoContext(ctx, "ota: trust root rotated", "version", u.Version)
	return nil
}
