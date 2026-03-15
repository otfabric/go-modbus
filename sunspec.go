package modbus

import (
	"context"
	"errors"
)

// SunSpec marker is the 4-byte ASCII "SunS" in two big-endian 16-bit registers.
const (
	SunSpecMarkerReg0 uint16 = 0x5375 // 'S'<<8 | 'u'
	SunSpecMarkerReg1 uint16 = 0x6E53 // 'n'<<8 | 'S'
)

// SunSpecDefaultBaseAddresses are the default SunSpec base addresses to probe.
// Official protocol candidates (0, 40000, 50000) are probed first, then adjacent
// offsets (1, 39999, 40001, 49999, 50001) to tolerate 0-based vs 1-based addressing
// confusion common in vendor documentation and tooling.
var SunSpecDefaultBaseAddresses = []uint16{0, 40000, 50000, 1, 39999, 40001, 49999, 50001}

// SunSpecOptions configures SunSpec detection and model chain discovery.
// If opts is nil, DetectSunSpec and DiscoverSunSpec use defaults:
// RegType = HoldingRegister, BaseAddresses = SunSpecDefaultBaseAddresses, MaxModels = 256.
// UnitID zero is treated as 1 for scanner ergonomics (documented tradeoff: callers must set UnitID explicitly to avoid accidental probe of unit 1).
type SunSpecOptions struct {
	// UnitID is the Modbus slave/unit ID (0–255). Zero defaults to 1 for scanner ergonomics.
	// Full range is allowed because SunSpec devices may sit behind a Modbus gateway.
	UnitID uint8
	// RegType is the register type to use. Zero value means HoldingRegister; set InputRegister to use FC04.
	RegType RegType
	// BaseAddresses are candidate SunSpec map start addresses to probe.
	// Default: []uint16{0, 40000, 50000, 1, 39999, 40001, 49999, 50001}. Override for custom layouts.
	BaseAddresses []uint16
	// MaxModels is the maximum number of model headers to read. Reaching MaxModels stops
	// enumeration and returns the models collected so far without error (normal truncation).
	// Zero means use default 256.
	MaxModels int
	// MaxAddressSpan is the maximum allowed span (end address − base) for the model chain.
	// Zero means no limit. Use to avoid runaway reads on bad devices.
	MaxAddressSpan uint16
}

// SunSpecProbeAttempt holds the result of probing one candidate base address.
// Error is for Go callers only and is omitted from JSON; ErrorString is set when Error is non-nil for serialization.
type SunSpecProbeAttempt struct {
	BaseAddress uint16
	RegType     RegType
	Registers   []uint16 // length 2 when read succeeded
	Matched     bool
	Error       error  `json:"-"`
	ErrorString string `json:"error,omitempty"`
}

// SunSpecDetectionResult is the result of SunSpec marker detection.
// Detected is false when no candidate base had the "SunS" marker; error is nil in that case.
type SunSpecDetectionResult struct {
	Detected    bool
	UnitID      uint8
	RegType     RegType
	BaseAddress uint16 // only valid when Detected is true
	Marker      [2]uint16
	Attempts    []SunSpecProbeAttempt
}

// SunSpecModelHeader is one model header from the SunSpec model chain (ID and length only).
// The library does not decode points or semantics; that belongs in a higher-level SunSpec layer.
type SunSpecModelHeader struct {
	ID           uint16
	Length       uint16 // payload length in registers (not including the 2-register header)
	StartAddress uint16
	EndAddress   uint16
	NextAddress  uint16
	HeaderRaw    [2]uint16
	IsEndModel   bool
}

// SunSpecDiscoveryResult combines detection and the list of model headers.
type SunSpecDiscoveryResult struct {
	Detection SunSpecDetectionResult
	Models    []SunSpecModelHeader
}

// SunSpecEndModelID and SunSpecEndModelLength denote the end-of-map sentinel
// (model ID 0xFFFF with length 0) that marks the end of the SunSpec model chain.
const (
	SunSpecEndModelID     uint16 = 0xFFFF
	SunSpecEndModelLength uint16 = 0
)

func (mc *ModbusClient) sunSpecOptions(opts *SunSpecOptions) SunSpecOptions {
	o := SunSpecOptions{
		UnitID:        1,
		RegType:       HoldingRegister,
		BaseAddresses: SunSpecDefaultBaseAddresses,
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

// validateSunSpecOptions returns ErrUnexpectedParameters if options are invalid after applying defaults.
func validateSunSpecOptions(o *SunSpecOptions) error {
	if o.RegType != HoldingRegister && o.RegType != InputRegister {
		return ErrUnexpectedParameters
	}
	if len(o.BaseAddresses) == 0 {
		return ErrUnexpectedParameters
	}
	return nil
}

// DetectSunSpec probes candidate base addresses for the SunSpec "SunS" marker.
// These APIs are read-only discovery helpers and do not modify device state.
// They use the same request path as other client methods (lock per read, retries, metrics).
//
// Result semantics:
//   - If at least one probe succeeds (valid register read) but no candidate has the "SunS" marker:
//     Detected is false, error is nil ("device is not SunSpec").
//   - If every probe fails (transport/exception so no candidate responded successfully):
//     returns a non-nil error (e.g. errors.Join of attempt errors) so that "could not read at all"
//     is not confused with "device is not SunSpec".
//
// A non-nil error is also returned for invalid options (unsupported RegType, empty BaseAddresses)
// or context cancellation.
func (mc *ModbusClient) DetectSunSpec(ctx context.Context, opts *SunSpecOptions) (*SunSpecDetectionResult, error) {
	o := mc.sunSpecOptions(opts)
	if err := validateSunSpecOptions(&o); err != nil {
		return nil, err
	}
	res := &SunSpecDetectionResult{
		UnitID:   o.UnitID,
		RegType:  o.RegType,
		Attempts: make([]SunSpecProbeAttempt, 0, len(o.BaseAddresses)),
	}
	var anyProbeSucceeded bool

	for _, base := range o.BaseAddresses {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}

		attempt := SunSpecProbeAttempt{BaseAddress: base, RegType: o.RegType}
		raw, err := mc.ReadRawBytes(ctx, o.UnitID, base, 4, o.RegType)
		if err != nil {
			attempt.Error = err
			attempt.ErrorString = err.Error()
			res.Attempts = append(res.Attempts, attempt)
			continue
		}
		if len(raw) != 4 {
			attempt.Registers = nil
			res.Attempts = append(res.Attempts, attempt)
			continue
		}
		anyProbeSucceeded = true
		regs := bytesToUint16s(BigEndian, raw)
		attempt.Registers = regs
		matched := len(regs) >= 2 && regs[0] == SunSpecMarkerReg0 && regs[1] == SunSpecMarkerReg1
		attempt.Matched = matched
		res.Attempts = append(res.Attempts, attempt)

		if matched {
			res.Detected = true
			res.BaseAddress = base
			res.Marker = [2]uint16{regs[0], regs[1]}
			return res, nil
		}
	}

	// If every probe failed due to transport (no candidate responded successfully),
	// return an error so "device not SunSpec" is not confused with "could not read at all".
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

// ReadSunSpecModelHeaders walks the SunSpec model chain starting at baseAddress+2 and
// returns model headers (ID and length) in device order. baseAddress must be a
// validated SunSpec base (e.g. from DetectSunSpec). opts may be nil to use defaults;
// UnitID, RegType, MaxModels, and MaxAddressSpan from opts apply.
// Reaching MaxModels stops enumeration and returns the models collected so far without error.
// Uses the same request path as other client methods (lock per read, retries, metrics).
func (mc *ModbusClient) ReadSunSpecModelHeaders(ctx context.Context, opts *SunSpecOptions, baseAddress uint16) ([]SunSpecModelHeader, error) {
	o := mc.sunSpecOptions(opts)
	if err := validateSunSpecOptions(&o); err != nil {
		return nil, err
	}
	// Guard baseAddress+2 in uint32 to avoid uint16 wrap (e.g. baseAddress 65535 -> 65537 wraps to 1)
	start := uint32(baseAddress) + 2
	if start > 0xFFFF {
		return nil, ErrSunSpecModelChainInvalid
	}
	addr := uint16(start)

	maxModels := o.MaxModels
	if maxModels <= 0 {
		maxModels = 256
	}

	var models []SunSpecModelHeader

	for len(models) < maxModels {
		select {
		case <-ctx.Done():
			return models, ctx.Err()
		default:
		}

		raw, err := mc.ReadRawBytes(ctx, o.UnitID, addr, 4, o.RegType)
		if err != nil {
			return models, err
		}
		if len(raw) != 4 {
			return models, ErrProtocolError
		}
		regs := bytesToUint16s(BigEndian, raw)
		id, length := regs[0], regs[1]

		// End model: ID 0xFFFF, Length 0
		isEnd := (id == SunSpecEndModelID && length == SunSpecEndModelLength)

		// Address overflow: compute in uint32 to avoid wrap before guard
		endExclusive := uint32(addr) + 2 + uint32(length)
		if endExclusive > 0x10000 {
			return models, ErrProtocolError
		}
		endAddr := uint16(endExclusive)
		nextAddr := endAddr

		h := SunSpecModelHeader{
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

		// Malformed: length 0 but not end model
		if length == 0 && id != SunSpecEndModelID {
			return models, ErrSunSpecModelChainInvalid
		}
		if nextAddr <= addr {
			return models, ErrSunSpecModelChainInvalid
		}

		// Span guard: exceeded caller-configured limit
		if o.MaxAddressSpan > 0 {
			if uint32(nextAddr)-uint32(baseAddress) > uint32(o.MaxAddressSpan) {
				return models, ErrSunSpecModelChainLimitExceeded
			}
		}

		addr = nextAddr
	}

	return models, nil
}

// DiscoverSunSpec detects SunSpec and, if found, enumerates the model chain.
// This is the main API for device fingerprinting and inventory. When the device
// is not SunSpec, Detection.Detected is false and error is nil.
func (mc *ModbusClient) DiscoverSunSpec(ctx context.Context, opts *SunSpecOptions) (*SunSpecDiscoveryResult, error) {
	det, err := mc.DetectSunSpec(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := &SunSpecDiscoveryResult{Detection: *det}
	if !det.Detected {
		return out, nil
	}

	models, err := mc.ReadSunSpecModelHeaders(ctx, opts, det.BaseAddress)
	out.Models = models // include partial results when err != nil
	return out, err
}
