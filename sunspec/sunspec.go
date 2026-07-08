// SPDX-License-Identifier: MIT

// Package sunspec provides SunSpec marker detection and model chain
// discovery for Modbus devices.
//
// Users should import this package directly:
//
//	import "github.com/otfabric/go-modbus/sunspec"
package sunspec

import (
	"context"
	"errors"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// RegType identifies the Modbus register type (holding or input).
type RegType = protocol.RegType

const (
	HoldingRegister RegType = protocol.HoldingRegister
	InputRegister   RegType = protocol.InputRegister
)

// Reader is the minimal interface needed by the SunSpec helpers.
// ModbusClient satisfies this through a thin adapter.
type Reader interface {
	ReadRawBytes(ctx context.Context, unitID uint8, addr uint16, byteCount uint16, regType RegType) ([]byte, error)
}

const (
	MarkerReg0     uint16 = 0x5375 // 'S'<<8 | 'u'
	MarkerReg1     uint16 = 0x6E53 // 'n'<<8 | 'S'
	EndModelID     uint16 = 0xFFFF
	EndModelLength uint16 = 0
)

// DefaultBaseAddresses lists the register addresses probed when detecting a
// SunSpec marker. Address 40000 is the most common starting point per SunSpec
// Information Model specification. Addresses 0 and 50000 cover devices that
// place the marker at the very start of the register map or at an alternative
// base. Addresses 1, 39999, 40001, 49999, and 50001 handle off-by-one
// deviations observed in real-world devices and gateways.
var DefaultBaseAddresses = []uint16{0, 40000, 50000, 1, 39999, 40001, 49999, 50001}

type Options struct {
	UnitID         uint8
	RegType        protocol.RegType
	BaseAddresses  []uint16
	MaxModels      int
	MaxAddressSpan uint16
}

type ProbeAttempt struct {
	BaseAddress uint16
	RegType     protocol.RegType
	Registers   []uint16
	Matched     bool
	Error       error  `json:"-"`
	ErrorString string `json:"error,omitempty"`
}

type DetectionResult struct {
	Detected    bool
	UnitID      uint8
	RegType     protocol.RegType
	BaseAddress uint16
	Marker      [2]uint16
	Attempts    []ProbeAttempt
}

type ModelHeader struct {
	ID           uint16
	Length       uint16
	StartAddress uint16
	EndAddress   uint16
	NextAddress  uint16
	HeaderRaw    [2]uint16
	IsEndModel   bool
}

type DiscoveryResult struct {
	Detection DetectionResult
	Models    []ModelHeader
}

func ApplyDefaults(opts *Options) Options {
	o := Options{
		UnitID:        1,
		RegType:       protocol.HoldingRegister,
		BaseAddresses: DefaultBaseAddresses,
		MaxModels:     256,
	}
	if opts != nil {
		o.UnitID = opts.UnitID
		if o.UnitID == 0 {
			o.UnitID = 1
		}
		o.RegType = opts.RegType
		o.MaxModels = opts.MaxModels
		o.MaxAddressSpan = opts.MaxAddressSpan
		if opts.BaseAddresses != nil {
			o.BaseAddresses = opts.BaseAddresses
		}
	}
	if o.MaxModels <= 0 {
		o.MaxModels = 256
	}
	return o
}

func ValidateOptions(o *Options) error {
	if o.RegType != protocol.HoldingRegister && o.RegType != protocol.InputRegister {
		return protocol.NewParameterError("sunspec.Options", "RegType", "must be HoldingRegister or InputRegister")
	}
	if len(o.BaseAddresses) == 0 {
		return protocol.NewParameterError("sunspec.Options", "BaseAddresses", "must not be empty")
	}
	return nil
}

func Detect(ctx context.Context, r Reader, opts *Options) (*DetectionResult, error) {
	o := ApplyDefaults(opts)
	if err := ValidateOptions(&o); err != nil {
		return nil, err
	}
	res := &DetectionResult{
		UnitID:   o.UnitID,
		RegType:  o.RegType,
		Attempts: make([]ProbeAttempt, 0, len(o.BaseAddresses)),
	}
	var anyProbeSucceeded bool

	for _, base := range o.BaseAddresses {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}

		attempt := ProbeAttempt{BaseAddress: base, RegType: o.RegType}
		raw, err := r.ReadRawBytes(ctx, o.UnitID, base, 4, o.RegType)
		if err != nil {
			attempt.Error = err
			attempt.ErrorString = err.Error()
			res.Attempts = append(res.Attempts, attempt)
			continue
		}
		if len(raw) != 4 {
			res.Attempts = append(res.Attempts, attempt)
			continue
		}
		anyProbeSucceeded = true
		regs := adu.BytesToUint16s(adu.BigEndian, raw)
		attempt.Registers = regs
		matched := len(regs) >= 2 && regs[0] == MarkerReg0 && regs[1] == MarkerReg1
		attempt.Matched = matched
		res.Attempts = append(res.Attempts, attempt)

		if matched {
			res.Detected = true
			res.BaseAddress = base
			res.Marker = [2]uint16{regs[0], regs[1]}
			return res, nil
		}
	}

	if !anyProbeSucceeded && len(res.Attempts) > 0 {
		var errs []error
		for _, a := range res.Attempts {
			if a.Error != nil {
				errs = append(errs, a.Error)
			}
		}
		if len(errs) > 0 {
			return res, errors.Join(errs...)
		}
	}
	return res, nil
}

func ReadModelHeaders(ctx context.Context, r Reader, opts *Options, baseAddress uint16) ([]ModelHeader, error) {
	o := ApplyDefaults(opts)
	if err := ValidateOptions(&o); err != nil {
		return nil, err
	}
	start := uint32(baseAddress) + 2
	if start > 0xFFFF {
		return nil, protocol.ErrSunSpecModelChainInvalid
	}
	addr := uint16(start)

	maxModels := o.MaxModels
	if maxModels <= 0 {
		maxModels = 256
	}

	var models []ModelHeader

	for len(models) < maxModels {
		select {
		case <-ctx.Done():
			return models, ctx.Err()
		default:
		}

		raw, err := r.ReadRawBytes(ctx, o.UnitID, addr, 4, o.RegType)
		if err != nil {
			return models, err
		}
		if len(raw) != 4 {
			return models, protocol.ErrProtocolError
		}
		regs := adu.BytesToUint16s(adu.BigEndian, raw)
		id, length := regs[0], regs[1]

		isEnd := (id == EndModelID && length == EndModelLength)

		endExclusive := uint32(addr) + 2 + uint32(length)
		if endExclusive > 0x10000 {
			return models, protocol.ErrProtocolError
		}
		endAddr := uint16(endExclusive)
		nextAddr := endAddr

		h := ModelHeader{
			ID:           id,
			Length:       length,
			StartAddress: addr,
			EndAddress:   endAddr - 1,
			NextAddress:  nextAddr,
			HeaderRaw:    [2]uint16{id, length},
			IsEndModel:   isEnd,
		}
		models = append(models, h)

		if isEnd {
			break
		}

		if length == 0 && id != EndModelID {
			return models, protocol.ErrSunSpecModelChainInvalid
		}
		if nextAddr <= addr {
			return models, protocol.ErrSunSpecModelChainInvalid
		}

		if o.MaxAddressSpan > 0 {
			if uint32(nextAddr)-uint32(baseAddress) > uint32(o.MaxAddressSpan) {
				return models, protocol.ErrSunSpecModelChainLimitExceeded
			}
		}

		addr = nextAddr
	}

	return models, nil
}

func Discover(ctx context.Context, r Reader, opts *Options) (*DiscoveryResult, error) {
	det, err := Detect(ctx, r, opts)
	if err != nil {
		return nil, err
	}
	out := &DiscoveryResult{Detection: *det}
	if !det.Detected {
		return out, nil
	}

	models, err := ReadModelHeaders(ctx, r, opts, det.BaseAddress)
	out.Models = models
	return out, err
}
