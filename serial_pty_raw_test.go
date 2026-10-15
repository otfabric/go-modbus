//go:build darwin || linux

// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// firstOf passes on the first request servePTY reports and drains the rest, so
// that servePTY never blocks.
func firstOf(reqs <-chan []byte) <-chan []byte {
	first := make(chan []byte, 1)
	go func() {
		if req, ok := <-reqs; ok {
			first <- req
		}
		for range reqs {
		}
	}()
	return first
}

// SendRawRequest on rtu:// and ascii:// through a pseudo-terminal: the serial
// port wrapper and its poll timeouts are in the path, unlike over TCP or UDP.
func TestSendRawRequest_OverPTY(t *testing.T) {
	t.Run("rtu", func(t *testing.T) {
		master, slavePath := openTestPTY(t)
		c, err := New(Config{URL: "rtu://" + slavePath, Speed: 19200, Timeout: 2 * time.Second})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err != nil {
			t.Skipf("serial port cannot be opened on a pseudo-terminal here: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		first := firstOf(servePTY(master, readRTURequest, answerRTU))
		runRawSerialCases(t, c)
		if got, want := <-first, adu.AssembleRTUFrame(0x11, 0x41, []byte{0x01, 0x02, 0x03}); !bytes.Equal(got, want) {
			t.Errorf("first request frame % X, want % X", got, want)
		}
	})

	t.Run("ascii", func(t *testing.T) {
		master, slavePath := openTestPTY(t)
		c, err := New(Config{URL: "ascii://" + slavePath, Speed: 19200, Timeout: 2 * time.Second})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err != nil {
			t.Skipf("serial port cannot be opened on a pseudo-terminal here: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		first := firstOf(servePTY(master,
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
			func(req []byte) []byte {
				unit, fc, payload, err := adu.ParseASCIIFrame(req)
				if err != nil {
					return nil
				}
				resFC, resPayload := rawSerialDevice(fc, payload)
				return adu.AssembleASCIIFrame(unit, resFC, resPayload)
			}))
		runRawSerialCases(t, c)
		if got, want := <-first, adu.AssembleASCIIFrame(0x11, 0x41, []byte{0x01, 0x02, 0x03}); !bytes.Equal(got, want) {
			t.Errorf("first request frame %q, want %q", got, want)
		}
	})
}
