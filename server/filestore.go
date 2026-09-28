package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileStore is a MemStore saved to one JSON file after every change, for the
// standalone server. It is not encrypted: reports and client records link an
// installation to what it runs, so keep the directory private, or implement
// Store over an encrypted database for multi-tenant use.
type FileStore struct {
	*MemStore
	path string
}

// OpenFileStore loads (or creates) the store at <dir>/state.json.
func OpenFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	fsr := &FileStore{MemStore: NewMemStore(), path: filepath.Join(dir, "state.json")}
	b, err := os.ReadFile(fsr.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &fsr.data); err != nil {
			return nil, fmt.Errorf("server: %s: %w", fsr.path, err)
		}
		if fsr.data.Clients == nil {
			fsr.data.Clients = map[string]ClientRecord{}
		}
	}
	fsr.onChange = fsr.save
	return fsr, nil
}

// save writes atomically: temp file, fsync, rename.
func (f *FileStore) save(d memData) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}
