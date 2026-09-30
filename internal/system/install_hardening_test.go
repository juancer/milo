//go:build linux

package system

import (
	"os"
	"path/filepath"
	"testing"
)

// The running service can delete files while the installer walks the tree; that must not
// leave the upgrade half-applied, and hard-linked files (other names may lie outside the tree)
// are left alone.
func TestChownTreeToleratesVanishedEntriesAndSkipsHardLinks(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	for _, f := range []string{"a", "gone", "sub/b", "z"} {
		os.WriteFile(filepath.Join(root, f), []byte("x"), 0o600)
	}
	os.WriteFile(filepath.Join(root, "sub", "b"), []byte("x"), 0o600)
	if err := os.Link(filepath.Join(root, "a"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	var chowned []string
	real := lchown
	t.Cleanup(func() { lchown = real })
	lchown = func(r *os.Root, name string, uid, gid int) error {
		if name == "gone" {
			r.Remove(name) // the service deletes it between the listing and the chown
		}
		chowned = append(chowned, name)
		return real(r, name, uid, gid)
	}
	if err := chownTree(root, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("chownTree failed on a vanished entry: %v", err)
	}
	seen := map[string]bool{}
	for _, n := range chowned {
		seen[n] = true
	}
	for _, n := range []string{"sub", "sub/b", "z", "gone"} {
		if !seen[n] {
			t.Errorf("%s was not visited (visited %v)", n, chowned)
		}
	}
	if seen["a"] || seen["linked"] {
		t.Errorf("a hard-linked file was re-owned: %v", chowned)
	}
}
