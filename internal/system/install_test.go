package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckDBFile(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "nexo.db")
	if err := CheckDBFile(dir, db); err != nil {
		t.Fatalf("absent path: %v", err)
	}
	if err := os.WriteFile(db, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckDBFile(dir, db); err != nil {
		t.Fatalf("regular file: %v", err)
	}
	os.Remove(db)

	// Dangling and existing symlinks are refused, and nothing is created at the target.
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.Symlink(target, db); err != nil {
		t.Fatal(err)
	}
	if err := CheckDBFile(dir, db); err == nil {
		t.Error("dangling symlink accepted")
	}
	if _, err := os.Lstat(target); err == nil {
		t.Error("target was created")
	}
	os.Remove(db)
	os.WriteFile(target, nil, 0o600)
	os.Symlink(target, db)
	if err := CheckDBFile(dir, db); err == nil {
		t.Error("symlink to existing file accepted")
	}
	os.Remove(db)
	os.Mkdir(db, 0o700)
	if err := CheckDBFile(dir, db); err == nil {
		t.Error("directory accepted")
	}
}
