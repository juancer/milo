// Package wakeword listens for "Hey Milo" (or whatever the parents choose)
// without sending any audio anywhere. Detection is local:
//
//   - the built-in template detector compares what it hears to a few recordings
//     of the phrase made on the device itself (pure Go, no models to download);
//   - an external detector (for example an openWakeWord or Porcupine sidecar)
//     can be plugged in through a tiny stdin/stdout protocol.
//
// Buttons keep working whether or not the wake word is on.
package wakeword

import (
	"encoding/binary"
	"errors"
	"math"
	"sync"
)

// Detector consumes 16 kHz mono 16-bit little-endian PCM and reports when the
// wake phrase was heard.
type Detector interface {
	// Process feeds audio and reports whether the phrase was detected in it.
	Process(pcm []byte) bool
	Close() error
}

const (
	blockSamples   = 320 // 20 ms
	preRollBlocks  = 5   // 100 ms kept before speech starts
	endSilenceBlks = 15  // 300 ms of quiet ends a phrase
	minPhraseMS    = 350
	maxPhraseMS    = 2500
	discardAfterMS = 3500
	cooldownMS     = 3000
	minSpeechRMS   = 300.0
)

// ThresholdFor maps the 1 (strict) - 10 (sensitive) sensitivity setting to a
// maximum distance. Distances are about 0 for a perfect repeat and about 1.4
// for unrelated sounds; real voices vary, which is why parents can watch the
// live scores in the admin and adjust.
func ThresholdFor(sensitivity int) float64 {
	sensitivity = max(1, min(10, sensitivity))
	return 0.45 + 0.07*float64(sensitivity)
}

// segmenter cuts a continuous stream into phrases using a simple energy
// detector with an adaptive noise floor.
type segmenter struct {
	carry    []byte
	noise    float64
	pre      [][]int16
	seg      []int16
	inSpeech bool
	quiet    int
}

func newSegmenter() *segmenter { return &segmenter{noise: 100} }

// feed returns any phrases completed by pcm.
func (s *segmenter) feed(pcm []byte) [][]int16 {
	data := append(s.carry, pcm...)
	n := len(data) / 2 / blockSamples * blockSamples * 2
	s.carry = append([]byte(nil), data[n:]...)
	var out [][]int16
	for off := 0; off < n; off += blockSamples * 2 {
		block := make([]int16, blockSamples)
		var sum float64
		for i := range block {
			v := int16(binary.LittleEndian.Uint16(data[off+i*2:]))
			block[i] = v
			sum += float64(v) * float64(v)
		}
		rms := math.Sqrt(sum / blockSamples)
		threshold := math.Max(minSpeechRMS, s.noise*3)
		speech := rms > threshold

		if !speech { // track the noise floor: fall fast, rise slowly
			if rms < s.noise {
				s.noise = 0.9*s.noise + 0.1*rms
			} else {
				s.noise = 0.995*s.noise + 0.005*rms
			}
		}
		switch {
		case !s.inSpeech && speech:
			s.inSpeech, s.quiet = true, 0
			s.seg = s.seg[:0]
			for _, p := range s.pre {
				s.seg = append(s.seg, p...)
			}
			s.seg = append(s.seg, block...)
		case s.inSpeech:
			s.seg = append(s.seg, block...)
			if speech {
				s.quiet = 0
			} else {
				s.quiet++
			}
			if s.quiet >= endSilenceBlks {
				out = append(out, s.finish())
			} else if len(s.seg)*1000/sampleRate > discardAfterMS {
				s.inSpeech, s.seg = false, s.seg[:0] // someone is just talking on and on
			}
		}
		s.pre = append(s.pre, block)
		if len(s.pre) > preRollBlocks {
			s.pre = s.pre[1:]
		}
	}
	return out
}

func (s *segmenter) finish() []int16 {
	// Drop most of the trailing silence (keep 100 ms).
	keepEnd := len(s.seg) - (s.quiet-preRollBlocks)*blockSamples
	if keepEnd < len(s.seg) && keepEnd > 0 {
		s.seg = s.seg[:keepEnd]
	}
	phrase := append([]int16(nil), s.seg...)
	s.inSpeech, s.seg, s.quiet = false, s.seg[:0], 0
	return phrase
}

func durationMS(samples []int16) int { return len(samples) * 1000 / sampleRate }

// TemplateDetector matches phrases against recorded templates.
type TemplateDetector struct {
	mu        sync.Mutex
	templates [][][]float64
	threshold float64
	seg       *segmenter
	pre       *Prefilter // nil: the audio goes to the detector as it is
	clock     int64      // samples processed, so cooldowns need no wall clock
	cooldown  int64
	// OnScore, if set, is called with the best distance of every candidate phrase.
	OnScore func(distance float64)
}

// NewTemplateDetector builds a detector from enrolled templates.
func NewTemplateDetector(templates [][][]float64, threshold float64) *TemplateDetector {
	return &TemplateDetector{templates: templates, threshold: threshold, seg: newSegmenter()}
}

// WithPrefilter makes the detector clean the audio first (see Prefilter). The samples it was
// enrolled from must have been cleaned the same way.
func (d *TemplateDetector) WithPrefilter() *TemplateDetector {
	d.pre = NewPrefilter()
	return d
}

// Process implements Detector.
func (d *TemplateDetector) Process(pcm []byte) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clock += int64(len(pcm) / 2)
	if d.pre != nil {
		pcm = d.pre.Process(pcm)
	}
	detected := false
	for _, phrase := range d.seg.feed(pcm) {
		if ms := durationMS(phrase); ms < minPhraseMS || ms > maxPhraseMS {
			continue
		}
		feats := Features(phrase)
		best := math.Inf(1)
		for _, t := range d.templates {
			best = math.Min(best, Distance(feats, t))
		}
		if d.OnScore != nil {
			d.OnScore(best)
		}
		if best <= d.threshold && d.clock >= d.cooldown {
			detected = true
			d.cooldown = d.clock + cooldownMS*sampleRate/1000
		}
	}
	return detected
}

// Close implements Detector.
func (d *TemplateDetector) Close() error { return nil }

// Enroll turns a recording of the phrase into a template. It picks the longest
// stretch of speech, so a little noise before or after is harmless.
func Enroll(pcm []byte) ([][]float64, error) {
	seg := newSegmenter()
	// Pad with silence so a phrase that runs to the end of the recording is closed.
	phrases := seg.feed(append(append([]byte(nil), pcm...), make([]byte, 2*blockSamples*(endSilenceBlks+2))...))
	var best []int16
	for _, p := range phrases {
		if len(p) > len(best) {
			best = p
		}
	}
	switch {
	case best == nil:
		return nil, errors.New("no speech was heard: say the phrase clearly, close to the microphone")
	case durationMS(best) < minPhraseMS:
		return nil, errors.New("that was too short: say the whole phrase")
	case durationMS(best) > maxPhraseMS:
		return nil, errors.New("that was too long: say only the wake phrase")
	}
	return Features(best), nil
}

// EncodeFeatures serialises features as little-endian float32 (13 values per frame).
func EncodeFeatures(f [][]float64) []byte {
	out := make([]byte, 0, len(f)*numCoeffs*4)
	for _, frame := range f {
		for _, v := range frame {
			out = binary.LittleEndian.AppendUint32(out, math.Float32bits(float32(v)))
		}
	}
	return out
}

// DecodeFeatures reverses EncodeFeatures.
func DecodeFeatures(b []byte) ([][]float64, error) {
	if len(b) == 0 || len(b)%(numCoeffs*4) != 0 {
		return nil, errors.New("wakeword: corrupt template")
	}
	frames := make([][]float64, len(b)/(numCoeffs*4))
	for i := range frames {
		frames[i] = make([]float64, numCoeffs)
		for j := range frames[i] {
			frames[i][j] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[(i*numCoeffs+j)*4:])))
		}
	}
	return frames, nil
}
