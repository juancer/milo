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
	st, err := store.Open(ctx, filepath.Join(DefaultDataDir, "nexo.db"))
	if err != nil {
		return err
	}
	st.Close()
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
	fmt.Fprintf(out, "\nOpen:\n\n  http://%s:8080\n", localIP())
	fmt.Fprintln(out, "\nFirst, set the admin password:\n\n  sudo -u nexo nexo admin-password")
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

func chownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
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
