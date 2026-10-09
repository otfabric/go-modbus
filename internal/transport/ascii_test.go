// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// scriptedLink is an RTULink whose Read returns pre-programmed chunks, to
// exercise frame reassembly without depending on socket timing.
type scriptedLink struct {
	mu       sync.Mutex
	chunks   [][]byte
	written  bytes.Buffer
	readErr  error // returned once chunks are exhausted (nil: (0, nil) polls)
	writeErr error
	dlErr    error
	closed   bool
}

func (l *scriptedLink) Read(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.chunks) == 0 {
		return 0, l.readErr
	}
	n := copy(p, l.chunks[0])
	if n == len(l.chunks[0]) {
		l.chunks = l.chunks[1:]
	} else {
		l.chunks[0] = l.chunks[0][n:]
	}
	return n, nil
}

func (l *scriptedLink) Write(p []byte) (int, error) {
	if l.writeErr != nil {
		return 0, l.writeErr
	}
	return l.written.Write(p)
}

func (l *scriptedLink) SetDeadline(time.Time) error { return l.dlErr }
func (l *scriptedLink) Close() error                { l.closed = true; return nil }

func asciiReq() *adu.Request {
	return &adu.Request{UnitID: 0x11, FunctionCode: byte(protocol.FCReadHoldingRegisters), Payload: []byte{0x00, 0x6B, 0x00, 0x03}}
}

func asciiRes() []byte {
	return adu.AssembleASCIIFrame(0x11, byte(protocol.FCReadHoldingRegisters), []byte{0x02, 0xAB, 0xCD})
}

func checkASCIIRes(t *testing.T, res *adu.Response) {
	t.Helper()
	if res.UnitID != 0x11 || res.FunctionCode != byte(protocol.FCReadHoldingRegisters) ||
		!bytes.Equal(res.Payload, []byte{0x02, 0xAB, 0xCD}) || res.TransactionID != 0 {
		t.Fatalf("unexpected response: %+v", res)
	}
}

func TestASCIIExecuteRequest(t *testing.T) {
	link := &scriptedLink{chunks: [][]byte{asciiRes()}}
	at := NewASCII(link, time.Second, logging.NopLogger())

	res, err := at.ExecuteRequest(context.Background(), asciiReq())
	if err != nil {
		t.Fatalf("ExecuteRequest: %v", err)
	}
	checkASCIIRes(t, res)
	if got, want := link.written.String(), ":1103006B00037E\r\n"; got != want {
		t.Fatalf("TX frame: want %q, got %q", want, got)
	}
}

func TestASCIIExecuteRequestReassembly(t *testing.T) {
	full := asciiRes()
	cases := map[string][][]byte{
		"byte by byte":          splitEvery(full, 1),
		"split mid frame":       {full[:5], full[5:]},
		"leading noise":         {[]byte("\x00\xffgarbage"), full},
		"noise line then frame": {[]byte("noise\r\n"), full},
		"aborted frame restart": {[]byte(":1103"), full},
		"aborted frame, one rx": {append([]byte(":1103"), full...)},
		"polls between chunks":  {full[:3], {}, {}, full[3:]},
	}
	for name, chunks := range cases {
		t.Run(name, func(t *testing.T) {
			at := NewASCII(&scriptedLink{chunks: chunks}, time.Second, logging.NopLogger())
			res, err := at.ExecuteRequest(context.Background(), asciiReq())
			if err != nil {
				t.Fatalf("ExecuteRequest: %v", err)
			}
			checkASCIIRes(t, res)
		})
	}
}

func splitEvery(b []byte, n int) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		k := min(n, len(b))
		out = append(out, b[:k])
		b = b[k:]
	}
	return out
}

func TestASCIIExecuteRequestErrors(t *testing.T) {
	badLRC := []byte(":110302ABCD00\r\n")
	sentinel := errors.New("boom")

	t.Run("bad lrc", func(t *testing.T) {
		at := NewASCII(&scriptedLink{chunks: [][]byte{badLRC}}, time.Second, logging.NopLogger())
		if _, err := at.ExecuteRequest(context.Background(), asciiReq()); !errors.Is(err, protocol.ErrBadLRC) {
			t.Fatalf("want ErrBadLRC, got %v", err)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		at := NewASCII(&scriptedLink{chunks: [][]byte{[]byte(":11ZZ02ABCD00\r\n")}}, time.Second, logging.NopLogger())
		if _, err := at.ExecuteRequest(context.Background(), asciiReq()); !errors.Is(err, protocol.ErrProtocolError) {
			t.Fatalf("want ErrProtocolError, got %v", err)
		}
	})
	t.Run("oversized frame", func(t *testing.T) {
		big := append([]byte{':'}, bytes.Repeat([]byte{'0'}, adu.MaxASCIIFrameLength+10)...)
		at := NewASCII(&scriptedLink{chunks: [][]byte{big}}, time.Second, logging.NopLogger())
		if _, err := at.ExecuteRequest(context.Background(), asciiReq()); !errors.Is(err, protocol.ErrProtocolError) {
			t.Fatalf("want ErrProtocolError, got %v", err)
		}
	})
	t.Run("oversized request", func(t *testing.T) {
		at := NewASCII(&scriptedLink{}, time.Second, logging.NopLogger())
		req := &adu.Request{UnitID: 1, FunctionCode: 0x10, Payload: make([]byte, 253)}
		if _, err := at.ExecuteRequest(context.Background(), req); !errors.Is(err, protocol.ErrProtocolError) {
			t.Fatalf("want ErrProtocolError, got %v", err)
		}
	})
	t.Run("poll timeout", func(t *testing.T) {
		// A link that only ever polls (serial port with nothing to read).
		at := NewASCII(&scriptedLink{}, 5*time.Millisecond, logging.NopLogger())
		if _, err := at.ExecuteRequest(context.Background(), asciiReq()); !errors.Is(err, protocol.ErrRequestTimedOut) {
			t.Fatalf("want ErrRequestTimedOut, got %v", err)
		}
	})
	t.Run("context deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		defer cancel()
		at := NewASCII(&scriptedLink{}, time.Minute, logging.NopLogger())
		_, err := at.ExecuteRequest(ctx, asciiReq())
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, protocol.ErrRequestTimedOut) {
			t.Fatalf("want deadline error, got %v", err)
		}
	})
	t.Run("context cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		at := NewASCII(&scriptedLink{}, time.Minute, logging.NopLogger())
		if _, err := at.ExecuteRequest(ctx, asciiReq()); !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	})
	t.Run("read error", func(t *testing.T) {
		at := NewASCII(&scriptedLink{readErr: sentinel}, time.Second, logging.NopLogger())
		if _, err := at.ExecuteRequest(context.Background(), asciiReq()); !errors.Is(err, sentinel) {
			t.Fatalf("want read error, got %v", err)
		}
	})
	t.Run("write error", func(t *testing.T) {
		at := NewASCII(&scriptedLink{writeErr: sentinel}, time.Second, logging.NopLogger())
		if _, err := at.ExecuteRequest(context.Background(), asciiReq()); !errors.Is(err, sentinel) {
			t.Fatalf("want write error, got %v", err)
		}
	})
	t.Run("deadline error", func(t *testing.T) {
		at := NewASCII(&scriptedLink{dlErr: sentinel}, time.Second, logging.NopLogger())
		if _, err := at.ExecuteRequest(context.Background(), asciiReq()); !errors.Is(err, sentinel) {
			t.Fatalf("want deadline error, got %v", err)
		}
		if _, _, err := at.ReadRequest(); !errors.Is(err, sentinel) {
			t.Fatalf("ReadRequest: want deadline error, got %v", err)
		}
	})
}

// A response that arrives after its request timed out must not be mistaken
// for the answer to the next request.
func TestASCIIStaleResponseDiscarded(t *testing.T) {
	link := &scriptedLink{}
	at := NewASCII(link, time.Second, logging.NopLogger())
	at.rxbuf = append(at.rxbuf, adu.AssembleASCIIFrame(0x22, 0x04, []byte{0x02, 0x00, 0x01})...)
	link.chunks = [][]byte{asciiRes()}

	res, err := at.ExecuteRequest(context.Background(), asciiReq())
	if err != nil {
		t.Fatalf("ExecuteRequest: %v", err)
	}
	checkASCIIRes(t, res)
}

func TestASCIIServerSide(t *testing.T) {
	// Two pipelined requests in a single read.
	rx := append(adu.AssembleASCIIFrame(1, 3, []byte{0, 0, 0, 1}), adu.AssembleASCIIFrame(2, 4, []byte{0, 5, 0, 2})...)
	link := &scriptedLink{chunks: [][]byte{rx}, readErr: io.EOF}
	at := NewASCII(link, time.Second, logging.NopLogger())

	req, txn, err := at.ReadRequest()
	if err != nil || req.UnitID != 1 || req.FunctionCode != 3 || txn != 0 {
		t.Fatalf("first request: req=%+v txn=%d err=%v", req, txn, err)
	}
	req, _, err = at.ReadRequest()
	if err != nil || req.UnitID != 2 || req.FunctionCode != 4 || !bytes.Equal(req.Payload, []byte{0, 5, 0, 2}) {
		t.Fatalf("second request: req=%+v err=%v", req, err)
	}
	if _, _, err = at.ReadRequest(); !errors.Is(err, io.EOF) {
		t.Fatalf("third request: want io.EOF, got %v", err)
	}

	if err := at.WriteResponse(&adu.Response{UnitID: 1, FunctionCode: 3, Payload: []byte{2, 0, 7}}); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	if got, want := link.written.String(), ":0103020007F3\r\n"; got != want {
		t.Fatalf("TX frame: want %q, got %q", want, got)
	}

	if err := at.Close(); err != nil || !link.closed {
		t.Fatalf("Close: err=%v closed=%v", err, link.closed)
	}
}

// End to end over a real connection: client and server ASCII transports.
func TestASCIIOverPipe(t *testing.T) {
	c, s := net.Pipe()
	defer func() { _ = c.Close(); _ = s.Close() }()

	server := NewASCII(s, time.Second, logging.NopLogger())
	done := make(chan error, 1)
	go func() {
		req, _, err := server.ReadRequest()
		if err != nil {
			done <- err
			return
		}
		done <- server.WriteResponse(&adu.Response{UnitID: req.UnitID, FunctionCode: req.FunctionCode, Payload: []byte{0x02, 0xAB, 0xCD}})
	}()

	client := NewASCII(c, time.Second, logging.NopLogger())
	res, err := client.ExecuteRequest(context.Background(), asciiReq())
	if err != nil {
		t.Fatalf("ExecuteRequest: %v", err)
	}
	checkASCIIRes(t, res)
	if err := <-done; err != nil {
		t.Fatalf("server: %v", err)
	}

	// No response: the connection deadline surfaces as a timeout error.
	client.Timeout = 10 * time.Millisecond
	go func() { _, _ = io.Copy(io.Discard, s) }()
	if _, err := client.ExecuteRequest(context.Background(), asciiReq()); !os.IsTimeout(err) {
		t.Fatalf("want timeout, got %v", err)
	}
}
