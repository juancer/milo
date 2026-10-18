package audio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTools puts scripts named after the given tools first on PATH. Each script appends its
// arguments to calls.log and runs the body that was given for it.
func fakeTools(t *testing.T, bodies map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	for name, body := range bodies {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> " + log + "\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return log
}

func readLog(log string) string {
	b, _ := os.ReadFile(log)
	return string(b)
}

func newTestALSA(t *testing.T) *ALSA {
	t.Helper()
	a, err := NewALSA(DefaultALSAConfig)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestVolumeUsesConfiguredMixerFirst(t *testing.T) {
	log := fakeTools(t, map[string]string{"amixer": "exit 0"})
	a := newTestALSA(t)
	if err := a.SetVolume(context.Background(), 40); err != nil {
		t.Fatal(err)
	}
	if got := readLog(log); !strings.Contains(got, "-c wm8960soundcard sset Speaker 40%") || strings.Count(got, "\n") != 1 {
		t.Errorf("calls:\n%s", got)
	}
}

func TestVolumeFindsAnotherALSAMixer(t *testing.T) {
	// The configured card does not exist; card 1 has a PCM control.
	log := fakeTools(t, map[string]string{"amixer": `
case "$*" in
  *wm8960soundcard*) echo "Invalid card number." ; exit 1 ;;
  "-c 0 scontrols") echo "Simple mixer control 'Headphone Jack',0"; exit 0 ;;
  "-c 1 scontrols") echo "Simple mixer control 'PCM',0"; exit 0 ;;
  *scontrols*) exit 1 ;;
esac
exit 0`})
	a := newTestALSA(t)
	if err := a.SetVolume(context.Background(), 55); err != nil {
		t.Fatal(err)
	}
	if got := readLog(log); !strings.Contains(got, "-q -c 1 sset PCM 55%") {
		t.Errorf("calls:\n%s", got)
	}
	// The next call goes straight to the backend that worked.
	if err := a.SetVolume(context.Background(), 60); err != nil {
		t.Fatal(err)
	}
	if strings.Count(readLog(log), "wm8960soundcard") != 1 {
		t.Errorf("the failing backend was tried again:\n%s", readLog(log))
	}
}

func TestVolumeFallsBackToPipeWireThenPulseAudio(t *testing.T) {
	log := fakeTools(t, map[string]string{
		"amixer": "exit 1",
		"wpctl":  "exit 0",
	})
	a := newTestALSA(t)
	if err := a.SetVolume(context.Background(), 30); err != nil {
		t.Fatal(err)
	}
	if got := readLog(log); !strings.Contains(got, "wpctl set-volume @DEFAULT_AUDIO_SINK@ 0.30") {
		t.Errorf("calls:\n%s", got)
	}

	log = fakeTools(t, map[string]string{"amixer": "exit 1", "pactl": "exit 0"})
	b := newTestALSA(t)
	if err := b.SetVolume(context.Background(), 80); err != nil {
		t.Fatal(err)
	}
	if got := readLog(log); !strings.Contains(got, "pactl set-sink-volume @DEFAULT_SINK@ 80%") {
		t.Errorf("calls:\n%s", got)
	}
}

func TestVolumeReportsTheConfiguredMixerErrorWhenNothingWorks(t *testing.T) {
	fakeTools(t, map[string]string{"amixer": "echo Invalid card number.; exit 1"})
	a := newTestALSA(t)
	err := a.SetVolume(context.Background(), 50)
	if err == nil || !strings.Contains(err.Error(), "Invalid card number") {
		t.Fatalf("err = %v", err)
	}
}
