//go:build linux

package system

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func ctimeOf(p string) int64 {
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		return -1
	}
	return st.Ctim.Nano()
}

// The installer runs as root over a tree the service account can modify. A directory
// that is swapped for a symlink while chownTree is walking must not make it touch files
// outside the tree.
func TestChownTreeDoesNotFollowASymlinkSwappedInMidWalk(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "data")
	outside := filepath.Join(base, "outside") // stands in for /etc
	for _, d := range []string{filepath.Join(root, "a"), filepath.Join(root, "z"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	victim := filepath.Join(outside, "shadow")
	os.WriteFile(victim, []byte("x"), 0o600)
	os.WriteFile(filepath.Join(root, "z", "f"), []byte("x"), 0o600)
	const n = 20000 // a large sibling directory widens the window between listing and descent
	for i := 0; i < n; i++ {
		os.WriteFile(filepath.Join(root, "a", fmt.Sprintf("f%06d", i)), nil, 0o600)
	}
	link := filepath.Join(root, "zl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	before := ctimeOf(victim)
	mid := filepath.Join(root, "a", fmt.Sprintf("f%06d", n/2))
	midBefore := ctimeOf(mid)

	done := make(chan struct{})
	swapped := make(chan bool, 1)
	go func() {
		for ctimeOf(mid) == midBefore {
			select {
			case <-done:
				swapped <- false
				return
			default:
				time.Sleep(50 * time.Microsecond)
			}
		}
		// Atomically exchange the real directory "z" with the symlink "zl".
		err := unix.Renameat2(unix.AT_FDCWD, filepath.Join(root, "z"), unix.AT_FDCWD, link, unix.RENAME_EXCHANGE)
		swapped <- err == nil
	}()
	err := chownTree(root, os.Getuid(), os.Getgid())
	close(done)
	<-swapped
	if err != nil {
		t.Logf("chownTree returned %v", err) // refusing to follow the swapped entry is fine
	}
	if after := ctimeOf(victim); after != before {
		t.Fatal("chownTree changed a file outside the tree through a swapped symlink")
	}
}

func TestChownTreeReownsNestedEntries(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	leaf := filepath.Join(root, "a", "b", "f")
	os.WriteFile(leaf, []byte("x"), 0o600)
	before := ctimeOf(leaf)
	time.Sleep(10 * time.Millisecond)
	if err := chownTree(root, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if ctimeOf(leaf) == before {
		t.Fatal("a nested file was not visited")
	}
}
