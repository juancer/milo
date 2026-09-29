package wakeword

import (
	"context"
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------- synthetic speech

// A "word" is a sequence of vowel-like syllables, each a harmonic tone shaped by
// two formant peaks. It is not real speech, but it exercises the whole pipeline
// with sounds that differ the way words do.
type syllable struct {
	f1, f2 float64
	ms     int
}

var (
	heyNexo = []syllable{{500, 1800, 150}, {400, 2200, 110}, {550, 1700, 140}, {650, 1100, 110}, {450, 900, 230}}
	others  = map[string][]syllable{
		"good morning":    {{700, 1200, 200}, {450, 900, 120}, {300, 2200, 150}, {550, 1600, 130}, {350, 2000, 220}},
		"hello there":     {{600, 1500, 130}, {550, 1700, 100}, {600, 900, 200}, {350, 2100, 140}, {500, 1500, 180}},
		"what is that":    {{650, 1100, 140}, {650, 1100, 90}, {380, 2000, 110}, {700, 1300, 210}, {600, 1000, 110}},
		"play some music": {{400, 2200, 100}, {500, 1800, 170}, {450, 850, 180}, {350, 800, 150}, {600, 1500, 200}},
	}
)

func speak(word []syllable, f0, stretch, gain float64, noise float64, rng *rand.Rand) []int16 {
	var out []int16
	phase := 0.0
	for _, s := range word {
		n := int(float64(s.ms) * stretch * sampleRate / 1000)
		for i := 0; i < n; i++ {
			t := float64(i) / float64(n)
			env := math.Sin(math.Pi * t) // syllable envelope
			f := f0 * (1 + 0.08*math.Sin(2*math.Pi*3*t))
			phase += 2 * math.Pi * f / sampleRate
			var v float64
			for h := 1; float64(h)*f < 4500; h++ {
				hf := float64(h) * f
				a := math.Exp(-math.Pow((hf-s.f1)/130, 2)) + 0.6*math.Exp(-math.Pow((hf-s.f2)/220, 2)) + 0.02
				v += a * math.Sin(float64(h)*phase)
			}
			sample := gain*3500*env*v/4 + noise*rng.NormFloat64()
			out = append(out, int16(math.Max(-32000, math.Min(32000, sample))))
		}
	}
	return out
}

func silenceSamples(ms int, noise float64, rng *rand.Rand) []int16 {
	n := ms * sampleRate / 1000
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(noise * rng.NormFloat64())
	}
	return out
}

func toBytes(s []int16) []byte {
	b := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

// stream builds "room noise, the phrase, room noise".
func stream(phrase []int16, rng *rand.Rand) []byte {
	var all []int16
	all = append(all, silenceSamples(1200, 40, rng)...)
	all = append(all, phrase...)
	all = append(all, silenceSamples(1000, 40, rng)...)
	return toBytes(all)
}

func enrolled(t *testing.T, sensitivity int) (*TemplateDetector, *[]float64) {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	var templates [][][]float64
	for _, v := range []struct{ f0, stretch float64 }{{125, 1.0}, {135, 0.92}, {118, 1.08}} {
		tpl, err := Enroll(stream(speak(heyNexo, v.f0, v.stretch, 1, 25, rng), rng))
		if err != nil {
			t.Fatalf("enroll: %v", err)
		}
		templates = append(templates, tpl)
	}
	d := NewTemplateDetector(templates, ThresholdFor(sensitivity))
	scores := &[]float64{}
	d.OnScore = func(x float64) { *scores = append(*scores, x) }
	return d, scores
}

func feedAll(d Detector, pcm []byte, chunk int) bool {
	hit := false
	for i := 0; i < len(pcm); i += chunk {
		if d.Process(pcm[i:min(i+chunk, len(pcm))]) {
			hit = true
		}
	}
	return hit
}

// ---------------------------------------------------------------- tests

func TestDetectsTheEnrolledPhraseDespiteVariation(t *testing.T) {
	d, scores := enrolled(t, 5)
	rng := rand.New(rand.NewSource(2))
	// Same phrase again: a bit slower/faster, higher/lower voice, quieter/louder, noisier room.
	variants := []struct{ f0, stretch, gain, noise float64 }{
		{125, 1.0, 1.0, 30}, {132, 0.9, 0.6, 60}, {120, 1.12, 1.5, 40}, {128, 1.05, 0.35, 50}, {136, 0.95, 1.0, 80},
	}
	for i, v := range variants {
		d.cooldown = 0
		if !feedAll(d, stream(speak(heyNexo, v.f0, v.stretch, v.gain, v.noise, rng), rng), 1600) {
			t.Errorf("variant %d not detected (scores %.2f)", i, *scores)
		}
	}
	t.Logf("true-positive distances: %.2f (threshold %.2f)", *scores, ThresholdFor(5))
}

func TestRejectsOtherPhrasesAndNoise(t *testing.T) {
	d, scores := enrolled(t, 5)
	rng := rand.New(rand.NewSource(3))
	for name, w := range others {
		for _, f0 := range []float64{115, 130, 145} {
			d.cooldown = 0
			if feedAll(d, stream(speak(w, f0, 1.0, 1, 30, rng), rng), 1600) {
				t.Errorf("%q at f0=%v triggered the wake word", name, f0)
			}
		}
	}
	// Loud non-speech noise and a long monologue never trigger it.
	d.cooldown = 0
	noise := toBytes(silenceSamples(4000, 2500, rng))
	if feedAll(d, noise, 3200) {
		t.Error("loud noise triggered the wake word")
	}
	long := speak(append(append([]syllable{}, others["good morning"]...), others["hello there"]...), 125, 2, 1, 30, rng)
	if feedAll(d, stream(long, rng), 1600) {
		t.Error("a long monologue triggered the wake word")
	}
	if feedAll(d, toBytes(silenceSamples(3000, 30, rng)), 1600) {
		t.Error("silence triggered the wake word")
	}
	t.Logf("all candidate distances: %.2f", *scores)
}

func TestSeparationBetweenPhraseAndOthers(t *testing.T) {
	// The detector only works if the same phrase scores well below everything else.
	rng := rand.New(rand.NewSource(4))
	tpl, _ := Enroll(stream(speak(heyNexo, 125, 1, 1, 25, rng), rng))
	score := func(w []syllable, f0, stretch float64) float64 {
		var phrases [][]int16
		s := newSegmenter()
		phrases = s.feed(stream(speak(w, f0, stretch, 1, 30, rng), rng))
		if len(phrases) == 0 {
			t.Fatal("no phrase segmented")
		}
		return Distance(Features(phrases[0]), tpl)
	}
	same := math.Max(score(heyNexo, 132, 0.93), score(heyNexo, 122, 1.1))
	minOther := math.Inf(1)
	for _, w := range others {
		minOther = math.Min(minOther, score(w, 125, 1))
	}
	t.Logf("worst same-phrase distance %.2f, best other-phrase distance %.2f", same, minOther)
	if same >= minOther {
		t.Fatalf("no margin: same phrase %.2f vs other %.2f", same, minOther)
	}
	if thr := ThresholdFor(5); !(same < thr && thr < minOther) {
		t.Fatalf("default threshold %.2f does not fall between %.2f and %.2f", thr, same, minOther)
	}
}

func TestChunkSizeDoesNotMatter(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	pcm := stream(speak(heyNexo, 127, 1.02, 1, 30, rng), rng)
	for _, chunk := range []int{320, 641, 1600, 3200, 9999} { // including odd byte counts
		d, _ := enrolled(t, 5)
		if !feedAll(d, pcm, chunk) {
			t.Errorf("chunk size %d: not detected", chunk)
		}
	}
}

func TestCooldownAvoidsDoubleTriggers(t *testing.T) {
	d, _ := enrolled(t, 5)
	rng := rand.New(rand.NewSource(6))
	first := d.Process(stream(speak(heyNexo, 126, 1, 1, 30, rng), rng))
	second := d.Process(stream(speak(heyNexo, 126, 1, 1, 30, rng), rng)) // immediately after
	if !first || second {
		t.Fatalf("first=%v second=%v; the second must be suppressed by the cooldown", first, second)
	}
}

func TestSensitivityMovesTheThreshold(t *testing.T) {
	if !(ThresholdFor(1) < ThresholdFor(5) && ThresholdFor(5) < ThresholdFor(10)) {
		t.Fatal("higher sensitivity must accept more")
	}
	if ThresholdFor(-5) != ThresholdFor(1) || ThresholdFor(99) != ThresholdFor(10) {
		t.Fatal("sensitivity must be clamped")
	}
}

func TestEnrollRejectsBadRecordings(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	if _, err := Enroll(toBytes(silenceSamples(2500, 30, rng))); err == nil {
		t.Error("silence accepted as a sample")
	}
	blip := speak([]syllable{{500, 1500, 120}}, 125, 1, 1, 20, rng)
	if _, err := Enroll(stream(blip, rng)); err == nil {
		t.Error("a 120 ms blip accepted")
	}
	monologue := speak(append(append([]syllable{}, others["good morning"]...), others["hello there"]...), 125, 2, 1, 20, rng)
	if _, err := Enroll(stream(monologue, rng)); err == nil {
		t.Error("a long sentence accepted as a wake phrase")
	}
}

func TestFeatureSerialisationRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	f, err := Enroll(stream(speak(heyNexo, 125, 1, 1, 25, rng), rng))
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeFeatures(EncodeFeatures(f))
	if err != nil || len(back) != len(f) {
		t.Fatalf("round trip: %d frames, %v", len(back), err)
	}
	if d := Distance(f, back); d > 1e-3 {
		t.Fatalf("round trip changed the features (distance %v)", d)
	}
	for _, bad := range [][]byte{nil, {1, 2, 3}, make([]byte, 13*4+1)} {
		if _, err := DecodeFeatures(bad); err == nil {
			t.Errorf("corrupt template %v accepted", len(bad))
		}
	}
}

func TestDistanceProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	a := Features(speak(heyNexo, 125, 1, 1, 20, rng))
	if d := Distance(a, a); d > 1e-9 {
		t.Errorf("distance to itself = %v", d)
	}
	if Distance(nil, a) < 1e9 || Distance(a, nil) < 1e9 {
		t.Error("empty sequence must be infinitely far")
	}
	b := Features(speak(others["hello there"], 125, 1, 1, 20, rng))
	if math.Abs(Distance(a, b)-Distance(b, a)) > 0.05 {
		t.Errorf("distance is not symmetric: %v vs %v", Distance(a, b), Distance(b, a))
	}
}

func TestExternalHelperProtocol(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper.sh")
	// Prints a line once it has received 32000 bytes (one second of audio).
	os.WriteFile(helper, []byte("#!/bin/sh\nhead -c 32000 > /dev/null\necho DETECTED\ncat > /dev/null\n"), 0o755)

	if err := ValidateExternalPath(helper); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ext, err := NewExternal(ctx, helper)
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Close()

	chunk := make([]byte, 3200)
	deadline := time.Now().Add(5 * time.Second)
	hit := false
	for time.Now().Before(deadline) && !hit {
		hit = ext.Process(chunk)
		time.Sleep(10 * time.Millisecond)
	}
	if !hit {
		t.Fatal("helper detection never reported")
	}
	if ext.Process(chunk) {
		t.Fatal("a detection must be reported once")
	}
	ext.Close()
	if ext.Alive() {
		t.Fatal("still alive after Close")
	}
	ext.Process(chunk) // must not panic or block after Close
}

func TestExternalPathValidation(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok")
	os.WriteFile(ok, []byte("#!/bin/sh\n"), 0o755)
	writable := filepath.Join(dir, "writable")
	os.WriteFile(writable, []byte("#!/bin/sh\n"), 0o755)
	os.Chmod(writable, 0o777)
	noexec := filepath.Join(dir, "noexec")
	os.WriteFile(noexec, []byte("x"), 0o644)
	if err := ValidateExternalPath(ok); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{
		"relative":       "helper.sh",
		"parent":         dir + "/../etc/passwd",
		"spaces":         dir + "/a b",
		"shell":          "/bin/sh; rm -rf /",
		"world writable": writable,
		"not executable": noexec,
		"missing":        dir + "/missing",
		"directory":      dir,
	} {
		if ValidateExternalPath(p) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestMockDetector(t *testing.T) {
	var m Mock
	if m.Process(nil) {
		t.Fatal("fired without a trigger")
	}
	m.Trigger()
	if !m.Process(nil) || m.Process(nil) {
		t.Fatal("trigger must fire exactly once")
	}
}
