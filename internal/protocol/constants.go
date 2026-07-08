// SPDX-License-Identifier: MIT

package protocol

// MEIType is the Modbus Encapsulated Interface type (FC43).
type MEIType uint8

const (
	MEIReadDeviceIdentification MEIType = 0x0E
)

// FC43 Read Device ID category codes.
const (
	ReadDeviceIDBasic      = 0x01
	ReadDeviceIDRegular    = 0x02
	ReadDeviceIDExtended   = 0x03
	ReadDeviceIDIndividual = 0x04
)

// RegType selects holding vs input registers for read operations.
type RegType uint

const (
	HoldingRegister RegType = 0
	InputRegister   RegType = 1
)

// PortModbusTCP is the well-known port for Modbus/TCP (IANA registered).
const PortModbusTCP = 502

// PortModbusTLS is the well-known port for Modbus/TCP Security (Modbus over TLS).
const PortModbusTLS = 802

// Protocol limits (Modbus spec).
const (
	MaxReadCoils      = 2000
	MaxWriteCoils     = 1968
	MaxReadRegisters  = 125
	MaxWriteRegisters = 123
	MaxRWReadRegs     = 125
	MaxRWWriteRegs    = 121
	MaxFIFOCount      = 31
	MaxFileByteCount  = 0xF5
	MaxFileReqDataLen = 0xFB
)
