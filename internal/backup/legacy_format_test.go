package backup

import (
	"bytes"
	"testing"
)

// Backups written before the project was renamed start with "NEXOBAK1" and must still open;
// new ones start with "MILOBAK1".
func TestBackupsMadeUnderTheFormerNameStillOpen(t *testing.T) {
	plain := randomBytes(2*chunkSize + 5)

	writeMagic = legacyMagic
	old := encrypt(t, plain)
	writeMagic = magic
	t.Cleanup(func() { writeMagic = magic })

	if string(old[:8]) != "NEXOBAK1" {
		t.Fatalf("legacy file starts with %q", old[:8])
	}
	got, err := decrypt(old, pass)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("a legacy backup did not open: err=%v", err)
	}
	if fresh := encrypt(t, plain); string(fresh[:8]) != "MILOBAK1" {
		t.Fatalf("new file starts with %q", fresh[:8])
	}
}
