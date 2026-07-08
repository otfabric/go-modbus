// SPDX-License-Identifier: MIT

package adu

import (
	"errors"
	"fmt"
)

// MBAP framing constants.
const (
	MBAPHeaderLength = 7
	MBAPLengthMin    = 2   // unit_id + function_code
	MBAPLengthMax    = 254 // per Modbus spec
)

// ADU-level errors (transport maps these to protocol sentinels for public API).
var (
	ErrInvalidMBAPLength = errors.New("modbus: invalid mbap length")
	ErrUnknownProtocolID = errors.New("modbus: unknown protocol id")
)

// AssembleMBAP builds an MBAP frame: transaction ID, protocol 0x0000, length, unit ID, function code, payload.
func AssembleMBAP(txnID uint16, unitID uint8, fc byte, payload []byte) []byte {
	length := uint16(2 + len(payload)) // unit_id + function_code + payload
	out := Uint16ToBytes(BigEndian, txnID)
	out = append(out, 0x00, 0x00) // protocol identifier
	out = append(out, Uint16ToBytes(BigEndian, length)...)
	out = append(out, unitID, fc)
	out = append(out, payload...)
	return out
}

// ParseMBAPHeader parses the 7-byte MBAP header.
// Returns transaction ID, unit ID, and PDU length (unit_id + fc + payload = 2..254).
// Body (fc + payload) length is mbapLen-1; caller must read that many bytes and then body[0]=fc, body[1:]=payload.
func ParseMBAPHeader(header []byte) (txnID uint16, unitID uint8, mbapLen int, err error) {
	if len(header) < MBAPHeaderLength {
		return 0, 0, 0, fmt.Errorf("modbus: mbap header too short: %d", len(header))
	}
	txnID = BytesToUint16(BigEndian, header[0:2])
	protocolID := BytesToUint16(BigEndian, header[2:4])
	mbapLen = int(BytesToUint16(BigEndian, header[4:6]))
	unitID = header[6]
	if protocolID != 0x0000 {
		return 0, 0, 0, ErrUnknownProtocolID
	}
	if mbapLen < MBAPLengthMin || mbapLen > MBAPLengthMax {
		return 0, 0, 0, fmt.Errorf("%w: received %d", ErrInvalidMBAPLength, mbapLen)
	}
	return txnID, unitID, mbapLen, nil
}
