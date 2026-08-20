// Package ipchost serves a directory to the native driver over the
// AFMPEG_NATIVE_SOCKET bridge — spec 0037 D4, phase D2.
//
// # Why this exists when afmpeg already has one
//
// afmpeg's pkg/afmpeg/native is the reference host, and this package deliberately
// does not use it or copy it. Two reasons, and the second is the point.
//
// Layering: afmpeg is the host, ffmpeg-wasi is the engine. An engine whose tests
// depend on its host cannot tell an engine regression from a host change, which
// is the ambiguity the conformance suite exists to remove.
//
// A second implementation: afmpeg's host and this driver could agree on
// behaviour the documentation does not describe, and no test on either side
// would notice. This host is written from docs/reference/driver-invocation-abi.md
// alone — not from afmpeg's code, and not from src/nativeio.c either, because a
// host built by reading the counterparty would reproduce the counterparty's
// assumptions instead of testing the written contract. Where this and the driver
// disagree, one of them or the document is wrong, and that is worth knowing.
//
// # The contract, as the document states it
//
// The driver speaks IPC only when AFMPEG_NATIVE_SOCKET names a Unix socket. It
// dials once per opened file, and each connection is one file session. The
// connection opens with a single version byte (1), which the host validates.
// Then frames, all integers little-endian:
//
//	Open   'O', mode ('r'|'w'), nameLen u32, name   -> status u8 (0 ok)
//	Read   'R', count u32                           -> n u32, then n bytes
//	Write  'W', len u32, bytes                      -> count written u32
//	Seek   'S', offset i64, whence u8               -> new position i64
//	Size   'Z'                                      -> size i64
//	Close  'C'                                      -> nothing; ends the session
//
// Two details the document calls out because they are easy to get wrong, and
// which are the acceptance test for this package:
//
//   - A Read reply of 0 means END OF FILE, not a short read. The driver turns it
//     into AVERROR_EOF. Return the bytes you have, or 0 when there are none left.
//   - Write replies with a COUNT, not a status byte. The driver passes it
//     straight to libav as the number of bytes written.
//
// Write mode opens O_RDWR rather than append, so a muxer's backward seeks — the
// non-fragmented MP4 moov/mdat patch on av_write_trailer — can overwrite earlier
// bytes.
package ipchost

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// errInjected marks a deliberately induced failure, so a test can tell it apart
// from the host genuinely going wrong.
var errInjected = errors.New("ipchost: injected fault")

// protocolVersion is the single byte a connection opens with.
const protocolVersion = 1

// Host serves files under one root directory. Zero value is not usable; call
// Listen.
type Host struct {
	root string
	ln   net.Listener

	// Fault injection. The point of an independently-implemented host is that it
	// can misbehave deliberately: afmpeg's host would never send a short count or
	// drop a connection mid-file, so nothing else in this estate can ask the
	// engine how it copes when one does. Zero values inject nothing.
	//
	// OverstateReadBy makes a Read reply claim MORE bytes than it sends, which is
	// the malformed-host case (ffmpeg-wasi#24).
	// CloseAfterReads drops the connection after N Read frames, which is a
	// mid-stream transport failure rather than an end of file (ffmpeg-wasi#15).
	// UnderstateWriteBy makes a Write reply acknowledge FEWER bytes than were
	// handed over — what an honest host does when it runs out of room, and what a
	// buggy one does by accident. libavformat never resends the remainder, so the
	// engine must treat it as a failure (ffmpeg-wasi#45).
	OverstateReadBy   uint32
	CloseAfterReads   int
	UnderstateWriteBy uint32

	reads atomic.Int64

	mu     sync.Mutex
	errs   []error
	opened []string
}

// Listen starts a host serving root, on a Unix socket inside sockDir. It returns
// the host and the socket path to put in AFMPEG_NATIVE_SOCKET.
//
// sockDir is separate from root because the socket must not appear in the
// directory the driver is being served — it would show up as a file the engine
// could try to open.
func Listen(root, sockDir string) (*Host, string, error) {
	// A Unix socket path is limited to about 100 bytes, and a t.TempDir() path
	// under a long CI working directory can approach it. Keep the name short.
	path := filepath.Join(sockDir, "s")

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", fmt.Errorf("ipchost: listening on %s: %w", path, err)
	}

	h := &Host{root: root, ln: ln}
	go h.accept()

	return h, path, nil
}

// Close stops the host. Sessions in flight end when their connections close.
func (h *Host) Close() error { return h.ln.Close() }

// Errors reports every session failure the host saw. A driver that gets a bad
// reply may fail in a way that looks like an engine bug, so the test needs to be
// able to ask whether the host was at fault.
func (h *Host) Errors() []error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]error(nil), h.errs...)
}

// Opened reports the names the driver asked for, in order. It is how a test
// asserts that the bridge was used at all — a driver that quietly fell back to
// the real filesystem would otherwise look identical to one that worked.
func (h *Host) Opened() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.opened...)
}

func (h *Host) fail(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errs = append(h.errs, err)
}

func (h *Host) noteOpen(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opened = append(h.opened, name)
}

func (h *Host) accept() {
	for {
		conn, err := h.ln.Accept()
		if err != nil {
			return // listener closed
		}
		go func() {
			defer func() { _ = conn.Close() }()
			if err := h.session(conn); err != nil && !errors.Is(err, io.EOF) {
				h.fail(err)
			}
		}()
	}
}

// session runs one file session: the version byte, then frames until Close or
// the connection ends.
func (h *Host) session(conn net.Conn) error {
	var version [1]byte
	if _, err := io.ReadFull(conn, version[:]); err != nil {
		return fmt.Errorf("reading the version byte: %w", err)
	}
	if version[0] != protocolVersion {
		return fmt.Errorf("the driver announced protocol version %d, want %d", version[0], protocolVersion)
	}

	var f *os.File
	// A session is spent once Open has been ATTEMPTED, not once it has succeeded.
	// Tracking only `f` meant a failed Open left the connection looking untouched,
	// so a driver could open again on it — and the contract is one connection per
	// opened file whether or not the open worked (ffmpeg-wasi#52).
	attempted := false
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()

	for {
		var frame [1]byte
		if _, err := io.ReadFull(conn, frame[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil // the driver dropped the connection; that ends the session
			}
			return fmt.Errorf("reading a frame tag: %w", err)
		}

		switch frame[0] {
		case 'O':
			var err error
			if attempted {
				return errors.New("a second Open frame arrived on one connection; " +
					"the contract is one connection per opened file, and that holds " +
					"whether or not the first Open succeeded")
			}
			attempted = true
			f, err = h.doOpen(conn)
			if err != nil {
				return err
			}

		case 'C':
			// Close is the end of a session, so there has to have been one. A
			// connection that opens nothing and closes is a driver bug, and a host
			// that shrugs at it lets that bug through conformance.
			if !attempted {
				return errors.New("a Close frame arrived before any Open; " +
					"a session must open a file before it can end one")
			}
			return nil

		default:
			if f == nil {
				return fmt.Errorf("frame %q arrived before Open", frame[0])
			}
			if err := h.doFrame(conn, f, frame[0]); err != nil {
				return err
			}
		}
	}
}

// doOpen handles 'O': mode, nameLen, name. It always replies with one status
// byte, including on failure — a driver waiting for a reply that never comes
// would hang rather than report.
func (h *Host) doOpen(conn net.Conn) (*os.File, error) {
	var mode [1]byte
	if _, err := io.ReadFull(conn, mode[:]); err != nil {
		return nil, fmt.Errorf("reading the Open mode: %w", err)
	}

	var nameLen uint32
	if err := binary.Read(conn, binary.LittleEndian, &nameLen); err != nil {
		return nil, fmt.Errorf("reading the Open name length: %w", err)
	}

	name := make([]byte, nameLen)
	if _, err := io.ReadFull(conn, name); err != nil {
		return nil, fmt.Errorf("reading the Open name: %w", err)
	}
	h.noteOpen(string(name))

	full, err := h.resolve(string(name))
	if err != nil {
		_, _ = conn.Write([]byte{1})
		return nil, err
	}

	var f *os.File
	switch mode[0] {
	case 'r':
		f, err = os.Open(full)
	case 'w':
		// O_RDWR, not O_APPEND: the document requires that a muxer's backward
		// seek can overwrite bytes it already wrote.
		f, err = os.OpenFile(full, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	default:
		_, _ = conn.Write([]byte{1})
		return nil, fmt.Errorf("the Open mode is %q, want 'r' or 'w'", mode[0])
	}
	if err != nil {
		// A missing file is an ordinary outcome, not a host fault: the engine
		// probes for files that may not exist. Report it to the driver and let
		// the session end without recording a host error.
		_, _ = conn.Write([]byte{1})
		return nil, nil //nolint:nilnil // the status byte carries the failure
	}

	if _, err := conn.Write([]byte{0}); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("writing the Open status: %w", err)
	}
	return f, nil
}

func (h *Host) doFrame(conn net.Conn, f *os.File, tag byte) error {
	switch tag {
	case 'R':
		var count uint32
		if err := binary.Read(conn, binary.LittleEndian, &count); err != nil {
			return fmt.Errorf("reading the Read count: %w", err)
		}

		if h.CloseAfterReads > 0 && int(h.reads.Add(1)) > h.CloseAfterReads {
			// Vanish mid-file. The engine sees a transport failure, not an EOF.
			return errInjected
		}

		buf := make([]byte, count)
		n, err := io.ReadFull(f, buf)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("reading the file: %w", err)
		}

		if h.OverstateReadBy > 0 {
			// Claim more than we send. A host that gets its own accounting wrong
			// looks exactly like this, and the engine must not write past its buffer.
			if err := binary.Write(conn, binary.LittleEndian, uint32(n)+h.OverstateReadBy); err != nil {
				return fmt.Errorf("writing the overstated Read count: %w", err)
			}
			if n > 0 {
				if _, err := conn.Write(buf[:n]); err != nil {
					return fmt.Errorf("writing the Read payload: %w", err)
				}
			}
			return nil
		}

		// n == 0 is exactly how end of file is reported. There is no separate
		// EOF frame, and a short read is n < count with n > 0.
		if err := binary.Write(conn, binary.LittleEndian, uint32(n)); err != nil {
			return fmt.Errorf("writing the Read count: %w", err)
		}
		if n > 0 {
			if _, err := conn.Write(buf[:n]); err != nil {
				return fmt.Errorf("writing the Read payload: %w", err)
			}
		}
		return nil

	case 'W':
		var length uint32
		if err := binary.Read(conn, binary.LittleEndian, &length); err != nil {
			return fmt.Errorf("reading the Write length: %w", err)
		}

		buf := make([]byte, length)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return fmt.Errorf("reading the Write payload: %w", err)
		}

		n, err := f.Write(buf)
		if err != nil {
			return fmt.Errorf("writing to the file: %w", err)
		}

		if h.UnderstateWriteBy > 0 {
			// The bytes really are on disk; only the acknowledgement is short. That
			// isolates what is under test — whether the engine ACTS on the count —
			// from whether the data survived.
			short := uint32(n)
			if short > h.UnderstateWriteBy {
				short -= h.UnderstateWriteBy
			} else {
				short = 0
			}
			return binary.Write(conn, binary.LittleEndian, short)
		}

		// A COUNT, not a status byte. The driver hands this straight to libav.
		return binary.Write(conn, binary.LittleEndian, uint32(n))

	case 'S':
		var offset int64
		if err := binary.Read(conn, binary.LittleEndian, &offset); err != nil {
			return fmt.Errorf("reading the Seek offset: %w", err)
		}
		var whence [1]byte
		if _, err := io.ReadFull(conn, whence[:]); err != nil {
			return fmt.Errorf("reading the Seek whence: %w", err)
		}

		// AVSEEK_FORCE is already masked off by the driver, and AVSEEK_SIZE
		// arrives as a Size frame instead, so this is a plain seek.
		pos, err := f.Seek(offset, int(whence[0]))
		if err != nil {
			return fmt.Errorf("seeking to %d whence %d: %w", offset, whence[0], err)
		}
		return binary.Write(conn, binary.LittleEndian, pos)

	case 'Z':
		st, err := f.Stat()
		if err != nil {
			return fmt.Errorf("sizing the file: %w", err)
		}
		return binary.Write(conn, binary.LittleEndian, st.Size())

	default:
		return fmt.Errorf("unknown frame tag %q", tag)
	}
}

// resolve maps a name the driver sent onto a path inside root.
//
// A traversal segment is REFUSED rather than clamped. Cleaning "/../secret"
// down to "/secret" would contain the escape safely enough, but it would also
// make an attempt to leave the directory indistinguishable from an ordinary
// miss — the driver would just see "no such file". The engine runs untrusted
// media and a filename is attacker-controlled input, so an attempt to walk out
// is worth surfacing, not absorbing.
func (h *Host) resolve(name string) (string, error) {
	for _, seg := range strings.Split(filepath.ToSlash(name), "/") {
		if seg == ".." {
			return "", fmt.Errorf("the name %q contains a %q segment, which resolves outside "+
				"the served directory", name, "..")
		}
	}

	full := filepath.Join(h.root, filepath.Clean("/"+strings.TrimPrefix(name, "/")))

	// Lexical containment, which catches the cases Clean leaves behind.
	rel, err := filepath.Rel(h.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("the name %q resolves outside the served directory", name)
	}

	// And now the filesystem, because none of the above can see a symlink.
	// filepath.Rel is purely lexical — it never touches the disk — so a link
	// inside the root pointing outside it satisfied every check here while a
	// comment claimed otherwise (ffmpeg-wasi#51).
	//
	// The root is evaluated too: a served directory that is itself reached
	// through a link would otherwise fail to match its own contents.
	realRoot, err := filepath.EvalSymlinks(h.root)
	if err != nil {
		return "", fmt.Errorf("resolving the served directory: %w", err)
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		// A path that does not exist yet is normal in write mode; check the
		// parent instead, which is where a link would have to be.
		realParent, perr := filepath.EvalSymlinks(filepath.Dir(full))
		if perr != nil {
			return "", fmt.Errorf("resolving %q: %w", name, perr)
		}
		realFull = filepath.Join(realParent, filepath.Base(full))
	}
	rel, err = filepath.Rel(realRoot, realFull)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("the name %q resolves outside the served directory "+
			"through a symbolic link", name)
	}
	return full, nil
}
