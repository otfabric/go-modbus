// SPDX-License-Identifier: MIT

package adu

import (
	"bytes"
	"errors"
	"testing"
)

func TestLRC(t *testing.T) {
	// Example from "MODBUS over Serial Line" §6.2.1: the LRC makes the
	// 8-bit sum of all frame bytes (LRC included) equal to zero.
	data := []byte{0x11, 0x03, 0x00, 0x6B, 0x00, 0x03}
	if got := LRC(data); got != 0x7E {
		t.Fatalf("LRC: want 0x7E, got 0x%02X", got)
	}
	if got := LRC(nil); got != 0 {
		t.Fatalf("LRC(nil): want 0, got 0x%02X", got)
	}
}

func TestAssembleASCIIFrame(t *testing.T) {
	got := AssembleASCIIFrame(0x11, 0x03, []byte{0x00, 0x6B, 0x00, 0x03})
	want := []byte(":1103006B00037E\r\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("frame: want %q, got %q", want, got)
	}
}

func TestParseASCIIFrameRoundTrip(t *testing.T) {
	payloads := [][]byte{
		nil,
		{0x00},
		{0x00, 0x6B, 0x00, 0x03},
		bytes.Repeat([]byte{0xA5}, 252),
	}
	for _, p := range payloads {
		frame := AssembleASCIIFrame(0xF7, 0x10, p)
		unit, fc, payload, err := ParseASCIIFrame(frame)
		if err != nil {
			t.Fatalf("payload len %d: %v", len(p), err)
		}
		if unit != 0xF7 || fc != 0x10 || !bytes.Equal(payload, p) {
			t.Fatalf("payload len %d: got unit=0x%02X fc=0x%02X payload=% X", len(p), unit, fc, payload)
		}
	}
}

func TestParseASCIIFrameLowerCase(t *testing.T) {
	unit, fc, payload, err := ParseASCIIFrame([]byte(":1103006b00037e\r\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if unit != 0x11 || fc != 0x03 || !bytes.Equal(payload, []byte{0x00, 0x6B, 0x00, 0x03}) {
		t.Fatalf("got unit=0x%02X fc=0x%02X payload=% X", unit, fc, payload)
	}
}

func TestParseASCIIFrameErrors(t *testing.T) {
	tooLong := append([]byte{':'}, bytes.Repeat([]byte{'0'}, MaxASCIIFrameLength)...)
	tooLong = append(tooLong, '\r', '\n')

	cases := []struct {
		name  string
		frame string
		want  error
	}{
		{"empty", "", ErrASCIIFrameMalformed},
		{"too short", ":1103\r\n", ErrASCIIFrameMalformed},
		{"missing start", "01103006B00037E\r\n", ErrASCIIFrameMalformed},
		{"missing cr", ":1103006B00037E\n\n", ErrASCIIFrameMalformed},
		{"missing lf", ":1103006B00037E\r\r", ErrASCIIFrameMalformed},
		{"odd hex count", ":1103006B00037E0\r\n", ErrASCIIFrameMalformed},
		{"non hex", ":1103006B0003ZZ\r\n", ErrASCIIFrameMalformed},
		{"too long", string(tooLong), ErrASCIIFrameMalformed},
		{"bad lrc", ":1103006B00037F\r\n", ErrASCIIFrameLRC},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := ParseASCIIFrame([]byte(tc.frame))
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}
