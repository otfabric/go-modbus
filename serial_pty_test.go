//go:build darwin || linux

// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// These tests exercise the serial port wrapper and the serial client
// transports against a pseudo-terminal: the client opens the slave side like a
// real serial device while the test plays the remote device on the master
// side. They are skipped when no pseudo-terminal can be allocated.

// openTestPTY returns the master side of a fresh pseudo-terminal and the
// device path of its slave side.
func openTestPTY(t *testing.T) (master *os.File, slavePath string) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	slavePath, err = ptySlavePath(master)
	if err != nil {
		_ = master.Close()
		t.Skipf("cannot resolve pseudo-terminal slave: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	return master, slavePath
}

// openPTYSerial opens a serial port wrapper on the slave side of a fresh
// pseudo-terminal.
func openPTYSerial(t *testing.T) (*serialPortWrapper, *os.File) {
	t.Helper()
	master, slavePath := openTestPTY(t)
	spw := newSerialPortWrapper(&serialPortConfig{Device: slavePath, Speed: 19200, DataBits: 8, StopBits: 1})
	if err := spw.Open(); err != nil {
		t.Skipf("serial port cannot be opened on a pseudo-terminal here: %v", err)
	}
	t.Cleanup(func() { _ = spw.Close() })
	return spw, master
}

// readWithTimeout reads exactly n bytes from r, failing the test rather than
// hanging if they do not arrive.
func readWithTimeout(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	type result struct {
		buf []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, n)
		_, err := io.ReadFull(r, buf)
		done <- result{buf, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("read: %v", res.err)
		}
		return res.buf
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out reading %d bytes", n)
		return nil
	}
}

func TestSerialWrapper_PTY_ReadWriteDeadline(t *testing.T) {
	spw, master := openPTYSerial(t)

	if err := spw.Open(); err == nil {
		t.Fatal("second Open on an open port: expected an error")
	}

	// Bytes written through the wrapper reach the remote side unmodified,
	// including values a terminal line discipline would otherwise translate.
	tx := []byte{0x11, 0x03, 0x00, 0x0A, 0x0D, 0x7F, 0xFF, 0x00}
	if n, err := spw.Write(tx); err != nil || n != len(tx) {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(tx))
	}
	if got := readWithTimeout(t, master, len(tx)); !bytes.Equal(got, tx) {
		t.Fatalf("remote side received % X, want % X", got, tx)
	}

	// With nothing to read, the port's short poll timeout is reported as an
	// empty read rather than an error.
	if err := spw.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if n, err := spw.Read(make([]byte, 8)); n != 0 || err != nil {
		t.Fatalf("idle Read = (%d, %v), want (0, nil)", n, err)
	}

	// Bytes sent by the remote side are delivered, possibly across reads.
	rx := []byte{0x11, 0x03, 0x02, 0x0D, 0x0A, 0x80}
	if _, err := master.Write(rx); err != nil {
		t.Fatalf("master write: %v", err)
	}
	if got := readWithTimeout(t, spw, len(rx)); !bytes.Equal(got, rx) {
		t.Fatalf("wrapper received % X, want % X", got, rx)
	}

	// Once the deadline has passed Read fails fast, without touching the
	// port: pending bytes stay queued for the next request.
	if _, err := master.Write([]byte{0x42}); err != nil {
		t.Fatalf("master write: %v", err)
	}
	if err := spw.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if n, err := spw.Read(make([]byte, 8)); n != 0 || !errors.Is(err, ErrRequestTimedOut) {
		t.Fatalf("Read after deadline = (%d, %v), want (0, ErrRequestTimedOut)", n, err)
	}
	// Writes ignore the deadline.
	if _, err := spw.Write([]byte{0x99}); err != nil {
		t.Fatalf("Write after read deadline: %v", err)
	}
	if got := readWithTimeout(t, master, 1); got[0] != 0x99 {
		t.Fatalf("remote side received %#x, want 0x99", got[0])
	}
	// Clearing the deadline makes the queued byte readable again.
	if err := spw.SetDeadline(time.Time{}); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if got := readWithTimeout(t, spw, 1); got[0] != 0x42 {
		t.Fatalf("wrapper received %#x, want 0x42", got[0])
	}

	if err := spw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := spw.Read(make([]byte, 1)); !errors.Is(err, ErrSerialPortNotOpen) {
		t.Fatalf("Read after Close: want ErrSerialPortNotOpen, got %v", err)
	}
	if _, err := spw.Write([]byte{0x00}); !errors.Is(err, ErrSerialPortNotOpen) {
		t.Fatalf("Write after Close: want ErrSerialPortNotOpen, got %v", err)
	}
	if err := spw.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// servePTY plays a serial device on the master side: it reads one request
// with readReq, records it, and answers with respond's result, until the
// client side is closed.
func servePTY(master *os.File, readReq func(io.Reader) ([]byte, error), respond func(req []byte) []byte) <-chan []byte {
	reqs := make(chan []byte, 16)
	go func() {
		defer close(reqs)
		for {
			req, err := readReq(master)
			if err != nil {
				return
			}
			reqs <- req
			if _, err := master.Write(respond(req)); err != nil {
				return
			}
		}
	}()
	return reqs
}

func TestRTUClient_OverPTY(t *testing.T) {
	master, slavePath := openTestPTY(t)

	c, err := New(Config{URL: "rtu://" + slavePath, Speed: 19200, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Skipf("serial port cannot be opened on a pseudo-terminal here: %v", err)
	}
	defer func() { _ = c.Close() }()
	if info := c.Info(); !info.IsOpen || info.Transport != TransportRTU || info.Endpoint != slavePath || info.PoolEnabled {
		t.Fatalf("unexpected Info %+v", info)
	}

	reqs := servePTY(master,
		func(r io.Reader) ([]byte, error) {
			frame := make([]byte, 8)
			_, err := io.ReadFull(r, frame)
			return frame, err
		},
		func(req []byte) []byte {
			if req[1] == 0x06 {
				return req // FC06 echoes the request
			}
			return adu.AssembleRTUFrame(req[0], req[1], []byte{0x04, 0xAE, 0x41, 0x56, 0x52})
		})

	ctx := context.Background()
	regs, err := c.ReadRegisters(ctx, 0x11, 0x006B, 2, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	if len(regs) != 2 || regs[0] != 0xAE41 || regs[1] != 0x5652 {
		t.Fatalf("regs = %#v, want [0xAE41 0x5652]", regs)
	}
	if got := <-reqs; !bytes.Equal(got, adu.AssembleRTUFrame(0x11, 0x03, []byte{0x00, 0x6B, 0x00, 0x02})) {
		t.Fatalf("request frame % X", got)
	}

	if err := c.WriteRegister(ctx, 0x11, 0x0001, 0x0A0D); err != nil {
		t.Fatalf("WriteRegister: %v", err)
	}
	if got := <-reqs; !bytes.Equal(got, adu.AssembleRTUFrame(0x11, 0x06, []byte{0x00, 0x01, 0x0A, 0x0D})) {
		t.Fatalf("request frame % X", got)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.ReadRegisters(ctx, 0x11, 0, 1, HoldingRegister); !errors.Is(err, ErrClientNotOpen) {
		t.Fatalf("request after Close: want ErrClientNotOpen, got %v", err)
	}
}

func TestRTUClient_OverPTY_Timeout(t *testing.T) {
	_, slavePath := openTestPTY(t)

	c, err := New(Config{URL: "rtu://" + slavePath, Speed: 19200, Timeout: 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Skipf("serial port cannot be opened on a pseudo-terminal here: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Nobody answers on the remote side.
	start := time.Now()
	_, err = c.ReadRegisters(context.Background(), 0x11, 0, 1, HoldingRegister)
	if !errors.Is(err, ErrRequestTimedOut) {
		t.Fatalf("want ErrRequestTimedOut, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("request took %v with a 40ms timeout", elapsed)
	}
}

func TestASCIIClient_OverPTY(t *testing.T) {
	master, slavePath := openTestPTY(t)

	c, err := New(Config{URL: "ascii://" + slavePath, Speed: 19200, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Skipf("serial port cannot be opened on a pseudo-terminal here: %v", err)
	}
	defer func() { _ = c.Close() }()
	if info := c.Info(); !info.IsOpen || info.Transport != TransportASCII {
		t.Fatalf("unexpected Info %+v", info)
	}

	reqs := servePTY(master,
		func(r io.Reader) ([]byte, error) {
			var line []byte
			b := make([]byte, 1)
			for {
				if _, err := io.ReadFull(r, b); err != nil {
					return nil, err
				}
				line = append(line, b[0])
				if b[0] == '\n' {
					return line, nil
				}
			}
		},
		func([]byte) []byte {
			return adu.AssembleASCIIFrame(0x11, 0x03, []byte{0x04, 0xAE, 0x41, 0x56, 0x52})
		})

	regs, err := c.ReadRegisters(context.Background(), 0x11, 0x006B, 2, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	if len(regs) != 2 || regs[0] != 0xAE41 || regs[1] != 0x5652 {
		t.Fatalf("regs = %#v, want [0xAE41 0x5652]", regs)
	}
	if got, want := string(<-reqs), ":1103006B00027F\r\n"; got != want {
		t.Fatalf("request frame %q, want %q", got, want)
	}
}
