package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/juancer/milo/internal/system"
)

func TestOpenCLIStoreRefusesSymlinkedDB(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.Symlink(target, filepath.Join(dir, "milo.db")); err != nil {
		t.Fatal(err)
	}
	cfg := system.Config{DataDir: dir}
	if _, err := openCLIStore(context.Background(), cfg); err == nil {
		t.Fatal("symlinked milo.db was opened")
	}
	if _, err := os.Lstat(target); err == nil {
		t.Error("target was created")
	}
}
