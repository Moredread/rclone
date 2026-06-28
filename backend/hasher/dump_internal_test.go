package hasher

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fstest"
	"github.com/rclone/rclone/lib/kv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDumpSeekGap is a regression test for a bug where `backend dump <remote>:<dir>`
// returned nothing when a sibling key sorted into the gap [dir, dir+"/").
//
// dump used Seek(dir) + break-on-first-mismatch. Seek lands on the first key
// >= dir, but the real entries live at dir+"/...". A sibling such as
// "foo-bar/..." ('-' = 0x2D < '/' = 0x2F) sorts into [dir, dir+"/") and is
// returned by Seek first, so the loop broke before reaching any "foo/..." key.
func TestDumpSeekGap(t *testing.T) {
	if !kv.Supported() {
		t.Skip("hasher is not supported on this OS")
	}
	ctx := context.Background()

	// hasher over a local temp remote, caching forever
	tempRoot, err := fstest.LocalRemote()
	require.NoError(t, err)
	remote := fmt.Sprintf(`:hasher,remote="%s",hashes="md5",max_age="1000h":`, tempRoot)
	rfs, err := fs.NewFs(ctx, remote)
	require.NoError(t, err)
	f := rfs.(*Fs)
	defer func() { _ = f.db.Stop(false) }()

	// seed records directly under known keys
	seed := func(key string) {
		require.NoError(t, f.db.Do(true, &kvPut{
			key:    key,
			fp:     anyFingerprint,
			hashes: operations.HashSums{"md5": "d41d8cd98f00b204e9800998ecf8427e"},
			age:    time.Hour * 1000,
		}))
	}
	seed("foo/a.zip")
	seed("foo/sub/b.zip")
	seed("foo-bar/x.json") // sibling sorting into the gap before "foo/"
	seed("aaa/c.zip")      // sorts before "foo" entirely

	// dump the "foo" subtree
	op := &kvDump{root: "foo", path: f.db.Path(), fs: f}
	require.NoError(t, f.db.Do(false, op))

	// must find exactly the two foo/ records, despite the foo-bar sibling
	assert.Equal(t, 2, op.num, "expected the two foo/ records, sibling must not abort the scan")
}
