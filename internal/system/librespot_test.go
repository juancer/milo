package system

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func librespotArchive(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestInstallLibrespot(t *testing.T) {
	archive := librespotArchive(t, "go-librespot", []byte("#!/bin/sh\n"))
	sum := sha256.Sum256(archive)
	asset := librespotAsset{"x.tar.gz", hex.EncodeToString(sum[:])}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(archive)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "go-librespot")

	if err := installLibrespot(context.Background(), io.Discard, dest, srv.URL+"/", asset); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dest)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("installed file: %v %v", fi, err)
	}
	if err := installLibrespot(context.Background(), io.Discard, dest, srv.URL+"/", asset); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("existing install was downloaded again (%d requests)", hits)
	}
}

func TestInstallLibrespotRefusesBadDownloads(t *testing.T) {
	archive := librespotArchive(t, "go-librespot", []byte("x"))
	sum := sha256.Sum256(archive)
	good := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		w.Write(archive)
	}))
	defer srv.Close()

	for name, a := range map[string]librespotAsset{
		"wrong checksum": {"x.tar.gz", strings.Repeat("0", 64)},
		"http error":     {"missing", good},
	} {
		dest := filepath.Join(t.TempDir(), "go-librespot")
		if err := installLibrespot(context.Background(), io.Discard, dest, srv.URL+"/", a); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Stat(dest); err == nil {
			t.Errorf("%s: file left behind", name)
		}
	}
}
