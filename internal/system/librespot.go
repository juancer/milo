package system

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// go-librespot is GPL-3.0 and Milo is Apache-2.0, so it is never linked or embedded: install
// downloads the pinned upstream release, checks its SHA-256 and drops the program next to Milo.
const (
	librespotVersion = "v0.10.5"
	librespotBase    = "https://github.com/devgianlu/go-librespot/releases/download/" + librespotVersion + "/"
	librespotPath    = "/usr/local/bin/go-librespot"
)

type librespotAsset struct{ name, sha256 string }

// librespotAssets maps GOARCH to the release archive. 32-bit Raspberry Pi OS uses the armv6 build.
var librespotAssets = map[string]librespotAsset{
	"arm64": {"go-librespot_linux_arm64.tar.gz", "35274c1d3576090096d3459ecf76ee9c025258d19dc76f0bf724f95c525467e1"},
	"arm":   {"go-librespot_linux_armv6.tar.gz", "963e6143839e1a3ef2b551ec2275bf0bdba8555f7b93bd1efedaf71e52b0685d"},
	"amd64": {"go-librespot_linux_x86_64.tar.gz", "f08647217d1dced339375680d731bb5fc2410a9d693f2e1235303511c35a051c"},
}

// maxLibrespotBinary caps the unpacked program; the real one is about 15 MB.
const maxLibrespotBinary = 64 << 20

// installLibrespot makes sure dest holds go-librespot. An existing file is left alone, so an
// upgrade of Milo never replaces a copy the owner installed by hand.
func installLibrespot(ctx context.Context, out io.Writer, dest, base string, a librespotAsset) error {
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+a.name, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", a.name, resp.Status)
	}
	archive, err := os.CreateTemp(filepath.Dir(dest), ".librespot-dl-*")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(archive, h), resp.Body); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.sha256 {
		return fmt.Errorf("download %s: checksum mismatch (got %s)", a.name, got)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s has no go-librespot program", a.name)
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == "go-librespot" {
			break
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".librespot-install-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(tr, maxLibrespotBinary+1))
	if err == nil && n > maxLibrespotBinary {
		err = errors.New("go-librespot is larger than expected")
	}
	if err != nil {
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
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return err
	}
	fmt.Fprintf(out, "installed %s (go-librespot %s, for Spotify)\n", dest, librespotVersion)
	return nil
}

// installLibrespotDefault is the install step. Spotify is optional, so a failure (no Internet,
// unsupported architecture) is reported but never stops the install.
func installLibrespotDefault(ctx context.Context, out io.Writer) {
	a, ok := librespotAssets[runtime.GOARCH]
	if !ok {
		fmt.Fprintf(out, "note: no go-librespot build for %s; Spotify stays unavailable\n", runtime.GOARCH)
		return
	}
	if err := installLibrespot(ctx, out, librespotPath, librespotBase, a); err != nil {
		fmt.Fprintf(out, "warning: could not install go-librespot, Spotify stays unavailable: %v\n", err)
	}
}
