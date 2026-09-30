package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
)

func withStdin(t *testing.T, text string) {
	t.Helper()
	r, w, _ := os.Pipe()
	w.WriteString(text)
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close() })
}

func TestBackupAndRestoreCommands(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	cfg := system.Config{DataDir: filepath.Join(home, "data"), KeyPath: filepath.Join(home, "etc", "master.key"), Hardware: "real", LogLevel: "info", LogFormat: "text"}
	os.MkdirAll(cfg.DataDir, 0o750)
	if _, err := secrets.GenerateKeyFile(cfg.KeyPath); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	st.SetSetting(ctx, "family.motto", "be kind")
	st.Close()

	file := filepath.Join(home, "family.nexo")
	withStdin(t, "a long backup passphrase\n")
	if err := backupCmd(ctx, cfg, []string{file, "--passphrase-stdin"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", info, err)
	}
	withStdin(t, "a long backup passphrase\n")
	if err := backupCmd(ctx, cfg, []string{file, "--passphrase-stdin"}); err == nil {
		t.Fatal("an existing backup file was overwritten")
	}
	withStdin(t, "short\n")
	if err := backupCmd(ctx, cfg, []string{filepath.Join(home, "weak.nexo"), "--passphrase-stdin"}); err == nil {
		t.Fatal("weak passphrase accepted")
	}
	if _, err := os.Stat(filepath.Join(home, "weak.nexo")); err == nil {
		t.Fatal("a failed backup left a file behind")
	}

	// A new device: different data dir, no key yet.
	fresh := system.Config{DataDir: filepath.Join(home, "new-data"), KeyPath: filepath.Join(home, "new-etc", "master.key")}
	withStdin(t, "not the passphrase at all\n")
	if err := restoreCmd(ctx, fresh, []string{file, "--passphrase-stdin"}); err == nil {
		t.Fatal("restored with the wrong passphrase")
	}
	withStdin(t, "a long backup passphrase\n")
	if err := restoreCmd(ctx, fresh, []string{file, "--passphrase-stdin"}); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(ctx, fresh.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if v, _ := restored.GetSetting(ctx, "family.motto"); v != "be kind" {
		t.Fatalf("restored motto = %q", v)
	}
	orig, _ := os.ReadFile(cfg.KeyPath)
	got, _ := os.ReadFile(fresh.KeyPath)
	if string(orig) != string(got) {
		t.Fatal("the master key was not carried over")
	}

	// Restoring over a device that has another key is refused unless asked for.
	other := system.Config{DataDir: filepath.Join(home, "other-data"), KeyPath: filepath.Join(home, "other-etc", "master.key")}
	secrets.GenerateKeyFile(other.KeyPath)
	withStdin(t, "a long backup passphrase\n")
	err = restoreCmd(ctx, other, []string{file, "--passphrase-stdin"})
	if err == nil || !strings.Contains(err.Error(), "--replace-key") {
		t.Fatalf("key mismatch = %v", err)
	}
	withStdin(t, "a long backup passphrase\n")
	if err := restoreCmd(ctx, other, []string{file, "--passphrase-stdin", "--replace-key"}); err != nil {
		t.Fatal(err)
	}
}

func TestBackupArgumentParsing(t *testing.T) {
	if _, err := parseBackupArgs(nil); err == nil {
		t.Error("missing file accepted")
	}
	if _, err := parseBackupArgs([]string{"a", "b"}); err == nil {
		t.Error("two files accepted")
	}
	if _, err := parseBackupArgs([]string{"--frobnicate", "a"}); err == nil {
		t.Error("unknown option accepted")
	}
	a, err := parseBackupArgs([]string{"f.nexo", "--no-key", "--passphrase-stdin"})
	if err != nil || a.file != "f.nexo" || !a.noKey || !a.stdin {
		t.Fatalf("args = %+v %v", a, err)
	}
}

// Run as root before nexo.db exists, the CLI must leave the files to the folder's owner.
func TestOpenCLIStoreLeavesFilesToTheFolderOwner(t *testing.T) {
	ctx := context.Background()
	cfg := system.Config{DataDir: t.TempDir()}
	st, err := openCLIStore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir, _ := os.Stat(cfg.DataDir)
	for _, ext := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(cfg.DBPath() + ext)
		if err != nil {
			t.Fatal(err)
		}
		if info.Sys().(*syscall.Stat_t).Uid != dir.Sys().(*syscall.Stat_t).Uid {
			t.Fatalf("nexo.db%s is owned by another user than the data folder", ext)
		}
	}
}
