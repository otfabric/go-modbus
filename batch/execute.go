// SPDX-License-Identifier: MIT

package batch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-modbus"
)

// Reader reads registers, coils and discrete inputs. *modbus.Client
// satisfies it.
type Reader interface {
	ReadRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType modbus.RegType) ([]uint16, error)
	ReadCoils(ctx context.Context, unitID uint8, addr uint16, quantity uint16) ([]bool, error)
	ReadDiscreteInputs(ctx context.Context, unitID uint8, addr uint16, quantity uint16) ([]bool, error)
}

// ErrIncomplete is matched by the error of an Execute that did not read
// every window.
var ErrIncomplete = errors.New("batch: not every window was read")

// ExecError is the error of an Execute that did not read every window. It
// matches ErrIncomplete, its first window error and the reason the batch
// stopped, so errors.Is(err, modbus.ErrIllegalDataAddress) and
// errors.Is(err, context.Canceled) both work.
type ExecError struct {
	// Failed and NotAttempted count the windows in those states; Total is
	// the number of windows.
	Failed, NotAttempted, Total int
	// First is the error of the first Failed window, nil when none failed.
	First error
	// Stop is why the remaining windows were not attempted: the context's
	// error, or the transport error that ended the batch. Nil when every
	// window was attempted.
	Stop error
}

func (e *ExecError) Error() string {
	cause := e.First
	if cause == nil {
		cause = e.Stop
	}
	return fmt.Sprintf("batch: %d of %d windows failed, %d not attempted: %v", e.Failed, e.Total, e.NotAttempted, cause)
}

// Is reports whether target is ErrIncomplete.
func (e *ExecError) Is(target error) bool { return target == ErrIncomplete }

// Unwrap returns First and Stop, those that are set.
func (e *ExecError) Unwrap() []error {
	var errs []error
	if e.First != nil {
		errs = append(errs, e.First)
	}
	if e.Stop != nil && e.Stop != e.First {
		errs = append(errs, e.Stop)
	}
	return errs
}

// Execute reads the plan's windows from r, in plan order, one request after
// another on the calling goroutine, and returns the result of every window
// and item. It starts no goroutines and keeps no state between calls, so a
// plan can be executed repeatedly and concurrently. Sequential requests are
// not an atomic snapshot: every window carries its own timing.
//
// A Modbus exception, and a request the client refuses through
// modbus.Config.AllowedFunctionCodes, fail only that window; the batch goes
// on. Any other error (a timeout, a broken connection, a protocol error)
// ends the batch: the remaining windows are NotAttempted, unless
// Options.ContinueOnTransportError is set. A cancelled or expired context
// always ends the batch. Before each window the context is checked, and the
// in-flight request is cancelled through it.
//
// The error is nil when every window is OK; an item whose codec rejects its
// registers is DecodeFailed and does not set it. Otherwise Execute returns
// the result together with an *ExecError. A nil r returns a nil result and a
// *ValidationError.
func (p *Plan) Execute(ctx context.Context, r Reader) (*Result, error) {
	if r == nil {
		return nil, &ValidationError{Index: -1, Reason: "nil reader"}
	}
	res := &Result{Started: time.Now(), Windows: make([]WindowResult, len(p.windows))}
	var stop error
	for i, w := range p.windows {
		wr := WindowResult{Window: w}
		wr.Items = append([]int(nil), w.Items...)
		if stop == nil {
			stop = ctxDone(ctx)
		}
		if stop != nil {
			wr.State, wr.Err = NotAttempted, fmt.Errorf("%w: %w", ErrNotAttempted, stop)
			res.Windows[i] = wr
			continue
		}

		wr.Started = time.Now()
		var err error
		switch w.FC {
		case modbus.FCReadCoils:
			wr.Bits, err = r.ReadCoils(ctx, w.UnitID, w.Addr, w.Count)
		case modbus.FCReadDiscreteInputs:
			wr.Bits, err = r.ReadDiscreteInputs(ctx, w.UnitID, w.Addr, w.Count)
		case modbus.FCReadHoldingRegisters:
			wr.Registers, err = r.ReadRegisters(ctx, w.UnitID, w.Addr, w.Count, modbus.HoldingRegister)
		default:
			wr.Registers, err = r.ReadRegisters(ctx, w.UnitID, w.Addr, w.Count, modbus.InputRegister)
		}
		wr.Finished = time.Now()
		if err == nil {
			err = checkLength(&wr)
		}
		if err != nil {
			wr.State, wr.Err, wr.Registers, wr.Bits = Failed, err, nil, nil
			switch {
			case ctxDone(ctx) != nil:
				stop = ctxDone(ctx)
			case !windowOnly(err) && !p.continueOnTransportError:
				stop = err
			}
		} else {
			wr.State = OK
		}
		res.Windows[i] = wr
	}
	res.Items = p.itemResults(res.Windows)
	res.Finished = time.Now()
	return res, executeError(res.Windows, stop)
}

// ctxDone returns the context's error, or context.DeadlineExceeded once its
// deadline has passed: a client can report a request that reached the
// deadline as timed out a moment before the context itself expires.
func ctxDone(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
		return context.DeadlineExceeded
	}
	return nil
}

// windowOnly reports whether err concerns only the request that returned
// it: a Modbus exception (the device answered), or a function code the
// client refuses to send.
func windowOnly(err error) bool {
	var exc *modbus.ExceptionError
	return errors.As(err, &exc) || errors.Is(err, modbus.ErrFunctionCodeNotAllowed)
}

// checkLength rejects data of the wrong length from a Reader.
func checkLength(wr *WindowResult) error {
	n := len(wr.Registers)
	if isBitFC(wr.FC) {
		n = len(wr.Bits)
	}
	if n != int(wr.Count) {
		return fmt.Errorf("batch: reader returned %d values for a window of %d: %w", n, wr.Count, modbus.ErrProtocolError)
	}
	return nil
}

// executeError returns the error of an Execute with these window results.
func executeError(windows []WindowResult, stop error) error {
	e := &ExecError{Total: len(windows), Stop: stop}
	for _, w := range windows {
		switch w.State {
		case Failed:
			e.Failed++
			if e.First == nil {
				e.First = w.Err
			}
		case NotAttempted:
			e.NotAttempted++
		case OK, DecodeFailed:
		}
	}
	if e.Failed == 0 && e.NotAttempted == 0 {
		return nil
	}
	if e.NotAttempted == 0 {
		e.Stop = nil // the batch ended after its last window
	}
	return e
}
