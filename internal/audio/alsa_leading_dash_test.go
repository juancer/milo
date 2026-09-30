package audio

import "testing"

func TestALSAConfigRejectsLeadingDash(t *testing.T) {
	for name, mut := range map[string]func(*ALSAConfig){
		"capture":  func(c *ALSAConfig) { c.CaptureDevice = "-Dfoo" },
		"playback": func(c *ALSAConfig) { c.PlaybackDevice = "--version" },
		"card":     func(c *ALSAConfig) { c.MixerCard = "-c" },
		"control":  func(c *ALSAConfig) { c.MixerControl = "-h" },
	} {
		c := DefaultALSAConfig
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s with a leading dash was accepted", name)
		}
	}
	if err := DefaultALSAConfig.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeWAVHugeChunkSizesDoNotPanic(t *testing.T) {
	wav := EncodeWAV(make([]byte, 8), 16000)
	// An unknown chunk claiming 0xFFFFFFFF bytes before the data chunk.
	bad := append([]byte{}, wav[:36]...)
	bad = append(bad, 'j', 'u', 'n', 'k', 0xff, 0xff, 0xff, 0xff)
	bad = append(bad, wav[36:]...)
	if _, _, err := DecodeWAV(bad); err == nil {
		t.Fatal("expected an error, the data chunk is hidden behind the huge chunk")
	}
	// A data chunk whose size is >= 2 GiB takes the rest of the file.
	huge := append([]byte{}, wav...)
	copy(huge[40:44], []byte{0xff, 0xff, 0xff, 0xff})
	pcm, _, err := DecodeWAV(huge)
	if err != nil || len(pcm) != 8 {
		t.Fatalf("pcm=%d err=%v", len(pcm), err)
	}
}
