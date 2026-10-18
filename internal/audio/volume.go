package audio

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Volume does not depend on one sound stack. The configured ALSA mixer is tried first; when it
// fails (another card name, Ubuntu with different controls, PipeWire or PulseAudio owning the
// card) Milo looks for a usable ALSA mixer by itself, then asks PipeWire (wpctl) and PulseAudio
// (pactl) for their default output. The first one that works is remembered.

// preferredControls are the playback volume controls Milo looks for, best first.
var preferredControls = []string{"Speaker", "Master", "PCM", "Headphone", "Headphones", "Line Out", "Digital", "Playback"}

type volumeBackend struct {
	name string
	set  func(ctx context.Context, percent int) error
}

func runTool(ctx context.Context, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s failed: %s", name, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// controlRe in alsa.go validates names; this extracts 'Name' from "Simple mixer control 'Name',0".
func parseControls(scontrols string) []string {
	var out []string
	for _, line := range strings.Split(scontrols, "\n") {
		if _, rest, ok := strings.Cut(line, "'"); ok {
			if name, _, ok := strings.Cut(rest, "'"); ok {
				out = append(out, name)
			}
		}
	}
	return out
}

// autoALSA finds a card with a known playback control and sets it.
func autoALSA(ctx context.Context, percent int) error {
	for card := 0; card < 8; card++ {
		id := strconv.Itoa(card)
		out, err := runTool(ctx, "amixer", "-c", id, "scontrols")
		if err != nil {
			continue // no such card
		}
		have := map[string]bool{}
		for _, c := range parseControls(out) {
			have[c] = true
		}
		for _, want := range preferredControls {
			if have[want] {
				_, err := runTool(ctx, "amixer", "-q", "-c", id, "sset", want, fmt.Sprintf("%d%%", percent))
				return err
			}
		}
	}
	return errors.New("no ALSA card with a volume control found")
}

func (a *ALSA) backends() []volumeBackend {
	return []volumeBackend{
		{"alsa", func(ctx context.Context, p int) error {
			_, err := runTool(ctx, "amixer", "-q", "-c", a.cfg.MixerCard, "sset", a.cfg.MixerControl, fmt.Sprintf("%d%%", p))
			return err
		}},
		{"alsa-auto", autoALSA},
		{"pipewire", func(ctx context.Context, p int) error {
			_, err := runTool(ctx, "wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", fmt.Sprintf("%.2f", float64(p)/100))
			return err
		}},
		{"pulseaudio", func(ctx context.Context, p int) error {
			_, err := runTool(ctx, "pactl", "set-sink-volume", "@DEFAULT_SINK@", fmt.Sprintf("%d%%", p))
			return err
		}},
	}
}

// setVolume tries the backend that worked last time first, then the rest in order. It returns
// the name of the one that worked, or the first error when none did.
func (a *ALSA) setVolume(ctx context.Context, percent int) (string, error) {
	all := a.backends()
	last, _ := a.volBackend.Load().(string)
	ordered := make([]volumeBackend, 0, len(all))
	for _, b := range all {
		if b.name == last {
			ordered = append(ordered, b)
		}
	}
	for _, b := range all {
		if b.name != last {
			ordered = append(ordered, b)
		}
	}
	var first error
	for _, b := range ordered {
		err := b.set(ctx, percent)
		if err == nil {
			a.volBackend.Store(b.name)
			return b.name, nil
		}
		if first == nil && b.name == "alsa" {
			first = fmt.Errorf("audio: %w", err)
		}
	}
	if first == nil {
		first = errors.New("audio: no volume control worked")
	}
	return "", first
}
