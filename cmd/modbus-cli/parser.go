// SPDX-License-Identifier: MIT

package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/otfabric/go-modbus"
)

type operationKind uint

const (
	readBools operationKind = iota + 1
	readUint16
	readInt16
	readUint32
	readInt32
	readFloat32
	readUint64
	readInt64
	readFloat64
	readBytes
	writeCoil
	writeUint16
	writeInt16
	writeInt32
	writeUint32
	writeFloat32
	writeInt64
	writeUint64
	writeFloat64
	writeBytes
	setUnitID
	sleep
	repeat
	date
	scanBools
	scanRegisters
	scanUnitID
	ping
)

type operation struct {
	op           operationKind
	addr         uint16
	isCoil       bool
	isHoldingReg bool
	count        uint16
	coil         bool
	value        any // typed register value for writeUint16 ... writeFloat64
	bytes        []byte
	duration     time.Duration
	unitID       uint8
}

func parseEndianness(s string) (modbus.Endianness, error) {
	switch s {
	case "big":
		return modbus.BigEndian, nil
	case "little":
		return modbus.LittleEndian, nil
	default:
		return 0, fmt.Errorf("unknown endianness setting '%s' (should be big or little)", s)
	}
}

func parseWordOrder(s string) (modbus.WordOrder, error) {
	switch s {
	case "highfirst", "hf":
		return modbus.HighWordFirst, nil
	case "lowfirst", "lf":
		return modbus.LowWordFirst, nil
	default:
		return 0, fmt.Errorf("unknown word order setting '%s' (should be one of highfirst, hf, lowfirst, lf)", s)
	}
}

// Protocol limits per request (Modbus Application Protocol V1.1b3, 6.1-6.4
// and 6.12).
const (
	maxReadBits      = 2000 // FC01, FC02
	maxReadRegisters = 125  // FC03, FC04
	maxWriteRegs     = 123  // FC16
)

// registersPerValue returns the number of 16-bit registers one value of the
// given read or write operation occupies, or 0 for operations that do not
// address registers by value.
func registersPerValue(op operationKind) uint16 {
	return registerWidths[op]
}

var registerWidths = map[operationKind]uint16{
	readUint16: 1, readInt16: 1, writeUint16: 1, writeInt16: 1,
	readUint32: 2, readInt32: 2, readFloat32: 2, writeUint32: 2, writeInt32: 2, writeFloat32: 2,
	readUint64: 4, readInt64: 4, readFloat64: 4, writeUint64: 4, writeInt64: 4, writeFloat64: 4,
}

// checkRange verifies that quantity items starting at addr fit both into a
// single request (at most limit items) and into the 16-bit address space.
func checkRange(addr uint16, quantity, limit uint32, unit string) error {
	if quantity == 0 {
		return fmt.Errorf("quantity must be at least 1 %s", unit)
	}
	if quantity > limit {
		return fmt.Errorf("quantity %d exceeds the limit of %d %s per request", quantity, limit, unit)
	}
	if uint32(addr)+quantity-1 > 0xffff {
		return fmt.Errorf("range 0x%04x+%d runs past the end of the address space (0xffff)", addr, quantity)
	}
	return nil
}

func parseOperations(args []string) ([]operation, error) {
	var runList []operation
	var err error
	var hasWork bool

	for _, arg := range args {
		var splitArgs []string
		var o operation

		verbAndRest := strings.SplitN(arg, ":", 2)
		verb := verbAndRest[0]
		if len(verbAndRest) < 2 && verb != "repeat" && verb != "date" {
			return nil, fmt.Errorf("illegal command format %q (should be command:arg1:arg2..., e.g. rh:uint32:0x1000+5)", arg)
		}

		if verb == "wr" || verb == "writeRegister" {
			splitArgs = strings.SplitN(arg, ":", 4)
		} else {
			splitArgs = strings.Split(arg, ":")
		}

		switch splitArgs[0] {
		case "rc", "readCoil", "readCoils",
			"rdi", "readDiscreteInput", "readDiscreteInputs":

			if len(splitArgs) != 2 {
				return nil, fmt.Errorf("need exactly 1 argument after rc/rdi, got %v", len(splitArgs)-1)
			}

			if splitArgs[0] == "rc" || splitArgs[0] == "readCoil" || splitArgs[0] == "readCoils" {
				o.isCoil = true
			}

			o.op = readBools
			o.addr, o.count, err = parseAddressAndCount(splitArgs[1], 1, maxReadBits, "coils/discrete inputs")
			if err != nil {
				return nil, fmt.Errorf("failed to parse address (%q): %w", splitArgs[1], err)
			}

		case "rh", "readHoldingRegister", "readHoldingRegisters",
			"ri", "readInputRegister", "readInputRegisters":

			if len(splitArgs) != 3 {
				return nil, fmt.Errorf("need exactly 2 arguments after rh/ri, got %v", len(splitArgs)-1)
			}

			if splitArgs[0] == "rh" || splitArgs[0] == "readHoldingRegister" ||
				splitArgs[0] == "readHoldingRegisters" {
				o.isHoldingReg = true
			}

			switch splitArgs[1] {
			case "uint16":
				o.op = readUint16
			case "int16":
				o.op = readInt16
			case "uint32":
				o.op = readUint32
			case "int32":
				o.op = readInt32
			case "float32":
				o.op = readFloat32
			case "uint64":
				o.op = readUint64
			case "int64":
				o.op = readInt64
			case "float64":
				o.op = readFloat64
			case "bytes":
				o.op = readBytes
			default:
				return nil, fmt.Errorf("unknown register type %q (should be one of uint16, int16, uint32, int32, float32, uint64, int64, float64, bytes)", splitArgs[1])
			}

			if o.op == readBytes {
				// the quantity is a byte count, two bytes per register
				o.addr, o.count, err = parseAddressAndBytes(splitArgs[2])
			} else {
				perValue := registersPerValue(o.op)
				o.addr, o.count, err = parseAddressAndCount(splitArgs[2], perValue,
					maxReadRegisters/uint32(perValue), splitArgs[1]+" values")
			}
			if err != nil {
				return nil, fmt.Errorf("failed to parse address (%q): %w", splitArgs[2], err)
			}

		case "wc", "writeCoil":
			if len(splitArgs) != 3 {
				return nil, fmt.Errorf("need exactly 2 arguments after writeCoil, got %v", len(splitArgs)-1)
			}

			o.op = writeCoil
			o.addr, err = parseUint16(splitArgs[1])
			if err != nil {
				return nil, fmt.Errorf("failed to parse address (%q): %w", splitArgs[1], err)
			}

			switch splitArgs[2] {
			case "true":
				o.coil = true
			case "false":
				o.coil = false
			default:
				return nil, fmt.Errorf("failed to parse coil value %q (should be true or false)", splitArgs[2])
			}

		case "wr", "writeRegister":
			if len(splitArgs) != 4 {
				return nil, fmt.Errorf("need exactly 3 arguments after writeRegister, got %v", len(splitArgs)-1)
			}

			o.addr, err = parseUint16(splitArgs[2])
			if err != nil {
				return nil, fmt.Errorf("failed to parse address (%q): %w", splitArgs[2], err)
			}

			switch splitArgs[1] {
			case "uint16":
				o.op = writeUint16
				o.value, err = parseUint16(splitArgs[3])

			case "int16":
				o.op = writeInt16
				o.value, err = parseInt16(splitArgs[3])

			case "uint32":
				o.op = writeUint32
				o.value, err = parseUint32(splitArgs[3])

			case "int32":
				o.op = writeInt32
				o.value, err = parseInt32(splitArgs[3])

			case "float32":
				o.op = writeFloat32
				o.value, err = parseFloat32(splitArgs[3])

			case "uint64":
				o.op = writeUint64
				o.value, err = parseUint64(splitArgs[3])

			case "int64":
				o.op = writeInt64
				o.value, err = parseInt64(splitArgs[3])

			case "float64":
				o.op = writeFloat64
				o.value, err = parseFloat64(splitArgs[3])

			case "bytes":
				o.op = writeBytes
				o.bytes, err = parseHexBytes(splitArgs[3])

			case "string":
				o.op = writeBytes
				o.bytes = []byte(splitArgs[3])

			default:
				return nil, fmt.Errorf("unknown register type %q (should be one of uint16, int16, uint32, int32, float32, uint64, int64, float64, bytes, string)", splitArgs[1])
			}

			if err != nil {
				return nil, fmt.Errorf("failed to parse %q as %s: %w", splitArgs[3], splitArgs[1], err)
			}

			var regs uint32
			if o.op == writeBytes {
				n := len(o.bytes)
				if n == 0 {
					return nil, fmt.Errorf("nothing to write: empty %s value", splitArgs[1])
				}
				if n > 2*maxWriteRegs {
					return nil, fmt.Errorf("%s value is %d bytes long, at most %d bytes fit into one request",
						splitArgs[1], n, 2*maxWriteRegs)
				}
				// one register per two bytes, an odd last byte is padded
				regs = (uint32(n) + 1) / 2
			} else {
				regs = uint32(registersPerValue(o.op))
			}
			if err = checkRange(o.addr, regs, maxWriteRegs, "registers"); err != nil {
				return nil, fmt.Errorf("cannot write %s at address 0x%04x: %w", splitArgs[1], o.addr, err)
			}

		case "sleep":
			if len(splitArgs) != 2 {
				return nil, fmt.Errorf("need exactly 1 argument after sleep, got %v", len(splitArgs)-1)
			}

			o.op = sleep
			o.duration, err = parseDuration(splitArgs[1])
			if err != nil {
				return nil, fmt.Errorf("failed to parse %q as duration: %w", splitArgs[1], err)
			}

		case "suid", "setUnitId", "sid":
			if len(splitArgs) != 2 {
				return nil, fmt.Errorf("need exactly 1 argument after setUnitId, got %v", len(splitArgs)-1)
			}

			o.op = setUnitID
			o.unitID, err = parseUnitID(splitArgs[1])
			if err != nil {
				return nil, fmt.Errorf("failed to parse %q as unit id: %w", splitArgs[1], err)
			}

		case "repeat":
			if len(splitArgs) != 1 {
				return nil, fmt.Errorf("repeat takes no arguments, got %v", len(splitArgs)-1)
			}

			// without any operation before it, repeat would spin forever
			// without doing anything
			if !hasWork {
				return nil, errors.New("repeat needs at least one operation before it (other than suid or repeat)")
			}

			o.op = repeat

		case "date":
			if len(splitArgs) != 1 {
				return nil, fmt.Errorf("date takes no arguments, got %v", len(splitArgs)-1)
			}

			o.op = date

		case "scan":
			if len(splitArgs) != 2 {
				return nil, fmt.Errorf("need exactly 1 argument after scan, got %v", len(splitArgs)-1)
			}

			switch splitArgs[1] {
			case "c", "coils":
				o.op = scanBools
				o.isCoil = true
			case "di", "discreteInputs":
				o.op = scanBools
				o.isCoil = false
			case "h", "hr", "holding", "holdingRegisters":
				o.op = scanRegisters
				o.isHoldingReg = true
			case "i", "ir", "input", "inputRegisters":
				o.op = scanRegisters
				o.isHoldingReg = false
			case "s", "sid":
				o.op = scanUnitID
			default:
				return nil, fmt.Errorf("unknown scan/register type %q (valid options: coils, di, hr, ir, s)", splitArgs[1])
			}

		case "ping":
			if len(splitArgs) < 2 || len(splitArgs) > 3 {
				return nil, fmt.Errorf("need 1 or 2 arguments after ping, got %v", len(splitArgs)-1)
			}

			o.op = ping
			o.count, err = parseUint16(splitArgs[1])
			if err != nil {
				return nil, fmt.Errorf("failed to parse ping count (%q): %w", splitArgs[1], err)
			}

			if o.count == 0 {
				return nil, fmt.Errorf("illegal ping count value (must be >= 1)")
			}

			if len(splitArgs) == 3 {
				o.duration, err = parseDuration(splitArgs[2])
				if err != nil {
					return nil, fmt.Errorf("failed to parse %q as duration: %w", splitArgs[2], err)
				}
			}

		default:
			return nil, fmt.Errorf("unsupported command %q", splitArgs[0])
		}

		if o.op != setUnitID && o.op != repeat {
			hasWork = true
		}

		runList = append(runList, o)

	}

	return runList, nil
}

// parseDuration parses a non-negative duration.
func parseDuration(in string) (d time.Duration, err error) {
	d, err = time.ParseDuration(in)
	if err == nil && d < 0 {
		err = errors.New("duration must not be negative")
	}

	return
}

func parseUint16(in string) (u16 uint16, err error) {
	var val uint64

	val, err = strconv.ParseUint(in, 0, 16)
	if err != nil {
		return
	}
	u16 = uint16(val)

	return
}

func parseInt16(in string) (i16 int16, err error) {
	var val int64

	val, err = strconv.ParseInt(in, 0, 16)
	if err == nil {
		i16 = int16(val)
	}

	return
}

func parseUint32(in string) (u32 uint32, err error) {
	var val uint64

	val, err = strconv.ParseUint(in, 0, 32)
	if err != nil {
		return
	}
	u32 = uint32(val)

	return
}

func parseInt32(in string) (i32 int32, err error) {
	var val int64

	val, err = strconv.ParseInt(in, 0, 32)
	if err == nil {
		i32 = int32(val)
	}

	return
}

func parseFloat32(in string) (f32 float32, err error) {
	var val float64

	val, err = strconv.ParseFloat(in, 32)
	if err == nil {
		f32 = float32(val)
	}

	return
}

func parseUint64(in string) (u64 uint64, err error) {
	return strconv.ParseUint(in, 0, 64)
}

func parseInt64(in string) (i64 int64, err error) {
	return strconv.ParseInt(in, 0, 64)
}

func parseFloat64(in string) (f64 float64, err error) {
	return strconv.ParseFloat(in, 64)
}

// parseAddressAndQuantity parses <addr>[+<extra>] and returns the address
// and the number of additional items after the first one.
func parseAddressAndQuantity(in string) (addr uint16, extra uint16, err error) {
	var split = strings.Split(in, "+")

	switch {
	case len(split) == 1:
		addr, err = parseUint16(in)

	case len(split) == 2:
		addr, err = parseUint16(split[0])
		if err != nil {
			return
		}
		extra, err = parseUint16(split[1])
	default:
		err = errors.New("illegal format")
	}

	return
}

// parseAddressAndCount parses <addr>[+<extra>] for a read of values that are
// regsPerValue registers (or bits) wide each, and rejects requests that
// exceed limit values (limit < 65536) or run past the end of the address
// space.
func parseAddressAndCount(in string, regsPerValue uint16, limit uint32, unit string) (addr uint16, count uint16, err error) {
	var extra uint16

	addr, extra, err = parseAddressAndQuantity(in)
	if err != nil {
		return
	}
	// computed in 32 bits: extra + 1 wraps to 0 in 16 bits for +65535
	quantity := uint32(extra) + 1
	if quantity > limit {
		err = fmt.Errorf("quantity %d exceeds the limit of %d %s per request", quantity, limit, unit)
		return
	}
	if err = checkRange(addr, quantity*uint32(regsPerValue), 0xffff, unit); err != nil {
		return
	}
	// extra < limit <= 0xffff: no wrap
	count = extra + 1

	return
}

// parseAddressAndBytes parses <addr>[+<extra>] for a raw byte read.
func parseAddressAndBytes(in string) (addr uint16, count uint16, err error) {
	var extra uint16

	addr, extra, err = parseAddressAndQuantity(in)
	if err != nil {
		return
	}
	quantity := uint32(extra) + 1
	if quantity > 2*maxReadRegisters {
		err = fmt.Errorf("quantity %d exceeds the limit of %d bytes per request", quantity, 2*maxReadRegisters)
		return
	}
	if err = checkRange(addr, (quantity+1)/2, maxReadRegisters, "registers"); err != nil {
		return
	}
	count = extra + 1

	return
}

func parseUnitID(in string) (addr uint8, err error) {
	var val uint64

	val, err = strconv.ParseUint(in, 0, 8)
	if err == nil {
		addr = uint8(val)
	}

	return
}

// parseHexBytes decodes a hex string, with an optional 0x prefix.
func parseHexBytes(in string) (out []byte, err error) {
	if strings.HasPrefix(in, "0x") || strings.HasPrefix(in, "0X") {
		in = in[2:]
	}
	out, err = hex.DecodeString(in)

	return
}
