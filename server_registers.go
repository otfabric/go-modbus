package modbus

import (
	"context"

	"github.com/otfabric/modbus/internal/adu"
)

// handleReadRegisters handles FC03 (ReadHoldingRegisters) and FC04 (ReadInputRegisters).
func (ms *Server) handleReadRegisters(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	addr, quantity, err := decodeAddrQuantity(req.Payload)
	if err != nil {
		return nil, err
	}

	if quantity > maxReadRegisters || quantity == 0 {
		return nil, ErrIllegalDataValue
	}
	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		return nil, ErrIllegalDataAddress
	}

	var regs []uint16
	fc := FunctionCode(req.FunctionCode)
	if fc == FCReadHoldingRegisters {
		regs, err = ms.handler.HandleHoldingRegisters(ctx, &HoldingRegistersRequest{
			ClientAddr:   clientAddr,
			ClientRole:   clientRole,
			UnitID:       req.UnitID,
			FunctionCode: fc,
			Addr:         addr,
			Quantity:     quantity,
			IsWrite:      false,
		})
	} else {
		regs, err = ms.handler.HandleInputRegisters(ctx, &InputRegistersRequest{
			ClientAddr:   clientAddr,
			ClientRole:   clientRole,
			UnitID:       req.UnitID,
			FunctionCode: fc,
			Addr:         addr,
			Quantity:     quantity,
		})
	}

	if err != nil {
		return nil, err
	}
	if len(regs) != int(quantity) {
		ms.logger.Errorf("handler returned %v 16-bit values, expected %v", len(regs), quantity)
		return nil, ErrServerDeviceFailure
	}

	payload := []byte{uint8(len(regs) * 2)}
	payload = append(payload, uint16sToBytes(BigEndian, regs)...)

	return newSuccessResponse(req, txnID, payload), nil
}

// handleWriteSingleRegister handles FC06 (WriteSingleRegister).
func (ms *Server) handleWriteSingleRegister(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	if len(req.Payload) != 4 {
		return nil, ErrProtocolError
	}

	addr := bytesToUint16(BigEndian, req.Payload[0:2])
	value := bytesToUint16(BigEndian, req.Payload[2:4])

	_, err := ms.handler.HandleHoldingRegisters(ctx, &HoldingRegistersRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FCWriteSingleRegister,
		Addr:         addr,
		Quantity:     1,
		IsWrite:      true,
		Args:         []uint16{value},
	})
	if err != nil {
		return nil, err
	}

	payload := uint16ToBytes(BigEndian, addr)
	payload = append(payload, uint16ToBytes(BigEndian, value)...)

	return newSuccessResponse(req, txnID, payload), nil
}

// handleMaskWriteRegister handles FC22 (MaskWriteRegister).
// Requires the RequestHandler to also implement MaskWriteHandler.
func (ms *Server) handleMaskWriteRegister(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	mwh, ok := ms.handler.(MaskWriteHandler)
	if !ok {
		return nil, ErrIllegalFunction
	}
	if len(req.Payload) != 6 {
		return nil, ErrProtocolError
	}

	addr := bytesToUint16(BigEndian, req.Payload[0:2])
	andMask := bytesToUint16(BigEndian, req.Payload[2:4])
	orMask := bytesToUint16(BigEndian, req.Payload[4:6])

	err := mwh.HandleMaskWrite(ctx, &MaskWriteRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FCMaskWriteRegister,
		Addr:         addr,
		AndMask:      andMask,
		OrMask:       orMask,
	})
	if err != nil {
		return nil, err
	}

	return newSuccessResponse(req, txnID, req.Payload), nil
}

// handleReadWriteMultipleRegisters handles FC23 (ReadWriteMultipleRegisters).
// Requires the RequestHandler to also implement ReadWriteHandler.
func (ms *Server) handleReadWriteMultipleRegisters(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	rwh, ok := ms.handler.(ReadWriteHandler)
	if !ok {
		return nil, ErrIllegalFunction
	}
	if len(req.Payload) < 10 {
		return nil, ErrProtocolError
	}

	readAddr := bytesToUint16(BigEndian, req.Payload[0:2])
	readQty := bytesToUint16(BigEndian, req.Payload[2:4])
	writeAddr := bytesToUint16(BigEndian, req.Payload[4:6])
	writeQty := bytesToUint16(BigEndian, req.Payload[6:8])
	byteCount := req.Payload[8]

	if readQty == 0 || readQty > maxRWReadRegs {
		return nil, ErrIllegalDataValue
	}
	if writeQty == 0 || writeQty > maxRWWriteRegs {
		return nil, ErrIllegalDataValue
	}
	if uint32(readAddr)+uint32(readQty)-1 > 0xffff {
		return nil, ErrIllegalDataAddress
	}
	if uint32(writeAddr)+uint32(writeQty)-1 > 0xffff {
		return nil, ErrIllegalDataAddress
	}
	expectedLen := int(writeQty) * 2
	if int(byteCount) != expectedLen {
		return nil, ErrProtocolError
	}
	if len(req.Payload)-9 != expectedLen {
		return nil, ErrProtocolError
	}

	writeValues := bytesToUint16s(BigEndian, req.Payload[9:])

	regs, err := rwh.HandleReadWriteRegisters(ctx, &ReadWriteRegistersRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FCReadWriteMultipleRegs,
		ReadAddr:     readAddr,
		ReadQty:      readQty,
		WriteAddr:    writeAddr,
		WriteValues:  writeValues,
	})
	if err != nil {
		return nil, err
	}
	if len(regs) != int(readQty) {
		ms.logger.Errorf("handler returned %v 16-bit values, expected %v", len(regs), readQty)
		return nil, ErrServerDeviceFailure
	}

	payload := []byte{uint8(len(regs) * 2)}
	payload = append(payload, uint16sToBytes(BigEndian, regs)...)
	return newSuccessResponse(req, txnID, payload), nil
}

// handleWriteMultipleRegisters handles FC16 (WriteMultipleRegisters).
func (ms *Server) handleWriteMultipleRegisters(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	if len(req.Payload) < 6 {
		return nil, ErrProtocolError
	}

	addr := bytesToUint16(BigEndian, req.Payload[0:2])
	quantity := bytesToUint16(BigEndian, req.Payload[2:4])

	if quantity > maxWriteRegisters || quantity == 0 {
		return nil, ErrIllegalDataValue
	}
	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		return nil, ErrIllegalDataAddress
	}

	expectedLen := int(quantity) * 2
	if req.Payload[4] != uint8(expectedLen) {
		return nil, ErrProtocolError
	}
	if len(req.Payload)-5 != expectedLen {
		return nil, ErrProtocolError
	}

	_, err := ms.handler.HandleHoldingRegisters(ctx, &HoldingRegistersRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FCWriteMultipleRegisters,
		Addr:         addr,
		Quantity:     quantity,
		IsWrite:      true,
		Args:         bytesToUint16s(BigEndian, req.Payload[5:]),
	})
	if err != nil {
		return nil, err
	}

	return newEchoAddrQuantityResponse(req, txnID, addr, quantity), nil
}
