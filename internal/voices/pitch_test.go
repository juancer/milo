package voices

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync/atomic"
	"testing"
)

const testRate = 22050

// harmonic is a voiced-speech-like signal: a fundamental with decaying harmonics.
func harmonic(f float64, seconds float64, weights ...float64) []float64 {
	if len(weights) == 0 {
		weights = []float64{1, 0.5, 0.33, 0.25, 0.2, 0.16}
	}
	n := int(seconds * testRate)
	out := make([]float64, n)
	for i := range out {
		t := float64(i) / testRate
		for k, w := range weights {
			out[i] += w * math.Sin(2*math.Pi*f*float64(k+1)*t)
		}
		out[i] *= 0.3
	}
	return out
}

func within(got, want, percent float64) bool { return math.Abs(got-want) <= want*percent/100 }

func TestEstimateF0FindsTheFundamental(t *testing.T) {
	for _, f := range []float64{90, 120, 220, 300} {
		got, ok := EstimateF0(harmonic(f, 1.5), testRate)
		if !ok || !within(got, f, 3) {
			t.Errorf("%.0f Hz signal measured as %.1f (ok=%v)", f, got, ok)
		}
	}
}

func TestEstimateF0DoesNotReadAnOctaveLow(t *testing.T) {
	// A strong second harmonic and a weak fundamental: still 100 Hz, not 50 (out of range) or 200.
	got, ok := EstimateF0(harmonic(100, 1.5, 0.5, 1, 0.3), testRate)
	if !ok || !within(got, 100, 3) {
		t.Errorf("measured %.1f (ok=%v), want about 100", got, ok)
	}
}

func TestEstimateF0IgnoresSilenceAndSeparatesPitches(t *testing.T) {
	silence := make([]float64, testRate/2)
	signal := append(append(append([]float64{}, silence...), harmonic(220, 1)...), silence...)
	got, ok := EstimateF0(signal, testRate)
	if !ok || !within(got, 220, 3) {
		t.Errorf("tone between silences measured as %.1f (ok=%v)", got, ok)
	}
	low, _ := EstimateF0(harmonic(120, 1.5), testRate)
	high, _ := EstimateF0(harmonic(220, 1.5), testRate)
	if high-low < 80 {
		t.Errorf("120 Hz and 220 Hz came out as %.1f and %.1f: too close to tell apart", low, high)
	}
}

func TestEstimateF0RefusesWhatIsNotVoicedSpeech(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	noise := make([]float64, 2*testRate)
	for i := range noise {
		noise[i] = rng.NormFloat64() * 0.2
	}
	for name, sig := range map[string][]float64{
		"white noise": noise,
		"silence":     make([]float64, testRate),
		"too short":   harmonic(150, 0.05),
		"empty":       nil,
	} {
		if hz, ok := EstimateF0(sig, testRate); ok {
			t.Errorf("%s measured as %.1f Hz", name, hz)
		}
	}
	if _, ok := EstimateF0(harmonic(150, 1), 4000); ok {
		t.Error("a sample rate too low to carry speech was accepted")
	}
}

func voice(key, quality string, fast bool) Voice {
	return Voice{Key: key, Quality: quality, Fast: fast, Language: "es_ES"}
}

func TestRankMatchesOrdersByPitchThenPrefersFastVoices(t *testing.T) {
	cands := []Match{
		{Voice: voice("a", "medium", false), F0: 190},
		{Voice: voice("b", "low", true), F0: 215},
		{Voice: voice("c", "medium", false), F0: 205},
		{Voice: voice("d", "low", true), F0: 205.2}, // within half a hertz of c: a tie, so the fast one first
		{Voice: voice("e", "x_low", true), F0: 90},
	}
	got := rankMatches(200, cands, 3)
	if len(got) != 3 || got[0].Voice.Key != "d" || got[1].Voice.Key != "c" || got[2].Voice.Key != "a" {
		t.Fatalf("order = %v", keysOf(got))
	}
	if got[2].DiffHz != 10 {
		t.Errorf("difference = %v, want 10", got[2].DiffHz)
	}
	if all := rankMatches(200, cands, 10); len(all) != 5 || all[4].Voice.Key != "e" {
		t.Errorf("limit above the count: %v", keysOf(all))
	}
}

func keysOf(ms []Match) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Voice.Key)
	}
	return out
}

func TestPickCandidatesTakesOnlyFastVoicesAndCaps(t *testing.T) {
	var vs []Voice
	for i := 0; i < 10; i++ {
		vs = append(vs, voice(fmt.Sprintf("m%02d", i), "medium", false))
	}
	vs = append(vs, voice("h00", "high", false))
	for i := 0; i < 14; i++ {
		vs = append(vs, voice(fmt.Sprintf("f%02d", i), "low", true))
	}
	vs = append(vs, voice("x00", "x_low", true), voice("src", "low", true))
	picked, capped, slower := pickCandidates(vs, "src", 12)
	if !capped || len(picked) != 12 || slower != 11 {
		t.Fatalf("picked %d, capped=%v, slower=%d", len(picked), capped, slower)
	}
	if picked[0].Key != "x00" {
		t.Errorf("x_low should come first, got %s", picked[0].Key)
	}
	for _, v := range picked {
		if !v.Fast {
			t.Errorf("%s is slow on a Pi 3 and must never be suggested", v.Key)
		}
		if v.Key == "src" {
			t.Error("the source voice was picked as its own match")
		}
	}
	if _, capped, _ := pickCandidates(vs[11:15], "src", 12); capped {
		t.Error("capped without reaching the cap")
	}
	if got, _, slower := pickCandidates(vs[:11], "src", 12); len(got) != 0 || slower != 11 {
		t.Errorf("only slow voices: picked %d, slower %d", len(got), slower)
	}
}

func TestSimilarSuggestsTheClosestPitchInAnotherLanguageAndCachesMeasures(t *testing.T) {
	m := newHub(t).manager(t)
	var calls atomic.Int32
	// davefx is a medium voice: slow on a Pi 3, so it is hidden and never measured or suggested.
	pitches := map[string]float64{"en_US-amy-low": 220, "es_ES-carlfm-x_low": 130, "es_ES-davefx-medium": 210}
	m.pitchOf = func(_ context.Context, key string) (float64, error) {
		calls.Add(1)
		hz, ok := pitches[key]
		if !ok {
			return 0, fmt.Errorf("no demo for %s", key)
		}
		return hz, nil
	}
	ctx := context.Background()
	got, err := m.Similar(ctx, "en_US-amy-low", "es", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceF0 != 220 || got.Considered != 1 || got.Analysed != 1 || got.Capped || got.Slower != 1 {
		t.Errorf("summary = %+v", got)
	}
	if len(got.Matches) != 1 || got.Matches[0].Voice.Key != "es_ES-carlfm-x_low" || got.Matches[0].DiffHz != 90 {
		t.Fatalf("matches = %+v (the slow voice must not be suggested)", got.Matches)
	}
	before := calls.Load()
	if _, err := m.Similar(ctx, "en_US-amy-low", "es", 3); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Errorf("the demos were measured again (%d calls after %d)", calls.Load(), before)
	}
	if _, err := m.Similar(ctx, "../evil", "es", 3); err == nil {
		t.Error("an invalid source key was accepted")
	}
}

func TestSimilarSkipsDemosThatCannotBeMeasured(t *testing.T) {
	m := newHub(t).manager(t)
	m.pitchOf = func(_ context.Context, key string) (float64, error) {
		if key == "es_ES-carlfm-x_low" {
			return 0, fmt.Errorf("broken demo")
		}
		return 200, nil
	}
	got, err := m.Similar(context.Background(), "en_US-amy-low", "es", 3)
	if err != nil || got.Analysed != 0 || len(got.Matches) != 0 || got.Considered != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestFamilyAndSettingKeyAndInstalledKeys(t *testing.T) {
	if Family("es_ES-davefx-medium") != "es" || Family("en_GB-alan-low") != "en" || SettingKey("es") != "tts.voice.es" {
		t.Error("Family or SettingKey wrong")
	}
	hub := newHub(t)
	m := hub.manager(t)
	if got := m.InstalledKeys(); len(got) != 0 {
		t.Fatalf("installed before anything was downloaded: %v", got)
	}
	if err := m.Install(context.Background(), "es_ES-carlfm-x_low", nil); err != nil {
		t.Fatal(err)
	}
	waitJob(t, m)
	if got := m.InstalledKeys(); len(got) != 1 || got[0] != "es_ES-carlfm-x_low" {
		t.Errorf("installed = %v", got)
	}
}
