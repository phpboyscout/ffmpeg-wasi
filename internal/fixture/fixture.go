// Package fixture builds the media the behavioural suite feeds to the engine.
//
// Everything here is written in pure Go from the standard library, and NOTHING
// here uses the engine. That is the point: phase C asserts what the engine does
// with an input, so the input has to be something we know independently. A
// fixture produced by the engine would make a wrong answer agree with itself —
// encode 30 frames wrongly, decode them back to the same wrong number, and the
// test passes while the engine is broken.
//
// So the properties a test asserts are the ones stated here: this many samples at
// this rate is exactly this long, this image is exactly these dimensions. The
// engine is then measured against arithmetic rather than against itself.
//
// The formats are chosen to be readable by the LEAN profile — WAV/pcm_s16le and
// PNG — so no behavioural test has to skip for want of a codec (spec 0036 D7).
package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
)

// PNG returns a w×h opaque RGBA image, PNG-encoded.
//
// The pattern is a per-frame colour ramp rather than a flat fill: a flat image
// compresses to almost nothing and encodes identically whatever the frame index,
// which would let a "the frames landed" assertion pass on an engine that wrote
// the same frame N times. seed shifts the hue so consecutive frames genuinely
// differ.
func PNG(w, h, seed int) ([]byte, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("fixture: PNG needs positive dimensions, got %dx%d", w, h)
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{
				R: uint8((x*255/max(w-1, 1) + seed*37) % 256),
				G: uint8((y*255/max(h-1, 1) + seed*61) % 256),
				B: uint8((seed * 91) % 256),
				A: 255,
			})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("fixture: encoding PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// WAVDuration is the exact duration of the WAV that WAV(rate, channels, samples)
// returns. Stated as arithmetic so a test asserts the engine against a number
// nothing in this package or the engine computed for it.
func WAVDuration(rate, samples int) float64 { return float64(samples) / float64(rate) }

// WAV returns a mono-or-stereo 16-bit PCM WAV of exactly `samples` frames at
// `rate` Hz, carrying a 440 Hz sine.
//
// Hand-built rather than taken from a library so the byte layout is visible: a
// canonical 44-byte RIFF/WAVE header followed by interleaved little-endian
// samples. A test asserting "the engine read 2.0 seconds" is asserting against
// the sample count written here.
func WAV(rate, channels, samples int) ([]byte, error) {
	if rate <= 0 || samples < 0 {
		return nil, fmt.Errorf("fixture: WAV needs a positive rate and non-negative samples, got %d/%d", rate, samples)
	}
	if channels != 1 && channels != 2 {
		return nil, fmt.Errorf("fixture: WAV supports 1 or 2 channels, got %d", channels)
	}

	const bitsPerSample = 16
	blockAlign := channels * bitsPerSample / 8
	dataLen := samples * blockAlign

	var b bytes.Buffer
	w32 := func(v uint32) { binary.Write(&b, binary.LittleEndian, v) } //nolint:errcheck // bytes.Buffer never fails
	w16 := func(v uint16) { binary.Write(&b, binary.LittleEndian, v) } //nolint:errcheck

	b.WriteString("RIFF")
	w32(uint32(36 + dataLen)) // size of everything after this field
	b.WriteString("WAVE")

	b.WriteString("fmt ")
	w32(16)                        // PCM fmt chunk size
	w16(1)                         // format: PCM
	w16(uint16(channels))          //
	w32(uint32(rate))              //
	w32(uint32(rate * blockAlign)) // byte rate
	w16(uint16(blockAlign))        //
	w16(bitsPerSample)             //

	b.WriteString("data")
	w32(uint32(dataLen))
	for i := range samples {
		v := int16(math.Sin(2*math.Pi*440*float64(i)/float64(rate)) * 0.5 * math.MaxInt16)
		for range channels {
			w16(uint16(v))
		}
	}

	return b.Bytes(), nil
}
