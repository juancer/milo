package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/juancer/nexo/internal/auth"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/systemd"
)

const (
	serviceUser = "nexo"
	binaryPath  = "/usr/local/bin/nexo"
	unitPath    = "/etc/systemd/system/nexo.service"
	configDir   = "/etc/nexo"
)

// Install sets Nexo up as a systemd service. It is idempotent: re-running it
// upgrades the binary and unit but never touches the database, the master key
// or any other user data.
func Install(ctx context.Context, out io.Writer) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("install is only supported on Linux (this is %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return errors.New("install must run as root (try: sudo nexo install)")
	}
	if runtime.GOARCH != "arm64" {
		fmt.Fprintf(out, "warning: this binary is %s; the primary target is linux/arm64 (Raspberry Pi OS 64-bit)\n", runtime.GOARCH)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemd (systemctl) not found")
	}

	if err := ensureServiceUser(out); err != nil {
		return err
	}
	u, err := user.Lookup(serviceUser)
	if err != nil {
		return fmt.Errorf("look up user %s: %w", serviceUser, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	// Hardware access: sound card and GPIO. Groups that do not exist are skipped.
	for _, g := range []string{"audio", "gpio", "netdev"} {
		if _, err := user.LookupGroup(g); err == nil {
			run("usermod", "-aG", g, serviceUser) //nolint:errcheck // best effort
		}
	}

	if err := os.MkdirAll(DefaultDataDir, 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}

	if err := installBinary(out); err != nil {
		return err
	}

	created, err := secrets.GenerateKeyFile(DefaultKeyPath)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(out, "created master key %s (root-only). Back it up: without it stored secrets cannot be decrypted.\n", DefaultKeyPath)
	}

	// Initialise the database and run migrations, then hand it to the service user.
	dbPath := filepath.Join(DefaultDataDir, "nexo.db")
	if err := CheckDBFile(DefaultDataDir, dbPath); err != nil {
		return err
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	// The first administrator gets a generated password, shown once below.
	created, adminUser, adminPass, err := auth.NewAccounts(st).EnsureFirstAdmin(ctx)
	st.Close()
	if err != nil {
		return err
	}
	if err := chownTree(DefaultDataDir, uid, gid); err != nil {
		return err
	}

	if err := os.WriteFile(unitPath, systemd.Unit, 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", "nexo.service"},
		{"restart", "nexo.service"},
	} {
		if err := run("systemctl", args...); err != nil {
			return err
		}
	}

	fmt.Fprintln(out, "\nNexo installed successfully.")
	if created {
		PrintFirstAdmin(out, fmt.Sprintf("http://%s:8080", localIP()), adminUser, adminPass)
	} else {
		fmt.Fprintf(out, "\nOpen:\n\n  http://%s:8080\n\nSign in with your existing account. Forgot the password?\n  sudo -u nexo nexo admin-reset\n", localIP())
	}
	return nil
}

func ensureServiceUser(out io.Writer) error {
	if _, err := user.Lookup(serviceUser); err == nil {
		return nil
	}
	if err := run("useradd", "--system", "--user-group", "--home-dir", DefaultDataDir,
		"--no-create-home", "--shell", "/usr/sbin/nologin", serviceUser); err != nil {
		return err
	}
	fmt.Fprintf(out, "created system user %s\n", serviceUser)
	return nil
}

// installBinary copies the running executable to binaryPath atomically.
func installBinary(out io.Writer) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if same, _ := sameFile(self, binaryPath); same {
		return nil
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(binaryPath), ".nexo-install-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), binaryPath); err != nil {
		return err
	}
	fmt.Fprintf(out, "installed %s\n", binaryPath)
	return nil
}

func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}

// chownTree re-owns root and everything below it. It runs as root over a directory the
// service account can write to, so every access goes through an os.Root: a directory
// swapped for a symlink while the walk is running cannot lead outside root.
func chownTree(root string, uid, gid int) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Lchown(".", uid, gid); err != nil {
		return err
	}
	return chownDir(r, ".", uid, gid)
}

// lchown is r.Lchown; tests replace it to imitate the service changing the tree mid-walk.
var lchown = func(r *os.Root, name string, uid, gid int) error { return r.Lchown(name, uid, gid) }

func chownDir(r *os.Root, dir string, uid, gid int) error {
	d, err := r.Open(dir)
	if errors.Is(err, fs.ErrNotExist) { // the running service removed it since it was listed
		return nil
	} else if err != nil {
		return err
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		// A file with other names is also reachable from somewhere else, perhaps outside the
		// tree: re-owning it would change that other name's owner too.
		if fi, err := r.Lstat(p); err == nil && !fi.IsDir() {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
				continue
			}
		}
		if err := lchown(r, p, uid, gid); errors.Is(err, fs.ErrNotExist) {
			continue // the service deleted it mid-walk
		} else if err != nil {
			return err
		}
		if e.IsDir() {
			if err := chownDir(r, p, uid, gid); err != nil {
				return err
			}
		}
	}
	return nil
}

// run executes a fixed command. Arguments are never derived from user input.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}

// Systemctl runs `systemctl <verb> nexo.service` with the terminal attached.
// verb is restricted to a fixed set.
func Systemctl(verb string) error {
	switch verb {
	case "start", "stop", "restart", "status":
	default:
		return fmt.Errorf("unsupported service command %q", verb)
	}
	return run("systemctl", verb, "nexo.service")
}

func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				return ipn.IP.String()
			}
		}
	}
	return "<device-ip>"
}

// PrintFirstAdmin shows the generated administrator credentials. This is the
// only time the password exists in clear text; it is written to the terminal of
// the person installing, never to a log.
func PrintFirstAdmin(out io.Writer, address, username, password string) {
	fmt.Fprintf(out, `
==================== YOUR ADMIN ACCOUNT ====================
  Open:      %s
  Username:  %s
  Password:  %s

  Write this down now: it is shown only once.
  You will be asked to choose your own password when you
  first sign in. Then add mom, dad... under the Admins tab.
  Lost it?   sudo -u nexo nexo admin-reset
============================================================
`, address, username, password)
}

// CheckDBFile refuses a database path that root should not open: the data
// directory belongs to the service user, who could have planted a symlink at
// dbPath. The path must be absent, or a regular file owned by the directory's owner.
func CheckDBFile(dataDir, dbPath string) error {
	fi, err := os.Lstat(dbPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("refusing to open %s: not a regular file (symlink or directory)", dbPath)
	}
	di, err := os.Stat(dataDir)
	if err != nil {
		return err
	}
	if fs, ok := fi.Sys().(*syscall.Stat_t); ok {
		if ds, ok := di.Sys().(*syscall.Stat_t); ok && fs.Uid != ds.Uid {
			return fmt.Errorf("refusing to open %s: not owned by the owner of %s", dbPath, dataDir)
		}
	}
	return nil
}
