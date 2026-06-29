package upstream_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/rclone/rclone/backend/union/policy"
	"github.com/rclone/rclone/backend/union/upstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gb = int64(1) << 30

// capacityOf snapshots the free space of each upstream before any routing, so
// the over-commit check below can compare against original capacity rather
// than a value that later reservations may have changed.
func capacityOf(ups ...*upstream.Fs) map[*upstream.Fs]int64 {
	m := map[*upstream.Fs]int64{}
	for _, u := range ups {
		free, _ := u.GetFreeSpace()
		m[u] = free
	}
	return m
}

// overCommitted reports an upstream whose routed bytes exceed its capacity.
// This is the invariant a reservation-aware create policy MUST uphold: the
// total size of files concurrently routed to a single branch may not exceed
// that branch's free space.
func overCommitted(routed, capacity map[*upstream.Fs]int64) error {
	for u, b := range routed {
		if b > capacity[u] {
			return fmt.Errorf("branch over-committed: routed %d GB onto %d GB free",
				b/gb, capacity[u]/gb)
		}
	}
	return nil
}

// TestMfsPicksMostFreeToday documents how mfs behaves today: it always picks
// the branch with the most free space, and — crucially — the decision does NOT
// reserve/debit that space. So repeated decisions keep returning the same
// branch until something else (a completed Put) updates the cached usage.
func TestMfsPicksMostFreeToday(t *testing.T) {
	ctx := context.Background()
	p, err := policy.Get("mfs")
	require.NoError(t, err)

	small := upstream.NewTestFs(50*gb, true)
	big := upstream.NewTestFs(100*gb, true)
	ups := []*upstream.Fs{small, big}

	got, err := p.Create(ctx, ups, "file0.bin")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Same(t, big, got[0], "mfs must pick the branch with the most free space")

	// No reservation happens here: asking again returns the same branch,
	// because nothing was debited until a Put actually completes.
	got2, err := p.Create(ctx, ups, "file1.bin")
	require.NoError(t, err)
	assert.Same(t, big, got2[0], "with no reservation every create targets the same branch")
}

// TestMfsReserveUpholdsInvariant is the headline test. Five concurrent pullers
// each upload a 24 GB file onto a 100 GB + 50 GB pair. A reservation-aware
// policy must claim each file's size on the branch it picks at decision time,
// so the decisions see the space earlier pullers already took and spread the
// load instead of stampeding the most-free branch.
//
// Expected result: big takes 4 files (96 GB <= 100 GB), small takes 1 (24 GB),
// and no branch is committed beyond its free space.
func TestMfsReserveUpholdsInvariant(t *testing.T) {
	ctx := context.Background()
	p, err := policy.Get("mfsreserve")
	require.NoError(t, err)

	big := upstream.NewTestFs(100*gb, true)
	small := upstream.NewTestFs(50*gb, true)
	ups := []*upstream.Fs{small, big}
	capacity := capacityOf(small, big)

	const pullers = 5
	const fileSize = 24 * gb

	var mu sync.Mutex
	routed := map[*upstream.Fs]int64{}

	var wg sync.WaitGroup
	for i := 0; i < pullers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// The upload path tells the create policy how big the file is so it
			// can reserve that space on the branch it chooses.
			rctx := policy.WithReserveSize(ctx, fileSize)
			got, err := p.Create(rctx, ups, fmt.Sprintf("file%d.bin", i))
			if err != nil || len(got) == 0 {
				return
			}
			mu.Lock()
			routed[got[0]] += fileSize
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	// Reservations spread the load instead of overflowing the most-free branch.
	assert.Equal(t, int64(96)*gb, routed[big], "big takes 4 files (96 GB <= 100 GB)")
	assert.Equal(t, int64(24)*gb, routed[small], "small takes 1 file (24 GB <= 50 GB)")
	require.NoError(t, overCommitted(routed, capacity),
		"no branch may be committed beyond its free space")
}

// TestMfsSpreadsWhenSpaceIsReserved shows the fix direction without changing
// any policy code: if each accepted upload immediately debited the chosen
// branch's free space (i.e. a reservation), the existing mfs selection would
// spread load and never over-commit. This is exactly the behaviour a real
// mfsreserve must produce.
func TestMfsSpreadsWhenSpaceIsReserved(t *testing.T) {
	ctx := context.Background()
	p, err := policy.Get("mfs")
	require.NoError(t, err)

	big := upstream.NewTestFs(100*gb, true)
	small := upstream.NewTestFs(50*gb, true)
	ups := []*upstream.Fs{small, big}
	capacity := capacityOf(small, big)

	const fileSize = 24 * gb
	routed := map[*upstream.Fs]int64{}

	for i := 0; i < 5; i++ {
		got, err := p.Create(ctx, ups, fmt.Sprintf("file%d.bin", i))
		require.NoError(t, err)
		u := got[0]
		// Reserve immediately: debit the chosen branch before the next decision.
		free, _ := u.GetFreeSpace()
		u.SetFreeSpace(free - fileSize)
		routed[u] += fileSize
	}

	// big: 100 -> picked 4x (96 GB), small: 50 -> picked 1x (24 GB). Neither
	// branch is over-committed once reservations are honoured.
	assert.Equal(t, int64(96)*gb, routed[big])
	assert.Equal(t, int64(24)*gb, routed[small])
	require.NoError(t, overCommitted(routed, capacity),
		"with reservations honoured the invariant holds")
}
