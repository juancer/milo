package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/juancer/nexo/internal/audit"
	"github.com/juancer/nexo/internal/auth"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
)

// openCLIStore opens the database and, when run as root, hands the files it may have just
// created to the folder's owner: the nexo service could not open root-owned ones.
func openCLIStore(ctx context.Context, cfg system.Config) (*store.Store, error) {
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(cfg.DataDir); err == nil {
		if sys, ok := info.Sys().(*syscall.Stat_t); ok {
			for _, ext := range []string{"", "-wal", "-shm"} {
				// Through the open file, not the path, so a swapped-in symlink is not followed.
				if f, err := os.OpenFile(cfg.DBPath()+ext, os.O_RDONLY|syscall.O_NOFOLLOW, 0); err == nil {
					f.Chown(int(sys.Uid), int(sys.Gid)) //nolint:errcheck
					f.Close()
				}
			}
		}
	}
	return st, nil
}

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

// parseUserFlag reads "--user name" and returns the remaining options.
func parseUserFlag(args []string) (user string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--user" {
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("%w: --user needs a name", errUsage)
			}
			user = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return user, rest, nil
}

// targetAdmin resolves --user (default: the owner).
func targetAdmin(ctx context.Context, st *store.Store, accounts *auth.Accounts, name string) (store.AdminUser, error) {
	if name == "" {
		return accounts.Owner(ctx)
	}
	u, err := st.GetAdminByName(ctx, name)
	if err != nil {
		return store.AdminUser{}, fmt.Errorf("no administrator called %q", name)
	}
	return u, nil
}

// adminPassword sets an administrator's password from the device's own shell,
// for someone who has lost access. It is done here rather than in the browser
// so nobody on the network can take over an account.
func adminPassword(ctx context.Context, cfg system.Config, args []string) error {
	user, rest, err := parseUserFlag(args)
	if err != nil {
		return err
	}
	fromStdin := false
	for _, a := range rest {
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
	} else if pw, err = promptPassword(); err != nil {
		return err
	}
	if err := auth.ValidatePassword(pw); err != nil {
		return err
	}
	st, err := openCLIStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	accounts := auth.NewAccounts(st)
	target, err := targetAdmin(ctx, st, accounts, user)
	if err != nil {
		return err
	}
	if err := accounts.SetPassword(ctx, target.ID, pw, false); err != nil {
		return err
	}
	auditCLI(ctx, cfg, st, "admin.password_set", target.Username)
	fmt.Printf("password for %q updated (their web sessions are signed out)\n", target.Username)
	return nil
}

// adminReset gives an administrator a new generated password, printed once.
func adminReset(ctx context.Context, cfg system.Config, args []string) error {
	user, rest, err := parseUserFlag(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("%w: unknown option %q", errUsage, rest[0])
	}
	st, err := openCLIStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	accounts := auth.NewAccounts(st)
	target, err := targetAdmin(ctx, st, accounts, user)
	if err != nil {
		return err
	}
	pw, err := accounts.Reset(ctx, target.ID)
	if err != nil {
		return err
	}
	auditCLI(ctx, cfg, st, "admin.password_reset", target.Username)
	system.PrintFirstAdmin(os.Stdout, "http://<this-device>"+cfg.Listen, target.Username, pw)
	return nil
}

func auditCLI(ctx context.Context, cfg system.Config, st *store.Store, action, target string) {
	if log, err := system.NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat); err == nil {
		audit.New(st, log).Record(ctx, "cli", action, target, nil)
	}
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
