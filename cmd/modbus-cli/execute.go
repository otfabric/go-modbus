// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/otfabric/go-modbus"
	"github.com/otfabric/go-modbus/codec"
)

// layoutName32 returns the 32-bit layout name for CLI codec selection (e.g. "4321").
func layoutName32(e modbus.Endianness, w modbus.WordOrder) string {
	if e == modbus.BigEndian && w == modbus.HighWordFirst {
		return "4321"
	}
	if e == modbus.BigEndian && w == modbus.LowWordFirst {
		return "2143"
	}
	if e == modbus.LittleEndian && w == modbus.HighWordFirst {
		return "3412"
	}
	return "1234"
}

// layoutName64 returns the 64-bit layout name for CLI codec selection (e.g. "87654321").
func layoutName64(e modbus.Endianness, w modbus.WordOrder) string {
	if e == modbus.BigEndian && w == modbus.HighWordFirst {
		return "87654321"
	}
	if e == modbus.BigEndian && w == modbus.LowWordFirst {
		return "21436587"
	}
	if e == modbus.LittleEndian && w == modbus.HighWordFirst {
		return "43218765"
	}
	return "65872143"
}

func regType(isHolding bool) modbus.RegType {
	if isHolding {
		return modbus.HoldingRegister
	}
	return modbus.InputRegister
}

// readRuntimeValues reads count multi-register values using the given codec ID,
// returning the decoded values. regsPerValue is 2 for 32-bit, 4 for 64-bit.
func readRuntimeValues(ctx context.Context, client *modbus.Client, unitID uint8, addr, count uint16,
	isHoldingReg bool, codecID string, regsPerValue uint16) ([]any, error) {

	rc, ok, err := codec.RuntimeCodecByID(codecID)
	if err != nil {
		return nil, fmt.Errorf("codec %s: %w", codecID, err)
	}
	if !ok {
		return nil, fmt.Errorf("unknown codec %q", codecID)
	}

	totalRegs := regsPerValue * count
	regs, err := client.ReadRegisters(ctx, unitID, addr, totalRegs, regType(isHoldingReg))
	if err != nil {
		return nil, err
	}

	values := make([]any, 0, count)
	for idx := uint16(0); idx < count; idx++ {
		slice := regs[idx*regsPerValue : (idx+1)*regsPerValue]
		decoded, decErr := codec.DecodeRegistersAny(slice, rc)
		if decErr != nil {
			return nil, fmt.Errorf("decode error at 0x%04x: %w", addr+idx*regsPerValue, decErr)
		}
		values = append(values, decoded)
	}
	return values, nil
}

// writeRuntimeValue encodes value with the given codec and writes it.
func writeRuntimeValue(ctx context.Context, client *modbus.Client, unitID uint8, addr uint16,
	value any, codecID string) error {

	rc, ok, err := codec.RuntimeCodecByID(codecID)
	if err != nil {
		return fmt.Errorf("codec %s: %w", codecID, err)
	}
	if !ok {
		return fmt.Errorf("unknown codec %q", codecID)
	}
	return codec.WriteRuntimeToClient(client, ctx, unitID, addr, value, rc)
}

// cliResult is a structured result for JSON output mode.
type cliResult struct {
	Op    string `json:"op"`
	Addr  uint16 `json:"addr,omitempty"`
	Type  string `json:"type,omitempty"`
	Value any    `json:"value,omitempty"`
	Hex   string `json:"hex,omitempty"`
	Error string `json:"error,omitempty"`
}

var jsonEncoder *json.Encoder

func emitJSON(r cliResult) {
	if jsonEncoder == nil {
		jsonEncoder = json.NewEncoder(os.Stdout)
	}
	_ = jsonEncoder.Encode(r)
}

func executeOperations(ctx context.Context, client *modbus.Client, runList []operation,
	initialUnitID uint8, endianness modbus.Endianness, wordOrder modbus.WordOrder,
	jsonOutput bool, failFast bool) (hadErrors bool) {

	currentUnitID := initialUnitID
	var err error

	trackErr := func(e error) {
		if e != nil {
			hadErrors = true
		}
	}

	for opIdx := 0; opIdx < len(runList); opIdx++ {
		if ctx.Err() != nil {
			hadErrors = true
			return
		}

		var o = &runList[opIdx]

		switch o.op {
		case readBools:
			var res []bool

			if o.isCoil {
				res, err = client.ReadCoils(ctx, currentUnitID, o.addr, o.count)
			} else {
				res, err = client.ReadDiscreteInputs(ctx, currentUnitID, o.addr, o.count)
			}
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "readBools", Addr: o.addr, Error: err.Error()})
				} else {
					fmt.Printf("failed to read coils/discrete inputs: %v\n", err)
				}
			} else {
				for idx := range res {
					a := o.addr + uint16(idx)
					if jsonOutput {
						emitJSON(cliResult{Op: "readBool", Addr: a, Type: "bool", Value: res[idx]})
					} else {
						fmt.Printf("0x%04x\t%-5v : %v\n", a, a, res[idx])
					}
				}
			}

		case readUint16, readInt16:
			var res []uint16

			if o.isHoldingReg {
				res, err = client.ReadRegisters(ctx, currentUnitID, o.addr, o.count, modbus.HoldingRegister)
			} else {
				res, err = client.ReadRegisters(ctx, currentUnitID, o.addr, o.count, modbus.InputRegister)
			}
			trackErr(err)
			if err != nil {
				typeName := "uint16"
				if o.op == readInt16 {
					typeName = "int16"
				}
				if jsonOutput {
					emitJSON(cliResult{Op: "read", Addr: o.addr, Type: typeName, Error: err.Error()})
				} else {
					fmt.Printf("failed to read holding/input registers: %v\n", err)
				}
			} else {
				for idx := range res {
					a := o.addr + uint16(idx)
					if o.op == readUint16 {
						if jsonOutput {
							emitJSON(cliResult{Op: "read", Addr: a, Type: "uint16", Value: res[idx], Hex: fmt.Sprintf("0x%04x", res[idx])})
						} else {
							fmt.Printf("0x%04x\t%-5v : 0x%04x\t%v\n", a, a, res[idx], res[idx])
						}
					} else {
						if jsonOutput {
							emitJSON(cliResult{Op: "read", Addr: a, Type: "int16", Value: int16(res[idx]), Hex: fmt.Sprintf("0x%04x", res[idx])})
						} else {
							fmt.Printf("0x%04x\t%-5v : 0x%04x\t%v\n", a, a, res[idx], int16(res[idx]))
						}
					}
				}
			}

		case readUint32, readInt32:
			layout := layoutName32(endianness, wordOrder)
			typeName := "uint32"
			if o.op == readInt32 {
				typeName = "int32"
			}
			vals, readErr := readRuntimeValues(ctx, client, currentUnitID, o.addr, o.count, o.isHoldingReg, typeName+"/layout:"+layout, 2)
			trackErr(readErr)
			if readErr != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "read", Addr: o.addr, Type: typeName, Error: readErr.Error()})
				} else {
					fmt.Printf("failed to read %s registers: %v\n", typeName, readErr)
				}
			} else {
				for idx, v := range vals {
					a := o.addr + uint16(idx)*2
					if o.op == readUint32 {
						u := v.(uint32)
						if jsonOutput {
							emitJSON(cliResult{Op: "read", Addr: a, Type: "uint32", Value: u, Hex: fmt.Sprintf("0x%08x", u)})
						} else {
							fmt.Printf("0x%04x\t%-5v : 0x%08x\t%v\n", a, a, u, u)
						}
					} else {
						s := v.(int32)
						if jsonOutput {
							emitJSON(cliResult{Op: "read", Addr: a, Type: "int32", Value: s, Hex: fmt.Sprintf("0x%08x", uint32(s))})
						} else {
							fmt.Printf("0x%04x\t%-5v : 0x%08x\t%v\n", a, a, uint32(s), s)
						}
					}
				}
			}

		case readFloat32:
			layout := layoutName32(endianness, wordOrder)
			vals, readErr := readRuntimeValues(ctx, client, currentUnitID, o.addr, o.count, o.isHoldingReg, "float32/layout:"+layout, 2)
			trackErr(readErr)
			if readErr != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "read", Addr: o.addr, Type: "float32", Error: readErr.Error()})
				} else {
					fmt.Printf("failed to read float32 registers: %v\n", readErr)
				}
			} else {
				for idx, v := range vals {
					a := o.addr + uint16(idx)*2
					if jsonOutput {
						emitJSON(cliResult{Op: "read", Addr: a, Type: "float32", Value: v.(float32)})
					} else {
						fmt.Printf("0x%04x\t%-5v : %f\n", a, a, v.(float32))
					}
				}
			}

		case readUint64, readInt64:
			layout := layoutName64(endianness, wordOrder)
			typeName := "uint64"
			if o.op == readInt64 {
				typeName = "int64"
			}
			vals, readErr := readRuntimeValues(ctx, client, currentUnitID, o.addr, o.count, o.isHoldingReg, typeName+"/layout:"+layout, 4)
			trackErr(readErr)
			if readErr != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "read", Addr: o.addr, Type: typeName, Error: readErr.Error()})
				} else {
					fmt.Printf("failed to read %s registers: %v\n", typeName, readErr)
				}
			} else {
				for idx, v := range vals {
					a := o.addr + uint16(idx)*4
					if o.op == readUint64 {
						u := v.(uint64)
						if jsonOutput {
							emitJSON(cliResult{Op: "read", Addr: a, Type: "uint64", Value: u, Hex: fmt.Sprintf("0x%016x", u)})
						} else {
							fmt.Printf("0x%04x\t%-5v : 0x%016x\t%v\n", a, a, u, u)
						}
					} else {
						s := v.(int64)
						if jsonOutput {
							emitJSON(cliResult{Op: "read", Addr: a, Type: "int64", Value: s, Hex: fmt.Sprintf("0x%016x", uint64(s))})
						} else {
							fmt.Printf("0x%04x\t%-5v : 0x%016x\t%v\n", a, a, uint64(s), s)
						}
					}
				}
			}

		case readFloat64:
			layout := layoutName64(endianness, wordOrder)
			vals, readErr := readRuntimeValues(ctx, client, currentUnitID, o.addr, o.count, o.isHoldingReg, "float64/layout:"+layout, 4)
			trackErr(readErr)
			if readErr != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "read", Addr: o.addr, Type: "float64", Error: readErr.Error()})
				} else {
					fmt.Printf("failed to read float64 registers: %v\n", readErr)
				}
			} else {
				for idx, v := range vals {
					a := o.addr + uint16(idx)*4
					if jsonOutput {
						emitJSON(cliResult{Op: "read", Addr: a, Type: "float64", Value: v.(float64)})
					} else {
						fmt.Printf("0x%04x\t%-5v : %f\n", a, a, v.(float64))
					}
				}
			}

		case readBytes:
			var res []byte
			if o.isHoldingReg {
				res, err = client.ReadRegisterBytes(ctx, currentUnitID, o.addr, o.count, modbus.HoldingRegister)
			} else {
				res, err = client.ReadRegisterBytes(ctx, currentUnitID, o.addr, o.count, modbus.InputRegister)
			}
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "read", Addr: o.addr, Type: "bytes", Error: err.Error()})
				} else {
					fmt.Printf("failed to read holding/input registers: %v\n", err)
				}
			} else {
				if jsonOutput {
					emitJSON(cliResult{Op: "read", Addr: o.addr, Type: "bytes", Value: fmt.Sprintf("%x", res)})
				} else {
					for idx := range res {
						if (idx % 16) == 0 {
							fmt.Printf("0x%04x\t%-5v : ",
								o.addr+(uint16(idx/2)), o.addr+(uint16(idx/2)))
						}
						fmt.Printf("%02x", res[idx])

						if (idx%16) == 15 || idx == len(res)-1 {
							fmt.Printf(" <%s>\n",
								decodeString(res[(idx/16*16):(idx/16*16)+(idx%16)+1]))
						} else if (idx % 16) == 7 {
							fmt.Printf(" ")
						}
					}
				}
			}

		case writeCoil:
			err = client.WriteCoil(ctx, currentUnitID, o.addr, o.coil)
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "writeCoil", Addr: o.addr, Value: o.coil, Error: err.Error()})
				} else {
					fmt.Printf("failed to write %v at coil address 0x%04x: %v\n",
						o.coil, o.addr, err)
				}
			} else {
				if jsonOutput {
					emitJSON(cliResult{Op: "writeCoil", Addr: o.addr, Value: o.coil})
				} else {
					fmt.Printf("wrote %v at coil address 0x%04x\n",
						o.coil, o.addr)
				}
			}

		case writeUint16:
			err = client.WriteRegister(ctx, currentUnitID, o.addr, o.u16)
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "uint16", Value: o.u16, Error: err.Error()})
				} else {
					fmt.Printf("failed to write %v at register address 0x%04x: %v\n",
						o.u16, o.addr, err)
				}
			} else {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "uint16", Value: o.u16})
				} else {
					fmt.Printf("wrote %v at register address 0x%04x\n",
						o.u16, o.addr)
				}
			}

		case writeInt16:
			err = client.WriteRegister(ctx, currentUnitID, o.addr, o.u16)
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "int16", Value: int16(o.u16), Error: err.Error()})
				} else {
					fmt.Printf("failed to write %v at register address 0x%04x: %v\n",
						int16(o.u16), o.addr, err)
				}
			} else {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "int16", Value: int16(o.u16)})
				} else {
					fmt.Printf("wrote %v at register address 0x%04x\n",
						int16(o.u16), o.addr)
				}
			}

		case writeUint32:
			err = writeRuntimeValue(ctx, client, currentUnitID, o.addr, o.u32, "uint32/layout:"+layoutName32(endianness, wordOrder))
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "uint32", Value: o.u32, Error: err.Error()})
				} else {
					fmt.Printf("failed to write %v at address 0x%04x: %v\n", o.u32, o.addr, err)
				}
			} else {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "uint32", Value: o.u32})
				} else {
					fmt.Printf("wrote %v at address 0x%04x\n", o.u32, o.addr)
				}
			}

		case writeInt32:
			err = writeRuntimeValue(ctx, client, currentUnitID, o.addr, int32(o.u32), "int32/layout:"+layoutName32(endianness, wordOrder))
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "int32", Value: int32(o.u32), Error: err.Error()})
				} else {
					fmt.Printf("failed to write %v at address 0x%04x: %v\n", int32(o.u32), o.addr, err)
				}
			} else {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "int32", Value: int32(o.u32)})
				} else {
					fmt.Printf("wrote %v at address 0x%04x\n", int32(o.u32), o.addr)
				}
			}

		case writeFloat32:
			err = writeRuntimeValue(ctx, client, currentUnitID, o.addr, o.f32, "float32/layout:"+layoutName32(endianness, wordOrder))
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "float32", Value: o.f32, Error: err.Error()})
				} else {
					fmt.Printf("failed to write %f at address 0x%04x: %v\n", o.f32, o.addr, err)
				}
			} else {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "float32", Value: o.f32})
				} else {
					fmt.Printf("wrote %f at address 0x%04x\n", o.f32, o.addr)
				}
			}

		case writeUint64:
			err = writeRuntimeValue(ctx, client, currentUnitID, o.addr, o.u64, "uint64/layout:"+layoutName64(endianness, wordOrder))
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "uint64", Value: o.u64, Error: err.Error()})
				} else {
					fmt.Printf("failed to write %v at address 0x%04x: %v\n", o.u64, o.addr, err)
				}
			} else {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "uint64", Value: o.u64})
				} else {
					fmt.Printf("wrote %v at address 0x%04x\n", o.u64, o.addr)
				}
			}

		case writeInt64:
			err = writeRuntimeValue(ctx, client, currentUnitID, o.addr, int64(o.u64), "int64/layout:"+layoutName64(endianness, wordOrder))
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "int64", Value: int64(o.u64), Error: err.Error()})
				} else {
					fmt.Printf("failed to write %v at address 0x%04x: %v\n", int64(o.u64), o.addr, err)
				}
			} else {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "int64", Value: int64(o.u64)})
				} else {
					fmt.Printf("wrote %v at address 0x%04x\n", int64(o.u64), o.addr)
				}
			}

		case writeFloat64:
			err = writeRuntimeValue(ctx, client, currentUnitID, o.addr, o.f64, "float64/layout:"+layoutName64(endianness, wordOrder))
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "float64", Value: o.f64, Error: err.Error()})
				} else {
					fmt.Printf("failed to write %f at address 0x%04x: %v\n", o.f64, o.addr, err)
				}
			} else {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "float64", Value: o.f64})
				} else {
					fmt.Printf("wrote %f at address 0x%04x\n", o.f64, o.addr)
				}
			}

		case writeBytes:
			err = client.WriteRegisterBytes(ctx, currentUnitID, o.addr, o.bytes)
			trackErr(err)
			if err != nil {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "bytes", Value: fmt.Sprintf("%x", o.bytes), Error: err.Error()})
				} else {
					fmt.Printf("failed to write %v at address 0x%04x: %v\n",
						o.bytes, o.addr, err)
				}
			} else {
				if jsonOutput {
					emitJSON(cliResult{Op: "write", Addr: o.addr, Type: "bytes", Value: fmt.Sprintf("%x", o.bytes)})
				} else {
					fmt.Printf("wrote %v bytes at address 0x%04x\n",
						len(o.bytes), o.addr)
				}
			}

		case sleep:
			select {
			case <-time.After(o.duration):
			case <-ctx.Done():
				return
			}

		case setUnitID:
			currentUnitID = o.unitID

		case repeat:
			opIdx = -1

		case date:
			now := time.Now().Format(time.RFC3339)
			if jsonOutput {
				emitJSON(cliResult{Op: "date", Value: now})
			} else {
				fmt.Printf("%s\n", now)
			}

		case scanBools:
			performBoolScan(ctx, client, currentUnitID, o.isCoil, jsonOutput)

		case scanRegisters:
			performRegisterScan(ctx, client, currentUnitID, o.isHoldingReg, jsonOutput)

		case scanUnitID:
			performUnitIDScan(ctx, client, jsonOutput)

		case ping:
			performPing(ctx, client, currentUnitID, o.count, o.duration, jsonOutput)

		default:
			fmt.Printf("unknown operation %v\n", o)
			os.Exit(100)
		}

		if failFast && hadErrors {
			return
		}
	}
	return
}
