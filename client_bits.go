// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"fmt"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// Reads multiple coils (function code 01).
func (mc *Client) ReadCoils(ctx context.Context, unitID uint8, addr uint16, quantity uint16) (values []bool, err error) {
	values, err = mc.readBools(ctx, unitID, addr, quantity, false)

	return
}

// Reads a single coil (function code 01).
func (mc *Client) ReadCoil(ctx context.Context, unitID uint8, addr uint16) (value bool, err error) {
	var values []bool

	values, err = mc.readBools(ctx, unitID, addr, 1, false)
	if err == nil {
		value = values[0]
	}

	return
}

// Reads multiple discrete inputs (function code 02).
func (mc *Client) ReadDiscreteInputs(ctx context.Context, unitID uint8, addr uint16, quantity uint16) (values []bool, err error) {
	values, err = mc.readBools(ctx, unitID, addr, quantity, true)

	return
}

// Reads a single discrete input (function code 02).
func (mc *Client) ReadDiscreteInput(ctx context.Context, unitID uint8, addr uint16) (value bool, err error) {
	var values []bool

	values, err = mc.readBools(ctx, unitID, addr, 1, true)
	if err == nil {
		value = values[0]
	}

	return
}

// Writes a single coil (function code 05).
func (mc *Client) WriteCoil(ctx context.Context, unitID uint8, addr uint16, value bool) (err error) {
	var payload uint16

	if value {
		payload = 0xff00
	} else {
		payload = 0x0000
	}

	err = mc.writeCoil(ctx, unitID, addr, payload)

	return
}

// WriteCoilRaw sends a write coil request (function code 05) with a
// specific payload value instead of the standard 0xFF00 (true) or 0x0000
// (false).
//
// WARNING: This is a deliberate violation of the Modbus specification and is
// intended for vendor-specific control semantics only (e.g. toggle, interlock,
// delayed open/close). Compliant devices may reject non-standard payloads.
// Using this method may break interoperability with other clients or gateways.
// Prefer WriteCoil for standard-compliant operations.
func (mc *Client) WriteCoilRaw(ctx context.Context, unitID uint8, addr uint16, payload uint16) (err error) {
	err = mc.writeCoil(ctx, unitID, addr, payload)

	return
}

// Writes multiple coils (function code 15).
func (mc *Client) WriteCoils(ctx context.Context, unitID uint8, addr uint16, values []bool) (err error) {
	quantity := uint16(len(values))
	if err = validateWriteBitsRange(addr, quantity); err != nil {
		return
	}

	encodedValues := encodeBools(values)

	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCWriteMultipleCoils),
	}
	req.Payload = uint16ToBytes(BigEndian, addr)
	req.Payload = append(req.Payload, uint16ToBytes(BigEndian, quantity)...)
	req.Payload = append(req.Payload, byte(len(encodedValues)))
	req.Payload = append(req.Payload, encodedValues...)

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCWriteMultipleCoils)
	}
	defer func() { reportOutcome(m, unitID, FCWriteMultipleCoils, start, err) }()

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

// Reads and returns quantity booleans.
// Digital inputs are read if di is true, otherwise coils are read.
func (mc *Client) readBools(ctx context.Context, unitID uint8, addr uint16, quantity uint16, di bool) (values []bool, err error) {
	if err = validateReadBitsRange(addr, quantity); err != nil {
		return
	}

	req := &adu.Request{
		UnitID: unitID,
	}
	if di {
		req.FunctionCode = byte(FCReadDiscreteInputs)
	} else {
		req.FunctionCode = byte(FCReadCoils)
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

	expectedDataLen := int(quantity) / 8
	if quantity%8 != 0 {
		expectedDataLen++
	}
	if len(data) != expectedDataLen {
		err = newProtocolError("readBools",
			fmt.Sprintf("byte count %d does not match expected %d for quantity %d", len(data), expectedDataLen, quantity))
		return
	}

	values = decodeBools(quantity, data)

	return
}

// Writes a single coil (function code 05) using the specified payload.
func (mc *Client) writeCoil(ctx context.Context, unitID uint8, addr uint16, payload uint16) (err error) {
	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCWriteSingleCoil),
	}
	req.Payload = uint16ToBytes(BigEndian, addr)
	req.Payload = append(req.Payload, uint16ToBytes(BigEndian, payload)...)

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCWriteSingleCoil)
	}
	defer func() { reportOutcome(m, unitID, FCWriteSingleCoil, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}
	err = expectEchoAddrValue(res, addr, payload)

	return
}
