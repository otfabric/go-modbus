// SPDX-License-Identifier: MIT

package adu

// Request represents a Modbus request PDU (unit id, function code, payload).
// Used by transport/session layers; function semantics live in protocol.
type Request struct {
	UnitID       uint8
	FunctionCode uint8
	Payload      []byte
	// Seq and TransactionID are set by a server for a request it read from a
	// connection: its position in the connection's requests (from 1) and the
	// MBAP transaction ID it arrived with. Zero elsewhere.
	Seq           uint64
	TransactionID uint16
}

// Response represents a Modbus response PDU.
// TransactionID is set for TCP (MBAP); zero for RTU.
type Response struct {
	UnitID        uint8
	FunctionCode  uint8
	Payload       []byte
	TransactionID uint16
	// ConnectionID identifies the connection the response came from (see
	// internal/connid); set by the session engine, 0 when unknown.
	ConnectionID uint64
}
