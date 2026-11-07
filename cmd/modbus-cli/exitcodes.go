// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"

	"github.com/otfabric/go-modbus"
)

// Exit codes of modbus-cli: the organisation table (otfabric/engineering
// standards/api-design.md, CLI-001). docs/exitcodes.md lists the same codes.
const (
	// ExitOK: every operation completed, or help or the version was printed.
	ExitOK = 0
	// ExitUsage: invalid flags, arguments or operations.
	ExitUsage = 2
	// ExitInvalidInput: a TLS certificate, key or CA file cannot be read or used.
	ExitInvalidInput = 3
	// ExitTransport: connection or transport failure.
	ExitTransport = 4
	// ExitTimeout: the device did not answer in time.
	ExitTimeout = 5
	// ExitProtocol: Modbus exception from the device, or a malformed or
	// unexpected response.
	ExitProtocol = 6
	// ExitPartial: some operations succeeded and some failed.
	ExitPartial = 7
	// ExitInternal: results could not be written, or an unclassified error.
	ExitInternal = 10
	// ExitInterrupted: SIGINT or SIGTERM before the run completed.
	ExitInterrupted = 130
)

// errInterrupted reports that the run was interrupted by a signal.
var errInterrupted = errors.New("interrupted")

// exitError carries an exit code chosen where the error arose (flag and
// argument errors, input files, output).
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func usageError(err error) error           { return &exitError{code: ExitUsage, err: err} }
func inputError(err error) error           { return &exitError{code: ExitInvalidInput, err: err} }
func outputError(err error) error          { return &exitError{code: ExitInternal, err: err} }
func usagef(format string, a ...any) error { return usageError(fmt.Errorf(format, a...)) }

// transportError marks an error from opening the connection: it is a
// transport failure unless the library classifies it more precisely.
type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// operationsError reports the operations that failed. When none succeeded,
// the first failure decides the exit code; otherwise the run is partial.
type operationsError struct {
	failed, succeeded int
	first             error
}

func (e *operationsError) Error() string {
	return fmt.Sprintf("%d of %d operations failed, the first with: %v",
		e.failed, e.failed+e.succeeded, e.first)
}

func (e *operationsError) Unwrap() error { return e.first }

// exitCode classifies err into one of the Exit… codes.
func exitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, errInterrupted) || errors.Is(err, context.Canceled) {
		return ExitInterrupted
	}
	if ee, ok := errors.AsType[*exitError](err); ok {
		return ee.code
	}
	if oe, ok := errors.AsType[*operationsError](err); ok && oe.succeeded > 0 {
		return ExitPartial
	}
	return libraryExitCode(err)
}

// libraryExitCode classifies an error returned by go-modbus by its sentinels
// and typed errors (ERRORS.md of go-modbus).
func libraryExitCode(err error) int {
	if _, ok := errors.AsType[*modbus.ExceptionError](err); ok {
		return ExitProtocol
	}
	for _, s := range []error{
		modbus.ErrIllegalFunction, modbus.ErrIllegalDataAddress, modbus.ErrIllegalDataValue,
		modbus.ErrServerDeviceFailure, modbus.ErrAcknowledge, modbus.ErrServerDeviceBusy,
		modbus.ErrMemoryParityError, modbus.ErrGWPathUnavailable, modbus.ErrGWTargetFailedToRespond,
		modbus.ErrProtocolError, modbus.ErrBadCRC, modbus.ErrBadLRC, modbus.ErrShortFrame,
		modbus.ErrBadUnitID, modbus.ErrBadTransactionID, modbus.ErrUnknownProtocolID,
		modbus.ErrInvalidMBAPLength,
	} {
		if errors.Is(err, s) {
			return ExitProtocol
		}
	}
	if errors.Is(err, modbus.ErrRequestTimedOut) || errors.Is(err, context.DeadlineExceeded) {
		return ExitTimeout
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return ExitTimeout
	}
	if errors.Is(err, modbus.ErrConfigurationError) || errors.Is(err, modbus.ErrUnexpectedParameters) {
		return ExitUsage
	}
	if _, ok := errors.AsType[*transportError](err); ok {
		return ExitTransport
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return ExitTransport
	}
	if _, ok := errors.AsType[syscall.Errno](err); ok {
		return ExitTransport
	}
	for _, s := range []error{modbus.ErrClientNotOpen, modbus.ErrSerialPortNotOpen, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed} {
		if errors.Is(err, s) {
			return ExitTransport
		}
	}
	return ExitInternal
}
