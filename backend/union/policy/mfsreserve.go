package policy

import (
	"context"
	"sync"

	"github.com/rclone/rclone/backend/union/upstream"
	"github.com/rclone/rclone/fs"
)

func init() {
	registerPolicy("mfsreserve", &MfsReserve{})
}

// reserveSizeKey is the context key under which the upload path passes the size
// of the file about to be created, so a reservation-aware policy can claim that
// space on the branch it chooses.
type reserveSizeKey struct{}

// WithReserveSize returns a copy of ctx carrying the size of the file about to
// be created. A reservation-aware create policy reserves this many bytes on the
// branch it picks.
func WithReserveSize(ctx context.Context, size int64) context.Context {
	return context.WithValue(ctx, reserveSizeKey{}, size)
}

// reserveSize extracts a size set by WithReserveSize.
func reserveSize(ctx context.Context) (int64, bool) {
	size, ok := ctx.Value(reserveSizeKey{}).(int64)
	return size, ok
}

// MfsReserve is a reservation-aware variant of mfs.
//
// Plain mfs picks the branch with the most free space, but the choice does not
// reserve anything until the upload completes, so several uploads launched
// together all see the same free figure and stampede the same branch (the
// "5 pullers x 24 GB onto a 100 GB target" overflow).
//
// MfsReserve claims the file's size on the chosen branch at decision time, so
// concurrent decisions see the space the earlier ones already took.
type MfsReserve struct {
	EpMfs
	mu sync.Mutex // serialises pick-and-reserve so concurrent creates don't race on the same free figure
}

// ReservesSpace implements policy.Reserver: MfsReserve reserves space at Create
// time, so the upload path must Release it when the upload finishes.
func (p *MfsReserve) ReservesSpace() bool { return true }

// Create category policy, governing the creation of files and directories
func (p *MfsReserve) Create(ctx context.Context, upstreams []*upstream.Fs, path string) ([]*upstream.Fs, error) {
	if len(upstreams) == 0 {
		return nil, fs.ErrorObjectNotFound
	}
	upstreams = filterNC(upstreams)
	if len(upstreams) == 0 {
		return nil, fs.ErrorPermissionDenied
	}

	size, ok := reserveSize(ctx)
	if !ok {
		// No size information (e.g. streamed upload of unknown size): fall back
		// to plain mfs behaviour without reserving.
		u, err := p.mfs(upstreams)
		return []*upstream.Fs{u}, err
	}

	// Pick and reserve atomically: while the lock is held no other create can
	// read the same free figure, so each decision sees the space earlier
	// in-flight uploads already claimed (GetFreeSpace nets out reservations).
	p.mu.Lock()
	defer p.mu.Unlock()
	u, err := p.mfs(upstreams)
	if err != nil {
		return nil, err
	}
	u.Reserve(size)
	return []*upstream.Fs{u}, nil
}
