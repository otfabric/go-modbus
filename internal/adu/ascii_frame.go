// SPDX-License-Identifier: MIT

package adu

import (
	"encoding/hex"
	"errors"
)

const (
	// MaxASCIIFrameLength is the maximum length of an ASCII ADU in characters:
	// ':' + 2 hex chars per byte for (unit id, function code, up to 252 data
	// bytes, LRC) + CR LF.
	MaxASCIIFrameLength = 1 + 2*(1+1+252+1) + 2

	// ASCIIFrameStart is the start-of-frame delimiter.
	ASCIIFrameStart = ':'
	// ASCIIFrameCR and ASCIIFrameLF terminate a frame.
	ASCIIFrameCR = '\r'
	ASCIIFrameLF = '\n'

	// minASCIIFrameLength is ':' + unit id + function code + LRC + CR LF.
	minASCIIFrameLength = 1 + 2*3 + 2
)

var (
	// ErrASCIIFrameMalformed is returned when a frame lacks its delimiters,
	// has an odd or too small number of hex characters, or holds non-hex data.
	ErrASCIIFrameMalformed = errors.New("adu: malformed ascii frame")
	// ErrASCIIFrameLRC is returned when the LRC does not match the frame contents.
	ErrASCIIFrameLRC = errors.New("adu: ascii frame lrc mismatch")
)

// LRC returns the longitudinal redundancy check of data: the two's complement
// of the 8-bit sum of all bytes.
func LRC(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum += b
	}
	return -sum
}

// AssembleASCIIFrame builds an ASCII frame: ':', then unitID, fc, payload and
// LRC as upper-case hex characters, then CR LF.
func AssembleASCIIFrame(unitID uint8, fc byte, payload []byte) []byte {
	const hexDigits = "0123456789ABCDEF"

	raw := make([]byte, 0, 2+len(payload)+1)
	raw = append(raw, unitID, fc)
	raw = append(raw, payload...)
	raw = append(raw, LRC(raw))

	frame := make([]byte, 0, 1+2*len(raw)+2)
	frame = append(frame, ASCIIFrameStart)
	for _, b := range raw {
		frame = append(frame, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	frame = append(frame, ASCIIFrameCR, ASCIIFrameLF)
	return frame
}

// ParseASCIIFrame decodes a complete ASCII frame (from ':' through CR LF),
// validates its LRC and returns the unit ID, function code and payload.
// Both upper- and lower-case hex characters are accepted.
func ParseASCIIFrame(frame []byte) (unitID uint8, fc byte, payload []byte, err error) {
	n := len(frame)
	if n < minASCIIFrameLength || n > MaxASCIIFrameLength ||
		frame[0] != ASCIIFrameStart ||
		frame[n-2] != ASCIIFrameCR || frame[n-1] != ASCIIFrameLF {
		return 0, 0, nil, ErrASCIIFrameMalformed
	}
	body := frame[1 : n-2]
	if len(body)%2 != 0 {
		return 0, 0, nil, ErrASCIIFrameMalformed
	}
	raw := make([]byte, len(body)/2)
	if _, err := hex.Decode(raw, body); err != nil {
		return 0, 0, nil, ErrASCIIFrameMalformed
	}
	last := len(raw) - 1
	if LRC(raw[:last]) != raw[last] {
		return 0, 0, nil, ErrASCIIFrameLRC
	}
	return raw[0], raw[1], raw[2:last], nil
}
