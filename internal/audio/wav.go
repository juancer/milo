package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

// The sample rates DecodeWAV accepts.
const (
	MinWAVRate = 8000
	MaxWAVRate = 192000
)

// EncodeWAV wraps 16-bit little-endian mono PCM in a WAV container.
func EncodeWAV(pcm []byte, rate int) []byte {
	var b bytes.Buffer
	b.Grow(44 + len(pcm))
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

// DecodeWAV extracts PCM data and the sample rate from a 16-bit mono WAV file.
func DecodeWAV(wav []byte) (pcm []byte, rate int, err error) {
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, 0, errors.New("audio: not a WAV file")
	}
	pos := 12
	for pos+8 <= len(wav) {
		id := string(wav[pos : pos+4])
		size := int64(binary.LittleEndian.Uint32(wav[pos+4 : pos+8])) // int64: int is 32 bits on ARMv7
		body := pos + 8
		switch id {
		case "fmt ":
			if body+16 > len(wav) {
				return nil, 0, errors.New("audio: truncated fmt chunk")
			}
			format := binary.LittleEndian.Uint16(wav[body:])
			channels := binary.LittleEndian.Uint16(wav[body+2:])
			rate = int(binary.LittleEndian.Uint32(wav[body+4:]))
			bits := binary.LittleEndian.Uint16(wav[body+14:])
			if rate < MinWAVRate || rate > MaxWAVRate {
				return nil, 0, fmt.Errorf("audio: unsupported sample rate %d Hz (need %d to %d)", rate, MinWAVRate, MaxWAVRate)
			}
			if format != 1 || channels != 1 || bits != 16 {
				return nil, 0, fmt.Errorf("audio: unsupported WAV (format %d, %d channels, %d bits); need 16-bit mono PCM", format, channels, bits)
			}
		case "data":
			end := len(wav) // streamed WAVs may carry a bogus size
			if size != 0 && int64(body)+size <= int64(len(wav)) {
				end = body + int(size)
			}
			if rate == 0 {
				return nil, 0, errors.New("audio: data before fmt chunk")
			}
			return wav[body:end], rate, nil
		}
		next := int64(body) + size + size%2
		if next > int64(len(wav)) {
			break
		}
		pos = int(next)
	}
	return nil, 0, errors.New("audio: no data chunk")
}

// Duration returns the playing time of a WAV file (0 if it cannot be parsed).
func Duration(wav []byte) time.Duration {
	pcm, rate, err := DecodeWAV(wav)
	if err != nil || rate == 0 {
		return 0
	}
	return time.Duration(len(pcm)/2) * time.Second / time.Duration(rate)
}

// rms returns the root-mean-square level of 16-bit little-endian PCM.
func rms(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		s := float64(int16(binary.LittleEndian.Uint16(pcm[i*2:])))
		sum += s * s
	}
	return math.Sqrt(sum / float64(n))
}

// Silence returns PCM silence of the given length.
func Silence(d time.Duration) []byte {
	return make([]byte, int(d.Seconds()*SampleRate)*2)
}
