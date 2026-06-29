package upstream

import (
	"time"

	"github.com/rclone/rclone/fs"
)

// NewTestFs builds a minimal *Fs for unit testing policy selection.
//
// It injects a fixed free-space value and freezes the usage cache so that
// GetFreeSpace serves the injected value instead of triggering a real
// updateUsage()/About() call on an underlying backend. This lets tests model
// upstreams of arbitrary capacity (e.g. a "100 GB target") without touching a
// real filesystem.
//
// The embedded fs.Fs is left nil on purpose: the space-aware create policies
// only call IsCreatable() and GetFreeSpace() on the happy path, neither of
// which dereferences it.
func NewTestFs(freeSpace int64, creatable bool) *Fs {
	free := freeSpace
	f := &Fs{
		writable:  true,
		creatable: creatable,
		usage:     &fs.Usage{Free: &free},
		cacheTime: time.Hour,
	}
	// Freeze the cache far in the future so GetFreeSpace never calls About().
	f.cacheExpiry.Store(time.Now().Add(24 * time.Hour).Unix())
	return f
}

// SetFreeSpace overwrites the cached free space (test helper).
//
// This models what a reservation would do: immediately reduce a branch's
// apparent free space the moment an upload is accepted, before it completes.
func (f *Fs) SetFreeSpace(freeSpace int64) {
	f.cacheMutex.Lock()
	defer f.cacheMutex.Unlock()
	if f.usage == nil {
		f.usage = &fs.Usage{}
	}
	free := freeSpace
	f.usage.Free = &free
}
