package ipchost

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests speak the protocol by hand rather than through the driver. They
// need no artefact and never skip, which matters: the host is the component
// spec 0037 says has the least trustworthy estimate, and a bug in it would
// surface as a confusing engine failure somewhere else entirely.
//
// The two documented gotchas — a Read reply of 0 meaning EOF, and Write
// replying with a count — are the acceptance test D4 names.

// client is a hand-written driver side: exactly the bytes the document says the
// driver sends, and nothing borrowed from src/nativeio.c.
type client struct {
	t    *testing.T
	conn net.Conn
}

func dial(t *testing.T, sock string) *client {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dialling the host: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := conn.Write([]byte{protocolVersion}); err != nil {
		t.Fatalf("sending the version byte: %v", err)
	}
	return &client{t: t, conn: conn}
}

func (c *client) open(mode byte, name string) byte {
	c.t.Helper()
	buf := []byte{'O', mode}
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(name)))
	buf = append(buf, name...)
	if _, err := c.conn.Write(buf); err != nil {
		c.t.Fatalf("sending Open: %v", err)
	}
	var status [1]byte
	if _, err := io.ReadFull(c.conn, status[:]); err != nil {
		c.t.Fatalf("reading the Open status: %v", err)
	}
	return status[0]
}

func (c *client) read(count uint32) []byte {
	c.t.Helper()
	buf := binary.LittleEndian.AppendUint32([]byte{'R'}, count)
	if _, err := c.conn.Write(buf); err != nil {
		c.t.Fatalf("sending Read: %v", err)
	}
	var n uint32
	if err := binary.Read(c.conn, binary.LittleEndian, &n); err != nil {
		c.t.Fatalf("reading the Read count: %v", err)
	}
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(c.conn, out); err != nil {
		c.t.Fatalf("reading the Read payload: %v", err)
	}
	return out
}

func (c *client) write(body []byte) uint32 {
	c.t.Helper()
	buf := binary.LittleEndian.AppendUint32([]byte{'W'}, uint32(len(body)))
	buf = append(buf, body...)
	if _, err := c.conn.Write(buf); err != nil {
		c.t.Fatalf("sending Write: %v", err)
	}
	var n uint32
	if err := binary.Read(c.conn, binary.LittleEndian, &n); err != nil {
		c.t.Fatalf("reading the Write count: %v", err)
	}
	return n
}

func (c *client) seek(offset int64, whence byte) int64 {
	c.t.Helper()
	buf := binary.LittleEndian.AppendUint64([]byte{'S'}, uint64(offset))
	buf = append(buf, whence)
	if _, err := c.conn.Write(buf); err != nil {
		c.t.Fatalf("sending Seek: %v", err)
	}
	var pos int64
	if err := binary.Read(c.conn, binary.LittleEndian, &pos); err != nil {
		c.t.Fatalf("reading the Seek position: %v", err)
	}
	return pos
}

func (c *client) size() int64 {
	c.t.Helper()
	if _, err := c.conn.Write([]byte{'Z'}); err != nil {
		c.t.Fatalf("sending Size: %v", err)
	}
	var sz int64
	if err := binary.Read(c.conn, binary.LittleEndian, &sz); err != nil {
		c.t.Fatalf("reading the Size: %v", err)
	}
	return sz
}

func (c *client) close() {
	c.t.Helper()
	if _, err := c.conn.Write([]byte{'C'}); err != nil {
		c.t.Fatalf("sending Close: %v", err)
	}
}

func serve(t *testing.T) (*Host, string, string) {
	t.Helper()
	root := t.TempDir()
	h, sock, err := Listen(root, t.TempDir())
	if err != nil {
		t.Fatalf("starting the host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, sock, root
}

// waitForError polls until the host has recorded an error whose text contains
// want. The session runs in its own goroutine and records the failure AFTER it
// has replied to the client, so a test that checks immediately races it — and
// would fail intermittently rather than honestly.
func waitForError(t *testing.T, h *Host, want string) {
	t.Helper()
	for range 200 {
		for _, e := range h.Errors() {
			if e != nil && strings.Contains(e.Error(), want) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("the host never recorded an error containing %q; errors were %v", want, h.Errors())
}

// The first of the two documented gotchas. A Read reply of 0 is END OF FILE,
// which the driver turns into AVERROR_EOF. A host that reported a short read as
// 0, or that never sent 0 at the end, would make every decode fail or hang, and
// the failure would look like an engine bug.
func TestReadReplyOfZeroMeansEndOfFile(t *testing.T) {
	t.Parallel()

	h, sock, root := serve(t)
	body := []byte("0123456789")
	if err := os.WriteFile(filepath.Join(root, "a.bin"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	c := dial(t, sock)
	if st := c.open('r', "a.bin"); st != 0 {
		t.Fatalf("Open replied status %d, want 0", st)
	}

	if got := c.read(4); !bytes.Equal(got, body[:4]) {
		t.Errorf("first Read returned %q, want %q", got, body[:4])
	}

	// A partial read is n < count with n > 0 — NOT zero. Confusing the two is
	// the mistake this test exists for: asking for more than remains must return
	// what remains, not signal EOF.
	if got := c.read(100); !bytes.Equal(got, body[4:]) {
		t.Errorf("a Read asking for more than remains returned %q, want %q — a short read "+
			"must return the bytes it has, not report end of file", got, body[4:])
	}

	if got := c.read(4); got != nil {
		t.Errorf("a Read at end of file returned %q, want a zero count — the driver turns "+
			"zero into AVERROR_EOF, so anything else makes the decode run past the end", got)
	}

	c.close()
	if errs := h.Errors(); len(errs) != 0 {
		t.Errorf("the host reported errors: %v", errs)
	}
}

// The second documented gotcha. Write replies with the COUNT WRITTEN, which the
// driver passes straight to libav. A host replying with a status byte would send
// 0 on success — which libav reads as "wrote nothing", so every muxed file would
// come out empty or stall.
func TestWriteReplyIsACountNotAStatus(t *testing.T) {
	t.Parallel()

	h, sock, root := serve(t)

	c := dial(t, sock)
	if st := c.open('w', "out.bin"); st != 0 {
		t.Fatalf("Open replied status %d, want 0", st)
	}

	body := []byte("hello world")
	got := c.write(body)

	if got == 0 {
		t.Fatalf("Write replied 0 for %d bytes — that is a status byte, not a count. "+
			"libav would read it as 'wrote nothing'", len(body))
	}
	if got != uint32(len(body)) {
		t.Errorf("Write replied %d, want %d — the reply is the number of bytes written", got, len(body))
	}
	c.close()

	on, err := os.ReadFile(filepath.Join(root, "out.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(on, body) {
		t.Errorf("the file holds %q, want %q", on, body)
	}
	if errs := h.Errors(); len(errs) != 0 {
		t.Errorf("the host reported errors: %v", errs)
	}
}

// Write mode must open O_RDWR rather than append, because a muxer seeks
// backwards to patch what it already wrote — the non-fragmented MP4 moov/mdat
// case the document names. An appending host silently produces a corrupt file.
func TestWriteModeAllowsABackwardSeekToOverwrite(t *testing.T) {
	t.Parallel()

	h, sock, root := serve(t)

	c := dial(t, sock)
	if st := c.open('w', "out.bin"); st != 0 {
		t.Fatalf("Open replied status %d, want 0", st)
	}

	c.write([]byte("AAAABBBB"))
	if pos := c.seek(0, 0); pos != 0 { // SEEK_SET
		t.Fatalf("Seek to 0 returned %d", pos)
	}
	c.write([]byte("ZZZZ"))
	c.close()

	on, err := os.ReadFile(filepath.Join(root, "out.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "ZZZZBBBB"; string(on) != want {
		t.Errorf("the file holds %q, want %q — a backward seek must overwrite, so write mode "+
			"cannot be O_APPEND", on, want)
	}
	if errs := h.Errors(); len(errs) != 0 {
		t.Errorf("the host reported errors: %v", errs)
	}
}

func TestSeekAndSizeReportPositions(t *testing.T) {
	t.Parallel()

	_, sock, root := serve(t)
	body := make([]byte, 5000)
	if err := os.WriteFile(filepath.Join(root, "a.bin"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	c := dial(t, sock)
	c.open('r', "a.bin")

	if got := c.size(); got != 5000 {
		t.Errorf("Size returned %d, want 5000", got)
	}
	if got := c.seek(100, 0); got != 100 { // SEEK_SET
		t.Errorf("SEEK_SET to 100 returned %d", got)
	}
	if got := c.seek(50, 1); got != 150 { // SEEK_CUR
		t.Errorf("SEEK_CUR by 50 from 100 returned %d, want 150", got)
	}
	if got := c.seek(-1000, 2); got != 4000 { // SEEK_END
		t.Errorf("SEEK_END by -1000 returned %d, want 4000", got)
	}
	c.close()
}

// The engine probes for files that may not exist. That is an ordinary outcome
// and must produce a non-zero status rather than a hang or a host error.
func TestOpeningAMissingFileRepliesNonZero(t *testing.T) {
	t.Parallel()

	h, sock, _ := serve(t)

	c := dial(t, sock)
	if st := c.open('r', "nope.bin"); st == 0 {
		t.Error("Open of a missing file replied 0, which tells the driver it succeeded")
	}
	if errs := h.Errors(); len(errs) != 0 {
		t.Errorf("a missing file was recorded as a host fault: %v", errs)
	}
}

func TestAWrongVersionByteIsRefused(t *testing.T) {
	t.Parallel()

	h, sock, _ := serve(t)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{99}); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	waitForError(t, h, "protocol version 99")
}

// The engine runs untrusted media, and a filename is attacker-controlled input.
// A name that walks out of the served directory must be refused, not resolved.
func TestANameCannotEscapeTheServedDirectory(t *testing.T) {
	t.Parallel()

	h, sock, root := serve(t)

	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, name := range []string{"../secret.txt", "a/../../secret.txt", "/../secret.txt"} {
		c := dial(t, sock)
		if st := c.open('r', name); st == 0 {
			t.Errorf("Open of %q succeeded — the name escaped the served directory", name)
		}
	}

	waitForError(t, h, "resolves outside")
}

// Opened() is how a test proves the bridge carried the I/O at all. Without it, a
// driver that ignored AFMPEG_NATIVE_SOCKET and read the real filesystem would
// pass every behavioural assertion while testing nothing about the bridge.
func TestOpenedRecordsTheNamesAsked(t *testing.T) {
	t.Parallel()

	h, sock, root := serve(t)
	if err := os.WriteFile(filepath.Join(root, "a.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := dial(t, sock)
	c.open('r', "a.bin") // the reply means Open has already recorded the name
	c.close()

	got := h.Opened()
	if len(got) != 1 || got[0] != "a.bin" {
		t.Errorf("Opened() is %v, want [a.bin]", got)
	}
}

// TestASymlinkCannotEscapeTheServedDirectory is the regression test for
// ffmpeg-wasi#51.
//
// The existing escape test covers ".." segments, which are rejected lexically.
// A symlink is not lexical: filepath.Rel never touches the filesystem, so a link
// inside the served root pointing outside it passed every check while a comment
// claimed the opposite.
//
// This matters more than a harness bug usually would. The conformance suite uses
// this host to prove the ENGINE stays inside the bridge — that is the whole
// assertion behind #14's regression test — so a host more permissive than a real
// one weakens every such test.
func TestASymlinkCannotEscapeTheServedDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("OUTSIDE THE ROOT"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A link inside the served directory pointing at it.
	if err := os.Symlink(secret, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}

	h, sock, err := Listen(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })

	// Control: an ordinary file in the root opens, so a refusal below is the
	// symlink and not the host being broken.
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := dial(t, sock).open('r', "ok.txt"); st != 0 {
		t.Fatalf("an ordinary file in the served root was refused (status %d)", st)
	}

	if st := dial(t, sock).open('r', "link.txt"); st == 0 {
		t.Errorf("a symlink pointing outside the served root was opened (ffmpeg-wasi#51).\n" +
			"filepath.Rel is purely lexical and cannot resolve one, so the containment " +
			"check never saw it — while the comment above it said it did.")
	}
}

// TestTheSessionSequenceIsEnforced is the regression test for ffmpeg-wasi#52.
//
// The host exists to hold the engine to the DOCUMENT, not to whatever the engine
// happens to do (spec 0037 D4). A host more forgiving than the document is
// exactly as useless as a check looser than the fault: a driver that violates
// the contract passes conformance, and the violation is found by a real host
// later, in someone else's process.
//
// Two sequences were accepted that the ABI forbids. Both are asserted here.
func TestTheSessionSequenceIsEnforced(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, sock, err := Listen(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })

	// waitForError polls the host's error log — sessions run in their own
	// goroutine, so a protocol error is recorded shortly after the client sees the
	// connection drop, not before.
	waitForError := func(t *testing.T, want string) bool {
		t.Helper()
		for range 100 {
			for _, e := range h.Errors() {
				if strings.Contains(e.Error(), want) {
					return true
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}

	t.Run("a second Open after a FAILED one", func(t *testing.T) {
		c := dial(t, sock)
		if st := c.open('r', "no-such-file"); st == 0 {
			t.Fatal("opening a missing file reported success")
		}
		// Written by hand: a refusal shows up as the host dropping the connection,
		// which the open() helper treats as a fatal harness error rather than as
		// the result under test.
		buf := []byte{'O', 'r', 0, 0, 0, 0}
		binary.LittleEndian.PutUint32(buf[2:], uint32(len("a.txt")))
		buf = append(buf, "a.txt"...)
		_, _ = c.conn.Write(buf)

		var st [1]byte
		_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.ReadFull(c.conn, st[:]); err == nil && st[0] == 0 {
			t.Error("a second Open was accepted on the same connection after the first " +
				"failed (ffmpeg-wasi#52).\nThe contract is one connection per opened file, " +
				"and a driver reusing a connection after a failed open would pass conformance.")
		}
		if !waitForError(t, "second Open") {
			t.Error("the host did not record the second Open as a protocol error")
		}
	})

	t.Run("Close before Open", func(t *testing.T) {
		c := dial(t, sock)
		if _, err := c.conn.Write([]byte{'C'}); err != nil {
			t.Fatal(err)
		}
		if !waitForError(t, "Close frame arrived before any Open") {
			t.Error("Close arrived before Open and the host treated it as a normal end of " +
				"session (ffmpeg-wasi#52).\nA driver that never opened anything would pass.")
		}
	})
}
