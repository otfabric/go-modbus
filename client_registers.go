package modbus

import (
	"context"
	"fmt"
	"time"

	"github.com/otfabric/modbus/internal/adu"
)

const maxRegisterBitIndex = 15

// Reads multiple 16-bit registers (function code 03 or 04).
func (mc *Client) ReadRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType RegType) (values []uint16, err error) {
	var mbPayload []byte

	// read quantity uint16 registers, as bytes
	mbPayload, err = mc.readRegisterPayload(ctx, unitID, addr, quantity, regType)
	if err != nil {
		return
	}

	// decode payload bytes as uint16s
	values = bytesToUint16s(BigEndian, mbPayload)

	return
}

// Reads a single 16-bit register (function code 03 or 04).
func (mc *Client) ReadRegister(ctx context.Context, unitID uint8, addr uint16, regType RegType) (value uint16, err error) {
	var mbPayload []byte

	// read 1 uint16 register, as bytes
	mbPayload, err = mc.readRegisterPayload(ctx, unitID, addr, 1, regType)
	if err == nil {
		value = bytesToUint16s(BigEndian, mbPayload)[0]
	}

	return
}

// ReadRegisterBytes reads one or more 16-bit registers (FC03/FC04) as raw bytes.
// byteCount is the number of bytes to read (the library reads ceil(byteCount/2)
// registers). No interpretation or reordering is applied; bytes are returned
// exactly as on the wire. For typed interpretation use codec.ReadFromClient.
// Odd byteCount is valid: the library reads ceil(byteCount/2) registers and
// returns exactly byteCount bytes, trimming the final padding byte when needed.
// This is still register-based access — odd byte counts are emulated via
// partial final register handling.
func (mc *Client) ReadRegisterBytes(ctx context.Context, unitID uint8, addr uint16, byteCount uint16, regType RegType) (values []byte, err error) {
	values, err = mc.readBytes(ctx, unitID, addr, byteCount, regType)
	return
}

// ReadHoldingRegister reads one holding register (FC03). Convenience wrapper for ReadRegister with HoldingRegister.
func (mc *Client) ReadHoldingRegister(ctx context.Context, unitID uint8, addr uint16) (uint16, error) {
	return mc.ReadRegister(ctx, unitID, addr, HoldingRegister)
}

// ReadHoldingRegisters reads multiple holding registers (FC03). Convenience wrapper for ReadRegisters with HoldingRegister.
func (mc *Client) ReadHoldingRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16) ([]uint16, error) {
	return mc.ReadRegisters(ctx, unitID, addr, quantity, HoldingRegister)
}

// ReadInputRegister reads one input register (FC04). Convenience wrapper for ReadRegister with InputRegister.
func (mc *Client) ReadInputRegister(ctx context.Context, unitID uint8, addr uint16) (uint16, error) {
	return mc.ReadRegister(ctx, unitID, addr, InputRegister)
}

// ReadInputRegisters reads multiple input registers (FC04). Convenience wrapper for ReadRegisters with InputRegister.
func (mc *Client) ReadInputRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16) ([]uint16, error) {
	return mc.ReadRegisters(ctx, unitID, addr, quantity, InputRegister)
}

// ReadRegisterBit reads one register (FC03/FC04) and returns the value of the bit at bitIndex (0 = LSB, 15 = MSB).
// Useful for status bits, alarm bits, and enums packed in a single register.
func (mc *Client) ReadRegisterBit(ctx context.Context, unitID uint8, addr uint16, bitIndex uint8, regType RegType) (bool, error) {
	if bitIndex > maxRegisterBitIndex {
		return false, newParameterError("ReadRegisterBit", "bitIndex",
			fmt.Sprintf("must be 0..%d, got %d", maxRegisterBitIndex, bitIndex))
	}
	reg, err := mc.ReadRegister(ctx, unitID, addr, regType)
	if err != nil {
		return false, err
	}
	return (reg>>bitIndex)&1 != 0, nil
}

// ReadRegisterBits reads one register (FC03/FC04) and returns count bits starting at bitIndex (0 = LSB).
// count must be 1–16 and bitIndex+count must not exceed 16. Useful for multi-bit fields (e.g. mode enums).
func (mc *Client) ReadRegisterBits(ctx context.Context, unitID uint8, addr uint16, bitIndex, count uint8, regType RegType) ([]bool, error) {
	if count == 0 || count > 16 || bitIndex > maxRegisterBitIndex || uint16(bitIndex)+uint16(count) > 16 {
		return nil, newParameterError("ReadRegisterBits", "bitIndex+count",
			fmt.Sprintf("bitIndex=%d count=%d out of valid range", bitIndex, count))
	}
	reg, err := mc.ReadRegister(ctx, unitID, addr, regType)
	if err != nil {
		return nil, err
	}
	out := make([]bool, count)
	for i := uint8(0); i < count; i++ {
		out[i] = (reg>>(bitIndex+i))&1 != 0
	}
	return out, nil
}

// WriteRegisterBit reads the register at addr (FC03), sets or clears the bit at bitIndex (0 = LSB, 15 = MSB),
// and writes the result back (FC16). Other bits are unchanged. Only holding registers are written.
//
// WARNING: This performs a client-side read-modify-write and is NOT atomic.
// Do not use when other clients or controller logic may concurrently modify
// the same register; prefer MaskWriteRegister (FC22) when the device supports it.
func (mc *Client) WriteRegisterBit(ctx context.Context, unitID uint8, addr uint16, bitIndex uint8, value bool) error {
	if bitIndex > maxRegisterBitIndex {
		return newParameterError("WriteRegisterBit", "bitIndex",
			fmt.Sprintf("must be 0..%d, got %d", maxRegisterBitIndex, bitIndex))
	}
	mbPayload, err := mc.readRegisterPayload(ctx, unitID, addr, 1, HoldingRegister)
	if err != nil {
		return err
	}
	reg := bytesToUint16s(BigEndian, mbPayload)[0]
	if value {
		reg |= 1 << bitIndex
	} else {
		reg &^= 1 << bitIndex
	}
	return mc.writeRegisterPayload(ctx, unitID, addr, uint16ToBytes(BigEndian, reg))
}

// UpdateRegisterMask performs a client-side read-modify-write on a single holding register:
// newVal = (old & ^mask) | (value & mask). It uses FC03 then FC16, not protocol FC22 (Mask Write Register).
// Only the bits set in mask are updated; others are preserved. Useful for control words and mode fields without clobbering adjacent bits.
//
// WARNING: This performs a client-side read-modify-write and is NOT atomic.
// Do not use when other clients or controller logic may concurrently modify
// the same register; prefer MaskWriteRegister (FC22) when the device supports it.
func (mc *Client) UpdateRegisterMask(ctx context.Context, unitID uint8, addr uint16, mask, value uint16) error {
	mbPayload, err := mc.readRegisterPayload(ctx, unitID, addr, 1, HoldingRegister)
	if err != nil {
		return err
	}
	old := bytesToUint16s(BigEndian, mbPayload)[0]
	newVal := (old & ^mask) | (value & mask)
	return mc.writeRegisterPayload(ctx, unitID, addr, uint16ToBytes(BigEndian, newVal))
}

// MaskWriteRegister performs an atomic read-modify-write on a single holding register
// using Modbus FC22 (0x16). The device applies: result = (current AND andMask) OR (orMask AND NOT andMask).
// This is a server-side atomic operation — no client-side read is needed.
// Use this when concurrent writers may modify the same register.
func (mc *Client) MaskWriteRegister(ctx context.Context, unitID uint8, addr uint16, andMask, orMask uint16) (err error) {
	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCMaskWriteRegister),
	}
	req.Payload = uint16ToBytes(BigEndian, addr)
	req.Payload = append(req.Payload, uint16ToBytes(BigEndian, andMask)...)
	req.Payload = append(req.Payload, uint16ToBytes(BigEndian, orMask)...)

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCMaskWriteRegister)
	}
	defer func() { reportOutcome(m, unitID, FCMaskWriteRegister, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return err
	}
	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return err
	}
	if len(res.Payload) != 6 {
		err = newProtocolError("MaskWriteRegister",
			fmt.Sprintf("expected exactly 6-byte echo, got %d bytes", len(res.Payload)))
		return err
	}
	echoAddr := bytesToUint16(BigEndian, res.Payload[0:2])
	echoAnd := bytesToUint16(BigEndian, res.Payload[2:4])
	echoOr := bytesToUint16(BigEndian, res.Payload[4:6])
	if echoAddr != addr || echoAnd != andMask || echoOr != orMask {
		err = newProtocolError("MaskWriteRegister",
			fmt.Sprintf("echo mismatch: expected addr=0x%04X and=0x%04X or=0x%04X, got addr=0x%04X and=0x%04X or=0x%04X",
				addr, andMask, orMask, echoAddr, echoAnd, echoOr))
	}
	return
}

// Writes a single 16-bit register (function code 06).
func (mc *Client) WriteRegister(ctx context.Context, unitID uint8, addr uint16, value uint16) (err error) {
	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCWriteSingleRegister),
	}
	req.Payload = uint16ToBytes(BigEndian, addr)
	req.Payload = append(req.Payload, uint16ToBytes(BigEndian, value)...)

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCWriteSingleRegister)
	}
	defer func() { reportOutcome(m, unitID, FCWriteSingleRegister, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}
	err = expectEchoAddrValue(res, addr, value)

	return
}

// Writes multiple 16-bit registers (function code 16).
func (mc *Client) WriteRegisters(ctx context.Context, unitID uint8, addr uint16, values []uint16) (err error) {
	payload := make([]byte, 0, len(values)*2)
	for _, value := range values {
		payload = append(payload, uint16ToBytes(BigEndian, value)...)
	}

	err = mc.writeRegisterPayload(ctx, unitID, addr, payload)

	return
}

// WriteRegisterBytes writes raw bytes into holding registers (FC16). No
// interpretation or reordering is applied. For typed values use
// codec.WriteToClient or WriteWithCodec. Odd-length slices are zero-padded
// to the next register boundary. This is still register-based access.
func (mc *Client) WriteRegisterBytes(ctx context.Context, unitID uint8, addr uint16, values []byte) (err error) {
	err = mc.writeBytes(ctx, unitID, addr, values)

	return
}

// Performs a combined read/write in a single Modbus transaction (function code 23).
// The write is executed on the server before the read.
// writeValues are encoded as big-endian (wire order).
// The returned slice contains the registers read, also as big-endian.
//
// Limits (per spec):
//
//	readQty:  1–125 (0x7D)
//	writeQty: 1–121 (0x79), implied by len(writeValues)
func (mc *Client) ReadWriteMultipleRegisters(ctx context.Context, unitID uint8, readAddr, readQty, writeAddr uint16, writeValues []uint16) (values []uint16, err error) {
	writeQty := uint16(len(writeValues))

	if readQty == 0 || readQty > maxRWReadRegs {
		err = newParameterError("ReadWriteMultipleRegisters", "readQty",
			fmt.Sprintf("must be 1..%d, got %d", maxRWReadRegs, readQty))
		return
	}
	if writeQty == 0 || writeQty > maxRWWriteRegs {
		err = newParameterError("ReadWriteMultipleRegisters", "writeValues",
			fmt.Sprintf("length must be 1..%d, got %d", maxRWWriteRegs, writeQty))
		return
	}
	if uint32(readAddr)+uint32(readQty)-1 > 0xffff {
		err = newParameterError("ReadWriteMultipleRegisters", "readAddr+readQty",
			fmt.Sprintf("range 0x%04X+%d overflows address space", readAddr, readQty))
		return
	}
	if uint32(writeAddr)+uint32(writeQty)-1 > 0xffff {
		err = newParameterError("ReadWriteMultipleRegisters", "writeAddr+writeQty",
			fmt.Sprintf("range 0x%04X+%d overflows address space", writeAddr, writeQty))
		return
	}

	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCReadWriteMultipleRegs),
	}
	req.Payload = uint16ToBytes(BigEndian, readAddr)
	req.Payload = append(req.Payload, uint16ToBytes(BigEndian, readQty)...)
	req.Payload = append(req.Payload, uint16ToBytes(BigEndian, writeAddr)...)
	req.Payload = append(req.Payload, uint16ToBytes(BigEndian, writeQty)...)
	req.Payload = append(req.Payload, byte(writeQty*2))
	for _, v := range writeValues {
		req.Payload = append(req.Payload, uint16ToBytes(BigEndian, v)...)
	}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCReadWriteMultipleRegs)
	}
	defer func() { reportOutcome(m, unitID, FCReadWriteMultipleRegs, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}

	data, err := extractByteCountPayload(res)
	if err != nil {
		return
	}
	if len(data) != 2*int(readQty) {
		err = newProtocolError("ReadWriteMultipleRegisters",
			fmt.Sprintf("expected %d data bytes for readQty=%d, got %d", 2*int(readQty), readQty, len(data)))
		return
	}
	values = bytesToUint16s(BigEndian, data)

	return
}

// Reads the contents of a FIFO queue of holding registers (function code 24).
// addr is the FIFO Pointer Address (the count register); registers are returned
// as big-endian uint16 values exactly as they arrive from the device.
// The FIFO queue may contain at most 31 registers; an exception response is
// returned by the server if the count exceeds 31.
func (mc *Client) ReadFIFOQueue(ctx context.Context, unitID uint8, addr uint16) (values []uint16, err error) {
	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCReadFIFOQueue),
		Payload:      uint16ToBytes(BigEndian, addr),
	}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCReadFIFOQueue)
	}
	defer func() { reportOutcome(m, unitID, FCReadFIFOQueue, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}

	if len(res.Payload) < 4 {
		err = newProtocolError("ReadFIFOQueue",
			fmt.Sprintf("payload too short: %d bytes, need at least 4", len(res.Payload)))
		return
	}
	byteCount := bytesToUint16(BigEndian, res.Payload[0:2])
	fifoCount := bytesToUint16(BigEndian, res.Payload[2:4])

	if fifoCount > maxFIFOCount {
		err = newProtocolError("ReadFIFOQueue",
			fmt.Sprintf("FIFO count %d exceeds maximum %d", fifoCount, maxFIFOCount))
		return
	}
	if int(byteCount) != 2+2*int(fifoCount) {
		err = newProtocolError("ReadFIFOQueue",
			fmt.Sprintf("byte count %d inconsistent with FIFO count %d", byteCount, fifoCount))
		return
	}
	if len(res.Payload) != 2+int(byteCount) {
		err = newProtocolError("ReadFIFOQueue",
			fmt.Sprintf("payload length %d does not match byte count %d", len(res.Payload)-2, byteCount))
		return
	}
	values = bytesToUint16s(BigEndian, res.Payload[4:])

	return
}

/*** unexported methods ***/

// Reads one or multiple 16-bit registers (function code 03 or 04) as bytes (wire order, no swap).
func (mc *Client) readBytes(ctx context.Context, unitID uint8, addr uint16, byteCount uint16, regType RegType) (values []byte, err error) {
	var regCount = (byteCount / 2) + (byteCount % 2)
	values, err = mc.readRegisterPayload(ctx, unitID, addr, regCount, regType)
	if err != nil {
		return
	}
	if byteCount%2 == 1 {
		values = values[0 : len(values)-1]
	}
	return
}

// Writes the given slice of bytes to 16-bit registers (wire order, no swap).
// Odd-length input is zero-padded to the next register boundary without
// mutating the caller's slice.
func (mc *Client) writeBytes(ctx context.Context, unitID uint8, addr uint16, values []byte) (err error) {
	buf := values
	if len(buf)%2 == 1 {
		buf = append([]byte(nil), values...)
		buf = append(buf, 0x00)
	}
	return mc.writeRegisterPayload(ctx, unitID, addr, buf)
}

// Reads and returns quantity registers of type regType, as bytes.
func (mc *Client) readRegisterPayload(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType RegType) (bytes []byte, err error) {
	req := &adu.Request{
		UnitID: unitID,
	}

	switch regType {
	case HoldingRegister:
		req.FunctionCode = byte(FCReadHoldingRegisters)
	case InputRegister:
		req.FunctionCode = byte(FCReadInputRegisters)
	default:
		err = newParameterError("ReadRegisters", "regType",
			fmt.Sprintf("unsupported register type %v", regType))
		return
	}

	if err = validateReadRegsRange(addr, quantity); err != nil {
		return
	}

	req.Payload = uint16ToBytes(BigEndian, addr)
	req.Payload = append(req.Payload, uint16ToBytes(BigEndian, quantity)...)

	fc := FunctionCode(req.FunctionCode)
	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, fc)
	}
	defer func() { reportOutcome(m, unitID, fc, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}

	data, err := extractByteCountPayload(res)
	if err != nil {
		return
	}
	if len(data) != 2*int(quantity) {
		err = newProtocolError("ReadRegisters",
			fmt.Sprintf("expected %d data bytes for %d registers, got %d", 2*int(quantity), quantity, len(data)))
		return
	}

	bytes = data

	return
}

// Writes multiple registers starting from base address addr.
// Register values are passed as bytes, each value being exactly 2 bytes.
func (mc *Client) writeRegisterPayload(ctx context.Context, unitID uint8, addr uint16, values []byte) (err error) {
	if len(values)%2 != 0 {
		return newParameterError("WriteRegisters", "values",
			fmt.Sprintf("byte slice length %d is odd, expected even", len(values)))
	}
	payloadLength := uint16(len(values))
	quantity := payloadLength / 2

	if err = validateWriteRegsRange(addr, quantity); err != nil {
		return
	}

	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCWriteMultipleRegisters),
	}
	req.Payload = uint16ToBytes(BigEndian, addr)
	req.Payload = append(req.Payload, uint16ToBytes(BigEndian, quantity)...)
	req.Payload = append(req.Payload, byte(payloadLength))
	req.Payload = append(req.Payload, values...)

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCWriteMultipleRegisters)
	}
	defer func() { reportOutcome(m, unitID, FCWriteMultipleRegisters, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}
	err = expectEchoAddrQuantity(res, addr, quantity)

	return
}
