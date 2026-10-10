// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"math"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/codec"
)

// e2eCodecRoundTrip writes v through the client with c, asserts the registers
// the server received and stored, then reads the value back from the holding
// registers and from a copy placed in the input registers.
func e2eCodecRoundTrip[T any](t *testing.T, p *e2ePair, dev *e2eDevice, name string, c codec.Codec[T], v T, want []uint16) {
	t.Helper()
	ctx := context.Background()
	const addr = 5000
	n := len(want)

	// Guard registers around the window must survive the write.
	dev.setHolding(addr-1, make([]uint16, n+2)...)
	dev.setHolding(addr-1, 0xA55A)
	dev.setHolding(addr+n, 0xA55A)
	dev.takeCalls()

	if err := codec.WriteToClient(p.client, ctx, e2eUnit, addr, v, c); err != nil {
		t.Errorf("%s: WriteToClient: %v", name, err)
		return
	}
	call := e2eOneCall(t, name+" write", dev)
	if call.FC != FCWriteMultipleRegisters || call.Addr != addr || !reflect.DeepEqual(call.Words, want) {
		t.Errorf("%s: server received fc=0x%02X addr=%d regs=%04X, want FC16 regs=%04X",
			name, uint8(call.FC), call.Addr, call.Words, want)
	}
	if got := dev.holdingAt(addr, n); !reflect.DeepEqual(got, want) {
		t.Errorf("%s: server stored %04X, want %04X", name, got, want)
	}
	if dev.holdingAt(addr-1, 1)[0] != 0xA55A || dev.holdingAt(addr+n, 1)[0] != 0xA55A {
		t.Errorf("%s: write touched neighbouring registers", name)
	}

	dev.setInput(addr, want...)
	for _, rt := range []RegType{HoldingRegister, InputRegister} {
		got, err := codec.ReadFromClient(p.client, ctx, e2eUnit, addr, rt, c)
		if err != nil {
			t.Errorf("%s: ReadFromClient(%v): %v", name, rt, err)
			continue
		}
		if !e2eValuesEqual(got, v) {
			t.Errorf("%s: ReadFromClient(%v) = %v, want %v", name, rt, got, v)
		}
		call := e2eOneCall(t, name+" read", dev)
		wantFC := FCReadHoldingRegisters
		if rt == InputRegister {
			wantFC = FCReadInputRegisters
		}
		if call.FC != wantFC || call.Addr != addr || int(call.Quantity) != n {
			t.Errorf("%s: read reached the server as fc=0x%02X addr=%d qty=%d", name, uint8(call.FC), call.Addr, call.Quantity)
		}
	}
}

// e2eValuesEqual compares decoded values, treating equal instants and equal IP
// addresses in different representations as equal.
func e2eValuesEqual(a, b any) bool {
	switch av := a.(type) {
	case time.Time:
		bv, ok := b.(time.Time)
		return ok && av.Equal(bv)
	case net.IP:
		bv, ok := b.(net.IP)
		return ok && av.Equal(bv)
	}
	return reflect.DeepEqual(a, b)
}

func e2eMust[T any](t *testing.T) func(c codec.Codec[T], err error) codec.Codec[T] {
	return func(c codec.Codec[T], err error) codec.Codec[T] {
		t.Helper()
		if err != nil {
			t.Fatalf("codec constructor: %v", err)
		}
		return c
	}
}

// TestE2E_Codec_TypedRoundTrip sends typed values through the real server with
// the typed codec helpers and checks the exact register image the server stored.
func TestE2E_Codec_TypedRoundTrip(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})

		str := e2eMust[string](t)
		mac48, _ := net.ParseMAC("00:11:22:33:44:55")
		mac64, _ := net.ParseMAC("00:11:22:33:44:55:66:77")
		when := time.Date(2024, 2, 29, 12, 34, 56, 0, time.UTC)

		// 16-bit.
		e2eCodecRoundTrip(t, p, dev, "uint16/21", codec.MustNewUint16Codec(codec.Layout16_21), uint16(0x1234), []uint16{0x1234})
		e2eCodecRoundTrip(t, p, dev, "uint16/12", codec.MustNewUint16Codec(codec.Layout16_12), uint16(0x1234), []uint16{0x3412})
		e2eCodecRoundTrip(t, p, dev, "int16/21", codec.MustNewInt16Codec(codec.Layout16_21), int16(-2), []uint16{0xFFFE})
		e2eCodecRoundTrip(t, p, dev, "int16/12", codec.MustNewInt16Codec(codec.Layout16_12), int16(-2), []uint16{0xFEFF})
		e2eCodecRoundTrip(t, p, dev, "int16 sign-magnitude", codec.NewInt16SignMagnitudeCodec(), int16(-5), []uint16{0x8005})

		// 32-bit, every layout family.
		e2eCodecRoundTrip(t, p, dev, "uint32/4321", codec.MustNewUint32Codec(codec.Layout32_4321), uint32(0x11223344), []uint16{0x1122, 0x3344})
		e2eCodecRoundTrip(t, p, dev, "uint32/3412", codec.MustNewUint32Codec(codec.Layout32_3412), uint32(0x11223344), []uint16{0x2211, 0x4433})
		e2eCodecRoundTrip(t, p, dev, "uint32/2143", codec.MustNewUint32Codec(codec.Layout32_2143), uint32(0x11223344), []uint16{0x3344, 0x1122})
		e2eCodecRoundTrip(t, p, dev, "uint32/1234", codec.MustNewUint32Codec(codec.Layout32_1234), uint32(0x11223344), []uint16{0x4433, 0x2211})
		e2eCodecRoundTrip(t, p, dev, "int32/4321", codec.MustNewInt32Codec(codec.Layout32_4321), int32(-123456789), []uint16{0xF8A4, 0x32EB})
		e2eCodecRoundTrip(t, p, dev, "int32/2143", codec.MustNewInt32Codec(codec.Layout32_2143), int32(-123456789), []uint16{0x32EB, 0xF8A4})
		e2eCodecRoundTrip(t, p, dev, "int32 min", codec.MustNewInt32Codec(codec.Layout32_4321), int32(math.MinInt32), []uint16{0x8000, 0x0000})
		e2eCodecRoundTrip(t, p, dev, "float32/4321", codec.MustNewFloat32Codec(codec.Layout32_4321), float32(3.14), []uint16{0x4048, 0xF5C3})
		e2eCodecRoundTrip(t, p, dev, "float32/3412", codec.MustNewFloat32Codec(codec.Layout32_3412), float32(3.14), []uint16{0x4840, 0xC3F5})
		e2eCodecRoundTrip(t, p, dev, "float32/2143", codec.MustNewFloat32Codec(codec.Layout32_2143), float32(3.14), []uint16{0xF5C3, 0x4048})
		e2eCodecRoundTrip(t, p, dev, "float32/1234", codec.MustNewFloat32Codec(codec.Layout32_1234), float32(3.14), []uint16{0xC3F5, 0x4840})
		e2eCodecRoundTrip(t, p, dev, "float32 -inf", codec.MustNewFloat32Codec(codec.Layout32_4321), float32(math.Inf(-1)), []uint16{0xFF80, 0x0000})

		// 48-bit.
		e2eCodecRoundTrip(t, p, dev, "uint48/654321", codec.MustNewUint48Codec(codec.Layout48_654321), uint64(0x112233445566), []uint16{0x1122, 0x3344, 0x5566})
		e2eCodecRoundTrip(t, p, dev, "uint48/563412", codec.MustNewUint48Codec(codec.Layout48_563412), uint64(0x112233445566), []uint16{0x2211, 0x4433, 0x6655})
		e2eCodecRoundTrip(t, p, dev, "uint48/214365", codec.MustNewUint48Codec(codec.Layout48_214365), uint64(0x112233445566), []uint16{0x5566, 0x3344, 0x1122})
		e2eCodecRoundTrip(t, p, dev, "uint48/123456", codec.MustNewUint48Codec(codec.Layout48_123456), uint64(0x112233445566), []uint16{0x6655, 0x4433, 0x2211})
		e2eCodecRoundTrip(t, p, dev, "int48/654321", codec.MustNewInt48Codec(codec.Layout48_654321), int64(-2), []uint16{0xFFFF, 0xFFFF, 0xFFFE})
		e2eCodecRoundTrip(t, p, dev, "int48/214365", codec.MustNewInt48Codec(codec.Layout48_214365), int64(-2), []uint16{0xFFFE, 0xFFFF, 0xFFFF})

		// 64-bit.
		e2eCodecRoundTrip(t, p, dev, "uint64/87654321", codec.MustNewUint64Codec(codec.Layout64_87654321), uint64(0x1122334455667788), []uint16{0x1122, 0x3344, 0x5566, 0x7788})
		e2eCodecRoundTrip(t, p, dev, "uint64/78563412", codec.MustNewUint64Codec(codec.Layout64_78563412), uint64(0x1122334455667788), []uint16{0x2211, 0x4433, 0x6655, 0x8877})
		e2eCodecRoundTrip(t, p, dev, "uint64/21436587", codec.MustNewUint64Codec(codec.Layout64_21436587), uint64(0x1122334455667788), []uint16{0x7788, 0x5566, 0x3344, 0x1122})
		e2eCodecRoundTrip(t, p, dev, "uint64/12345678", codec.MustNewUint64Codec(codec.Layout64_12345678), uint64(0x1122334455667788), []uint16{0x8877, 0x6655, 0x4433, 0x2211})
		e2eCodecRoundTrip(t, p, dev, "int64/87654321", codec.MustNewInt64Codec(codec.Layout64_87654321), int64(-1234567890123), []uint16{0xFFFF, 0xFEE0, 0x8E04, 0xFB35})
		e2eCodecRoundTrip(t, p, dev, "int64/21436587", codec.MustNewInt64Codec(codec.Layout64_21436587), int64(-1234567890123), []uint16{0xFB35, 0x8E04, 0xFEE0, 0xFFFF})
		e2eCodecRoundTrip(t, p, dev, "float64/87654321", codec.MustNewFloat64Codec(codec.Layout64_87654321), math.Pi, []uint16{0x4009, 0x21FB, 0x5444, 0x2D18})
		e2eCodecRoundTrip(t, p, dev, "float64/78563412", codec.MustNewFloat64Codec(codec.Layout64_78563412), math.Pi, []uint16{0x0940, 0xFB21, 0x4454, 0x182D})
		e2eCodecRoundTrip(t, p, dev, "float64/21436587", codec.MustNewFloat64Codec(codec.Layout64_21436587), math.Pi, []uint16{0x2D18, 0x5444, 0x21FB, 0x4009})
		e2eCodecRoundTrip(t, p, dev, "float64/12345678", codec.MustNewFloat64Codec(codec.Layout64_12345678), math.Pi, []uint16{0x182D, 0x4454, 0xFB21, 0x0940})

		// Decimal limbs (modulo 10000).
		e2eCodecRoundTrip(t, p, dev, "uint32 m10k high-to-low", codec.MustNewUint32M10kCodec(codec.DecimalLimbOrder(2)), uint32(12345678), []uint16{0x04D2, 0x162E})
		e2eCodecRoundTrip(t, p, dev, "uint32 m10k low-to-high", codec.MustNewUint32M10kCodec(codec.DecimalLimbOrder(1)), uint32(12345678), []uint16{0x162E, 0x04D2})

		// Strings.
		e2eCodecRoundTrip(t, p, dev, "ascii", str(codec.NewAsciiCodec(3)), "Hi!", []uint16{0x4869, 0x2120, 0x2020})
		e2eCodecRoundTrip(t, p, dev, "ascii fixed", str(codec.NewAsciiFixedCodec(3)), "Hi!   ", []uint16{0x4869, 0x2120, 0x2020})
		e2eCodecRoundTrip(t, p, dev, "ascii reverse", str(codec.NewAsciiReverseCodec(3)), "Hello", []uint16{0x6548, 0x6C6C, 0x206F})
		e2eCodecRoundTrip(t, p, dev, "bcd", str(codec.NewBCDCodec(2)), "1234", []uint16{0x0102, 0x0304})
		e2eCodecRoundTrip(t, p, dev, "packed bcd", str(codec.NewPackedBCDCodec(2)), "12345678", []uint16{0x1234, 0x5678})
		e2eCodecRoundTrip(t, p, dev, "signed packed bcd", str(codec.NewSignedPackedBCDCodec(2)), "-1234567", []uint16{0x1234, 0x567C})
		e2eCodecRoundTrip(t, p, dev, "packed bcd reverse", str(codec.NewPackedBCDReverseCodec(2)), "12345678", []uint16{0x3412, 0x7856})
		e2eCodecRoundTrip(t, p, dev, "utf16be", str(codec.NewUTF16BECodec(3)), "héé", []uint16{0x0068, 0x00E9, 0x00E9})
		e2eCodecRoundTrip(t, p, dev, "utf16le", str(codec.NewUTF16LECodec(3)), "héé", []uint16{0x6800, 0xE900, 0xE900})

		// Bytes and addresses.
		e2eCodecRoundTrip(t, p, dev, "bytes", e2eMust[[]byte](t)(codec.NewBytesCodec(6)), []byte{1, 2, 3, 4, 5, 6}, []uint16{0x0102, 0x0304, 0x0506})
		e2eCodecRoundTrip(t, p, dev, "uint8 slice", e2eMust[[]uint8](t)(codec.NewUint8SliceCodec(4)), []uint8{0xDE, 0xAD, 0xBE, 0xEF}, []uint16{0xDEAD, 0xBEEF})
		e2eCodecRoundTrip(t, p, dev, "ipv4", codec.NewIPAddrCodec(), net.IPv4(192, 168, 1, 10), []uint16{0xC0A8, 0x010A})
		e2eCodecRoundTrip(t, p, dev, "ipv6", codec.NewIPv6AddrCodec(), net.ParseIP("2001:db8::1"), []uint16{0x2001, 0x0DB8, 0, 0, 0, 0, 0, 1})
		e2eCodecRoundTrip(t, p, dev, "eui48", codec.NewEUI48Codec(), mac48, []uint16{0x0011, 0x2233, 0x4455})
		e2eCodecRoundTrip(t, p, dev, "eui64", codec.NewEUI64Codec(), mac64, []uint16{0x0011, 0x2233, 0x4455, 0x6677})

		// Time.
		e2eCodecRoundTrip(t, p, dev, "datetime ymdhms", codec.NewDateTimeYMDhmsUTCCodec(), when, []uint16{0x07E8, 0x0002, 0x001D, 0x000C, 0x0022, 0x0038})
		e2eCodecRoundTrip(t, p, dev, "datetime s2000", codec.NewDateTime2S2000Codec(), when, []uint16{0x2D73, 0x3670})
		e2eCodecRoundTrip(t, p, dev, "datetime iec870", codec.NewDateTimeIEC870UTCCodec(), when, []uint16{0xC0DA, 0x220C, 0x9D02, 0x1800})

		// The uint32 convenience wrappers.
		ctx := context.Background()
		if err := codec.WriteUint32ToClient(p.client, ctx, e2eUnit, 6000, 0xDEADBEEF, codec.Layout32_2143); err != nil {
			t.Fatalf("WriteUint32ToClient: %v", err)
		}
		if got := dev.holdingAt(6000, 2); !reflect.DeepEqual(got, []uint16{0xBEEF, 0xDEAD}) {
			t.Errorf("WriteUint32ToClient stored %04X", got)
		}
		dev.setInput(6000, 0xBEEF, 0xDEAD)
		for _, rt := range []RegType{HoldingRegister, InputRegister} {
			if got, err := codec.ReadUint32FromClient(p.client, ctx, e2eUnit, 6000, rt, codec.Layout32_2143); err != nil || got != 0xDEADBEEF {
				t.Errorf("ReadUint32FromClient(%v) = 0x%08X, %v", rt, got, err)
			}
		}
	})
}

// e2eSampleFor returns a value the codec described by desc can encode, together
// with its register image, by decoding a few candidate register patterns.
func e2eSampleFor(desc codec.CodecDescriptor) (value any, regs []uint16, ok bool) {
	n := int(desc.RegisterSpec.Count)
	patterns := make([][]uint16, 0, 5)
	mixed := make([]uint16, n)
	digits := make([]uint16, n)
	ascii := make([]uint16, n)
	for i := range mixed {
		mixed[i] = 0x1234 + uint16(i)*0x1111
		digits[i] = uint16(i%10)<<8 | uint16((i+1)%10)
		ascii[i] = uint16('A'+i%26)<<8 | uint16('a'+i%26)
	}
	signed := append([]uint16(nil), digits...)
	if n > 0 {
		signed[n-1] = signed[n-1]&0xFFF0 | 0x000C
	}
	patterns = append(patterns, mixed, ascii, digits, signed, make([]uint16, n))
	for _, pat := range patterns {
		v, err := codec.DecodeWithDescriptor(pat, desc)
		if err != nil {
			continue
		}
		enc, err := codec.EncodeWithDescriptor(v, desc)
		if err != nil || len(enc) != n {
			continue
		}
		return v, enc, true
	}
	// Time codecs reject most arbitrary register images.
	when := time.Date(2024, 2, 29, 12, 34, 56, 0, time.UTC)
	if enc, err := codec.EncodeWithDescriptor(when, desc); err == nil && len(enc) == n {
		return when, enc, true
	}
	return nil, nil, false
}

// TestE2E_Codec_EveryDescriptor round-trips one value per registered codec
// descriptor (every codec, every layout) through the real server with the
// runtime codec helpers.
func TestE2E_Codec_EveryDescriptor(t *testing.T) {
	descs := codec.AvailableCodecDescriptors()
	if len(descs) < 100 {
		t.Fatalf("only %d codec descriptors registered", len(descs))
	}
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		ctx := context.Background()

		for i, desc := range descs {
			value, want, ok := e2eSampleFor(desc)
			if !ok {
				t.Errorf("%s: no sample value found", desc.ID)
				continue
			}
			rc, err := codec.RuntimeCodecFromDescriptor(desc)
			if err != nil {
				t.Errorf("%s: RuntimeCodecFromDescriptor: %v", desc.ID, err)
				continue
			}
			addr := uint16(100 + i*20)
			if err := codec.WriteRuntimeToClient(p.client, ctx, e2eUnit, addr, value, rc); err != nil {
				t.Errorf("%s: WriteRuntimeToClient(%v): %v", desc.ID, value, err)
				continue
			}
			if got := dev.holdingAt(int(addr), len(want)); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: server stored %04X, want %04X", desc.ID, got, want)
				continue
			}
			dev.setInput(int(addr), want...)
			for _, rt := range []RegType{HoldingRegister, InputRegister} {
				got, err := codec.ReadRuntimeFromClient(p.client, ctx, e2eUnit, addr, rt, rc)
				if err != nil {
					t.Errorf("%s: ReadRuntimeFromClient(%v): %v", desc.ID, rt, err)
					continue
				}
				// Comparing register images sidesteps NaN and time zone
				// representation differences.
				back, err := codec.EncodeWithDescriptor(got, desc)
				if err != nil || !reflect.DeepEqual(back, want) {
					t.Errorf("%s: read back %v (regs %04X, err %v), want %v (regs %04X)", desc.ID, got, back, err, value, want)
				}
			}
		}
		if calls := dev.takeCalls(); len(calls) != 3*len(descs) {
			t.Errorf("server received %d requests, want %d", len(calls), 3*len(descs))
		}
	})
}

// Codec errors never reach the wire, and server exceptions surface unchanged
// through the codec helpers.
func TestE2E_Codec_Errors(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		ctx := context.Background()
		u32 := codec.MustNewUint32Codec(codec.Layout32_4321)
		ascii, err := codec.NewAsciiCodec(2)
		if err != nil {
			t.Fatal(err)
		}
		rc, err := codec.RuntimeCodecFromDescriptor(codec.AvailableCodecDescriptors()[0])
		if err != nil {
			t.Fatal(err)
		}

		var valueErr *codec.CodecValueError
		if err := codec.WriteToClient[uint32](p.client, ctx, e2eUnit, 0, 1, nil); !errors.As(err, &valueErr) {
			t.Errorf("WriteToClient(nil codec): %v", err)
		}
		if _, err := codec.ReadFromClient[uint32](p.client, ctx, e2eUnit, 0, HoldingRegister, nil); !errors.As(err, &valueErr) {
			t.Errorf("ReadFromClient(nil codec): %v", err)
		}
		if err := codec.WriteToClient(p.client, ctx, e2eUnit, 0, "h\u00e9", ascii); err == nil {
			t.Error("WriteToClient(non-ASCII string) succeeded")
		}
		if err := codec.WriteRuntimeToClient(p.client, ctx, e2eUnit, 0, struct{}{}, rc); !errors.As(err, &valueErr) {
			t.Errorf("WriteRuntimeToClient(wrong type): %v", err)
		}
		if err := codec.WriteUint32ToClient(p.client, ctx, e2eUnit, 0, 1, codec.Layout16_21); err == nil {
			t.Error("WriteUint32ToClient(16-bit layout) succeeded")
		}
		if _, err := codec.ReadUint32FromClient(p.client, ctx, e2eUnit, 0, HoldingRegister, codec.Layout64_12345678); err == nil {
			t.Error("ReadUint32FromClient(64-bit layout) succeeded")
		}
		if n := dev.callCount(); n != 0 {
			t.Errorf("codec errors reached the server: %d requests", n)
		}

		dev.setHook(func(_ context.Context, c e2eCall) error {
			if c.IsWrite {
				return ErrIllegalDataValue
			}
			return ErrIllegalDataAddress
		})
		err = codec.WriteToClient(p.client, ctx, e2eUnit, 10, uint32(7), u32)
		e2eWantException(t, "WriteToClient", err, FCWriteMultipleRegisters, exIllegalDataValue)
		_, err = codec.ReadFromClient(p.client, ctx, e2eUnit, 10, HoldingRegister, u32)
		e2eWantException(t, "ReadFromClient(holding)", err, FCReadHoldingRegisters, exIllegalDataAddress)
		_, err = codec.ReadFromClient(p.client, ctx, e2eUnit, 10, InputRegister, u32)
		e2eWantException(t, "ReadFromClient(input)", err, FCReadInputRegisters, exIllegalDataAddress)
		_, err = codec.ReadRuntimeFromClient(p.client, ctx, e2eUnit, 10, InputRegister, rc)
		e2eWantException(t, "ReadRuntimeFromClient", err, FCReadInputRegisters, exIllegalDataAddress)

		// The codec end of the address range: a 4-register value at 65532.
		dev.setHook(nil)
		u64 := codec.MustNewUint64Codec(codec.Layout64_87654321)
		if err := codec.WriteToClient(p.client, ctx, e2eUnit, 0xFFFC, uint64(0x0102030405060708), u64); err != nil {
			t.Fatalf("WriteToClient at 65532: %v", err)
		}
		if got := dev.holdingAt(0xFFFC, 4); !reflect.DeepEqual(got, []uint16{0x0102, 0x0304, 0x0506, 0x0708}) {
			t.Errorf("stored %04X at the end of the address range", got)
		}
		dev.takeCalls()
		err = codec.WriteToClient(p.client, ctx, e2eUnit, 0xFFFD, uint64(1), u64)
		e2eWantParamError(t, "WriteToClient past 65535", err, dev)
		_, err = codec.ReadFromClient(p.client, ctx, e2eUnit, 0xFFFD, HoldingRegister, u64)
		e2eWantParamError(t, "ReadFromClient past 65535", err, dev)
	})
}
