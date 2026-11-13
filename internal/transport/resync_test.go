// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
)

// tcpPair returns the two ends of a loopback TCP connection. Unlike net.Pipe, a
// write does not wait for the peer to read.
func tcpPair(t *testing.T) (client, peer net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	peer = <-accepted
	if peer == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	return client, peer
}

// rtuReadRegsAnswer answers every FC03 request read from conn with the value of
// its start address, at once.
func rtuReadRegsAnswer(conn net.Conn) {
	buf := make([]byte, 8)
	for {
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		frame := adu.AssembleRTUFrame(buf[0], buf[1], []byte{2, buf[2], buf[3]})
		if _, err := conn.Write(frame); err != nil {
			return
		}
	}
}

func readRegsReq(addr uint16) *adu.Request {
	return &adu.Request{UnitID: 1, FunctionCode: 0x03, Payload: []byte{byte(addr >> 8), byte(addr), 0, 1}}
}

// An exchange abandoned after the request was written (here during the silent
// interval that follows it) leaves its answer to be discarded: the next request
// gets its own answer.
func TestRTUAnswerAfterCancelDrained(t *testing.T) {
	client, peer := tcpPair(t)
	go rtuReadRegsAnswer(peer)
	// At 1200 bit/s the request (8 bytes) and t3.5 take about 105 ms.
	rt := NewRTU(client, "", 1200, time.Second, logging.NopLogger())

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(40*time.Millisecond, cancel)
	if _, err := rt.ExecuteRequest(ctx, readRegsReq(100)); err == nil {
		t.Fatal("first request answered, want the cancellation")
	}
	time.Sleep(20 * time.Millisecond) // the first answer is on the link
	for _, addr := range []uint16{200, 300} {
		res, err := rt.ExecuteRequest(context.Background(), readRegsReq(addr))
		want := []byte{2, byte(addr >> 8), byte(addr)}
		if err != nil || !bytes.Equal(res.Payload, want) {
			t.Fatalf("request for %d: %+v, %v; want payload % X", addr, res, err, want)
		}
	}
}

// Before the request that follows a failed exchange, an answer that is still
// being received is discarded as a whole, past the minimum window of 3 x t3.5,
// until the line has been quiet for t3.5.
func TestRTUDrainUntilQuiet(t *testing.T) {
	client, peer := tcpPair(t)
	// 4800 bit/s: t3.5 is about 8 ms, the minimum window about 24 ms.
	rt := NewRTU(client, "", 4800, 50*time.Millisecond, logging.NopLogger())
	req := readRegsReq(100)
	reqLen := len(adu.AssembleRTUFrame(1, 0x03, req.Payload))
	trickle := make(chan struct{})
	go func() {
		buf := make([]byte, reqLen)
		if _, err := io.ReadFull(peer, buf); err != nil {
			return
		}
		<-trickle
		// Late, slow bytes for about 80 ms, 2 ms apart.
		for range 40 {
			if _, err := peer.Write([]byte{0x55}); err != nil {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		rtuReadRegsAnswer(peer)
	}()
	if _, err := rt.ExecuteRequest(context.Background(), req); err == nil {
		t.Fatal("first request answered, want a timeout")
	}
	close(trickle)
	rt.Timeout = time.Second
	res, err := rt.ExecuteRequest(context.Background(), readRegsReq(200))
	if err != nil || !bytes.Equal(res.Payload, []byte{2, 0, 200}) {
		t.Fatalf("second request: %+v, %v; want its own answer", res, err)
	}
}

// Resync makes the next request discard what is pending on the link.
func TestRTUResync(t *testing.T) {
	client, peer := tcpPair(t)
	rt := NewRTU(client, "", 19200, time.Second, logging.NopLogger())
	stray := adu.AssembleRTUFrame(1, 0x03, []byte{2, 0x11, 0x11})
	if _, err := peer.Write(stray); err != nil {
		t.Fatal(err)
	}
	go rtuReadRegsAnswer(peer)
	time.Sleep(10 * time.Millisecond)
	rt.Resync()
	res, err := rt.ExecuteRequest(context.Background(), readRegsReq(200))
	if err != nil || !bytes.Equal(res.Payload, []byte{2, 0, 200}) {
		t.Fatalf("request after Resync: %+v, %v; want its own answer", res, err)
	}
}

func TestASCIIResync(t *testing.T) {
	client, peer := tcpPair(t)
	at := NewASCII(client, time.Second, logging.NopLogger())
	stray := adu.AssembleASCIIFrame(1, 0x03, []byte{2, 0x11, 0x11})
	if _, err := peer.Write(stray); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	go func() {
		buf := make([]byte, 64)
		var got []byte
		for !bytes.Contains(got, []byte("\r\n")) {
			n, err := peer.Read(buf)
			if err != nil {
				return
			}
			got = append(got, buf[:n]...)
		}
		_, _ = peer.Write(adu.AssembleASCIIFrame(1, 0x03, []byte{2, 0, 200}))
	}()
	at.Resync()
	res, err := at.ExecuteRequest(context.Background(), readRegsReq(200))
	if err != nil || !bytes.Equal(res.Payload, []byte{2, 0, 200}) {
		t.Fatalf("request after Resync: %+v, %v; want its own answer", res, err)
	}
}

// shiftPeer answers the first request only once the second has arrived, before
// the second's own answer, then answers every request at once. frameLen is the
// length of a request; answer builds the answer to one.
func shiftPeer(conn net.Conn, frameLen int, answer func(req []byte) []byte) {
	buf := make([]byte, frameLen)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}
	late := answer(append([]byte{}, buf...))
	for {
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		out := answer(buf)
		if late != nil {
			out = append(late, out...)
			late = nil
		}
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

// An answer that arrives after the drain that follows its timed-out request is
// read, at most, as the answer to the one request after it: the request after
// that finds the line not quiet, discards it, and gets its own answer.
func TestSerialLateAnswerMisreadOnce(t *testing.T) {
	for _, framing := range []string{"rtu", "ascii"} {
		t.Run(framing, func(t *testing.T) {
			client, peer := tcpPair(t)
			var tr interface {
				ExecuteRequest(context.Context, *adu.Request) (*adu.Response, error)
			}
			var frameLen int
			var answer func([]byte) []byte
			if framing == "rtu" {
				tr = NewRTU(client, "", 19200, 50*time.Millisecond, logging.NopLogger())
				frameLen = 8
				answer = func(req []byte) []byte { return adu.AssembleRTUFrame(req[0], req[1], []byte{2, req[2], req[3]}) }
			} else {
				tr = NewASCII(client, 50*time.Millisecond, logging.NopLogger())
				frameLen = len(adu.AssembleASCIIFrame(1, 0x03, []byte{0, 0, 0, 1}))
				answer = func(req []byte) []byte {
					u, fc, p, err := adu.ParseASCIIFrame(req)
					if err != nil {
						return nil
					}
					return adu.AssembleASCIIFrame(u, fc, []byte{2, p[0], p[1]})
				}
			}
			go shiftPeer(peer, frameLen, answer)

			ctx := context.Background()
			if _, err := tr.ExecuteRequest(ctx, readRegsReq(100)); err == nil {
				t.Fatal("first request answered, want a timeout")
			}
			// The late answer is taken for this one: there is no transaction ID.
			_, _ = tr.ExecuteRequest(ctx, readRegsReq(200))
			for _, addr := range []uint16{300, 400} {
				res, err := tr.ExecuteRequest(ctx, readRegsReq(addr))
				want := []byte{2, byte(addr >> 8), byte(addr)}
				if err != nil || !bytes.Equal(res.Payload, want) {
					t.Fatalf("request for %d: %+v, %v; want payload % X", addr, res, err, want)
				}
			}
		})
	}
}
