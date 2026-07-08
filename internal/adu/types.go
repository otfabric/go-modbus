// SPDX-License-Identifier: MIT

package adu

// Request represents a Modbus request PDU (unit id, function code, payload).
// Used by transport/session layers; function semantics live in protocol.
type Request struct {
	UnitID       uint8
	FunctionCode uint8
	Payload      []byte
}

// Response represents a Modbus response PDU.
// TransactionID is set for TCP (MBAP); zero for RTU.
type Response struct {
	UnitID        uint8
	FunctionCode  uint8
	Payload       []byte
	TransactionID uint16
}
