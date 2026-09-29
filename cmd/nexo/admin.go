package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/juancer/nexo/internal/audit"
	"github.com/juancer/nexo/internal/auth"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
)

func keygen(cfg system.Config) error {
	created, err := secrets.GenerateKeyFile(cfg.KeyPath)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("created master key %s\n", cfg.KeyPath)
	} else {
		fmt.Printf("master key %s already exists; left unchanged\n", cfg.KeyPath)
	}
	return nil
}

// adminPassword sets the web admin password. It is done from the device
// (not the web UI) so nobody on the network can claim an unconfigured admin.
func adminPassword(ctx context.Context, cfg system.Config, args []string) error {
	fromStdin := false
	for _, a := range args {
		if a != "--password-stdin" {
			return fmt.Errorf("%w: unknown option %q", errUsage, a)
		}
		fromStdin = true
	}

	var pw string
	if fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("read password from stdin: %w", err)
		}
		pw = strings.TrimRight(line, "\r\n")
	} else {
		var err error
		if pw, err = promptPassword(); err != nil {
			return err
		}
	}
	if err := auth.ValidatePassword(pw); err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.SetSetting(ctx, "admin.password_hash", hash); err != nil {
		return err
	}
	log, err := system.NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return err
	}
	audit.New(st, log).Record(ctx, "cli", "admin.password_set", "", nil)
	fmt.Println("admin password updated (existing web sessions are signed out)")
	return nil
}

func promptPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("%w: stdin is not a terminal; use --password-stdin", errUsage)
	}
	fmt.Fprint(os.Stderr, "New admin password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", fmt.Errorf("passwords do not match")
	}
	return string(first), nil
}
