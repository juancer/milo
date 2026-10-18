package wakeword

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
)

func pcmOf(samples []float64) []byte {
	out := make([]byte, 0, len(samples)*2)
	for _, s := range samples {
		out = binary.LittleEndian.AppendUint16(out, uint16(int16(math.Max(-32768, math.Min(32767, s)))))
	}
	return out
}

func rmsOf(pcm []byte, from, to int) float64 {
	var sum float64
	n := 0
	for i := from; i < to && 2*i+1 < len(pcm); i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
		sum += v * v
		n++
	}
	return math.Sqrt(sum / float64(n))
}

// noisyTone is steady noise (rms 1800) for 2 s, then 0.5 s of a 1 kHz tone on top of it.
func noisyTone() (samples []float64, burstFrom, burstTo int) {
	r := rand.New(rand.NewSource(1))
	n := 3 * sampleRate
	burstFrom, burstTo = 2*sampleRate, 2*sampleRate+sampleRate/2
	for i := 0; i < n; i++ {
		v := r.NormFloat64() * 1800
		if i >= burstFrom && i < burstTo {
			v += 6000 * math.Sin(2*math.Pi*1000*float64(i)/sampleRate)
		}
		samples = append(samples, v)
	}
	return
}

func TestPrefilterTurnsDownSteadyNoiseAndKeepsSpeechLikeSounds(t *testing.T) {
	samples, from, to := noisyTone()
	in := pcmOf(samples)
	out := NewPrefilter().Process(in)
	noiseIn, noiseOut := rmsOf(in, sampleRate, 2*sampleRate), rmsOf(out, sampleRate, 2*sampleRate)
	if noiseOut > noiseIn*0.35 {
		t.Errorf("noise only went from %.0f to %.0f, want at least 9 dB less", noiseIn, noiseOut)
	}
	// Compare the burst with the tone alone (6000/sqrt2): it must come through nearly whole.
	burst := rmsOf(out, from+prefilterDelay, to)
	if burst < 3000 {
		t.Errorf("the tone came out at %.0f, want most of 4243", burst)
	}
	if ratio := burst / noiseOut; ratio < 2*(4243/1800.0) {
		t.Errorf("signal-to-noise only improved to %.1f (was %.1f)", ratio, 4243/1800.0)
	}
}

func TestPrefilterRemovesRumble(t *testing.T) {
	var s []float64
	for i := 0; i < 2*sampleRate; i++ {
		s = append(s, 8000*math.Sin(2*math.Pi*30*float64(i)/sampleRate))
	}
	in := pcmOf(s)
	out := NewPrefilter().Process(in)
	if got := rmsOf(out, sampleRate, 2*sampleRate); got > 0.1*rmsOf(in, sampleRate, 2*sampleRate) {
		t.Errorf("30 Hz rumble left at %.0f", got)
	}
}

func TestPrefilterGivesTheSameOutputHoweverTheAudioIsChunked(t *testing.T) {
	samples, _, _ := noisyTone()
	in := pcmOf(samples)
	whole := NewPrefilter().Process(in)
	p := NewPrefilter()
	var chunked []byte
	for i := 0; i < len(in); i += 333 {
		chunked = append(chunked, p.Process(in[i:min(i+333, len(in))])...)
	}
	if !bytes.Equal(whole, chunked) {
		t.Fatal("chunking changed the output")
	}
	if got, want := len(whole), len(in)-2*prefilterDelay+2*(prefilterDelay)-2*(len(in)/2%preHop); got < len(in)-2*preN || got > len(in) {
		t.Errorf("output length %d for input %d (want %d at most)", got, len(in), want)
	}
}

func TestFilterAllIsAlignedWithTheInput(t *testing.T) {
	samples, from, to := noisyTone()
	in := pcmOf(samples)
	out := FilterAll(in)
	if len(out) < len(in) {
		t.Fatalf("output %d shorter than input %d", len(out), len(in))
	}
	if rmsOf(out, from, to) < 3000 {
		t.Error("the burst is not where the input had it")
	}
}

// A loud, hissing microphone (noise at about 5 % of full scale, like an open WM8960 mic) with the
// phrase on top. Without the prefilter the phrase is not even found (the speech threshold is three
// times the noise). With the prefilter on both sides, enrolled voices are recognised and other
// phrases are still rejected.
func TestPrefilterDetectsInLoudNoise(t *testing.T) {
	const noise = 1500
	rng := rand.New(rand.NewSource(7))
	loud := func(word []syllable, f0, stretch, gain float64) []byte {
		var all []int16
		all = append(all, silenceSamples(1500, noise, rng)...)
		all = append(all, speak(word, f0, stretch, gain, noise, rng)...)
		all = append(all, silenceSamples(1200, noise, rng)...)
		return toBytes(all)
	}
	var templates [][][]float64
	for _, v := range []struct{ f0, stretch float64 }{{125, 1.0}, {135, 0.92}, {118, 1.08}} {
		tpl, err := Enroll(FilterAll(loud(heyMilo, v.f0, v.stretch, 2.5)))
		if err != nil {
			t.Fatalf("enroll: %v", err)
		}
		templates = append(templates, tpl)
	}
	var scores []float64
	detect := func(pcm []byte) bool {
		d := NewTemplateDetector(templates, ThresholdFor(7)).WithPrefilter()
		d.OnScore = func(x float64) { scores = append(scores, x) }
		return feedAll(d, pcm, 1600)
	}
	hits := 0
	for _, v := range []struct{ f0, stretch float64 }{{125, 1.0}, {130, 0.95}, {120, 1.1}, {128, 1.02}} {
		if detect(loud(heyMilo, v.f0, v.stretch, 2.5)) {
			hits++
		}
	}
	t.Logf("scores %.2f", scores)
	if hits < 3 {
		t.Errorf("only %d of 4 repeats were detected in loud noise", hits)
	}
	for name, w := range others {
		if detect(loud(w, 130, 1.0, 2.5)) {
			t.Errorf("%q triggered the wake word in loud noise", name)
		}
	}
}
