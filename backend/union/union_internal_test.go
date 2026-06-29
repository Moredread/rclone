package union

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/union/policy"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fstest"
	"github.com/rclone/rclone/fstest/fstests"
	"github.com/rclone/rclone/lib/random"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MakeTestDirs makes directories in /tmp for testing
func MakeTestDirs(t *testing.T, n int) (dirs []string) {
	for i := 1; i <= n; i++ {
		dir := t.TempDir()
		dirs = append(dirs, dir)
	}
	return dirs
}

func (f *Fs) TestInternalReadOnly(t *testing.T) {
	if f.name != "TestUnionRO" {
		t.Skip("Only on RO union")
	}
	dir := "TestInternalReadOnly"
	ctx := context.Background()
	rofs := f.upstreams[len(f.upstreams)-1]
	assert.False(t, rofs.IsWritable())

	// Put a file onto the read only fs
	contents := random.String(50)
	file1 := fstest.NewItem(dir+"/file.txt", contents, time.Now())
	obj1 := fstests.PutTestContents(ctx, t, rofs, &file1, contents, true)

	// Check read from readonly fs via union
	o, err := f.NewObject(ctx, file1.Path)
	require.NoError(t, err)
	assert.Equal(t, int64(50), o.Size())

	// Now call Update on the union Object with new data
	contents2 := random.String(100)
	file2 := fstest.NewItem(dir+"/file.txt", contents2, time.Now())
	in := bytes.NewBufferString(contents2)
	src := object.NewStaticObjectInfo(file2.Path, file2.ModTime, file2.Size, true, nil, nil)
	err = o.Update(ctx, in, src)
	require.NoError(t, err)
	assert.Equal(t, int64(100), o.Size())

	// Check we read the new object via the union
	o, err = f.NewObject(ctx, file1.Path)
	require.NoError(t, err)
	assert.Equal(t, int64(100), o.Size())

	// Remove the object
	assert.NoError(t, o.Remove(ctx))

	// Check we read the old object in the read only layer now
	o, err = f.NewObject(ctx, file1.Path)
	require.NoError(t, err)
	assert.Equal(t, int64(50), o.Size())

	// Remove file and dir from read only fs
	assert.NoError(t, obj1.Remove(ctx))
	assert.NoError(t, rofs.Rmdir(ctx, dir))
}

func (f *Fs) InternalTest(t *testing.T) {
	t.Run("ReadOnly", f.TestInternalReadOnly)
}

var _ fstests.InternalTester = (*Fs)(nil)

// TestReservePutReleases drives real concurrent uploads through a union whose
// create policy is the reservation-aware mfsreserve, and checks that every
// reservation made at create time is released once the upload finishes - i.e.
// the put() wiring reserves and releases in balance and nothing leaks.
func TestReservePutReleases(t *testing.T) {
	if *fstest.RemoteName != "" {
		t.Skip("Skipping as -remote set")
	}
	ctx := context.Background()
	dirs := MakeTestDirs(t, 2)
	fsString := fmt.Sprintf(":union,upstreams='%s %s',create_policy=mfsreserve:", dirs[0], dirs[1])
	f, err := fs.NewFs(ctx, fsString)
	require.NoError(t, err)
	unionFs := f.(*Fs)

	// The create policy must actually be reservation-aware, otherwise put()
	// never reserves and this test proves nothing.
	_, ok := unionFs.createPolicy.(policy.Reserver)
	require.True(t, ok, "create policy should implement policy.Reserver")

	const n = 8
	const size = 100
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			contents := random.String(size)
			name := fmt.Sprintf("file%d.txt", i)
			src := object.NewStaticObjectInfo(name, time.Now(), int64(len(contents)), true, nil, nil)
			_, errs[i] = f.Put(ctx, bytes.NewBufferString(contents), src)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
	}

	// Every reservation must have been released after the uploads finished.
	for _, u := range unionFs.upstreams {
		assert.Zero(t, u.Reserved(), "reservation leaked on upstream %s", u.Name())
	}

	// All files are present and readable through the union.
	for i := 0; i < n; i++ {
		o, err := f.NewObject(ctx, fmt.Sprintf("file%d.txt", i))
		require.NoError(t, err)
		assert.Equal(t, int64(size), o.Size())
	}
}

// errReader fails the upload by returning an error instead of data.
type errReader struct{ err error }

func (e errReader) Read(p []byte) (int, error) { return 0, e.err }

// TestReservePutReleasesOnFailure exercises the failure recovery path: the
// create policy reserves space, the upload then fails, and the reservation
// must still be released so a failed upload does not permanently shrink the
// branch's apparent free space.
func TestReservePutReleasesOnFailure(t *testing.T) {
	if *fstest.RemoteName != "" {
		t.Skip("Skipping as -remote set")
	}
	ctx := context.Background()
	dirs := MakeTestDirs(t, 2)
	fsString := fmt.Sprintf(":union,upstreams='%s %s',create_policy=mfsreserve:", dirs[0], dirs[1])
	f, err := fs.NewFs(ctx, fsString)
	require.NoError(t, err)
	unionFs := f.(*Fs)

	// A non-trivial size so the reservation is observable if it were to leak.
	src := object.NewStaticObjectInfo("fail.txt", time.Now(), 1024, true, nil, nil)
	_, err = f.Put(ctx, errReader{err: errors.New("boom")}, src)
	require.Error(t, err, "upload from a failing reader must error")

	// Even though the upload failed, the reservation made at create time must
	// have been released - nothing leaked on any branch.
	for _, u := range unionFs.upstreams {
		assert.Zero(t, u.Reserved(), "reservation leaked after failed upload on %s", u.Name())
	}

	// And the failed upload left no object behind in the union.
	_, err = f.NewObject(ctx, "fail.txt")
	assert.ErrorIs(t, err, fs.ErrorObjectNotFound, "failed upload must not leave an object")
}

// This specifically tests a union of local which can Move but not
// Copy and :memory: which can Copy but not Move to makes sure that
// the resulting union can Move
func TestMoveCopy(t *testing.T) {
	if *fstest.RemoteName != "" {
		t.Skip("Skipping as -remote set")
	}
	ctx := context.Background()
	dirs := MakeTestDirs(t, 1)
	fsString := fmt.Sprintf(":union,upstreams='%s :memory:bucket':", dirs[0])
	f, err := fs.NewFs(ctx, fsString)
	require.NoError(t, err)

	unionFs := f.(*Fs)
	fLocal := unionFs.upstreams[0].Fs
	fMemory := unionFs.upstreams[1].Fs

	if runtime.GOOS == "darwin" {
		// need to disable as this test specifically tests a local that can't Copy
		f.Features().Disable("Copy")
		fLocal.Features().Disable("Copy")
	}

	t.Run("Features", func(t *testing.T) {
		assert.NotNil(t, f.Features().Move)
		assert.Nil(t, f.Features().Copy)

		// Check underlying are as we are expect
		assert.NotNil(t, fLocal.Features().Move)
		assert.Nil(t, fLocal.Features().Copy)
		assert.Nil(t, fMemory.Features().Move)
		assert.NotNil(t, fMemory.Features().Copy)
	})

	// Put a file onto the local fs
	contentsLocal := random.String(50)
	fileLocal := fstest.NewItem("local.txt", contentsLocal, time.Now())
	_ = fstests.PutTestContents(ctx, t, fLocal, &fileLocal, contentsLocal, true)
	objLocal, err := f.NewObject(ctx, fileLocal.Path)
	require.NoError(t, err)

	// Put a file onto the memory fs
	contentsMemory := random.String(60)
	fileMemory := fstest.NewItem("memory.txt", contentsMemory, time.Now())
	_ = fstests.PutTestContents(ctx, t, fMemory, &fileMemory, contentsMemory, true)
	objMemory, err := f.NewObject(ctx, fileMemory.Path)
	require.NoError(t, err)

	fstest.CheckListing(t, f, []fstest.Item{fileLocal, fileMemory})

	t.Run("MoveLocal", func(t *testing.T) {
		fileLocal.Path = "local-renamed.txt"
		_, err := operations.Move(ctx, f, nil, fileLocal.Path, objLocal)
		require.NoError(t, err)
		fstest.CheckListing(t, f, []fstest.Item{fileLocal, fileMemory})

		// Check can retrieve object from union
		obj, err := f.NewObject(ctx, fileLocal.Path)
		require.NoError(t, err)
		assert.Equal(t, fileLocal.Size, obj.Size())

		// Check can retrieve object from underlying
		obj, err = fLocal.NewObject(ctx, fileLocal.Path)
		require.NoError(t, err)
		assert.Equal(t, fileLocal.Size, obj.Size())

		t.Run("MoveMemory", func(t *testing.T) {
			fileMemory.Path = "memory-renamed.txt"
			_, err := operations.Move(ctx, f, nil, fileMemory.Path, objMemory)
			require.NoError(t, err)
			fstest.CheckListing(t, f, []fstest.Item{fileLocal, fileMemory})

			// Check can retrieve object from union
			obj, err := f.NewObject(ctx, fileMemory.Path)
			require.NoError(t, err)
			assert.Equal(t, fileMemory.Size, obj.Size())

			// Check can retrieve object from underlying
			obj, err = fMemory.NewObject(ctx, fileMemory.Path)
			require.NoError(t, err)
			assert.Equal(t, fileMemory.Size, obj.Size())
		})
	})
}
