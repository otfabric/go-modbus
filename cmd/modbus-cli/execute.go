// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/otfabric/go-modbus"
	"github.com/otfabric/go-modbus/codec"
)

// layoutName16 returns the 16-bit layout name for CLI codec selection (e.g. "21").
func layoutName16(e modbus.Endianness) string {
	if e == modbus.LittleEndian {
		return "12"
	}
	return "21"
}

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
		return "78563412"
	}
	return "12345678"
}

// layoutName returns the layout name for a value regs registers wide.
func layoutName(e modbus.Endianness, w modbus.WordOrder, regs uint16) string {
	switch regs {
	case 1:
		return layoutName16(e)
	case 2:
		return layoutName32(e, w)
	default:
		return layoutName64(e, w)
	}
}

// numericType describes how a typed register read or write is decoded and
// printed.
type numericType struct {
	name string
	// hexCodec is the unsigned codec of the same width used to print the
	// hex representation of a signed value; empty for unsigned values
	// (printed directly) and floats (no hex).
	hexCodec string
	isFloat  bool
}

var numericTypes = map[operationKind]numericType{
	readUint16:   {name: "uint16"},
	readInt16:    {name: "int16", hexCodec: "uint16"},
	readUint32:   {name: "uint32"},
	readInt32:    {name: "int32", hexCodec: "uint32"},
	readFloat32:  {name: "float32", isFloat: true},
	readUint64:   {name: "uint64"},
	readInt64:    {name: "int64", hexCodec: "uint64"},
	readFloat64:  {name: "float64", isFloat: true},
	writeUint16:  {name: "uint16"},
	writeInt16:   {name: "int16"},
	writeUint32:  {name: "uint32"},
	writeInt32:   {name: "int32"},
	writeFloat32: {name: "float32", isFloat: true},
	writeUint64:  {name: "uint64"},
	writeInt64:   {name: "int64"},
	writeFloat64: {name: "float64", isFloat: true},
}

func regType(isHolding bool) modbus.RegType {
	if isHolding {
		return modbus.HoldingRegister
	}
	return modbus.InputRegister
}

func runtimeCodec(codecID string) (codec.RuntimeCodec, error) {
	rc, ok, err := codec.RuntimeCodecByID(codecID)
	if err != nil {
		return nil, fmt.Errorf("codec %s: %w", codecID, err)
	}
	if !ok {
		return nil, fmt.Errorf("unknown codec %q", codecID)
	}
	return rc, nil
}

// readRuntimeValues reads count values of regsPerValue registers each and
// decodes every value with each of the given codecs: values[i][j] is value i
// decoded with codecIDs[j].
func readRuntimeValues(ctx context.Context, client *modbus.Client, unitID uint8, addr, count uint16,
	isHoldingReg bool, regsPerValue uint16, codecIDs ...string) ([][]any, error) {

	codecs := make([]codec.RuntimeCodec, 0, len(codecIDs))
	for _, id := range codecIDs {
		rc, err := runtimeCodec(id)
		if err != nil {
			return nil, err
		}
		codecs = append(codecs, rc)
	}

	totalRegs := regsPerValue * count
	regs, err := client.ReadRegisters(ctx, unitID, addr, totalRegs, regType(isHoldingReg))
	if err != nil {
		return nil, err
	}

	values := make([][]any, 0, count)
	for idx := range count {
		slice := regs[idx*regsPerValue : (idx+1)*regsPerValue]
		decoded := make([]any, 0, len(codecs))
		for _, rc := range codecs {
			v, decErr := codec.DecodeRegistersAny(slice, rc)
			if decErr != nil {
				return nil, fmt.Errorf("decode error at 0x%04x: %w", addr+idx*regsPerValue, decErr)
			}
			decoded = append(decoded, v)
		}
		values = append(values, decoded)
	}
	return values, nil
}

// writeRuntimeValue encodes value with the given codec and writes it: with
// FC06 when it fits into a single register, with FC16 otherwise.
func writeRuntimeValue(ctx context.Context, client *modbus.Client, unitID uint8, addr uint16,
	value any, codecID string) error {

	rc, err := runtimeCodec(codecID)
	if err != nil {
		return err
	}
	regs, err := codec.EncodeRegistersAny(value, rc)
	if err != nil {
		return err
	}
	if len(regs) == 1 {
		return client.WriteRegister(ctx, unitID, addr, regs[0])
	}
	return client.WriteRegisters(ctx, unitID, addr, regs)
}

// cliResult is a structured result for JSON output mode.
type cliResult struct {
	Op    string  `json:"op"`
	Addr  *uint16 `json:"addr,omitempty"`
	Type  string  `json:"type,omitempty"`
	Value any     `json:"value,omitempty"`
	Hex   string  `json:"hex,omitempty"`
	Error string  `json:"error,omitempty"`
}

// at returns a pointer to addr, for cliResult.Addr: a plain uint16 with
// omitempty would drop address 0.
func at(addr uint16) *uint16 {
	return &addr
}

// output writes operation results either as text or as line-delimited JSON.
type output struct {
	w    io.Writer
	json bool
	enc  *json.Encoder
}

func newOutput(w io.Writer, jsonOutput bool) *output {
	return &output{w: w, json: jsonOutput, enc: json.NewEncoder(w)}
}

func (out *output) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(out.w, format, args...)
}

func (out *output) emit(r cliResult) {
	r.Value = jsonValue(r.Value)
	_ = out.enc.Encode(r)
}

// jsonValue replaces the float values JSON cannot represent (NaN, ±Inf),
// which would make encoding fail and the record disappear, with their
// names as strings.
func jsonValue(v any) any {
	var f float64
	switch x := v.(type) {
	case float32:
		f = float64(x)
	case float64:
		f = x
	default:
		return v
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return v
}

func executeOperations(ctx context.Context, client *modbus.Client, runList []operation,
	initialUnitID uint8, endianness modbus.Endianness, wordOrder modbus.WordOrder,
	out *output, failFast bool) (hadErrors bool) {

	currentUnitID := initialUnitID
	jsonOutput := out.json
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
					out.emit(cliResult{Op: "readBools", Addr: at(o.addr), Error: err.Error()})
				} else {
					out.printf("failed to read coils/discrete inputs: %v\n", err)
				}
			} else {
				for idx := range res {
					a := o.addr + uint16(idx)
					if jsonOutput {
						out.emit(cliResult{Op: "readBool", Addr: at(a), Type: "bool", Value: res[idx]})
					} else {
						out.printf("0x%04x\t%-5v : %v\n", a, a, res[idx])
					}
				}
			}

		case readUint16, readInt16, readUint32, readInt32, readFloat32,
			readUint64, readInt64, readFloat64:
			t := numericTypes[o.op]
			regs := registersPerValue(o.op)
			layout := layoutName(endianness, wordOrder, regs)
			codecIDs := []string{t.name + "/layout:" + layout}
			if t.hexCodec != "" {
				codecIDs = append(codecIDs, t.hexCodec+"/layout:"+layout)
			}
			vals, readErr := readRuntimeValues(ctx, client, currentUnitID, o.addr, o.count, o.isHoldingReg, regs, codecIDs...)
			trackErr(readErr)
			if readErr != nil {
				if jsonOutput {
					out.emit(cliResult{Op: "read", Addr: at(o.addr), Type: t.name, Error: readErr.Error()})
				} else {
					out.printf("failed to read %s registers: %v\n", t.name, readErr)
				}
				break
			}
			for idx, v := range vals {
				a := o.addr + uint16(idx)*regs
				if t.isFloat {
					if jsonOutput {
						out.emit(cliResult{Op: "read", Addr: at(a), Type: t.name, Value: v[0]})
					} else {
						out.printf("0x%04x\t%-5v : %f\n", a, a, v[0])
					}
					continue
				}
				raw := v[len(v)-1] // the unsigned representation
				hex := fmt.Sprintf("0x%0*x", 4*int(regs), raw)
				if jsonOutput {
					out.emit(cliResult{Op: "read", Addr: at(a), Type: t.name, Value: v[0], Hex: hex})
				} else {
					out.printf("0x%04x\t%-5v : %s\t%v\n", a, a, hex, v[0])
				}
			}

		case readBytes:
			var res []byte
			res, err = client.ReadRegisterBytes(ctx, currentUnitID, o.addr, o.count, regType(o.isHoldingReg))
			trackErr(err)
			if err != nil {
				if jsonOutput {
					out.emit(cliResult{Op: "read", Addr: at(o.addr), Type: "bytes", Error: err.Error()})
				} else {
					out.printf("failed to read holding/input registers: %v\n", err)
				}
			} else {
				if jsonOutput {
					out.emit(cliResult{Op: "read", Addr: at(o.addr), Type: "bytes", Value: fmt.Sprintf("%x", res)})
				} else {
					for idx := range res {
						if (idx % 16) == 0 {
							out.printf("0x%04x\t%-5v : ",
								o.addr+(uint16(idx/2)), o.addr+(uint16(idx/2)))
						}
						out.printf("%02x", res[idx])

						if (idx%16) == 15 || idx == len(res)-1 {
							out.printf(" <%s>\n",
								decodeString(res[(idx/16*16):(idx/16*16)+(idx%16)+1]))
						} else if (idx % 16) == 7 {
							out.printf(" ")
						}
					}
				}
			}

		case writeCoil:
			err = client.WriteCoil(ctx, currentUnitID, o.addr, o.coil)
			trackErr(err)
			if err != nil {
				if jsonOutput {
					out.emit(cliResult{Op: "writeCoil", Addr: at(o.addr), Value: o.coil, Error: err.Error()})
				} else {
					out.printf("failed to write %v at coil address 0x%04x: %v\n",
						o.coil, o.addr, err)
				}
			} else {
				if jsonOutput {
					out.emit(cliResult{Op: "writeCoil", Addr: at(o.addr), Value: o.coil})
				} else {
					out.printf("wrote %v at coil address 0x%04x\n",
						o.coil, o.addr)
				}
			}

		case writeUint16, writeInt16, writeUint32, writeInt32, writeFloat32,
			writeUint64, writeInt64, writeFloat64:
			t := numericTypes[o.op]
			regs := registersPerValue(o.op)
			err = writeRuntimeValue(ctx, client, currentUnitID, o.addr, o.value,
				t.name+"/layout:"+layoutName(endianness, wordOrder, regs))
			trackErr(err)
			if jsonOutput {
				r := cliResult{Op: "write", Addr: at(o.addr), Type: t.name, Value: o.value}
				if err != nil {
					r.Error = err.Error()
				}
				out.emit(r)
				break
			}
			verb := "%v"
			if t.isFloat {
				verb = "%f"
			}
			where := "address"
			if regs == 1 {
				where = "register address"
			}
			if err != nil {
				out.printf("failed to write "+verb+" at "+where+" 0x%04x: %v\n", o.value, o.addr, err)
			} else {
				out.printf("wrote "+verb+" at "+where+" 0x%04x\n", o.value, o.addr)
			}

		case writeBytes:
			err = client.WriteRegisterBytes(ctx, currentUnitID, o.addr, o.bytes)
			trackErr(err)
			if err != nil {
				if jsonOutput {
					out.emit(cliResult{Op: "write", Addr: at(o.addr), Type: "bytes", Value: fmt.Sprintf("%x", o.bytes), Error: err.Error()})
				} else {
					out.printf("failed to write %v at address 0x%04x: %v\n",
						o.bytes, o.addr, err)
				}
			} else {
				if jsonOutput {
					out.emit(cliResult{Op: "write", Addr: at(o.addr), Type: "bytes", Value: fmt.Sprintf("%x", o.bytes)})
				} else {
					out.printf("wrote %v bytes at address 0x%04x\n",
						len(o.bytes), o.addr)
				}
			}

		case sleep:
			select {
			case <-time.After(o.duration):
			case <-ctx.Done():
				hadErrors = true
				return
			}

		case setUnitID:
			currentUnitID = o.unitID

		case repeat:
			opIdx = -1

		case date:
			now := time.Now().Format(time.RFC3339)
			if jsonOutput {
				out.emit(cliResult{Op: "date", Value: now})
			} else {
				out.printf("%s\n", now)
			}

		case scanBools:
			performBoolScan(ctx, client, currentUnitID, o.isCoil, out)

		case scanRegisters:
			performRegisterScan(ctx, client, currentUnitID, o.isHoldingReg, out)

		case scanUnitID:
			performUnitIDScan(ctx, client, out)

		case ping:
			performPing(ctx, client, currentUnitID, o.count, o.duration, out)
		}

		if failFast && hadErrors {
			return
		}
	}
	return
}
