// SPDX-License-Identifier: MIT

package codec

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
)

// zeroSpecCodec declares a zero-register shape, which the client helpers must refuse to read.
type zeroSpecCodec struct{}

func (zeroSpecCodec) ID() string                               { return "zero/spec" }
func (zeroSpecCodec) Name() string                             { return "zero" }
func (zeroSpecCodec) RegisterSpec() RegisterSpec               { return RegisterSpec{} }
func (zeroSpecCodec) ByteSpec() ByteSpec                       { return ByteSpec{} }
func (zeroSpecCodec) DecodeRegisters([]uint16) (uint16, error) { return 0, nil }
func (zeroSpecCodec) EncodeRegisters(uint16) ([]uint16, error) { return nil, nil }

// withRegistry replaces the descriptor registry for the duration of the test.
func withRegistry(t *testing.T, descs ...CodecDescriptor) {
	t.Helper()
	savedDesc, savedIDs := registeredDescriptors, registeredIDs
	t.Cleanup(func() { registeredDescriptors, registeredIDs = savedDesc, savedIDs })
	registeredDescriptors = nil
	registeredIDs = make(map[string]struct{})
	for _, d := range descs {
		registerCodecDescriptor(d)
	}
}

//
// Generic and runtime helpers
//

func TestDecodeEncodeRegisters_NilCodec(t *testing.T) {
	_, err := DecodeRegisters[uint16]([]uint16{1}, nil)
	assertValueError(t, err, "codec")
	_, err = EncodeRegisters[uint16](1, nil)
	assertValueError(t, err, "codec")
	_, err = DecodeRegistersAny([]uint16{1}, nil)
	assertValueError(t, err, "codec")
	_, err = EncodeRegistersAny(uint16(1), nil)
	assertValueError(t, err, "codec")
}

func TestDecodeRegistersAny_WrongRegisterCount(t *testing.T) {
	rc := MustRuntimeCodecByID("uint32/layout:4321")
	for _, regs := range [][]uint16{nil, {1}, {1, 2, 3}} {
		v, err := DecodeRegistersAny(regs, rc)
		var ce *CodecRegisterCountError
		if !errors.As(err, &ce) {
			t.Fatalf("len %d: want *CodecRegisterCountError, got %v", len(regs), err)
		}
		if v != nil || ce.Codec != "uint32/layout:4321" || ce.Expected.Count != 2 || int(ce.Actual) != len(regs) {
			t.Errorf("len %d: v=%v err=%+v", len(regs), v, ce)
		}
	}
}

func TestEncodeRegistersAny_PropagatesAndValidates(t *testing.T) {
	// Codec-level rejection of the value.
	sm := AsRuntimeCodec(NewInt16SignMagnitudeCodec(), CodecValueInt16)
	regs, err := EncodeRegistersAny(int16(-32768), sm)
	if regs != nil {
		t.Errorf("got registers %v alongside error", regs)
	}
	assertValueError(t, err, "int16_sign_magnitude")

	// Encoder that returns the wrong number of registers is caught after encoding.
	bad := AsRuntimeEncoder[uint16](badEncoderCodec{}, CodecValueUint16)
	regs, err = EncodeRegistersAny(uint16(1), bad)
	var ce *CodecRegisterCountError
	if !errors.As(err, &ce) || regs != nil {
		t.Fatalf("want *CodecRegisterCountError and nil registers, got %v, %v", regs, err)
	}
	if ce.Codec != "bad/encoder" || ce.Expected.Count != 1 || ce.Actual != 2 {
		t.Errorf("got %+v", ce)
	}
}

func TestRuntimeAdapters_DelegateMetadataAndErrors(t *testing.T) {
	inner := MustNewUint32Codec(Layout32_2143)

	dec := AsRuntimeDecoder[uint32](inner, CodecValueUint32)
	assertMeta(t, dec, "uint32/layout:2143", "uint32", 2)
	if dec.ValueKind() != CodecValueUint32 {
		t.Errorf("decoder ValueKind = %v", dec.ValueKind())
	}
	v, err := dec.DecodeRegistersAny([]uint16{0x0304, 0x0102})
	if err != nil || v != uint32(0x01020304) {
		t.Errorf("decoder: got %v (%T), %v; want uint32 0x01020304", v, v, err)
	}
	if v, err := dec.DecodeRegistersAny([]uint16{1}); v != nil || !errors.Is(err, ErrCodecRegisterCount) {
		t.Errorf("decoder wrong count: got %v, %v", v, err)
	}

	enc := AsRuntimeEncoder[uint32](inner, CodecValueUint32)
	assertMeta(t, enc, "uint32/layout:2143", "uint32", 2)
	if enc.ValueKind() != CodecValueUint32 {
		t.Errorf("encoder ValueKind = %v", enc.ValueKind())
	}
	regs, err := enc.EncodeRegistersAny(uint32(0x01020304))
	if err != nil || !slices.Equal(regs, []uint16{0x0304, 0x0102}) {
		t.Errorf("encoder: got %04X, %v", regs, err)
	}

	rc := AsRuntimeCodec(inner, CodecValueUint32)
	if v, err := rc.DecodeRegistersAny(nil); v != nil || !errors.Is(err, ErrCodecRegisterCount) {
		t.Errorf("codec wrong count: got %v, %v", v, err)
	}
}

// Every registered codec must reject a value of the wrong dynamic type with a
// *CodecValueError naming the codec, never panic or encode garbage.
func TestRuntimeCodecs_WrongDynamicTypeRejected(t *testing.T) {
	type unrelated struct{ X int }
	for _, d := range AvailableCodecDescriptors() {
		rc, err := RuntimeCodecFromDescriptor(d)
		if err != nil {
			t.Fatalf("%s: %v", d.ID, err)
		}
		for _, v := range []any{unrelated{1}, nil, complex64(1)} {
			regs, err := EncodeRegistersAny(v, rc)
			if regs != nil {
				t.Errorf("%s: value %T produced registers %v", d.ID, v, regs)
			}
			reason := valueErrorReason(t, err, d.ID)
			if !strings.HasPrefix(reason, "wrong value type") {
				t.Errorf("%s: value %T: Reason = %q", d.ID, v, reason)
			}
		}
	}
}

// Width-compatible but distinct Go types are not interchangeable in the runtime API.
func TestRuntimeCodecs_SameWidthDifferentTypeRejected(t *testing.T) {
	cases := []struct {
		id    string
		value any
	}{
		{"uint16/layout:21", int16(1)},
		{"int16/layout:21", uint16(1)},
		{"uint32/layout:4321", int32(1)},
		{"uint32/layout:4321", 1}, // untyped constant becomes int
		{"float32/layout:4321", float64(1)},
		{"uint48/layout:654321", int64(1)},
		{"int64/layout:87654321", uint64(1)},
		{"uint32_m10k/order:low_to_high", uint64(1)},
		{"ascii/registers:2", []byte("AB")},
		{"bytes/bytes:4", "ABCD"},
		{"ip_addr", "10.0.0.1"},
		{"eui48", []byte{1, 2, 3, 4, 5, 6}},
		{"datetime2_s2000", int64(0)},
	}
	for _, tc := range cases {
		_, err := EncodeRegistersAny(tc.value, MustRuntimeCodecByID(tc.id))
		assertValueError(t, err, tc.id)
	}
}

//
// Runtime registry
//

func TestRuntimeCodecFromDescriptor_Errors(t *testing.T) {
	one := []RegisterLayoutDescriptor{{Name: "21", Layout: Layout16_21}}
	two := []RegisterLayoutDescriptor{{Name: "4321", Layout: Layout32_4321}}

	t.Run("numeric layout width mismatch", func(t *testing.T) {
		for name, layouts := range map[string][]RegisterLayoutDescriptor{
			"uint16": two, "int16": two,
			"uint32": one, "int32": one, "float32": one,
			"uint48": one, "int48": one,
			"uint64": one, "int64": one, "float64": one,
		} {
			rc, err := RuntimeCodecFromDescriptor(CodecDescriptor{ID: name + "/layout:" + layouts[0].Name, Layouts: layouts})
			var le *CodecLayoutError
			if rc != nil || !errors.As(err, &le) {
				t.Errorf("%s: want *CodecLayoutError, got %v, %v", name, rc, err)
				continue
			}
			if le.Codec != name || le.Layout != layouts[0].Layout {
				t.Errorf("%s: got {Codec:%q Layout:%s}", name, le.Codec, le.Layout)
			}
		}
	})

	t.Run("numeric without layout", func(t *testing.T) {
		rc, err := RuntimeCodecFromDescriptor(CodecDescriptor{ID: "uint32/layout:4321"})
		if rc != nil || !errors.Is(err, ErrCodecLayout) {
			t.Errorf("want ErrCodecLayout, got %v, %v", rc, err)
		}
	})

	t.Run("unknown numeric family", func(t *testing.T) {
		rc, err := RuntimeCodecFromDescriptor(CodecDescriptor{ID: "uint24/layout:21", Layouts: one})
		if rc != nil || !errors.Is(err, ErrUnknownCodec) {
			t.Errorf("want ErrUnknownCodec, got %v, %v", rc, err)
		}
	})

	t.Run("m10k unknown order", func(t *testing.T) {
		for _, name := range []string{"uint32_m10k", "uint48_m10k", "uint64_m10k", "int32_m10k", "int48_m10k", "int64_m10k"} {
			rc, err := RuntimeCodecFromDescriptor(CodecDescriptor{ID: name + "/order:sideways"})
			if rc != nil || !errors.Is(err, ErrUnknownCodec) {
				t.Errorf("%s: want ErrUnknownCodec, got %v, %v", name, rc, err)
			}
		}
	})

	t.Run("text with zero registers", func(t *testing.T) {
		for _, tc := range textCtors() {
			rc, err := RuntimeCodecFromDescriptor(CodecDescriptor{ID: tc.name + "/registers:0"})
			if rc != nil || !errors.Is(err, ErrCodecValue) {
				t.Errorf("%s: want ErrCodecValue, got %v, %v", tc.name, rc, err)
			}
		}
	})

	t.Run("bytes with invalid byte count", func(t *testing.T) {
		for _, d := range []CodecDescriptor{
			{ID: "bytes/bytes:3", ByteSpec: ByteSpec{Count: 3}},
			{ID: "bytes/bytes:0"},
			{ID: "uint8_slice/bytes:5", ByteSpec: ByteSpec{Count: 5}},
			{ID: "uint8_slice/bytes:0"},
		} {
			rc, err := RuntimeCodecFromDescriptor(d)
			if rc != nil || !errors.Is(err, ErrCodecValue) {
				t.Errorf("%s: want ErrCodecValue, got %v, %v", d.ID, rc, err)
			}
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		for _, id := range []string{"", "nope", "ip_addr ", "datetime", "ascii"} {
			rc, err := RuntimeCodecFromDescriptor(CodecDescriptor{ID: id})
			if rc != nil || !errors.Is(err, ErrUnknownCodec) {
				t.Errorf("%q: want ErrUnknownCodec, got %v, %v", id, rc, err)
			}
		}
	})
}

// A registered descriptor that cannot be instantiated must surface as an error
// (naming the descriptor) from every runtime lookup, not be silently skipped.
func TestRuntimeRegistry_UninstantiableDescriptor(t *testing.T) {
	const id = "uint32/layout:21"
	withRegistry(t, CodecDescriptor{
		ID:           id,
		Name:         "uint32",
		Family:       CodecFamilyInteger,
		ValueKind:    CodecValueUint32,
		RegisterSpec: RegisterSpec{Count: 2},
		ByteSpec:     ByteSpec{Count: 4},
		Layouts:      []RegisterLayoutDescriptor{{Name: "21", Layout: Layout16_21}},
	})

	check := func(name string, rcs []RuntimeCodec, err error) {
		t.Helper()
		if rcs != nil || !errors.Is(err, ErrCodecLayout) {
			t.Errorf("%s: want nil, ErrCodecLayout; got %v, %v", name, rcs, err)
			return
		}
		if !strings.Contains(err.Error(), `"`+id+`"`) {
			t.Errorf("%s: error does not name the descriptor: %v", name, err)
		}
	}
	rcs, err := RuntimeCodecsForRegisterCount(2)
	check("RuntimeCodecsForRegisterCount", rcs, err)
	rcs, err = RuntimeCodecsForByteCount(4)
	check("RuntimeCodecsForByteCount", rcs, err)
	rcs, err = FindRuntimeCodecs(CodecQuery{Family: CodecFamilyInteger})
	check("FindRuntimeCodecs", rcs, err)

	// Non-matching queries are unaffected by the broken descriptor.
	if rcs, err := RuntimeCodecsForRegisterCount(3); err != nil || len(rcs) != 0 {
		t.Errorf("RuntimeCodecsForRegisterCount(3) = %v, %v; want empty, nil", rcs, err)
	}

	rc, ok, err := RuntimeCodecByID(id)
	if rc != nil || ok || !errors.Is(err, ErrCodecLayout) {
		t.Errorf("RuntimeCodecByID = %v, %v, %v; want nil, false, ErrCodecLayout", rc, ok, err)
	}
	r := mustPanic(t, func() { MustRuntimeCodecByID(id) })
	if perr, isErr := r.(error); !isErr || !errors.Is(perr, ErrCodecLayout) {
		t.Errorf("MustRuntimeCodecByID panic value = %v, want error wrapping ErrCodecLayout", r)
	}
}

//
// Descriptor registry
//

func TestRegisterCodecDescriptor_DuplicateIDPanics(t *testing.T) {
	withRegistry(t, CodecDescriptor{ID: "dup/id", RegisterSpec: RegisterSpec{Count: 1}})
	r := mustPanic(t, func() { registerCodecDescriptor(CodecDescriptor{ID: "dup/id"}) })
	if s, _ := r.(string); !strings.Contains(s, "dup/id") {
		t.Errorf("panic value = %v, want message naming the duplicate ID", r)
	}
	if n := len(AvailableCodecDescriptors()); n != 1 {
		t.Errorf("registry has %d descriptors after rejected duplicate, want 1", n)
	}
}

func TestCodecCandidatesForByteCount(t *testing.T) {
	got := CodecCandidatesForByteCount(4)
	if want := len(CodecDescriptorsForByteCount(4)); len(got) != want || want == 0 {
		t.Fatalf("got %d candidates, want %d (non-zero)", len(got), want)
	}
	byID := make(map[string]string, len(got))
	for _, c := range got {
		d, ok := CodecDescriptorByID(c.CodecID)
		if !ok {
			t.Errorf("candidate %q is not a registered descriptor", c.CodecID)
			continue
		}
		if d.ByteSpec.Count != 4 {
			t.Errorf("candidate %q has ByteSpec %d, want 4", c.CodecID, d.ByteSpec.Count)
		}
		byID[c.CodecID] = c.LayoutName
	}
	for id, layout := range map[string]string{
		"uint32/layout:4321":            "4321",
		"float32/layout:2143":           "2143",
		"ip_addr":                       "",
		"bytes/bytes:4":                 "",
		"ascii/registers:2":             "",
		"uint32_m10k/order:high_to_low": "",
		"datetime2_s2000":               "4321",
	} {
		if got, ok := byID[id]; !ok || got != layout {
			t.Errorf("candidate %q: LayoutName = %q (present=%v), want %q", id, got, ok, layout)
		}
	}
	for _, absent := range []string{"uint16/layout:21", "uint64/layout:87654321", "eui48"} {
		if _, ok := byID[absent]; ok {
			t.Errorf("candidate %q must not be listed for 4 bytes", absent)
		}
	}
	if got := CodecCandidatesForByteCount(7); got != nil {
		t.Errorf("odd byte count: got %v, want nil", got)
	}
}

func TestFindCodecDescriptors_ByteCountFilter(t *testing.T) {
	got := FindCodecDescriptors(CodecQuery{ByteCount: 6, Family: CodecFamilyHardwareAddress})
	if len(got) != 1 || got[0].ID != "eui48" {
		t.Errorf("got %v, want exactly eui48", got)
	}
	all := FindCodecDescriptors(CodecQuery{ByteCount: 6})
	if len(all) != len(CodecDescriptorsForByteCount(6)) {
		t.Errorf("ByteCount filter returned %d, want %d", len(all), len(CodecDescriptorsForByteCount(6)))
	}
	for _, d := range all {
		if d.ByteSpec.Count != 6 {
			t.Errorf("%s: ByteSpec %d does not match filter", d.ID, d.ByteSpec.Count)
		}
	}
	if got := FindCodecDescriptors(CodecQuery{RegisterCount: 2, ByteCount: 6}); got != nil {
		t.Errorf("contradictory query: got %v, want nil", got)
	}
}

func TestCodecFamily_String_OutOfRange(t *testing.T) {
	if got := CodecFamily(200).String(); got != "unknown" {
		t.Errorf("CodecFamily(200).String() = %q, want \"unknown\"", got)
	}
	if got := CodecValueKind(200).String(); got != "unknown" {
		t.Errorf("CodecValueKind(200).String() = %q, want \"unknown\"", got)
	}
}

func TestValidateSpecs_EmptyCodecIDDefaults(t *testing.T) {
	err := ValidateRegisterSpec(RegisterSpec{Count: 2}, []uint16{1}, "")
	var rc *CodecRegisterCountError
	if !errors.As(err, &rc) || rc.Codec != "codec" || rc.Expected.Count != 2 || rc.Actual != 1 {
		t.Errorf("ValidateRegisterSpec: got %v", err)
	}
	err = ValidateByteSpec(ByteSpec{Count: 4}, []byte{1, 2, 3}, "")
	var bc *CodecByteCountError
	if !errors.As(err, &bc) || bc.Codec != "codec" || bc.Expected.Count != 4 || bc.Actual != 3 {
		t.Errorf("ValidateByteSpec: got %v", err)
	}
}

//
// Bytes and address codecs
//

func TestUint8SliceCodec_VectorsAndLength(t *testing.T) {
	c, err := NewUint8SliceCodec(4)
	if err != nil {
		t.Fatal(err)
	}
	assertMeta(t, c, "uint8_slice/bytes:4", "uint8_slice", 2)

	regs, err := EncodeRegisters([]uint8{0x01, 0x02, 0xFE, 0xFF}, c)
	if err != nil || !slices.Equal(regs, []uint16{0x0102, 0xFEFF}) {
		t.Errorf("encode: got %04X, %v", regs, err)
	}
	back, err := DecodeRegisters([]uint16{0x0102, 0xFEFF}, c)
	if err != nil || !slices.Equal(back, []uint8{0x01, 0x02, 0xFE, 0xFF}) {
		t.Errorf("decode: got %X, %v", back, err)
	}

	for _, n := range []int{0, 3, 5} {
		regs, err := EncodeRegisters(make([]uint8, n), c)
		if regs != nil {
			t.Errorf("len %d: got registers %v alongside error", n, regs)
		}
		assertValueError(t, err, c.ID())
	}
	_, err = EncodeRegisters(nil, c)
	assertValueError(t, err, c.ID())
}

func TestBytesAndAddressCodecs_DirectDecodeWrongRegisterCount(t *testing.T) {
	bc, err := NewBytesCodec(6)
	if err != nil {
		t.Fatal(err)
	}
	uc, err := NewUint8SliceCodec(6)
	if err != nil {
		t.Fatal(err)
	}
	assertDirectCountError(t, bc)
	assertDirectCountError(t, uc)
	assertDirectCountError(t, NewIPAddrCodec())
	assertDirectCountError(t, NewIPv6AddrCodec())
	assertDirectCountError(t, NewEUI48Codec())
	assertDirectCountError(t, NewEUI64Codec())
}

func TestAddressCodecs_ExactVectorsAndRejections(t *testing.T) {
	ip4 := NewIPAddrCodec()
	regs, err := EncodeRegisters(net.IPv4(192, 168, 1, 10), ip4) // 16-byte form is accepted
	if err != nil || !slices.Equal(regs, []uint16{0xC0A8, 0x010A}) {
		t.Errorf("ip_addr encode: got %04X, %v", regs, err)
	}

	ip6 := NewIPv6AddrCodec()
	regs, err = EncodeRegisters(net.ParseIP("2001:db8::1"), ip6)
	if want := []uint16{0x2001, 0x0DB8, 0, 0, 0, 0, 0, 0x0001}; err != nil || !slices.Equal(regs, want) {
		t.Errorf("ipv6_addr encode: got %04X, %v", regs, err)
	}
	back, err := DecodeRegisters(regs, ip6)
	if err != nil || !back.Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("ipv6_addr decode: got %v, %v", back, err)
	}

	for name, ip := range map[string]net.IP{
		"nil":           nil,
		"empty":         {},
		"5 bytes":       {1, 2, 3, 4, 5},
		"ipv4":          net.IPv4(10, 0, 0, 1),
		"ipv4 (4-byte)": {10, 0, 0, 1},
	} {
		_, err := EncodeRegisters(ip, ip6)
		if reason := valueErrorReason(t, err, "ipv6_addr"); reason == "" {
			t.Errorf("ipv6_addr %s: empty reason", name)
		}
	}
	for name, ip := range map[string]net.IP{
		"nil":     nil,
		"5 bytes": {1, 2, 3, 4, 5},
		"ipv6":    net.ParseIP("2001:db8::1"),
	} {
		_, err := EncodeRegisters(ip, ip4)
		if reason := valueErrorReason(t, err, "ip_addr"); reason == "" {
			t.Errorf("ip_addr %s: empty reason", name)
		}
	}

	for _, mac := range []net.HardwareAddr{nil, {1, 2, 3, 4, 5}, {1, 2, 3, 4, 5, 6, 7, 8}} {
		_, err := EncodeRegisters(mac, NewEUI48Codec())
		assertValueError(t, err, "eui48")
	}
	for _, mac := range []net.HardwareAddr{nil, {1, 2, 3, 4, 5, 6}, {1, 2, 3, 4, 5, 6, 7, 8, 9}} {
		_, err := EncodeRegisters(mac, NewEUI64Codec())
		assertValueError(t, err, "eui64")
	}
}

//
// Client helpers
//

func TestClientHelpers_ZeroRegisterSpecRejectedBeforeRead(t *testing.T) {
	// A reader that must never be reached.
	mock := &mockRW{err: errors.New("transport must not be used")}
	ctx := context.Background()

	_, err := ReadFromClient[uint16](mock, ctx, 1, 0, HoldingRegister, zeroSpecCodec{})
	var ce *CodecRegisterCountError
	if !errors.As(err, &ce) || ce.Codec != "zero/spec" {
		t.Errorf("ReadFromClient: want *CodecRegisterCountError for zero/spec, got %v", err)
	}

	v, err := ReadRuntimeFromClient(mock, ctx, 1, 0, InputRegister, AsRuntimeCodec[uint16](zeroSpecCodec{}, CodecValueUint16))
	ce = nil
	if v != nil || !errors.As(err, &ce) || ce.Codec != "zero/spec" {
		t.Errorf("ReadRuntimeFromClient: want nil, *CodecRegisterCountError; got %v, %v", v, err)
	}
}

func TestClientHelpers_ErrorsPropagateAndNothingIsWritten(t *testing.T) {
	ctx := context.Background()
	transportErr := errors.New("transport down")

	t.Run("runtime read error", func(t *testing.T) {
		mock := &mockRW{err: transportErr}
		v, err := ReadRuntimeFromClient(mock, ctx, 1, 0, HoldingRegister, MustRuntimeCodecByID("uint32/layout:4321"))
		if v != nil || !errors.Is(err, transportErr) {
			t.Errorf("got %v, %v; want nil, transport error", v, err)
		}
	})
	t.Run("runtime write error", func(t *testing.T) {
		mock := &mockRW{err: transportErr}
		err := WriteRuntimeToClient(mock, ctx, 1, 0, uint32(7), MustRuntimeCodecByID("uint32/layout:4321"))
		if !errors.Is(err, transportErr) {
			t.Errorf("got %v, want transport error", err)
		}
	})
	t.Run("typed encode error", func(t *testing.T) {
		mock := &mockRW{}
		err := WriteToClient(mock, ctx, 1, 0, int16(-32768), NewInt16SignMagnitudeCodec())
		assertValueError(t, err, "int16_sign_magnitude")
		if mock.written != nil {
			t.Errorf("registers %v were written despite encode error", mock.written)
		}
	})
	t.Run("runtime wrong value type", func(t *testing.T) {
		mock := &mockRW{}
		err := WriteRuntimeToClient(mock, ctx, 1, 0, "42", MustRuntimeCodecByID("uint32/layout:4321"))
		assertValueError(t, err, "uint32/layout:4321")
		if mock.written != nil {
			t.Errorf("registers %v were written despite type error", mock.written)
		}
	})
	t.Run("uint32 helpers reject non-32-bit layout", func(t *testing.T) {
		mock := &mockRW{regs: []uint16{1, 2}}
		v, err := ReadUint32FromClient(mock, ctx, 1, 0, HoldingRegister, Layout16_21)
		if v != 0 || !errors.Is(err, ErrCodecLayout) {
			t.Errorf("read: got %d, %v; want 0, ErrCodecLayout", v, err)
		}
		err = WriteUint32ToClient(mock, ctx, 1, 0, 7, Layout48_654321)
		if !errors.Is(err, ErrCodecLayout) || mock.written != nil {
			t.Errorf("write: got %v (written=%v); want ErrCodecLayout and nothing written", err, mock.written)
		}
	})
}

func TestClientHelpers_Uint32LayoutIsApplied(t *testing.T) {
	ctx := context.Background()
	mock := &mockRW{regs: []uint16{0x0304, 0x0102}}
	v, err := ReadUint32FromClient(mock, ctx, 1, 0, InputRegister, Layout32_2143)
	if err != nil || v != 0x01020304 {
		t.Errorf("read: got %#x, %v; want 0x01020304", v, err)
	}
	if err := WriteUint32ToClient(mock, ctx, 1, 0, 0x01020304, Layout32_1234); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(mock.written, []uint16{0x0403, 0x0201}) {
		t.Errorf("write: got %04X, want [0403 0201]", mock.written)
	}
}

func TestDescriptorHelpers_UnknownDescriptor(t *testing.T) {
	desc := CodecDescriptor{ID: "no/such/codec", RegisterSpec: RegisterSpec{Count: 1}}
	v, err := DecodeWithDescriptor([]uint16{1}, desc)
	if v != nil || !errors.Is(err, ErrUnknownCodec) {
		t.Errorf("DecodeWithDescriptor: got %v, %v; want nil, ErrUnknownCodec", v, err)
	}
	regs, err := EncodeWithDescriptor(uint16(1), desc)
	if regs != nil || !errors.Is(err, ErrUnknownCodec) {
		t.Errorf("EncodeWithDescriptor: got %v, %v; want nil, ErrUnknownCodec", regs, err)
	}
}

func TestDescriptorHelpers_ExactVectors(t *testing.T) {
	cases := []struct {
		id    string
		value any
		regs  []uint16
	}{
		{"int32/layout:2143", int32(-2), []uint16{0xFFFE, 0xFFFF}},
		{"uint48/layout:123456", uint64(0x010203040506), []uint16{0x0605, 0x0403, 0x0201}},
		{"int16_sign_magnitude", int16(-5), []uint16{0x8005}},
		{"int48_m10k/order:high_to_low", int64(-1), []uint16{0xFFFF, 9999, 9999}},
		{"packed_bcd/registers:1", "1234", []uint16{0x1234}},
		{"ascii_reverse/registers:1", "AB", []uint16{0x4241}},
	}
	for _, tc := range cases {
		desc, ok := CodecDescriptorByID(tc.id)
		if !ok {
			t.Errorf("%s: not registered", tc.id)
			continue
		}
		regs, err := EncodeWithDescriptor(tc.value, desc)
		if err != nil || !slices.Equal(regs, tc.regs) {
			t.Errorf("%s: encode = %04X, %v; want %04X", tc.id, regs, err, tc.regs)
		}
		v, err := DecodeWithDescriptor(tc.regs, desc)
		if err != nil || v != tc.value {
			t.Errorf("%s: decode = %v (%T), %v; want %v (%T)", tc.id, v, v, err, tc.value, tc.value)
		}
	}
}
