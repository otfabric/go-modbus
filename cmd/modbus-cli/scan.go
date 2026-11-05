// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-modbus"
)

func performBoolScan(ctx context.Context, client *modbus.Client, unitID uint8, isCoil bool, out *output) {
	var err error
	var addr uint32
	var val bool
	var count uint
	var regType string

	if isCoil {
		regType = "coil"
	} else {
		regType = "discrete input"
	}

	if !out.json {
		out.printf("starting %s scan\n", regType)
	}

	for addr = 0; addr <= 0xffff; addr++ {
		if ctx.Err() != nil {
			// interrupted: stop instead of failing the remaining addresses
			return
		}
		if isCoil {
			val, err = client.ReadCoil(ctx, unitID, uint16(addr))
		} else {
			val, err = client.ReadDiscreteInput(ctx, unitID, uint16(addr))
		}
		if errors.Is(err, modbus.ErrIllegalDataAddress) || errors.Is(err, modbus.ErrIllegalFunction) {
			continue
		} else if err != nil {
			if out.json {
				out.emit(cliResult{Op: "scan", Addr: at(uint16(addr)), Type: regType, Error: err.Error()})
			} else {
				out.printf("failed to read %s at address 0x%04x: %v\n", regType, addr, err)
			}
		} else {
			if out.json {
				out.emit(cliResult{Op: "scan", Addr: at(uint16(addr)), Type: regType, Value: val})
			} else {
				out.printf("0x%04x\t%-5v : %v\n", addr, addr, val)
			}
			count++
		}
	}

	if out.json {
		out.emit(cliResult{Op: "scanSummary", Type: regType, Value: count})
	} else {
		out.printf("found %v %ss\n", count, regType)
	}
}

func performRegisterScan(ctx context.Context, client *modbus.Client, unitID uint8, isHoldingReg bool, out *output) {
	var err error
	var addr uint32
	var val uint16
	var count uint
	var regType string

	if isHoldingReg {
		regType = "holding register"
	} else {
		regType = "input register"
	}

	if !out.json {
		out.printf("starting %s scan\n", regType)
	}

	for addr = 0; addr <= 0xffff; addr++ {
		if ctx.Err() != nil {
			// interrupted: stop instead of failing the remaining addresses
			return
		}
		if isHoldingReg {
			val, err = client.ReadRegister(ctx, unitID, uint16(addr), modbus.HoldingRegister)
		} else {
			val, err = client.ReadRegister(ctx, unitID, uint16(addr), modbus.InputRegister)
		}
		if errors.Is(err, modbus.ErrIllegalDataAddress) || errors.Is(err, modbus.ErrIllegalFunction) {
			continue
		} else if err != nil {
			if out.json {
				out.emit(cliResult{Op: "scan", Addr: at(uint16(addr)), Type: regType, Error: err.Error()})
			} else {
				out.printf("failed to read %s at address 0x%04x: %v\n", regType, addr, err)
			}
		} else {
			if out.json {
				out.emit(cliResult{Op: "scan", Addr: at(uint16(addr)), Type: regType, Value: val, Hex: fmt.Sprintf("0x%04x", val)})
			} else {
				out.printf("0x%04x\t%-5v : 0x%04x\t%v\n", addr, addr, val, val)
			}
			count++
		}
	}

	if out.json {
		out.emit(cliResult{Op: "scanSummary", Type: regType, Value: count})
	} else {
		out.printf("found %v %ss\n", count, regType)
	}
}

func performUnitIDScan(ctx context.Context, client *modbus.Client, out *output) {
	var err error
	var countOk uint
	var countErr uint
	var countTimeout uint
	var countGWTimeout uint

	if !out.json {
		out.printf("starting unit id scan\n")
	}

	for unitID := uint(0); unitID <= 0xff; unitID++ {
		if ctx.Err() != nil {
			return
		}
		_, err = client.ReadRegister(ctx, uint8(unitID), 0, modbus.InputRegister)
		switch {
		case err == nil,
			errors.Is(err, modbus.ErrIllegalDataAddress),
			errors.Is(err, modbus.ErrIllegalFunction),
			errors.Is(err, modbus.ErrIllegalDataValue):
			if out.json {
				out.emit(cliResult{Op: "unitIDScan", Value: unitID, Type: "ok"})
			} else {
				out.printf("0x%02x (%3v): ok\n", unitID, unitID)
			}
			countOk++

		case errors.Is(err, modbus.ErrRequestTimedOut):
			countTimeout++

		case errors.Is(err, modbus.ErrGWTargetFailedToRespond):
			countGWTimeout++

		default:
			if out.json {
				out.emit(cliResult{Op: "unitIDScan", Value: unitID, Error: err.Error()})
			} else {
				out.printf("0x%02x (%3v): %v\n", unitID, unitID, err)
			}
			countErr++
		}
	}

	if out.json {
		out.emit(cliResult{Op: "unitIDScanSummary", Value: map[string]uint{
			"found": countOk, "errors": countErr, "timeouts": countTimeout, "gwTimeouts": countGWTimeout,
		}})
	} else {
		out.printf("found %v devices (%v errors, %v timeouts, %v gateway timeouts)\n",
			countOk, countErr, countTimeout, countGWTimeout)
	}
}

func performPing(ctx context.Context, client *modbus.Client, unitID uint8, count uint16, interval time.Duration, out *output) {
	var err error
	var okCount uint
	var timeoutCount uint
	var otherErrCount uint
	var startTs time.Time
	var ts time.Time
	var rtt time.Duration
	var minRTT time.Duration
	var maxRTT time.Duration
	var avgRTT time.Duration

	if !out.json {
		out.printf("ping: sending %v requests...\n", count)
	}

	startTs = time.Now()

	for run := range count {
		if ctx.Err() != nil {
			return
		}
		ts = time.Now()
		_, err = client.ReadRegister(ctx, unitID, 0x0000, modbus.HoldingRegister)

		rtt = time.Since(ts)
		avgRTT += rtt

		if run == 0 || rtt < minRTT {
			minRTT = rtt
		}

		if rtt > maxRTT {
			maxRTT = rtt
		}

		switch {
		case err == nil,
			errors.Is(err, modbus.ErrIllegalDataAddress),
			errors.Is(err, modbus.ErrIllegalFunction):
			okCount++
			if out.json {
				out.emit(cliResult{Op: "ping", Value: map[string]any{"seq": run + 1, "rtt": rtt.String(), "status": "ok"}})
			} else {
				out.printf("ok: seq = %v, time: %v\n", run+1, rtt.Round(time.Microsecond))
			}

		case errors.Is(err, modbus.ErrRequestTimedOut),
			errors.Is(err, modbus.ErrGWTargetFailedToRespond):
			timeoutCount++
			if out.json {
				out.emit(cliResult{Op: "ping", Value: map[string]any{"seq": run + 1, "rtt": rtt.String(), "status": "timeout"}, Error: err.Error()})
			} else {
				out.printf("timeout (%v): seq = %v, time: %v\n", err, run+1, rtt.Round(time.Microsecond))
			}

		default:
			otherErrCount++
			if out.json {
				out.emit(cliResult{Op: "ping", Value: map[string]any{"seq": run + 1, "rtt": rtt.String(), "status": "error"}, Error: err.Error()})
			} else {
				out.printf("error (%v): seq = %v, time: %v\n", err, run+1, rtt.Round(time.Microsecond))
			}
		}

		if interval > 0 {
			select {
			case <-time.After(interval):
			case <-ctx.Done():
				return
			}
		}
	}

	if out.json {
		out.emit(cliResult{Op: "pingSummary", Value: map[string]any{
			"queries":  count,
			"replies":  okCount,
			"errors":   otherErrCount,
			"timeouts": timeoutCount,
			"duration": time.Since(startTs).Round(time.Millisecond).String(),
			"rttMin":   minRTT.Round(time.Microsecond).String(),
			"rttAvg":   (avgRTT / time.Duration(count)).Round(time.Microsecond).String(),
			"rttMax":   maxRTT.Round(time.Microsecond).String(),
		}})
	} else {
		out.printf("--- ping statistics ---\n"+
			"%v queries, %v target replies, %v transmission errors, %v timeouts, time: %v\n",
			count, okCount, otherErrCount, timeoutCount,
			time.Since(startTs).Round(time.Millisecond))

		out.printf("rtt min/avg/max = %v/%v/%v\n",
			minRTT.Round(time.Microsecond),
			(avgRTT / time.Duration(count)).Round(time.Microsecond),
			maxRTT.Round(time.Microsecond))
	}
}
