package modbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/modbus/internal/adu"
	"github.com/otfabric/modbus/internal/protocol"
)

// runOneProbe runs a single detection probe.
// Returns (true, nil) on valid response, (false, nil) on expected probe-negative
// outcomes (timeout, Modbus exception, gateway failure), (false, err) on real
// transport/client errors (broken socket, protocol corruption, client not open, etc.).
func (mc *Client) runOneProbe(ctx context.Context, unitID uint8, p protocol.DetectionProbe) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	req := &adu.Request{UnitID: unitID, FunctionCode: byte(p.FC), Payload: p.Payload}

	fc := FunctionCode(req.FunctionCode)
	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, fc)
	}

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		reportOutcome(m, unitID, fc, start, err)
		if errors.Is(err, ErrRequestTimedOut) ||
			errors.Is(err, ErrGWTargetFailedToRespond) {
			return false, nil
		}
		var excErr *ExceptionError
		if errors.As(err, &excErr) {
			return false, nil
		}
		return false, err
	}
	valid := p.Validate(FunctionCode(req.FunctionCode), protocol.Response{FunctionCode: FunctionCode(res.FunctionCode), Payload: res.Payload})
	if valid {
		reportOutcome(m, unitID, fc, start, nil)
	} else {
		reportOutcome(m, unitID, fc, start, fmt.Errorf("probe validation failed"))
	}
	return valid, nil
}

// SupportsFunction probes the given unit with a single function code and returns whether
// the unit responded with a structurally valid Modbus response (normal or exception). Use after Open().
// Only FCs that have a detection probe are supported: FC08, FC43, FC03, FC04, FC01, FC02, FC11, FC18, FC20.
// For an unsupported fc, returns (false, ErrUnexpectedParameters).
func (mc *Client) SupportsFunction(ctx context.Context, unitID uint8, fc FunctionCode) (bool, error) {
	p, ok := protocol.GetProbeForFC(fc)
	if !ok {
		return false, newParameterError("SupportsFunction", "fc",
			fmt.Sprintf("no probe defined for FC 0x%02X", uint8(fc)))
	}
	return mc.runOneProbe(ctx, unitID, p)
}

// SupportsDeviceIdentification reports whether the given unit supports Read Device Identification (FC43).
// It is equivalent to SupportsFunction(ctx, unitID, FCEncapsulatedInterface). Use after Open().
func (mc *Client) SupportsDeviceIdentification(ctx context.Context, unitID uint8) (bool, error) {
	return mc.SupportsFunction(ctx, unitID, FCEncapsulatedInterface)
}

// ProbeOutcome classifies the result of a function code probe.
type ProbeOutcome uint8

const (
	// ProbeSupported indicates the unit responded with a structurally valid
	// normal (non-exception) response for the probed function code.
	ProbeSupported ProbeOutcome = iota
	// ProbeException indicates the unit responded with a Modbus exception.
	ProbeException
	// ProbeTimeout indicates the probe timed out (no response received).
	ProbeTimeout
	// ProbeTransportError indicates a transport-level failure (broken socket,
	// gateway failure, protocol corruption).
	ProbeTransportError
	// ProbeValidationFailed indicates the response was received but failed
	// structural validation for the probed function code.
	ProbeValidationFailed
)

func (o ProbeOutcome) String() string {
	switch o {
	case ProbeSupported:
		return "supported"
	case ProbeException:
		return "exception"
	case ProbeTimeout:
		return "timeout"
	case ProbeTransportError:
		return "transport_error"
	case ProbeValidationFailed:
		return "validation_failed"
	default:
		return fmt.Sprintf("ProbeOutcome(%d)", o)
	}
}

// ProbeResult contains the detailed outcome of a function code probe.
type ProbeResult struct {
	// Outcome classifies the probe result.
	Outcome ProbeOutcome
	// Supported is true only when Outcome == ProbeSupported.
	Supported bool
	// ExceptionCode is set when Outcome == ProbeException.
	ExceptionCode ExceptionCode
	// Err holds the underlying error for Timeout, TransportError, or
	// any other non-exception failure. Nil for Supported and ValidationFailed.
	Err error
	// ResponseFC is the function code from the device response. Set whenever
	// a response was received (Supported, Exception, ValidationFailed).
	ResponseFC FunctionCode
	// RawPayload is the raw response payload bytes. Set for non-exception
	// responses to aid field debugging of quirky devices.
	RawPayload []byte
	// Reason is a short explanation for non-success outcomes (e.g.
	// "response validation failed", "request timed out").
	Reason string
}

// ProbeFunction probes the given unit with a single function code and returns
// a detailed ProbeResult describing the outcome. Unlike SupportsFunction, it
// never returns a non-nil error for expected probe-negative outcomes (timeout,
// exception); those are captured in the result. Only context cancellation and
// unsupported probe FCs return a non-nil error.
func (mc *Client) ProbeFunction(ctx context.Context, unitID uint8, fc FunctionCode) (ProbeResult, error) {
	p, ok := protocol.GetProbeForFC(fc)
	if !ok {
		return ProbeResult{}, newParameterError("ProbeFunction", "fc",
			fmt.Sprintf("no probe defined for FC 0x%02X", uint8(fc)))
	}

	select {
	case <-ctx.Done():
		return ProbeResult{}, ctx.Err()
	default:
	}

	req := &adu.Request{UnitID: unitID, FunctionCode: byte(p.FC), Payload: p.Payload}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, fc)
	}

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ProbeResult{}, err
		}
		reportOutcome(m, unitID, fc, start, err)
		if errors.Is(err, ErrRequestTimedOut) || errors.Is(err, ErrGWTargetFailedToRespond) {
			return ProbeResult{Outcome: ProbeTimeout, Err: err, Reason: "request timed out"}, nil
		}
		var excErr *ExceptionError
		if errors.As(err, &excErr) {
			return ProbeResult{Outcome: ProbeException, ExceptionCode: excErr.ExceptionCode,
				Reason: fmt.Sprintf("exception 0x%02X", excErr.ExceptionCode)}, nil
		}
		return ProbeResult{Outcome: ProbeTransportError, Err: err, Reason: err.Error()}, nil
	}

	resFC := FunctionCode(res.FunctionCode)
	payload := append([]byte(nil), res.Payload...)

	if resFC.IsException() && len(res.Payload) >= 1 {
		reportOutcome(m, unitID, fc, start, fmt.Errorf("exception 0x%02X", res.Payload[0]))
		return ProbeResult{
			Outcome: ProbeException, ExceptionCode: ExceptionCode(res.Payload[0]),
			ResponseFC: resFC, RawPayload: payload,
			Reason: fmt.Sprintf("exception 0x%02X", res.Payload[0]),
		}, nil
	}

	valid := p.Validate(FunctionCode(req.FunctionCode), protocol.Response{
		FunctionCode: resFC,
		Payload:      res.Payload,
	})
	if !valid {
		reportOutcome(m, unitID, fc, start, fmt.Errorf("probe validation failed"))
		return ProbeResult{
			Outcome: ProbeValidationFailed, ResponseFC: resFC, RawPayload: payload,
			Reason: "response validation failed",
		}, nil
	}
	reportOutcome(m, unitID, fc, start, nil)
	return ProbeResult{
		Outcome: ProbeSupported, Supported: true,
		ResponseFC: resFC, RawPayload: payload,
	}, nil
}
