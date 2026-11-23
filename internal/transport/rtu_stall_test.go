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
	"github.com/otfabric/go-modbus/internal/protocol"
)

// stallLink is an RTULink that holds a whole frame and behaves as a socket
// does when its reader runs late: at the reads listed in stalls, it waits
// stall before looking, and when the deadline has passed by then it returns a
// timeout without handing over the bytes that are waiting, as a net.Conn does
// once its deadline has expired. Once the frame is consumed it reports a
// timeout: the line is quiet.
type stallLink struct {
	mu       sync.Mutex
	data     []byte
	deadline time.Time
	reads    int
	stalls   map[int]bool
	stall    time.Duration
}

func (l *stallLink) Close() error                { return nil }
func (l *stallLink) Write(p []byte) (int, error) { return len(p), nil }

func (l *stallLink) SetDeadline(t time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.deadline = t
	return nil
}

func (l *stallLink) Read(p []byte) (int, error) {
	l.mu.Lock()
	l.reads++
	stalled := l.stalls[l.reads]
	deadline := l.deadline
	l.mu.Unlock()
	if stalled {
		time.Sleep(l.stall)
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.data) == 0 {
		return 0, os.ErrDeadlineExceeded
	}
	n := copy(p, l.data)
	l.data = l.data[n:]
	return n, nil
}

// stallRTU is an RTU transport on link whose inter-frame silence is 1 ms,
// shorter than the stalls of stallLink.
func stallRTU(link RTULink) *RTU {
	rt := rawRTU(link)
	rt.t35 = time.Millisecond
	return rt
}

// deviceIDPayload is the payload of an FC43/14 response with the given
// objects: Read Device ID code 01, conformity level 01, no more to follow.
func deviceIDPayload(objects ...string) []byte {
	p := []byte{byte(protocol.MEIReadDeviceIdentification), 0x01, 0x01, 0x00, 0x00, byte(len(objects))}
	for i, o := range objects {
		p = append(p, byte(i), byte(len(o)))
		p = append(p, o...)
	}
	return p
}

// A reader that runs late inside a frame of unknown length must not take the
// timeout for the end of the frame: FC43/14 is read by its structure, and any
// other variable-length frame checks that the line is quiet once more.
func TestRTUVariableLength_LateReader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fc      protocol.FunctionCode
		payload []byte
	}{
		{"read device identification", protocol.FCEncapsulatedInterface, deviceIDPayload("ACME", "X1", "1.0")},
		{"read device identification, no objects", protocol.FCEncapsulatedInterface, deviceIDPayload()},
		{"other MEI type", protocol.FCEncapsulatedInterface, []byte{0x0D, 0x01, 0x02, 0x03, 0x04}},
		{"diagnostics", protocol.FCDiagnostics, []byte{0x00, 0x00, 0xA5, 0x37}},
	} {
		frame := adu.AssembleRTUFrame(0x05, byte(tc.fc), tc.payload)
		// Stall at every read after the header, one at a time.
		for at := 2; at <= len(frame); at++ {
			link := &stallLink{data: append([]byte(nil), frame...), stalls: map[int]bool{at: true}, stall: 5 * time.Millisecond}
			deadline := time.Now().Add(time.Second)
			_ = link.SetDeadline(deadline) // as ExecuteRequest does
			res, err := stallRTU(link).readRTUFrame(context.Background(), deadline)
			if err != nil {
				t.Fatalf("%s, stall at read %d: %v", tc.name, at, err)
			}
			if res.UnitID != 0x05 || res.FunctionCode != byte(tc.fc) || !bytes.Equal(res.Payload, tc.payload) {
				t.Fatalf("%s, stall at read %d: got %+v", tc.name, at, res)
			}
		}
	}
}

// An FC43/14 response is read by its structure: the bytes after it are left
// on the line, a frame cut short is ErrShortFrame, one whose objects run past
// the largest RTU frame is a protocol error, and a corrupted one fails its CRC.
func TestRTUDeviceIDFrame(t *testing.T) {
	payload := deviceIDPayload("ACME", "X1")
	frame := adu.AssembleRTUFrame(0x05, byte(protocol.FCEncapsulatedInterface), payload)

	t.Run("exact", func(t *testing.T) {
		link := &stallLink{data: append(append([]byte(nil), frame...), 0xEE, 0xEE)}
		res, err := rawRTU(link).readRTUFrame(context.Background(), time.Now().Add(time.Second))
		if err != nil || !bytes.Equal(res.Payload, payload) {
			t.Fatalf("got %+v, %v", res, err)
		}
		if !bytes.Equal(link.data, []byte{0xEE, 0xEE}) {
			t.Errorf("left % X on the line, want EE EE", link.data)
		}
	})

	t.Run("bad crc", func(t *testing.T) {
		bad := append([]byte(nil), frame...)
		bad[len(bad)-1] ^= 0xFF
		if _, err := rawRTU(&stallLink{data: bad}).readRTUFrame(context.Background(), time.Now().Add(time.Second)); !errors.Is(err, protocol.ErrBadCRC) {
			t.Fatalf("want ErrBadCRC, got %v", err)
		}
	})

	for _, cut := range []int{4, 8, 10, 12, len(frame) - 1} {
		link := &scriptedLink{chunks: [][]byte{frame[:cut]}, readErr: io.EOF}
		_, err := rawRTU(link).readRTUFrame(context.Background(), time.Now().Add(time.Second))
		if !errors.Is(err, protocol.ErrShortFrame) {
			t.Errorf("cut at %d: want ErrShortFrame, got %v", cut, err)
		}
	}

	// Objects that announce more than an RTU frame holds.
	huge := []byte{0x05, byte(protocol.FCEncapsulatedInterface), byte(protocol.MEIReadDeviceIdentification), 0x01, 0x01, 0x00, 0x00, 0x02, 0x00, 0xF0}
	huge = append(huge, bytes.Repeat([]byte{'a'}, 0xF0)...)
	huge = append(huge, 0x01, 0xF0)
	link := &scriptedLink{chunks: [][]byte{huge}, readErr: io.EOF}
	if _, err := rawRTU(link).readRTUFrame(context.Background(), time.Now().Add(time.Second)); !errors.Is(err, protocol.ErrProtocolError) {
		t.Errorf("oversized: want ErrProtocolError, got %v", err)
	}
}

// Interrupt ends an FC43/14 read in flight: the request deadline ExecuteRequest
// set is not set again while the frame is read.
func TestRTUDeviceIDFrame_Interrupt(t *testing.T) {
	p1, p2 := net.Pipe()
	defer func() { _ = p1.Close(); _ = p2.Close() }()
	rt := rawRTU(p2)
	frame := adu.AssembleRTUFrame(0x05, byte(protocol.FCEncapsulatedInterface), deviceIDPayload("ACME"))
	go func() { _, _ = p1.Write(frame[:6]) }() // the rest never comes
	_ = p2.SetDeadline(time.Now().Add(10 * time.Second))
	done := make(chan error, 1)
	go func() { _, err := rt.readRTUFrame(context.Background(), time.Now().Add(10*time.Second)); done <- err }()
	time.Sleep(20 * time.Millisecond)
	rt.Interrupt()
	select {
	case err := <-done:
		if !os.IsTimeout(err) {
			t.Fatalf("want a timeout, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Interrupt did not end the read")
	}
}
