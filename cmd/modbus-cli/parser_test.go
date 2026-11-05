// SPDX-License-Identifier: MIT

package main

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-modbus"
)

func TestParseOperations_Valid(t *testing.T) {
	tests := []struct {
		arg  string
		want operation
	}{
		{"rc:0", operation{op: readBools, isCoil: true, count: 1}},
		{"rc:0x10+9", operation{op: readBools, isCoil: true, addr: 0x10, count: 10}},
		{"readCoils:1", operation{op: readBools, isCoil: true, addr: 1, count: 1}},
		{"rdi:5+1999", operation{op: readBools, addr: 5, count: 2000}},
		{"rc:63536+1999", operation{op: readBools, isCoil: true, addr: 63536, count: 2000}},
		{"rc:0xffff", operation{op: readBools, isCoil: true, addr: 0xffff, count: 1}},
		{"rh:uint16:0+124", operation{op: readUint16, isHoldingReg: true, count: 125}},
		{"ri:int16:0xffff", operation{op: readInt16, addr: 0xffff, count: 1}},
		{"readHoldingRegisters:uint32:2+61", operation{op: readUint32, isHoldingReg: true, addr: 2, count: 62}},
		{"rh:int32:0xfffe", operation{op: readInt32, isHoldingReg: true, addr: 0xfffe, count: 1}},
		{"readInputRegisters:float32:0", operation{op: readFloat32, count: 1}},
		{"rh:uint64:0+30", operation{op: readUint64, isHoldingReg: true, count: 31}},
		{"rh:int64:0xfffc", operation{op: readInt64, isHoldingReg: true, addr: 0xfffc, count: 1}},
		{"ri:float64:8", operation{op: readFloat64, addr: 8, count: 1}},
		{"rh:bytes:0+249", operation{op: readBytes, isHoldingReg: true, count: 250}},
		{"rh:bytes:0xffff+1", operation{op: readBytes, isHoldingReg: true, addr: 0xffff, count: 2}},
		{"wc:3:true", operation{op: writeCoil, addr: 3, coil: true}},
		{"writeCoil:4:false", operation{op: writeCoil, addr: 4}},
		{"wr:uint16:1:0xffff", operation{op: writeUint16, addr: 1, value: uint16(0xffff)}},
		{"wr:int16:1:-32768", operation{op: writeInt16, addr: 1, value: int16(math.MinInt16)}},
		{"wr:uint32:1:4294967295", operation{op: writeUint32, addr: 1, value: uint32(math.MaxUint32)}},
		{"wr:int32:1:-2147483648", operation{op: writeInt32, addr: 1, value: int32(math.MinInt32)}},
		{"wr:float32:1:1.5", operation{op: writeFloat32, addr: 1, value: float32(1.5)}},
		{"wr:uint64:1:18446744073709551615", operation{op: writeUint64, addr: 1, value: uint64(math.MaxUint64)}},
		{"wr:int64:1:-9223372036854775808", operation{op: writeInt64, addr: 1, value: int64(math.MinInt64)}},
		{"wr:float64:0xfffc:-2.25", operation{op: writeFloat64, addr: 0xfffc, value: float64(-2.25)}},
		{"wr:bytes:2:0102ab", operation{op: writeBytes, addr: 2, bytes: []byte{1, 2, 0xab}}},
		{"wr:bytes:2:0xCAFE", operation{op: writeBytes, addr: 2, bytes: []byte{0xca, 0xfe}}},
		{"wr:string:0xffff:ab", operation{op: writeBytes, addr: 0xffff, bytes: []byte("ab")}},
		{"wr:string:0:a:b:c", operation{op: writeBytes, bytes: []byte("a:b:c")}},
		{"writeRegister:uint16:0:7", operation{op: writeUint16, value: uint16(7)}},
		{"sleep:1s", operation{op: sleep, duration: time.Second}},
		{"suid:0xff", operation{op: setUnitID, unitID: 0xff}},
		{"sid:0", operation{op: setUnitID}},
		{"setUnitId:7", operation{op: setUnitID, unitID: 7}},
		{"date", operation{op: date}},
		{"scan:c", operation{op: scanBools, isCoil: true}},
		{"scan:discreteInputs", operation{op: scanBools}},
		{"scan:hr", operation{op: scanRegisters, isHoldingReg: true}},
		{"scan:inputRegisters", operation{op: scanRegisters}},
		{"scan:s", operation{op: scanUnitID}},
		{"ping:3", operation{op: ping, count: 3}},
		{"ping:65535:10ms", operation{op: ping, count: 65535, duration: 10 * time.Millisecond}},
	}

	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			got, err := parseOperations([]string{tt.arg})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d operations, want 1", len(got))
			}
			if !reflect.DeepEqual(got[0], tt.want) {
				t.Fatalf("got %+v, want %+v", got[0], tt.want)
			}
		})
	}
}

func TestParseOperations_Errors(t *testing.T) {
	tests := []struct {
		arg     string
		wantErr string
	}{
		{"", "illegal command format"},
		{"rc", "illegal command format"},
		{"bogus:1", "unsupported command \"bogus\""},
		{"rc:", "failed to parse address"},
		{"rc:1:2", "need exactly 1 argument"},
		{"rc:-1", "failed to parse address"},
		{"rc:65536", "failed to parse address"},
		{"rc:1+", "failed to parse address"},
		{"rc:+1", "failed to parse address"},
		{"rc:1+2+3", "illegal format"},
		// +65535 used to wrap the quantity to 0
		{"rc:0+65535", "quantity 65536 exceeds the limit of 2000"},
		{"rdi:0+2000", "quantity 2001 exceeds the limit of 2000"},
		{"rc:0xffff+1", "runs past the end of the address space"},
		{"rc:65000+1000", "runs past the end of the address space"},
		{"rh:uint16", "need exactly 2 arguments"},
		{"rh:uint8:0", "unknown register type \"uint8\""},
		{"rh:uint16:0+125", "quantity 126 exceeds the limit of 125"},
		{"rh:uint16:0xffff+1", "runs past the end of the address space"},
		{"rh:uint32:0+62", "quantity 63 exceeds the limit of 62"},
		{"rh:float32:0xffff", "runs past the end of the address space"},
		// 16385 * 4 registers used to wrap to 4 and read a single value
		{"rh:uint64:0+16384", "quantity 16385 exceeds the limit of 31"},
		{"rh:float64:0xfffd", "runs past the end of the address space"},
		{"rh:bytes:0+250", "quantity 251 exceeds the limit of 250 bytes"},
		{"rh:bytes:0xffff+2", "runs past the end of the address space"},
		{"wc:1", "need exactly 2 arguments"},
		{"wc:x:true", "failed to parse address"},
		{"wc:1:1", "failed to parse coil value"},
		{"wr:uint16:1", "need exactly 3 arguments"},
		{"wr:uint16:x:1", "failed to parse address"},
		{"wr:uint8:1:1", "unknown register type \"uint8\""},
		{"wr:uint16:1:65536", "failed to parse \"65536\" as uint16"},
		{"wr:uint16:1:-1", "failed to parse \"-1\" as uint16"},
		{"wr:int16:1:32768", "as int16"},
		{"wr:int16:1:0xffff", "as int16"},
		{"wr:uint32:1:4294967296", "as uint32"},
		{"wr:int32:1:2147483648", "as int32"},
		{"wr:float32:1:1e39", "as float32"},
		{"wr:float32:1:abc", "as float32"},
		{"wr:uint64:1:-1", "as uint64"},
		{"wr:int64:1:9223372036854775808", "as int64"},
		{"wr:float64:1:1e400", "as float64"},
		{"wr:bytes:1:0g", "as bytes"},
		{"wr:bytes:1:123", "as bytes"},
		{"wr:bytes:1:", "nothing to write: empty bytes value"},
		{"wr:bytes:1:0x", "nothing to write"},
		{"wr:string:1:", "nothing to write: empty string value"},
		{"wr:string:0:" + strings.Repeat("a", 247), "247 bytes long, at most 246"},
		{"wr:uint32:0xffff:1", "runs past the end of the address space"},
		{"wr:int64:0xfffd:1", "runs past the end of the address space"},
		{"wr:float64:0xfffd:1", "runs past the end of the address space"},
		{"wr:string:0xffff:abc", "runs past the end of the address space"},
		{"sleep", "illegal command format"},
		{"sleep:1s:2", "need exactly 1 argument"},
		{"sleep:abc", "as duration"},
		{"sleep:-1s", "duration must not be negative"},
		{"suid:1:2", "need exactly 1 argument"},
		{"suid:256", "as unit id"},
		{"suid:-1", "as unit id"},
		{"repeat:1", "repeat takes no arguments"},
		{"date:1", "date takes no arguments"},
		{"scan", "illegal command format"},
		{"scan:c:1", "need exactly 1 argument"},
		{"scan:x", "unknown scan/register type"},
		{"ping", "illegal command format"},
		{"ping:1:2:3", "need 1 or 2 arguments"},
		{"ping:x", "failed to parse ping count"},
		{"ping:0", "illegal ping count"},
		{"ping:65536", "failed to parse ping count"},
		{"ping:1:x", "as duration"},
		{"ping:1:-5ms", "duration must not be negative"},
		// control characters in the input are quoted, not echoed raw
		{"bogus\x1b[2J:1", `unsupported command "bogus\x1b[2J"`},
	}

	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			_, err := parseOperations([]string{tt.arg})
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseOperations_Repeat(t *testing.T) {
	ok := [][]string{
		{"date", "repeat"},
		{"suid:1", "rc:0", "repeat"},
		{"sleep:1s", "repeat", "repeat"},
	}
	for _, args := range ok {
		if _, err := parseOperations(args); err != nil {
			t.Errorf("%v: unexpected error: %v", args, err)
		}
	}

	// a repeat with nothing to do before it would spin the CPU forever
	bad := [][]string{
		{"repeat"},
		{"suid:1", "repeat"},
		{"repeat", "date"},
	}
	for _, args := range bad {
		_, err := parseOperations(args)
		if err == nil || !strings.Contains(err.Error(), "repeat needs at least one operation") {
			t.Errorf("%v: got %v, want a repeat error", args, err)
		}
	}
}

func TestParseOperations_Multiple(t *testing.T) {
	got, err := parseOperations([]string{"suid:2", "rh:uint16:0+1", "wc:1:true"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].op != setUnitID || got[1].op != readUint16 || got[2].op != writeCoil {
		t.Fatalf("unexpected run list %+v", got)
	}

	if got, err := parseOperations(nil); err != nil || got != nil {
		t.Fatalf("empty args: got %v, %v", got, err)
	}
}

func TestParseEndiannessAndWordOrder(t *testing.T) {
	for in, want := range map[string]modbus.Endianness{"big": modbus.BigEndian, "little": modbus.LittleEndian} {
		got, err := parseEndianness(in)
		if err != nil || got != want {
			t.Errorf("parseEndianness(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := parseEndianness("middle"); err == nil {
		t.Error("parseEndianness(middle): expected an error")
	}

	for in, want := range map[string]modbus.WordOrder{
		"highfirst": modbus.HighWordFirst, "hf": modbus.HighWordFirst,
		"lowfirst": modbus.LowWordFirst, "lf": modbus.LowWordFirst,
	} {
		got, err := parseWordOrder(in)
		if err != nil || got != want {
			t.Errorf("parseWordOrder(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := parseWordOrder("mf"); err == nil {
		t.Error("parseWordOrder(mf): expected an error")
	}
}

func TestRegistersPerValue(t *testing.T) {
	for op, want := range map[operationKind]uint16{
		readUint16: 1, writeInt16: 1, readFloat32: 2, writeUint32: 2,
		readInt64: 4, writeFloat64: 4, readBools: 0, readBytes: 0,
	} {
		if got := registersPerValue(op); got != want {
			t.Errorf("registersPerValue(%v) = %d, want %d", op, got, want)
		}
	}
}

func TestCheckRange(t *testing.T) {
	if err := checkRange(0, 0, 10, "x"); err == nil || !strings.Contains(err.Error(), "at least 1") {
		t.Errorf("quantity 0: got %v", err)
	}
	if err := checkRange(0xfff0, 16, 125, "x"); err != nil {
		t.Errorf("last register: %v", err)
	}
}

func TestDecodeString(t *testing.T) {
	in := []byte("ok\x1b[31m\x00\x7f\xff~ ")
	if got, want := decodeString(in), "ok.[31m...~ "; got != want {
		t.Fatalf("decodeString = %q, want %q", got, want)
	}
}
