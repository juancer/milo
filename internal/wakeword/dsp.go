package wakeword

import (
	"math"
	"math/cmplx"
)

// Speech features for the template wake-word detector: MFCCs (mel-frequency
// cepstral coefficients), the classic compact description of how speech sounds.
// Pure Go, no dependencies, cheap enough for a Raspberry Pi 3B.

const (
	sampleRate = 16000
	frameLen   = 400 // 25 ms
	frameHop   = 160 // 10 ms
	fftSize    = 512
	melBands   = 24
	numCoeffs  = 13
	minFreq    = 100.0
	maxFreq    = 5000.0
)

var (
	window     = hamming(frameLen)
	melFilters = buildMelFilters()
	dctTable   = buildDCT()
)

func hamming(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(n-1))
	}
	return w
}

func hzToMel(f float64) float64 { return 2595 * math.Log10(1+f/700) }
func melToHz(m float64) float64 { return 700 * (math.Pow(10, m/2595) - 1) }

func buildMelFilters() [][]float64 {
	lo, hi := hzToMel(minFreq), hzToMel(maxFreq)
	bins := make([]int, melBands+2)
	for i := range bins {
		hz := melToHz(lo + (hi-lo)*float64(i)/float64(melBands+1))
		bins[i] = int(math.Floor(float64(fftSize+1) * hz / sampleRate))
	}
	filters := make([][]float64, melBands)
	for m := 0; m < melBands; m++ {
		f := make([]float64, fftSize/2+1)
		for k := bins[m]; k < bins[m+1]; k++ {
			f[k] = float64(k-bins[m]) / float64(max(1, bins[m+1]-bins[m]))
		}
		for k := bins[m+1]; k < bins[m+2]; k++ {
			f[k] = float64(bins[m+2]-k) / float64(max(1, bins[m+2]-bins[m+1]))
		}
		filters[m] = f
	}
	return filters
}

func buildDCT() [][]float64 {
	t := make([][]float64, numCoeffs)
	for c := range t {
		t[c] = make([]float64, melBands)
		for m := range t[c] {
			t[c][m] = math.Cos(math.Pi * float64(c) * (float64(m) + 0.5) / melBands)
		}
	}
	return t
}

// fft is an in-place radix-2 Cooley-Tukey transform; len(x) must be a power of two.
func fft(x []complex128) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Rect(1, -2*math.Pi/float64(size))
		for start := 0; start < n; start += size {
			wn := complex(1, 0)
			for k := 0; k < size/2; k++ {
				a, b := x[start+k], x[start+k+size/2]*wn
				x[start+k], x[start+k+size/2] = a+b, a-b
				wn *= w
			}
		}
	}
}

// Features converts 16-bit samples into normalised MFCC frames. Each
// coefficient is scaled to zero mean and unit variance over the utterance, so
// the result does not depend on how loud or how far away the speaker was.
func Features(samples []int16) [][]float64 {
	if len(samples) < frameLen {
		return nil
	}
	x := make([]float64, len(samples))
	prev := 0.0
	for i, s := range samples { // pre-emphasis lifts the high frequencies speech needs
		v := float64(s)
		x[i] = v - 0.97*prev
		prev = v
	}
	nFrames := (len(x)-frameLen)/frameHop + 1
	out := make([][]float64, nFrames)
	buf := make([]complex128, fftSize)
	mel := make([]float64, melBands)
	for f := 0; f < nFrames; f++ {
		for i := range buf {
			buf[i] = 0
		}
		off := f * frameHop
		for i := 0; i < frameLen; i++ {
			buf[i] = complex(x[off+i]*window[i], 0)
		}
		fft(buf)
		for m := 0; m < melBands; m++ {
			var e float64
			for k, w := range melFilters[m] {
				if w > 0 {
					re, im := real(buf[k]), imag(buf[k])
					e += w * (re*re + im*im)
				}
			}
			mel[m] = math.Log(e + 1e-3)
		}
		c := make([]float64, numCoeffs)
		for i := range c {
			for m, v := range mel {
				c[i] += dctTable[i][m] * v
			}
		}
		out[f] = c
	}
	normalise(out)
	return out
}

// normalise applies per-coefficient mean and variance normalisation in place.
func normalise(frames [][]float64) {
	n := float64(len(frames))
	for d := 0; d < numCoeffs; d++ {
		var mean float64
		for _, f := range frames {
			mean += f[d]
		}
		mean /= n
		var variance float64
		for _, f := range frames {
			variance += (f[d] - mean) * (f[d] - mean)
		}
		std := math.Sqrt(variance/n) + 1e-6
		for _, f := range frames {
			f[d] = (f[d] - mean) / std
		}
	}
}

// Distance is the dynamic-time-warping distance between two feature sequences,
// tolerant of the phrase being said faster or slower. It is the average
// per-coefficient RMS difference along the best alignment: about 0 for
// identical speech and about 1.4 for unrelated sounds.
func Distance(a, b [][]float64) float64 {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return math.Inf(1)
	}
	band := max(n, m)/2 + 1 // a wide Sakoe-Chiba band: the phrase may be stretched a lot
	inf := math.Inf(1)
	prev := make([]float64, m+1)
	cur := make([]float64, m+1)
	steps := make([]int, m+1)
	prevSteps := make([]int, m+1)
	for j := range prev {
		prev[j] = inf
	}
	prev[0] = 0
	for i := 1; i <= n; i++ {
		for j := range cur {
			cur[j] = inf
		}
		for j := max(1, i-band); j <= min(m, i+band); j++ {
			cost := frameDistance(a[i-1], b[j-1])
			best, bs := prev[j-1], prevSteps[j-1] // diagonal
			if prev[j] < best {
				best, bs = prev[j], prevSteps[j]
			}
			if cur[j-1] < best {
				best, bs = cur[j-1], steps[j-1]
			}
			if math.IsInf(best, 1) {
				continue
			}
			cur[j], steps[j] = best+cost, bs+1
		}
		prev, cur = cur, prev
		prevSteps, steps = steps, prevSteps
	}
	if math.IsInf(prev[m], 1) || prevSteps[m] == 0 {
		return inf
	}
	return prev[m] / float64(prevSteps[m])
}

func frameDistance(a, b []float64) float64 {
	var s float64
	for i := range a {
		d := a[i] - b[i]
		s += d * d
	}
	return math.Sqrt(s / float64(len(a)))
}
