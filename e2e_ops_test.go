// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"net"
)

// e2eOp is one exported Client request method, called with arguments that are
// valid for the e2e device.
type e2eOp struct {
	name string
	// fc is the function code of the first request the method puts on the wire.
	fc FunctionCode
	// probe marks SupportsFunction / ProbeFunction style methods, which turn
	// exceptions into a result instead of an error.
	probe bool
	// requests is the number of requests a successful call puts on the wire.
	requests int
	call     func(ctx context.Context, c *Client) error
}

// e2eAllOps lists every exported request method of Client. Our server serves
// every one of them: there is deliberately no way to mark a method as not
// served, so a client function the server cannot answer fails the e2e suite
// (see TestE2E_EveryClientFunctionIsServed).
func e2eAllOps() []e2eOp {
	u := e2eUnit
	one := func(name string, fc FunctionCode, call func(ctx context.Context, c *Client) error) e2eOp {
		return e2eOp{name: name, fc: fc, requests: 1, call: call}
	}
	ops := []e2eOp{
		one("ReadCoils", FCReadCoils, func(ctx context.Context, c *Client) error {
			_, err := c.ReadCoils(ctx, u, 0, 9)
			return err
		}),
		one("ReadCoil", FCReadCoils, func(ctx context.Context, c *Client) error {
			_, err := c.ReadCoil(ctx, u, 3)
			return err
		}),
		one("ReadDiscreteInputs", FCReadDiscreteInputs, func(ctx context.Context, c *Client) error {
			_, err := c.ReadDiscreteInputs(ctx, u, 0, 9)
			return err
		}),
		one("ReadDiscreteInput", FCReadDiscreteInputs, func(ctx context.Context, c *Client) error {
			_, err := c.ReadDiscreteInput(ctx, u, 3)
			return err
		}),
		one("WriteCoil", FCWriteSingleCoil, func(ctx context.Context, c *Client) error {
			return c.WriteCoil(ctx, u, 900, true)
		}),
		one("WriteCoilRaw", FCWriteSingleCoil, func(ctx context.Context, c *Client) error {
			return c.WriteCoilRaw(ctx, u, 901, 0xFF00)
		}),
		one("WriteCoils", FCWriteMultipleCoils, func(ctx context.Context, c *Client) error {
			return c.WriteCoils(ctx, u, 910, []bool{true, false, true})
		}),
		one("ReadRegisters/holding", FCReadHoldingRegisters, func(ctx context.Context, c *Client) error {
			_, err := c.ReadRegisters(ctx, u, 0, 3, HoldingRegister)
			return err
		}),
		one("ReadRegisters/input", FCReadInputRegisters, func(ctx context.Context, c *Client) error {
			_, err := c.ReadRegisters(ctx, u, 0, 3, InputRegister)
			return err
		}),
		one("ReadRegister", FCReadHoldingRegisters, func(ctx context.Context, c *Client) error {
			_, err := c.ReadRegister(ctx, u, 1, HoldingRegister)
			return err
		}),
		one("ReadRegisterBytes", FCReadInputRegisters, func(ctx context.Context, c *Client) error {
			_, err := c.ReadRegisterBytes(ctx, u, 0, 5, InputRegister)
			return err
		}),
		one("ReadHoldingRegister", FCReadHoldingRegisters, func(ctx context.Context, c *Client) error {
			_, err := c.ReadHoldingRegister(ctx, u, 1)
			return err
		}),
		one("ReadHoldingRegisters", FCReadHoldingRegisters, func(ctx context.Context, c *Client) error {
			_, err := c.ReadHoldingRegisters(ctx, u, 1, 2)
			return err
		}),
		one("ReadInputRegister", FCReadInputRegisters, func(ctx context.Context, c *Client) error {
			_, err := c.ReadInputRegister(ctx, u, 1)
			return err
		}),
		one("ReadInputRegisters", FCReadInputRegisters, func(ctx context.Context, c *Client) error {
			_, err := c.ReadInputRegisters(ctx, u, 1, 2)
			return err
		}),
		one("ReadRegisterBit", FCReadHoldingRegisters, func(ctx context.Context, c *Client) error {
			_, err := c.ReadRegisterBit(ctx, u, 1, 3, HoldingRegister)
			return err
		}),
		one("ReadRegisterBits", FCReadInputRegisters, func(ctx context.Context, c *Client) error {
			_, err := c.ReadRegisterBits(ctx, u, 1, 3, 4, InputRegister)
			return err
		}),
		{name: "WriteRegisterBit", fc: FCReadHoldingRegisters, requests: 2,
			call: func(ctx context.Context, c *Client) error {
				return c.WriteRegisterBit(ctx, u, 920, 3, true)
			}},
		{name: "UpdateRegisterMask", fc: FCReadHoldingRegisters, requests: 2,
			call: func(ctx context.Context, c *Client) error {
				return c.UpdateRegisterMask(ctx, u, 921, 0x00F0, 0x00A0)
			}},
		one("MaskWriteRegister", FCMaskWriteRegister, func(ctx context.Context, c *Client) error {
			return c.MaskWriteRegister(ctx, u, 922, 0x00F2, 0x0025)
		}),
		one("WriteRegister", FCWriteSingleRegister, func(ctx context.Context, c *Client) error {
			return c.WriteRegister(ctx, u, 923, 0x1234)
		}),
		one("WriteRegisters", FCWriteMultipleRegisters, func(ctx context.Context, c *Client) error {
			return c.WriteRegisters(ctx, u, 924, []uint16{1, 2})
		}),
		one("WriteRegisterBytes", FCWriteMultipleRegisters, func(ctx context.Context, c *Client) error {
			return c.WriteRegisterBytes(ctx, u, 926, []byte{1, 2, 3})
		}),
		one("ReadWriteMultipleRegisters", FCReadWriteMultipleRegs, func(ctx context.Context, c *Client) error {
			_, err := c.ReadWriteMultipleRegisters(ctx, u, 0, 2, 930, []uint16{7})
			return err
		}),
		one("ReadDeviceIdentification", FCEncapsulatedInterface, func(ctx context.Context, c *Client) error {
			_, err := c.ReadDeviceIdentification(ctx, u, DeviceIDBasic, 0)
			return err
		}),
		one("ReadAllDeviceIdentification", FCEncapsulatedInterface, func(ctx context.Context, c *Client) error {
			_, err := c.ReadAllDeviceIdentification(ctx, u)
			return err
		}),
		one("ReadExceptionStatus", FCReadExceptionStatus, func(ctx context.Context, c *Client) error {
			_, err := c.ReadExceptionStatus(ctx, u)
			return err
		}),
		one("GetCommEventCounter", FCGetCommEventCounters, func(ctx context.Context, c *Client) error {
			_, err := c.GetCommEventCounter(ctx, u)
			return err
		}),
		one("GetCommEventLog", FCGetCommEventLog, func(ctx context.Context, c *Client) error {
			_, err := c.GetCommEventLog(ctx, u)
			return err
		}),

		// Diagnostics, Report Server ID, file records and FIFO queue.
		one("Diagnostics", FCDiagnostics, func(ctx context.Context, c *Client) error {
			_, err := c.Diagnostics(ctx, u, DiagReturnQueryData, []byte{0xCA, 0xFE})
			return err
		}),
		one("DiagnosticLoopback", FCDiagnostics, func(ctx context.Context, c *Client) error {
			_, err := c.DiagnosticLoopback(ctx, u, 0x1234)
			return err
		}),
		one("DiagnosticRegister", FCDiagnostics, func(ctx context.Context, c *Client) error {
			_, err := c.DiagnosticRegister(ctx, u)
			return err
		}),
		one("BusMessageCount", FCDiagnostics, func(ctx context.Context, c *Client) error {
			_, err := c.BusMessageCount(ctx, u)
			return err
		}),
		one("DiagnosticForceListenOnlyMode", FCDiagnostics, func(ctx context.Context, c *Client) error {
			return c.DiagnosticForceListenOnlyMode(ctx, u)
		}),
		one("DiagnosticClearCounters", FCDiagnostics, func(ctx context.Context, c *Client) error {
			return c.DiagnosticClearCounters(ctx, u)
		}),
		one("DiagnosticBusCommunicationErrorCount", FCDiagnostics, func(ctx context.Context, c *Client) error {
			_, err := c.DiagnosticBusCommunicationErrorCount(ctx, u)
			return err
		}),
		one("DiagnosticBusExceptionErrorCount", FCDiagnostics, func(ctx context.Context, c *Client) error {
			_, err := c.DiagnosticBusExceptionErrorCount(ctx, u)
			return err
		}),
		one("DiagnosticServerMessageCount", FCDiagnostics, func(ctx context.Context, c *Client) error {
			_, err := c.DiagnosticServerMessageCount(ctx, u)
			return err
		}),
		one("DiagnosticServerNoResponseCount", FCDiagnostics, func(ctx context.Context, c *Client) error {
			_, err := c.DiagnosticServerNoResponseCount(ctx, u)
			return err
		}),
		one("DiagnosticServerNAKCount", FCDiagnostics, func(ctx context.Context, c *Client) error {
			_, err := c.DiagnosticServerNAKCount(ctx, u)
			return err
		}),
		one("DiagnosticServerBusyCount", FCDiagnostics, func(ctx context.Context, c *Client) error {
			_, err := c.DiagnosticServerBusyCount(ctx, u)
			return err
		}),
		one("DiagnosticBusCharacterOverrunCount", FCDiagnostics, func(ctx context.Context, c *Client) error {
			_, err := c.DiagnosticBusCharacterOverrunCount(ctx, u)
			return err
		}),
		one("DiagnosticClearOverrunCounterAndFlag", FCDiagnostics, func(ctx context.Context, c *Client) error {
			return c.DiagnosticClearOverrunCounterAndFlag(ctx, u)
		}),
		one("ReportServerID", FCReportServerID, func(ctx context.Context, c *Client) error {
			_, err := c.ReportServerID(ctx, u)
			return err
		}),
		one("ReadFileRecords", FCReadFileRecord, func(ctx context.Context, c *Client) error {
			_, err := c.ReadFileRecords(ctx, u, []FileRecordRequest{{FileNumber: 4, RecordNumber: 1, RecordLength: 2}})
			return err
		}),
		one("WriteFileRecords", FCWriteFileRecord, func(ctx context.Context, c *Client) error {
			return c.WriteFileRecords(ctx, u, []FileRecord{{FileNumber: 4, RecordNumber: 7, Data: []uint16{0x06AF, 0x04BE}}})
		}),
		one("ReadFIFOQueue", FCReadFIFOQueue, func(ctx context.Context, c *Client) error {
			_, err := c.ReadFIFOQueue(ctx, u, 0x04DE)
			return err
		}),
	}

	// Probes: an error is only returned for failures that are not a probe result.
	probes := []e2eOp{
		one("SupportsFunction", FCReadHoldingRegisters, func(ctx context.Context, c *Client) error {
			_, err := c.SupportsFunction(ctx, u, FCReadHoldingRegisters)
			return err
		}),
		one("SupportsDeviceIdentification", FCEncapsulatedInterface, func(ctx context.Context, c *Client) error {
			_, err := c.SupportsDeviceIdentification(ctx, u)
			return err
		}),
		one("ProbeFunction", FCReadCoils, func(ctx context.Context, c *Client) error {
			res, err := c.ProbeFunction(ctx, u, FCReadCoils)
			if err != nil {
				return err
			}
			return res.Err
		}),
	}
	for i := range probes {
		probes[i].probe = true
	}
	return append(ops, probes...)
}

// e2eIsParamError reports whether err is a client-side parameter error.
func e2eIsParamError(err error) bool { return errors.Is(err, ErrUnexpectedParameters) }

// e2eSplitHostPort splits host:port.
func e2eSplitHostPort(hostPort string) (host, port string, err error) {
	return net.SplitHostPort(hostPort)
}
