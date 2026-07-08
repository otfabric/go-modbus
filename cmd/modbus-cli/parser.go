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
	u16          uint16
	u32          uint32
	f32          float32
	u64          uint64
	f64          float64
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

func parseOperations(args []string) ([]operation, error) {
	var runList []operation
	var err error

	for _, arg := range args {
		var splitArgs []string
		var o operation

		verbAndRest := strings.SplitN(arg, ":", 2)
		verb := verbAndRest[0]
		if len(verbAndRest) < 2 && verb != "repeat" && verb != "date" {
			return nil, fmt.Errorf("illegal command format (should be command:arg1:arg2..., e.g. rh:uint32:0x1000+5)")
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
			var extra uint16
			o.addr, extra, err = parseAddressAndQuantity(splitArgs[1])
			if err != nil {
				return nil, fmt.Errorf("failed to parse address ('%v'): %w", splitArgs[1], err)
			}
			o.count = extra + 1

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
				return nil, fmt.Errorf("unknown register type '%v' (should be one of uint16, int16, uint32, int32, float32, uint64, int64, float64, bytes)", splitArgs[1])
			}

			var extra uint16
			o.addr, extra, err = parseAddressAndQuantity(splitArgs[2])
			if err != nil {
				return nil, fmt.Errorf("failed to parse address ('%v'): %w", splitArgs[2], err)
			}
			o.count = extra + 1

		case "wc", "writeCoil":
			if len(splitArgs) != 3 {
				return nil, fmt.Errorf("need exactly 2 arguments after writeCoil, got %v", len(splitArgs)-1)
			}

			o.op = writeCoil
			o.addr, err = parseUint16(splitArgs[1])
			if err != nil {
				return nil, fmt.Errorf("failed to parse address ('%v'): %w", splitArgs[1], err)
			}

			switch splitArgs[2] {
			case "true":
				o.coil = true
			case "false":
				o.coil = false
			default:
				return nil, fmt.Errorf("failed to parse coil value '%v' (should be true or false)", splitArgs[2])
			}

		case "wr", "writeRegister":
			if len(splitArgs) != 4 {
				return nil, fmt.Errorf("need exactly 3 arguments after writeRegister, got %v", len(splitArgs)-1)
			}

			o.addr, err = parseUint16(splitArgs[2])
			if err != nil {
				return nil, fmt.Errorf("failed to parse address ('%v'): %w", splitArgs[2], err)
			}

			switch splitArgs[1] {
			case "uint16":
				o.op = writeUint16
				o.u16, err = parseUint16(splitArgs[3])

			case "int16":
				o.op = writeInt16
				o.u16, err = parseInt16(splitArgs[3])

			case "uint32":
				o.op = writeUint32
				o.u32, err = parseUint32(splitArgs[3])

			case "int32":
				o.op = writeInt32
				o.u32, err = parseInt32(splitArgs[3])

			case "float32":
				o.op = writeFloat32
				o.f32, err = parseFloat32(splitArgs[3])

			case "uint64":
				o.op = writeUint64
				o.u64, err = parseUint64(splitArgs[3])

			case "int64":
				o.op = writeInt64
				o.u64, err = parseInt64(splitArgs[3])

			case "float64":
				o.op = writeFloat64
				o.f64, err = parseFloat64(splitArgs[3])

			case "bytes":
				o.op = writeBytes
				o.bytes, err = parseHexBytes(splitArgs[3])

			case "string":
				o.op = writeBytes
				o.bytes = []byte(splitArgs[3])

			default:
				return nil, fmt.Errorf("unknown register type '%v' (should be one of uint16, int16, uint32, int32, float32, uint64, int64, float64, bytes, string)", splitArgs[1])
			}

			if err != nil {
				return nil, fmt.Errorf("failed to parse '%s' as %s: %w", splitArgs[3], splitArgs[1], err)
			}

		case "sleep":
			if len(splitArgs) != 2 {
				return nil, fmt.Errorf("need exactly 1 argument after sleep, got %v", len(splitArgs)-1)
			}

			o.op = sleep
			o.duration, err = time.ParseDuration(splitArgs[1])
			if err != nil {
				return nil, fmt.Errorf("failed to parse '%s' as duration: %w", splitArgs[1], err)
			}

		case "suid", "setUnitId", "sid":
			if len(splitArgs) != 2 {
				return nil, fmt.Errorf("need exactly 1 argument after setUnitId, got %v", len(splitArgs)-1)
			}

			o.op = setUnitID
			o.unitID, err = parseUnitID(splitArgs[1])
			if err != nil {
				return nil, fmt.Errorf("failed to parse '%s' as unit id: %w", splitArgs[1], err)
			}

		case "repeat":
			if len(splitArgs) != 1 {
				return nil, fmt.Errorf("repeat takes no arguments, got %v", len(splitArgs)-1)
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
				return nil, fmt.Errorf("unknown scan/register type '%s' (valid options: coils, di, hr, ir, s)", splitArgs[1])
			}

		case "ping":
			if len(splitArgs) < 2 || len(splitArgs) > 3 {
				return nil, fmt.Errorf("need 1 or 2 arguments after ping, got %v", len(splitArgs)-1)
			}

			o.op = ping
			o.count, err = parseUint16(splitArgs[1])
			if err != nil {
				return nil, fmt.Errorf("failed to parse ping count ('%v'): %w", splitArgs[1], err)
			}

			if o.count == 0 {
				return nil, fmt.Errorf("illegal ping count value (must be >= 1)")
			}

			if len(splitArgs) == 3 {
				o.duration, err = time.ParseDuration(splitArgs[2])
				if err != nil {
					return nil, fmt.Errorf("failed to parse '%s' as duration: %w", splitArgs[2], err)
				}
			}

		default:
			return nil, fmt.Errorf("unsupported command '%v'", splitArgs[0])
		}

		runList = append(runList, o)

	}

	return runList, nil
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

func parseInt16(in string) (u16 uint16, err error) {
	var val int64

	val, err = strconv.ParseInt(in, 0, 16)
	if err == nil {
		u16 = uint16(int16(val))
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

func parseInt32(in string) (u32 uint32, err error) {
	var val int64

	val, err = strconv.ParseInt(in, 0, 32)
	if err == nil {
		u32 = uint32(int32(val))
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
	var val uint64

	val, err = strconv.ParseUint(in, 0, 64)
	if err == nil {
		u64 = val
	}

	return
}

func parseInt64(in string) (u64 uint64, err error) {
	var val int64

	val, err = strconv.ParseInt(in, 0, 64)
	if err == nil {
		u64 = uint64(val)
	}

	return
}

func parseFloat64(in string) (f64 float64, err error) {
	var val float64

	val, err = strconv.ParseFloat(in, 64)
	if err == nil {
		f64 = val
	}

	return
}

func parseAddressAndQuantity(in string) (addr uint16, quantity uint16, err error) {
	var split = strings.Split(in, "+")

	switch {
	case len(split) == 1:
		addr, err = parseUint16(in)

	case len(split) == 2:
		addr, err = parseUint16(split[0])
		if err != nil {
			return
		}
		quantity, err = parseUint16(split[1])
	default:
		err = errors.New("illegal format")
	}

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

func parseHexBytes(in string) (out []byte, err error) {
	out, err = hex.DecodeString(in)

	return
}
