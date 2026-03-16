package modbus

import (
	"context"

	"github.com/otfabric/go-modbus/internal/adu"
)

// handleReadBools handles FC01 (ReadCoils) and FC02 (ReadDiscreteInputs).
func (ms *Server) handleReadBools(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	addr, quantity, err := decodeAddrQuantity(req.Payload)
	if err != nil {
		return nil, err
	}

	if quantity > maxReadCoils || quantity == 0 {
		return nil, ErrIllegalDataValue
	}
	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		return nil, ErrIllegalDataAddress
	}

	var coils []bool
	fc := FunctionCode(req.FunctionCode)
	if fc == FCReadCoils {
		coils, err = ms.handler.HandleCoils(ctx, &CoilsRequest{
			ClientAddr:   clientAddr,
			ClientRole:   clientRole,
			UnitID:       req.UnitID,
			FunctionCode: fc,
			Addr:         addr,
			Quantity:     quantity,
			IsWrite:      false,
		})
	} else {
		coils, err = ms.handler.HandleDiscreteInputs(ctx, &DiscreteInputsRequest{
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
	if len(coils) != int(quantity) {
		ms.logger.Errorf("handler returned %v bools, expected %v", len(coils), quantity)
		return nil, ErrServerDeviceFailure
	}

	byteCount := uint8(len(coils) / 8)
	if len(coils)%8 != 0 {
		byteCount++
	}

	payload := []byte{byteCount}
	payload = append(payload, encodeBools(coils)...)

	return newSuccessResponse(req, txnID, payload), nil
}

// handleWriteSingleCoil handles FC05 (WriteSingleCoil).
func (ms *Server) handleWriteSingleCoil(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	if len(req.Payload) != 4 {
		return nil, ErrProtocolError
	}

	addr := bytesToUint16(BigEndian, req.Payload[0:2])

	if (req.Payload[2] != 0xff && req.Payload[2] != 0x00) || req.Payload[3] != 0x00 {
		return nil, ErrIllegalDataValue
	}

	_, err := ms.handler.HandleCoils(ctx, &CoilsRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FCWriteSingleCoil,
		Addr:         addr,
		Quantity:     1,
		IsWrite:      true,
		Args:         []bool{req.Payload[2] == 0xff},
	})
	if err != nil {
		return nil, err
	}

	payload := uint16ToBytes(BigEndian, addr)
	payload = append(payload, req.Payload[2], req.Payload[3])

	return newSuccessResponse(req, txnID, payload), nil
}

// handleWriteMultipleCoils handles FC15 (WriteMultipleCoils).
func (ms *Server) handleWriteMultipleCoils(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	if len(req.Payload) < 6 {
		return nil, ErrProtocolError
	}

	addr := bytesToUint16(BigEndian, req.Payload[0:2])
	quantity := bytesToUint16(BigEndian, req.Payload[2:4])

	if quantity > maxWriteCoils || quantity == 0 {
		return nil, ErrIllegalDataValue
	}
	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		return nil, ErrIllegalDataAddress
	}

	expectedLen := int(quantity) / 8
	if quantity%8 != 0 {
		expectedLen++
	}

	if req.Payload[4] != uint8(expectedLen) {
		return nil, ErrProtocolError
	}
	if len(req.Payload)-5 != expectedLen {
		return nil, ErrProtocolError
	}

	_, err := ms.handler.HandleCoils(ctx, &CoilsRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FCWriteMultipleCoils,
		Addr:         addr,
		Quantity:     quantity,
		IsWrite:      true,
		Args:         decodeBools(quantity, req.Payload[5:]),
	})
	if err != nil {
		return nil, err
	}

	return newEchoAddrQuantityResponse(req, txnID, addr, quantity), nil
}
