package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/modbus"
)

func performBoolScan(ctx context.Context, client *modbus.Client, unitID uint8, isCoil bool, jsonOutput bool) {
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

	if !jsonOutput {
		fmt.Printf("starting %s scan\n", regType)
	}

	for addr = 0; addr <= 0xffff; addr++ {
		if isCoil {
			val, err = client.ReadCoil(ctx, unitID, uint16(addr))
		} else {
			val, err = client.ReadDiscreteInput(ctx, unitID, uint16(addr))
		}
		if errors.Is(err, modbus.ErrIllegalDataAddress) || errors.Is(err, modbus.ErrIllegalFunction) {
			continue
		} else if err != nil {
			if jsonOutput {
				emitJSON(cliResult{Op: "scan", Addr: uint16(addr), Type: regType, Error: err.Error()})
			} else {
				fmt.Printf("failed to read %s at address 0x%04x: %v\n", regType, addr, err)
			}
		} else {
			if jsonOutput {
				emitJSON(cliResult{Op: "scan", Addr: uint16(addr), Type: regType, Value: val})
			} else {
				fmt.Printf("0x%04x\t%-5v : %v\n", addr, addr, val)
			}
			count++
		}
	}

	if jsonOutput {
		emitJSON(cliResult{Op: "scanSummary", Type: regType, Value: count})
	} else {
		fmt.Printf("found %v %ss\n", count, regType)
	}
}

func performRegisterScan(ctx context.Context, client *modbus.Client, unitID uint8, isHoldingReg bool, jsonOutput bool) {
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

	if !jsonOutput {
		fmt.Printf("starting %s scan\n", regType)
	}

	for addr = 0; addr <= 0xffff; addr++ {
		if isHoldingReg {
			val, err = client.ReadRegister(ctx, unitID, uint16(addr), modbus.HoldingRegister)
		} else {
			val, err = client.ReadRegister(ctx, unitID, uint16(addr), modbus.InputRegister)
		}
		if errors.Is(err, modbus.ErrIllegalDataAddress) || errors.Is(err, modbus.ErrIllegalFunction) {
			continue
		} else if err != nil {
			if jsonOutput {
				emitJSON(cliResult{Op: "scan", Addr: uint16(addr), Type: regType, Error: err.Error()})
			} else {
				fmt.Printf("failed to read %s at address 0x%04x: %v\n", regType, addr, err)
			}
		} else {
			if jsonOutput {
				emitJSON(cliResult{Op: "scan", Addr: uint16(addr), Type: regType, Value: val, Hex: fmt.Sprintf("0x%04x", val)})
			} else {
				fmt.Printf("0x%04x\t%-5v : 0x%04x\t%v\n", addr, addr, val, val)
			}
			count++
		}
	}

	if jsonOutput {
		emitJSON(cliResult{Op: "scanSummary", Type: regType, Value: count})
	} else {
		fmt.Printf("found %v %ss\n", count, regType)
	}
}

func performUnitIDScan(ctx context.Context, client *modbus.Client, jsonOutput bool) {
	var err error
	var countOk uint
	var countErr uint
	var countTimeout uint
	var countGWTimeout uint

	if !jsonOutput {
		fmt.Println("starting unit id scan")
	}

	for unitID := uint(0); unitID <= 0xff; unitID++ {
		_, err = client.ReadRegister(ctx, uint8(unitID), 0, modbus.InputRegister)
		switch {
		case err == nil,
			errors.Is(err, modbus.ErrIllegalDataAddress),
			errors.Is(err, modbus.ErrIllegalFunction),
			errors.Is(err, modbus.ErrIllegalDataValue):
			if jsonOutput {
				emitJSON(cliResult{Op: "unitIDScan", Value: unitID, Type: "ok"})
			} else {
				fmt.Printf("0x%02x (%3v): ok\n", unitID, unitID)
			}
			countOk++

		case errors.Is(err, modbus.ErrRequestTimedOut):
			countTimeout++

		case errors.Is(err, modbus.ErrGWTargetFailedToRespond):
			countGWTimeout++

		default:
			if jsonOutput {
				emitJSON(cliResult{Op: "unitIDScan", Value: unitID, Error: err.Error()})
			} else {
				fmt.Printf("0x%02x (%3v): %v\n", unitID, unitID, err)
			}
			countErr++
		}
	}

	if jsonOutput {
		emitJSON(cliResult{Op: "unitIDScanSummary", Value: map[string]uint{
			"found": countOk, "errors": countErr, "timeouts": countTimeout, "gwTimeouts": countGWTimeout,
		}})
	} else {
		fmt.Printf("found %v devices (%v errors, %v timeouts, %v gateway timeouts)\n",
			countOk, countErr, countTimeout, countGWTimeout)
	}
}

func performPing(ctx context.Context, client *modbus.Client, unitID uint8, count uint16, interval time.Duration, jsonOutput bool) {
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

	if !jsonOutput {
		fmt.Printf("ping: sending %v requests...\n", count)
	}

	startTs = time.Now()

	for run := uint16(0); run < count; run++ {
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
			if jsonOutput {
				emitJSON(cliResult{Op: "ping", Value: map[string]any{"seq": run + 1, "rtt": rtt.String(), "status": "ok"}})
			} else {
				fmt.Printf("ok: seq = %v, time: %v\n", run+1, rtt.Round(time.Microsecond))
			}

		case errors.Is(err, modbus.ErrRequestTimedOut),
			errors.Is(err, modbus.ErrGWTargetFailedToRespond):
			timeoutCount++
			if jsonOutput {
				emitJSON(cliResult{Op: "ping", Value: map[string]any{"seq": run + 1, "rtt": rtt.String(), "status": "timeout"}, Error: err.Error()})
			} else {
				fmt.Printf("timeout (%v): seq = %v, time: %v\n", err, run+1, rtt.Round(time.Microsecond))
			}

		default:
			otherErrCount++
			if jsonOutput {
				emitJSON(cliResult{Op: "ping", Value: map[string]any{"seq": run + 1, "rtt": rtt.String(), "status": "error"}, Error: err.Error()})
			} else {
				fmt.Printf("error (%v): seq = %v, time: %v\n", err, run+1, rtt.Round(time.Microsecond))
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

	if jsonOutput {
		emitJSON(cliResult{Op: "pingSummary", Value: map[string]any{
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
		fmt.Printf("--- ping statistics ---\n"+
			"%v queries, %v target replies, %v transmission errors, %v timeouts, time: %v\n",
			count, okCount, otherErrCount, timeoutCount,
			time.Since(startTs).Round(time.Millisecond))

		fmt.Printf("rtt min/avg/max = %v/%v/%v\n",
			minRTT.Round(time.Microsecond),
			(avgRTT / time.Duration(count)).Round(time.Microsecond),
			maxRTT.Round(time.Microsecond))
	}
}
