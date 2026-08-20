package fixture

import (
	"encoding/binary"
	"fmt"
	"math"
)

// maxFixtureSeconds bounds a generated file. The writer emits one cluster per
// second, so an unbounded span is an unbounded allocation.
const maxFixtureSeconds = 3600

// A minimal Matroska writer, here for one reason: CHAPTERS.
//
// Nothing else in this project can produce a file with chapters. There is no
// ffmetadata demuxer in any profile, no container the suite can author chapters
// into as text, and asking the engine to make one would mean copying them from a
// file that already has them — which is the thing we do not have.
//
// So the alternative to these eighty lines is that chapter behaviour is
// permanently untestable, and spec 0036's rule against engine-produced fixtures
// exists precisely to stop that being acceptable. The timings asserted against
// this file are arithmetic the test owns.
//
// It is deliberately the smallest thing libavformat will open: an EBML header, a
// Segment holding Info, one subtitle track (S_TEXT/UTF8 needs no extradata) and
// the chapter list. There are no clusters, so it carries no media — it is a
// carrier for chapters and nothing else.

// ebml element IDs, as their on-wire bytes.
var (
	idEBML      = []byte{0x1A, 0x45, 0xDF, 0xA3}
	idSegment   = []byte{0x18, 0x53, 0x80, 0x67}
	idInfo      = []byte{0x15, 0x49, 0xA9, 0x66}
	idTimeScale = []byte{0x2A, 0xD7, 0xB1}
	idTracks    = []byte{0x16, 0x54, 0xAE, 0x6B}
	idTrackNum  = []byte{0xD7}
	idTrackUID  = []byte{0x73, 0xC5}
	idTrackType = []byte{0x83}
	idCodecID   = []byte{0x86}
	idChapters  = []byte{0x10, 0x43, 0xA7, 0x70}
	idEdition   = []byte{0x45, 0xB9}
	idEditionID = []byte{0x45, 0xBC}
	idAtom      = []byte{0xB6}
	idChapUID   = []byte{0x73, 0xC4}
	idChapStart = []byte{0x91}
	idChapEnd   = []byte{0x92}
	idChapDisp  = []byte{0x80}
	idChapStr   = []byte{0x85}
	idChapLang  = []byte{0x43, 0x7C}
	idDuration  = []byte{0x44, 0x89}
	idCues      = []byte{0x1C, 0x53, 0xBB, 0x6B}
	idCuePoint  = []byte{0xBB}
	idCueTime   = []byte{0xB3}
	idCueTrkPos = []byte{0xB7}
	idCueTrack  = []byte{0xF7}
	idCueClust  = []byte{0xF1}
	idCluster   = []byte{0x1F, 0x43, 0xB6, 0x75}
	idTimecode  = []byte{0xE7}
	idSimpleBlk = []byte{0xA3}
)

// vint encodes a length as an EBML variable-size integer, always in 8 bytes so
// the caller never has to think about which width a value needs.
func vint(n uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, n)
	b[0] |= 0x01 // the 8-byte length marker
	return b
}

// el wraps a payload in its ID and length.
func el(id, payload []byte) []byte {
	out := append([]byte{}, id...)
	out = append(out, vint(uint64(len(payload)))...)
	return append(out, payload...)
}

// uintEl is an element holding an unsigned integer, big-endian, minimal width.
func uintEl(id []byte, v uint64) []byte {
	var b []byte
	if v == 0 {
		b = []byte{0}
	}
	for v > 0 {
		b = append([]byte{byte(v & 0xff)}, b...)
		v >>= 8
	}
	return el(id, b)
}

func strEl(id []byte, s string) []byte { return el(id, []byte(s)) }

// floatEl is an element holding a float64 — Matroska's Duration is a float, not
// an integer, and writing it as one makes the demuxer read past the end.
func floatEl(id []byte, v float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, math.Float64bits(v))
	return el(id, b)
}

// Chapter is one entry: start and end in seconds, and a title.
type Chapter struct {
	Start, End float64
	Title      string
}

// MatroskaWithChapters returns a Matroska file carrying exactly these chapters
// and no media. Times are stored in nanoseconds, which is Matroska's unit at the
// default timecode scale.
func MatroskaWithChapters(chapters []Chapter) []byte {
	// A fixture builder that hangs is worse than a missing fixture: it would look
	// like the engine hanging. Times are test-authored, so this is a guard against
	// a typo rather than against an attacker — but a stray 1e9 in a chapter end
	// would otherwise spin the one-cluster-per-second loop until the machine died.
	for i, c := range chapters {
		if c.Start < 0 || c.End < c.Start || c.End > maxFixtureSeconds {
			panic(fmt.Sprintf("fixture: chapter %d is %v–%vs, which is not a usable range "+
				"(0..%v)", i, c.Start, c.End, maxFixtureSeconds))
		}
	}
	header := el(idEBML, concat(
		el([]byte{0x42, 0x86}, []byte{1}), // EBMLVersion
		el([]byte{0x42, 0xF7}, []byte{1}), // EBMLReadVersion
		el([]byte{0x42, 0xF2}, []byte{4}), // EBMLMaxIDLength
		el([]byte{0x42, 0xF3}, []byte{8}), // EBMLMaxSizeLength
		strEl([]byte{0x42, 0x82}, "matroska"),
		el([]byte{0x42, 0x87}, []byte{4}), // DocTypeVersion
		el([]byte{0x42, 0x85}, []byte{2}), // DocTypeReadVersion
	))

	// Duration and one cluster are not decoration. Without them libavformat reads
	// to the end looking for media to index and fails the open with "End of file"
	// — a Matroska carrying only chapters is not something it will accept.
	var last float64
	for _, c := range chapters {
		if c.End > last {
			last = c.End
		}
	}
	// MuxingApp and WritingApp are mandatory in the Matroska schema. libav does
	// not enforce them, but a fixture that is only accidentally acceptable would
	// fail the chapter tests for a reason that has nothing to do with chapters.
	info := el(idInfo, concat(
		uintEl(idTimeScale, 1000000), // 1ms per tick
		floatEl(idDuration, last*1000),
		strEl([]byte{0x4D, 0x80}, "ffmpeg-wasi/internal/fixture"),
		strEl([]byte{0x57, 0x41}, "ffmpeg-wasi/internal/fixture"),
	))

	// One subtitle track: S_TEXT/UTF8 needs no CodecPrivate, so the file opens
	// without carrying any media at all.
	tracks := el(idTracks, el([]byte{0xAE}, concat(
		uintEl(idTrackNum, 1),
		uintEl(idTrackUID, 1),
		uintEl(idTrackType, 17), // subtitle
		strEl(idCodecID, "S_TEXT/UTF8"),
	)))

	var atoms []byte
	for i, c := range chapters {
		atoms = append(atoms, el(idAtom, concat(
			uintEl(idChapUID, uint64(i+1)),
			uintEl(idChapStart, uint64(c.Start*1e9)),
			uintEl(idChapEnd, uint64(c.End*1e9)),
			el(idChapDisp, concat(strEl(idChapStr, c.Title), strEl(idChapLang, "eng"))),
		))...)
	}
	chaps := el(idChapters, el(idEdition, concat(uintEl(idEditionID, 1), atoms)))

	// Clusters across the whole span, one per second. A single cluster at t=0 is
	// enough to OPEN the file but not to SEEK it: the demuxer scans clusters to
	// find a target, so seeking to 60s in a file whose only cluster is at zero
	// fails with "cannot seek input". A test that wants to exercise the seek needs
	// somewhere to land.
	// Clusters across the whole span, one per second, PLUS a Cues index.
	//
	// A single cluster at t=0 is enough to OPEN the file but not to SEEK it, and
	// clusters alone are not enough either: libavformat's matroska demuxer wants
	// Cues, and without them avformat_seek_file fails outright with "cannot seek
	// input". A test that needs the seek needs somewhere to land AND an index
	// saying where it is.
	//
	// Cue positions are byte offsets from the start of the Segment's DATA, which
	// is why the Cues element goes last: putting it first would make each cluster's
	// offset depend on the size of the index describing it.
	base := len(info) + len(tracks) + len(chaps)
	var clusters, cues []byte
	for ms := 0; float64(ms) <= last*1000; ms += 1000 {
		block := concat([]byte{0x81}, []byte{0x00, 0x00}, []byte{0x00}, []byte(" "))
		cluster := el(idCluster, concat(uintEl(idTimecode, uint64(ms)), el(idSimpleBlk, block)))
		cues = append(cues, el(idCuePoint, concat(
			uintEl(idCueTime, uint64(ms)),
			el(idCueTrkPos, concat(uintEl(idCueTrack, 1),
				uintEl(idCueClust, uint64(base+len(clusters))))),
		))...)
		clusters = append(clusters, cluster...)
	}

	return concat(header, el(idSegment, concat(info, tracks, chaps, clusters, el(idCues, cues))))
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// MatroskaAtTimecode returns a file whose single cluster sits at an absurd
// timecode, for exercising what the engine does with a timestamp from untrusted
// media that is near the limits of int64 (ffmpeg-wasi#44).
func MatroskaAtTimecode(ms uint64) []byte {
	header := el(idEBML, concat(
		el([]byte{0x42, 0x86}, []byte{1}),
		el([]byte{0x42, 0xF7}, []byte{1}),
		el([]byte{0x42, 0xF2}, []byte{4}),
		el([]byte{0x42, 0xF3}, []byte{8}),
		strEl([]byte{0x42, 0x82}, "matroska"),
		el([]byte{0x42, 0x87}, []byte{4}),
		el([]byte{0x42, 0x85}, []byte{2}),
	))
	info := el(idInfo, concat(uintEl(idTimeScale, 1000000), floatEl(idDuration, 1000)))
	tracks := el(idTracks, el([]byte{0xAE}, concat(
		uintEl(idTrackNum, 1), uintEl(idTrackUID, 1),
		uintEl(idTrackType, 17), strEl(idCodecID, "S_TEXT/UTF8"),
	)))
	block := concat([]byte{0x81}, []byte{0x00, 0x00}, []byte{0x00}, []byte("x"))
	cluster := el(idCluster, concat(uintEl(idTimecode, ms), el(idSimpleBlk, block)))
	return concat(header, el(idSegment, concat(info, tracks, cluster)))
}
