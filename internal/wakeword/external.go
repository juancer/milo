package wakeword

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// External runs a helper program that does the detection, for example an
// openWakeWord or Porcupine wrapper. This is how neural detectors (which need
// Python or a vendor library) can be used without putting Python on the device
// by default.
//
// Protocol: the helper receives raw 16 kHz mono 16-bit little-endian PCM on
// standard input and prints one line on standard output each time it hears
// the wake phrase. The path is set by an administrator in the admin UI. It
// takes no arguments and must be an absolute path to an executable that only
// its owner can modify.
type External struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	detected atomic.Bool
	audio    chan []byte
	done     chan struct{}
	once     sync.Once
}

var externalPathRe = regexp.MustCompile(`^/[A-Za-z0-9_./-]{1,200}$`)

// ValidateExternalPath checks that path is a safe helper to run.
func ValidateExternalPath(path string) error {
	if !externalPathRe.MatchString(path) || strings.Contains(path, "..") {
		return errors.New("the helper must be an absolute path without spaces or ..")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("helper not found: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("the helper must be an executable file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("the helper must not be writable by group or others")
	}
	return nil
}

// NewExternal starts the helper.
func NewExternal(ctx context.Context, path string) (*External, error) {
	if err := ValidateExternalPath(path); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot start the wake-word helper: %w", err)
	}
	e := &External{cmd: cmd, stdin: stdin, audio: make(chan []byte, 64), done: make(chan struct{})}
	go func() { // detections
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			e.detected.Store(true)
		}
		e.Close()
	}()
	go func() { // audio writer: never lets a slow helper stall the microphone loop
		defer stdin.Close()
		for {
			select {
			case b := <-e.audio:
				if _, err := stdin.Write(b); err != nil {
					return
				}
			case <-e.done:
				return
			}
		}
	}()
	return e, nil
}

// Process implements Detector.
func (e *External) Process(pcm []byte) bool {
	select {
	case e.audio <- append([]byte(nil), pcm...):
	default: // helper is behind: drop audio rather than block
	}
	return e.detected.Swap(false)
}

// Alive reports whether the helper is still running.
func (e *External) Alive() bool {
	select {
	case <-e.done:
		return false
	default:
		return true
	}
}

// Close implements Detector.
func (e *External) Close() error {
	e.once.Do(func() {
		close(e.done)
		if e.cmd.Process != nil {
			e.cmd.Process.Kill() //nolint:errcheck
		}
		go e.cmd.Wait() //nolint:errcheck
	})
	return nil
}

// Mock is a Detector triggered by hand: the development simulator's "say the
// wake word" button and tests use it.
type Mock struct{ fired atomic.Bool }

// Trigger makes the next Process call report a detection.
func (m *Mock) Trigger() { m.fired.Store(true) }

// Process implements Detector.
func (m *Mock) Process([]byte) bool { return m.fired.Swap(false) }

// Close implements Detector.
func (m *Mock) Close() error { return nil }
