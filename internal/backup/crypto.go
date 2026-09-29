package backup

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// File format ("NEXOBAK1"):
//
//	magic(8) | salt(16) | argon2 time(4) | argon2 memory KiB(4) | argon2 threads(1) | nonce prefix(16)
//	then chunks, each: length(4, big endian) | XChaCha20-Poly1305 ciphertext of up to 64 KiB
//
// The key comes from the passphrase with Argon2id. Every chunk's nonce is the
// prefix plus a counter, and the last chunk is marked in its associated data,
// together with the whole header, so chunks cannot be reordered, dropped,
// truncated or moved to another file without detection.
const (
	magic        = "NEXOBAK1"
	chunkSize    = 64 << 10
	headerLen    = 8 + 16 + 4 + 4 + 1 + 16
	argonTime    = 3
	argonMemory  = 32 * 1024 // KiB; comfortable on a 1 GB Raspberry Pi
	argonThreads = 1
)

// Errors.
var (
	ErrNotABackup      = errors.New("this is not a Nexo backup file")
	ErrWrongPassphrase = errors.New("wrong passphrase, or the file is damaged")
	ErrTruncated       = errors.New("the backup file is incomplete")
)

func deriveKey(pass string, salt []byte, t, m uint32, p uint8) []byte {
	return argon2.IDKey([]byte(pass), salt, t, m, p, chacha20poly1305.KeySize)
}

type encWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	prefix []byte
	buf    []byte
	n      uint64
	closed bool
}

// newEncWriter writes the header and returns a writer that encrypts what it is given.
func newEncWriter(w io.Writer, passphrase string) (io.WriteCloser, error) {
	header := make([]byte, headerLen)
	copy(header, magic)
	if _, err := rand.Read(header[8:24]); err != nil { // salt
		return nil, err
	}
	binary.BigEndian.PutUint32(header[24:], argonTime)
	binary.BigEndian.PutUint32(header[28:], argonMemory)
	header[32] = argonThreads
	if _, err := rand.Read(header[33:49]); err != nil { // nonce prefix
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(deriveKey(passphrase, header[8:24], argonTime, argonMemory, argonThreads))
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(header); err != nil {
		return nil, err
	}
	return &encWriter{w: w, aead: aead, header: header, prefix: header[33:49], buf: make([]byte, 0, chunkSize)}, nil
}

func nonceFor(prefix []byte, n uint64) []byte {
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	copy(nonce, prefix)
	binary.BigEndian.PutUint64(nonce[16:], n)
	return nonce
}

func aad(header []byte, final bool) []byte {
	out := append([]byte(nil), header...)
	if final {
		return append(out, 1)
	}
	return append(out, 0)
}

func (e *encWriter) seal(final bool) error {
	ct := e.aead.Seal(nil, nonceFor(e.prefix, e.n), e.buf, aad(e.header, final))
	e.n++
	e.buf = e.buf[:0]
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ct)))
	if _, err := e.w.Write(l[:]); err != nil {
		return err
	}
	_, err := e.w.Write(ct)
	return err
}

func (e *encWriter) Write(p []byte) (int, error) {
	if e.closed {
		return 0, errors.New("backup: write after close")
	}
	total := len(p)
	for len(p) > 0 {
		room := chunkSize - len(e.buf)
		take := min(room, len(p))
		e.buf = append(e.buf, p[:take]...)
		p = p[take:]
		if len(e.buf) == chunkSize {
			if err := e.seal(false); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

// Close writes the final chunk. It must be called to produce a valid file.
func (e *encWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.seal(true)
}

type decReader struct {
	r      io.Reader
	aead   cipher.AEAD
	header []byte
	prefix []byte
	n      uint64
	out    bytes.Reader
	done   bool
}

// newDecReader checks the header and returns a reader of the decrypted stream.
// A wrong passphrase is detected on the first read.
func newDecReader(r io.Reader, passphrase string) (io.Reader, error) {
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(r, header); err != nil || string(header[:8]) != magic {
		return nil, ErrNotABackup
	}
	t, m, p := binary.BigEndian.Uint32(header[24:]), binary.BigEndian.Uint32(header[28:]), header[32]
	if t < 1 || t > 10 || m < 8*1024 || m > 256*1024 || p < 1 || p > 4 { // bounds stop a crafted file from exhausting memory
		return nil, ErrNotABackup
	}
	aead, err := chacha20poly1305.NewX(deriveKey(passphrase, header[8:24], t, m, p))
	if err != nil {
		return nil, err
	}
	return &decReader{r: r, aead: aead, header: header, prefix: header[33:49]}, nil
}

func (d *decReader) Read(p []byte) (int, error) {
	for d.out.Len() == 0 {
		if d.done {
			return 0, io.EOF
		}
		var l [4]byte
		if _, err := io.ReadFull(d.r, l[:]); err != nil {
			return 0, ErrTruncated
		}
		size := binary.BigEndian.Uint32(l[:])
		if size < uint32(d.aead.Overhead()) || size > chunkSize+uint32(d.aead.Overhead()) {
			return 0, ErrWrongPassphrase
		}
		ct := make([]byte, size)
		if _, err := io.ReadFull(d.r, ct); err != nil {
			return 0, ErrTruncated
		}
		nonce := nonceFor(d.prefix, d.n)
		pt, err := d.aead.Open(nil, nonce, ct, aad(d.header, false))
		if err != nil {
			// It may be the final chunk.
			var err2 error
			if pt, err2 = d.aead.Open(nil, nonce, ct, aad(d.header, true)); err2 != nil {
				if d.n == 0 {
					return 0, ErrWrongPassphrase
				}
				return 0, fmt.Errorf("%w (chunk %d failed authentication)", ErrWrongPassphrase, d.n)
			}
			d.done = true
			var extra [1]byte
			if n, _ := d.r.Read(extra[:]); n > 0 {
				return 0, errors.New("backup: unexpected data after the end of the file")
			}
		}
		d.n++
		d.out.Reset(pt)
	}
	return d.out.Read(p)
}
