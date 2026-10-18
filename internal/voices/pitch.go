package voices

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"

	"github.com/hajimehoshi/go-mp3"
)

// Voice similarity. The Piper catalog says nothing about a voice's gender or age (only its name,
// quality and speaker count), so Milo does not guess from names. It measures instead: every voice
// has a short demo recording, and the median fundamental frequency (F0, the pitch of the voice)
// over the voiced stretches of that demo is a number that can be compared across languages. A
// young woman's voice sits around 200 to 260 Hz and a deep man's voice around 90 to 120 Hz, so the
// closest pitch in another language is usually a similar-sounding speaker. It matches PITCH only:
// it cannot know age, accent or character, so the suggestion has to be listened to.

const (
	minF0Hz = 60.0
	maxF0Hz = 400.0

	// maxDemosPerRequest caps how many demos one suggestion downloads and analyses (about 190 KB each).
	maxDemosPerRequest = 12
	demoParallelism    = 4
	maxAnalysedSeconds = 8 // of each demo: more adds cost, not accuracy
	minVoicedFrames    = 8
)

// EstimateF0 returns the median fundamental frequency of the voiced stretches of a mono signal, and
// false when there is not enough voiced speech to say. Frames of 40 ms are checked for energy and
// periodicity with a normalised autocorrelation over lags of 60 to 400 Hz; the shortest lag that
// is nearly as strong as the best one is taken, which avoids reading an octave too low.
func EstimateF0(x []float64, rate int) (float64, bool) {
	if rate < 8000 || len(x) == 0 {
		return 0, false
	}
	frame := rate * 40 / 1000
	hop := rate * 20 / 1000
	minLag := int(float64(rate) / maxF0Hz)
	maxLag := int(float64(rate) / minF0Hz)
	if len(x) < frame+maxLag {
		return 0, false
	}
	peak := 0.0
	for _, v := range x {
		peak = math.Max(peak, math.Abs(v))
	}
	if peak == 0 {
		return 0, false
	}
	var pitches []float64
	corr := make([]float64, maxLag+1)
	for start := 0; start+frame+maxLag <= len(x); start += hop {
		seg := x[start : start+frame+maxLag]
		var e0 float64
		for _, v := range seg[:frame] {
			e0 += v * v
		}
		if math.Sqrt(e0/float64(frame)) < 0.05*peak {
			continue // silence or a faint breath
		}
		best := 0.0
		for lag := minLag; lag <= maxLag; lag++ {
			var num, e1 float64
			for i := 0; i < frame; i++ {
				num += seg[i] * seg[i+lag]
				e1 += seg[i+lag] * seg[i+lag]
			}
			c := 0.0
			if e1 > 0 {
				c = num / math.Sqrt(e0*e1)
			}
			corr[lag] = c
			best = math.Max(best, c)
		}
		if best < 0.5 {
			continue // noise or an unvoiced sound (s, f, ...)
		}
		lag := minLag
		for l := minLag; l <= maxLag; l++ {
			if corr[l] >= 0.9*best && corr[l] >= corr[max(l-1, minLag)] && corr[l] >= corr[min(l+1, maxLag)] {
				lag = l
				break
			}
		}
		pitches = append(pitches, float64(rate)/float64(lag))
	}
	if len(pitches) < minVoicedFrames {
		return 0, false
	}
	sort.Float64s(pitches)
	return pitches[len(pitches)/2], true
}

// decodeMono decodes an MP3 into mono samples in [-1, 1]. A malformed file can make the decoder
// panic, so that is contained and reported as an error.
func decodeMono(b []byte) (samples []float64, rate int, err error) {
	defer func() {
		if r := recover(); r != nil {
			samples, rate, err = nil, 0, fmt.Errorf("voices: the demo cannot be decoded: %v", r)
		}
	}()
	dec, err := mp3.NewDecoder(bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	rate = dec.SampleRate()
	limit := rate * maxAnalysedSeconds * 4 // 16-bit stereo: 4 bytes per frame
	raw, err := io.ReadAll(io.LimitReader(dec, int64(limit)))
	if err != nil {
		return nil, 0, err
	}
	for i := 0; i+3 < len(raw); i += 4 {
		l := int16(uint16(raw[i]) | uint16(raw[i+1])<<8)
		r := int16(uint16(raw[i+2]) | uint16(raw[i+3])<<8)
		samples = append(samples, (float64(l)+float64(r))/2/32768)
	}
	return samples, rate, nil
}

// Pitch returns the median F0 of a voice's demo, analysing it once and keeping the result.
func (m *Manager) Pitch(ctx context.Context, key string) (float64, error) {
	m.mu.Lock()
	if hz, ok := m.pitch[key]; ok {
		m.mu.Unlock()
		return hz, nil
	}
	m.mu.Unlock()
	var hz float64
	if m.pitchOf != nil { // tests
		var err error
		if hz, err = m.pitchOf(ctx, key); err != nil {
			return 0, err
		}
	} else {
		mp3b, err := m.Sample(ctx, key)
		if err != nil {
			return 0, err
		}
		samples, rate, err := decodeMono(mp3b)
		if err != nil {
			return 0, err
		}
		var ok bool
		if hz, ok = EstimateF0(samples, rate); !ok {
			return 0, errors.New("voices: not enough voiced speech in the demo to measure its pitch")
		}
	}
	m.mu.Lock()
	if m.pitch == nil {
		m.pitch = map[string]float64{}
	}
	m.pitch[key] = hz
	m.mu.Unlock()
	return hz, nil
}

// Match is a candidate voice and how far its pitch is from the source voice.
type Match struct {
	Voice  Voice   `json:"voice"`
	F0     float64 `json:"f0"`      // Hz
	DiffHz float64 `json:"diff_hz"` // absolute difference from the source voice
}

// Similarity is the answer to "which voice in this language sounds like that one".
type Similarity struct {
	SourceF0   float64 `json:"source_f0"`
	Matches    []Match `json:"matches"`
	Considered int     `json:"considered"` // fast voices the language has (the only ones suggested)
	Analysed   int     `json:"analysed"`   // demos actually measured (capped)
	Capped     bool    `json:"capped"`     // some fast voices were left out to limit downloads
	Slower     int     `json:"slower"`     // voices left out because they are slow on a Raspberry Pi 3
}

// pickCandidates chooses which voices to measure: only the fast qualities (x_low and low, which a
// Pi 3 speaks in about real time; the slower ones are hidden everywhere in the picker, so they are
// never suggested), x_low first, never the source itself, at most max of them. slower counts the
// voices left out because they are slow.
func pickCandidates(vs []Voice, source string, max int) (picked []Voice, capped bool, slower int) {
	for _, v := range vs {
		switch {
		case v.Key == source:
		case !v.Fast:
			slower++
		default:
			picked = append(picked, v)
		}
	}
	sort.SliceStable(picked, func(i, j int) bool {
		a, b := picked[i], picked[j]
		if qa, qb := qualityOrder[a.Quality], qualityOrder[b.Quality]; qa != qb {
			return qa < qb
		}
		return a.Key < b.Key
	})
	if len(picked) > max {
		return picked[:max], true, slower
	}
	return picked, false, slower
}

// rankMatches orders measured candidates by closeness of pitch (faster qualities first on a tie)
// and keeps the best limit.
func rankMatches(source float64, cands []Match, limit int) []Match {
	out := append([]Match(nil), cands...)
	for i := range out {
		out[i].DiffHz = math.Abs(out[i].F0 - source)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		// Differences under half a hertz are a tie (measurement noise): bucket them so the order is consistent.
		if ba, bb := int(a.DiffHz/0.5), int(b.DiffHz/0.5); ba != bb {
			return ba < bb
		}
		if a.Voice.Fast != b.Voice.Fast {
			return a.Voice.Fast
		}
		return a.Voice.Key < b.Voice.Key
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Similar suggests voices of one language family whose pitch is closest to the voice sourceKey.
// It downloads only the demos it needs (the source's, and up to maxDemosPerRequest candidates'),
// lazily and in parallel; demos it has measured before are not fetched again.
func (m *Manager) Similar(ctx context.Context, sourceKey, family string, limit int) (Similarity, error) {
	if !ValidKey(sourceKey) {
		return Similarity{}, errors.New("voices: not a voice")
	}
	source, err := m.Pitch(ctx, sourceKey)
	if err != nil {
		return Similarity{}, err
	}
	all, err := m.List(ctx, family)
	if err != nil {
		return Similarity{}, err
	}
	picked, capped, slower := pickCandidates(all, sourceKey, maxDemosPerRequest)

	results := make([]*Match, len(picked))
	sem := make(chan struct{}, demoParallelism)
	var wg sync.WaitGroup
	for i, v := range picked {
		wg.Add(1)
		go func(i int, v Voice) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if hz, err := m.Pitch(ctx, v.Key); err == nil {
				results[i] = &Match{Voice: v, F0: hz}
			}
		}(i, v)
	}
	wg.Wait()
	var measured []Match
	for _, r := range results {
		if r != nil {
			measured = append(measured, *r)
		}
	}
	return Similarity{
		SourceF0: source, Matches: rankMatches(source, measured, limit),
		Considered: len(all) - slower, Analysed: len(measured), Capped: capped, Slower: slower,
	}, nil
}
