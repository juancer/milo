package wakeword

import (
	"encoding/binary"
	"math"
	"math/cmplx"
)

// Prefilter cleans the microphone signal before the detector sees it: a 100 Hz high-pass takes
// out rumble and mains hum, and spectral noise reduction turns down steady background noise
// (an open microphone with a lot of gain hisses at about 5 % of full scale). It never sends
// audio anywhere and is deterministic: the same samples give the same output however they are
// split into chunks. The output is the input delayed by prefilterDelay samples.
//
// Spectral noise reduction: the signal is cut into 50 % overlapping frames with a square-root
// Hann window, each frequency bin is scaled by a gain computed from how far its power is above
// a slowly-tracking noise estimate, and the frames are added back together.
type Prefilter struct {
	// high-pass biquad (direct form I state)
	x1, x2, y1, y2 float64

	odd    []byte        // a half sample left over from the previous chunk
	in     []float64     // filtered samples waiting for a full frame
	acc    [preN]float64 // overlap-add buffer
	noise  [preBins]float64
	gain   [preBins]float64
	frames int
}

const (
	preN           = 512
	preHop         = preN / 2
	preBins        = preN/2 + 1
	prefilterDelay = preHop

	preAlpha  = 2.2  // how much of the noise estimate is subtracted
	preFloor  = 0.12 // lowest gain: -18 dB, so nothing is ever cut to silence
	preWarmup = 40   // frames in which the noise estimate follows the signal both ways
)

var (
	preWindow = sqrtHann(preN)
	// 2nd-order Butterworth high-pass, 100 Hz at 16 kHz.
	hpB0, hpB1, hpB2, hpA1, hpA2 = highPass(100)
)

func sqrtHann(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = math.Sqrt(0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(n))))
	}
	return w
}

func highPass(fc float64) (b0, b1, b2, a1, a2 float64) {
	w0 := 2 * math.Pi * fc / sampleRate
	alpha := math.Sin(w0) / (2 * math.Sqrt2 / 2)
	cos := math.Cos(w0)
	a0 := 1 + alpha
	return (1 + cos) / 2 / a0, -(1 + cos) / a0, (1 + cos) / 2 / a0, -2 * cos / a0, (1 - alpha) / a0
}

// NewPrefilter returns a filter at the start of a stream.
func NewPrefilter() *Prefilter {
	p := &Prefilter{in: make([]float64, preHop)} // half a frame of silence of history
	for k := range p.gain {
		p.gain[k] = 1
	}
	return p
}

// Process filters 16-bit little-endian PCM and returns what is ready, which is len(pcm) bytes on
// average (the first prefilterDelay samples are silence).
func (p *Prefilter) Process(pcm []byte) []byte {
	var out []byte
	if len(p.odd) > 0 {
		pcm = append(p.odd, pcm...)
		p.odd = nil
	}
	if len(pcm)%2 == 1 {
		p.odd = []byte{pcm[len(pcm)-1]}
		pcm = pcm[:len(pcm)-1]
	}
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		y := hpB0*v + hpB1*p.x1 + hpB2*p.x2 - hpA1*p.y1 - hpA2*p.y2
		p.x2, p.x1, p.y2, p.y1 = p.x1, v, p.y1, y
		p.in = append(p.in, y)
		for len(p.in) >= preN {
			out = p.frame(out)
		}
	}
	return out
}

// Flush returns the samples still held back, as if silence followed.
func (p *Prefilter) Flush() []byte {
	return p.Process(make([]byte, 2*preN))
}

func (p *Prefilter) frame(out []byte) []byte {
	var spec [preN]complex128
	for i := 0; i < preN; i++ {
		spec[i] = complex(p.in[i]*preWindow[i], 0)
	}
	fft(spec[:])
	for k := 0; k < preBins; k++ {
		pow := real(spec[k])*real(spec[k]) + imag(spec[k])*imag(spec[k])
		p.track(k, pow)
		g := 1.0
		if pow > 1e-9 {
			g = math.Max(preFloor, 1-preAlpha*p.noise[k]/pow)
		}
		if g < p.gain[k] { // open fast, close gently, so the start of a word is not clipped
			g = 0.6*p.gain[k] + 0.4*g
		}
		p.gain[k] = g
		spec[k] *= complex(g, 0)
		if k > 0 && k < preN/2 {
			spec[preN-k] = cmplx.Conj(spec[k])
		}
	}
	// inverse transform through the forward one: ifft(x) = conj(fft(conj(x))) / N
	for i := range spec {
		spec[i] = cmplx.Conj(spec[i])
	}
	fft(spec[:])
	for i := 0; i < preN; i++ {
		p.acc[i] += real(spec[i]) / preN * preWindow[i]
	}
	for i := 0; i < preHop; i++ {
		v := math.Max(-32768, math.Min(32767, math.Round(p.acc[i])))
		out = binary.LittleEndian.AppendUint16(out, uint16(int16(v)))
	}
	copy(p.acc[:], p.acc[preHop:])
	for i := preHop; i < preN; i++ {
		p.acc[i] = 0
	}
	p.in = p.in[preHop:]
	p.frames++
	return out
}

// track updates the noise estimate of one bin. Bins close to the estimate pull it towards the
// mean noise power; a bin far above it (speech) barely moves it, and only upwards, so speech does
// not become "noise" but a change of background still wins within a few seconds.
func (p *Prefilter) track(k int, pow float64) {
	switch {
	case p.frames < 1: // the first frame is half silence
	case p.frames == 1:
		p.noise[k] = pow
	case p.frames < preWarmup:
		p.noise[k] = 0.9*p.noise[k] + 0.1*pow
	case pow < 4*p.noise[k]:
		p.noise[k] = 0.97*p.noise[k] + 0.03*pow
	default:
		p.noise[k] *= 1.002
	}
}

// FilterAll runs a whole recording through a fresh Prefilter (for enrolling a sample). The result
// is aligned with the input and has a little silence at the end.
func FilterAll(pcm []byte) []byte {
	p := NewPrefilter()
	out := p.Process(pcm)
	out = append(out, p.Flush()...)
	return out[2*prefilterDelay:]
}
