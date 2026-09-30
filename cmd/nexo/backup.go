package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/juancer/nexo/internal/backup"
	"github.com/juancer/nexo/internal/system"
)

// readPassphrase gets a passphrase from stdin (--passphrase-stdin) or the terminal.
func readPassphrase(fromStdin, confirm bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("read passphrase from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("%w: stdin is not a terminal; use --passphrase-stdin", errUsage)
	}
	ask := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	first, err := ask("Backup passphrase: ")
	if err != nil || !confirm {
		return first, err
	}
	second, err := ask("Repeat passphrase: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", fmt.Errorf("the passphrases do not match")
	}
	return first, nil
}

type backupArgs struct {
	file       string
	stdin      bool
	noKey      bool
	replaceKey bool
}

func parseBackupArgs(args []string) (backupArgs, error) {
	var a backupArgs
	for _, arg := range args {
		switch arg {
		case "--passphrase-stdin":
			a.stdin = true
		case "--no-key":
			a.noKey = true
		case "--replace-key":
			a.replaceKey = true
		default:
			if strings.HasPrefix(arg, "-") || a.file != "" {
				return a, fmt.Errorf("%w: unexpected %q", errUsage, arg)
			}
			a.file = arg
		}
	}
	if a.file == "" {
		return a, fmt.Errorf("%w: give the backup file name", errUsage)
	}
	return a, nil
}

// backupCmd writes an encrypted backup file. It is safe to run while Nexo is running.
func backupCmd(ctx context.Context, cfg system.Config, args []string) error {
	a, err := parseBackupArgs(args)
	if err != nil {
		return err
	}
	if a.replaceKey {
		return fmt.Errorf("%w: --replace-key is only for restore", errUsage)
	}
	pass, err := readPassphrase(a.stdin, !a.stdin)
	if err != nil {
		return err
	}
	st, err := openCLIStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	name, _ := st.DeviceName(ctx)

	opts := backup.Options{Passphrase: pass, NexoVersion: version, DeviceName: name, WorkDir: cfg.DataDir}
	if !a.noKey {
		opts.KeyPath = cfg.KeyPath
	}
	f, err := os.OpenFile(a.file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := backup.Create(ctx, st, opts, f); err != nil {
		f.Close()
		os.Remove(a.file)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	kind := "database and master key"
	if a.noKey {
		kind = "database only (without the master key)"
	}
	fmt.Printf("backup written to %s: %s, encrypted.\nKeep the passphrase safe: without it the file cannot be opened.\n", a.file, kind)
	return nil
}

// restoreCmd installs a backup file. Nexo must be stopped first.
func restoreCmd(ctx context.Context, cfg system.Config, args []string) error {
	a, err := parseBackupArgs(args)
	if err != nil {
		return err
	}
	pass, err := readPassphrase(a.stdin, false)
	if err != nil {
		return err
	}
	f, err := os.Open(a.file)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	c, err := backup.Open(ctx, f, pass, cfg.DataDir)
	if err != nil {
		return err
	}
	defer c.Close()
	m := c.Manifest
	fmt.Printf("backup of %q made %s (Nexo %s, %s)\n", m.DeviceName, m.CreatedAt.Local().Format(time.DateTime), m.NexoVersion,
		map[bool]string{true: "with master key", false: "without master key"}[m.IncludesKey])

	previous, err := backup.Install(c, cfg.DataDir, cfg.KeyPath, a.replaceKey)
	if err != nil {
		if err == backup.ErrKeyMismatch {
			return fmt.Errorf("%w\nRun again with --replace-key to use the backup's master key (the key file %s will be replaced)", err, cfg.KeyPath)
		}
		return err
	}
	fmt.Printf("restored into %s\n", filepath.Join(cfg.DataDir, "nexo.db"))
	if previous != "" {
		fmt.Printf("the previous database was kept as %s (delete it when you are sure)\n", previous)
	}
	fmt.Println("Now start Nexo again: sudo systemctl start nexo")
	return nil
}
