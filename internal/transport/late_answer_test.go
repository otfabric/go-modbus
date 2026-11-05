// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// lateAnswerPeer reads two requests of reqLen bytes from conn. It leaves the
// first unanswered until timedOut is closed, then sends late, and answers the
// second with next.
func lateAnswerPeer(t *testing.T, conn net.Conn, reqLen int, timedOut <-chan struct{}, late, next []byte) {
	t.Helper()
	go func() {
		buf := make([]byte, reqLen)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		<-timedOut
		if _, err := conn.Write(late); err != nil {
			return
		}
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		_, _ = conn.Write(next)
	}()
}

// After a timed-out exchange, its late answer is discarded rather than read as
// the answer to the next request.
func TestRTULateAnswerDrained(t *testing.T) {
	p1, p2 := net.Pipe()
	defer func() { _ = p1.Close(); _ = p2.Close() }()
	rt := NewRTU(p2, "", 19200, 50*time.Millisecond, logging.NopLogger())
	req := &adu.Request{UnitID: 1, FunctionCode: 0x03, Payload: []byte{0, 100, 0, 1}}
	timedOut := make(chan struct{})
	late := adu.AssembleRTUFrame(1, 0x03, []byte{2, 0x11, 0x11})
	next := adu.AssembleRTUFrame(1, 0x03, []byte{2, 0x22, 0x22})
	lateAnswerPeer(t, p1, len(adu.AssembleRTUFrame(1, 0x03, req.Payload)), timedOut, late, next)

	if _, err := rt.ExecuteRequest(context.Background(), req); err == nil {
		t.Fatal("first request answered, want a timeout")
	}
	close(timedOut)
	time.Sleep(20 * time.Millisecond) // the late answer is on the link
	res, err := rt.ExecuteRequest(context.Background(), req)
	if err != nil || !bytes.Equal(res.Payload, []byte{2, 0x22, 0x22}) {
		t.Fatalf("second request: %v, %v; want its own answer", res, err)
	}
}

func TestASCIILateAnswerDrained(t *testing.T) {
	p1, p2 := net.Pipe()
	defer func() { _ = p1.Close(); _ = p2.Close() }()
	at := NewASCII(p2, 50*time.Millisecond, logging.NopLogger())
	req := &adu.Request{UnitID: 1, FunctionCode: 0x03, Payload: []byte{0, 100, 0, 1}}
	timedOut := make(chan struct{})
	late := adu.AssembleASCIIFrame(1, 0x03, []byte{2, 0x11, 0x11})
	next := adu.AssembleASCIIFrame(1, 0x03, []byte{2, 0x22, 0x22})
	lateAnswerPeer(t, p1, len(adu.AssembleASCIIFrame(1, 0x03, req.Payload)), timedOut, late, next)

	if _, err := at.ExecuteRequest(context.Background(), req); err == nil {
		t.Fatal("first request answered, want a timeout")
	}
	close(timedOut)
	time.Sleep(20 * time.Millisecond)
	res, err := at.ExecuteRequest(context.Background(), req)
	if err != nil || !bytes.Equal(res.Payload, []byte{2, 0x22, 0x22}) {
		t.Fatalf("second request: %v, %v; want its own answer", res, err)
	}
}

// A variable-length RTU answer that keeps trickling in stops at the request
// deadline, or when ctx is cancelled.
func TestRTUVariableLengthTrickleBounded(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			p1, p2 := net.Pipe()
			defer func() { _ = p1.Close(); _ = p2.Close() }()
			timeout := 100 * time.Millisecond
			if mode == "cancel" {
				timeout = time.Second
			}
			rt := NewRTU(p2, "", 2400, timeout, logging.NopLogger()) // t3.5 = 16 ms
			req := &adu.Request{UnitID: 1, FunctionCode: 0x08, Payload: []byte{0, 0, 0x12, 0x34}}
			go func() {
				buf := make([]byte, len(adu.AssembleRTUFrame(1, 0x08, req.Payload)))
				if _, err := io.ReadFull(p1, buf); err != nil {
					return
				}
				if _, err := p1.Write([]byte{1, 0x08, 0x00}); err != nil {
					return
				}
				for range 1000 {
					if _, err := p1.Write([]byte{0x55}); err != nil {
						return
					}
					time.Sleep(2 * time.Millisecond)
				}
			}()
			ctx := context.Background()
			want := protocol.ErrRequestTimedOut
			if mode == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				// After the gap that follows the request (about 55 ms at 2400 bit/s),
				// while the answer trickles in.
				time.AfterFunc(100*time.Millisecond, cancel)
				want = context.Canceled
			}
			start := time.Now()
			_, err := rt.ExecuteRequest(ctx, req)
			if !errors.Is(err, want) {
				t.Errorf("err %v, want %v", err, want)
			}
			if d := time.Since(start); d > 300*time.Millisecond {
				t.Errorf("request took %v, want about 100 ms", d)
			}
		})
	}
}
