package ota

import "errors"

// Sentinels map one-to-one onto the CLI exit codes, so a caller branches with
// errors.Is and the CLI never parses a message.
var (
	// ErrRefused: the offer failed verification, replay, expiry, channel or
	// downgrade rules. Nothing was downloaded or changed.
	ErrRefused = errors.New("ota: offer refused")
	// ErrDeferred: the host deferred or refused the offer.
	ErrDeferred = errors.New("ota: update deferred by host")
	// ErrDrainTimeout: the host's drain step overran its deadline; nothing was swapped.
	ErrDrainTimeout = errors.New("ota: host drain exceeded its deadline")
	// ErrDownload: the download failed or its bytes did not match the offer.
	ErrDownload = errors.New("ota: download failed")
	// ErrRolledBack: the new version was installed, failed its health gate and
	// was replaced by the previous one.
	ErrRolledBack = errors.New("ota: update rolled back")
	// ErrLocked: another updater holds the lock for this installation.
	ErrLocked = errors.New("ota: another update is in progress")
	// ErrRestartNeeded: the update is verified and staged, and applies on the
	// program's next start.
	ErrRestartNeeded = errors.New("ota: update staged; restart needed")
)
