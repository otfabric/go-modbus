// SPDX-License-Identifier: MIT

package codec

import (
	"errors"
	"slices"
	"testing"
)

// codecMeta is the non-generic part of every Codec[T], used by metadata tables.
type codecMeta interface {
	ID() string
	Name() string
	RegisterSpec() RegisterSpec
	ByteSpec() ByteSpec
}

// assertMeta checks the identity and shape reported by a codec.
func assertMeta(t *testing.T, c codecMeta, wantID, wantName string, wantRegs uint16) {
	t.Helper()
	if got := c.ID(); got != wantID {
		t.Errorf("ID() = %q, want %q", got, wantID)
	}
	if got := c.Name(); got != wantName {
		t.Errorf("%s: Name() = %q, want %q", wantID, got, wantName)
	}
	if got := c.RegisterSpec().Count; got != wantRegs {
		t.Errorf("%s: RegisterSpec().Count = %d, want %d", wantID, got, wantRegs)
	}
	if got := c.ByteSpec().Count; got != wantRegs*2 {
		t.Errorf("%s: ByteSpec().Count = %d, want %d", wantID, got, wantRegs*2)
	}
}

// assertVector checks that v encodes to exactly want and that want decodes back to v.
func assertVector[T comparable](t *testing.T, c Codec[T], v T, want []uint16) {
	t.Helper()
	got, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatalf("%s: encode %v: %v", c.ID(), v, err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s: encode %v = %04X, want %04X", c.ID(), v, got, want)
	}
	back, err := DecodeRegisters(want, c)
	if err != nil {
		t.Fatalf("%s: decode %04X: %v", c.ID(), want, err)
	}
	if back != v {
		t.Errorf("%s: decode %04X = %v, want %v", c.ID(), want, back, v)
	}
}

// assertDirectCountError calls the codec's own DecodeRegisters (bypassing the
// package-level helper that validates first) with one register too many and
// checks the typed error it reports.
func assertDirectCountError[T any](t *testing.T, c Decoder[T]) {
	t.Helper()
	spec := c.RegisterSpec()
	_, err := c.DecodeRegisters(make([]uint16, spec.Count+1))
	var ce *CodecRegisterCountError
	if !errors.As(err, &ce) {
		t.Fatalf("%s: want *CodecRegisterCountError, got %v", c.ID(), err)
	}
	if ce.Codec != c.ID() || ce.Expected != spec || ce.Actual != spec.Count+1 {
		t.Errorf("%s: got %+v, want Codec=%q Expected=%+v Actual=%d", c.ID(), ce, c.ID(), spec, spec.Count+1)
	}
	if !errors.Is(err, ErrCodecRegisterCount) {
		t.Errorf("%s: error does not wrap ErrCodecRegisterCount: %v", c.ID(), err)
	}
}

// assertValueError checks err is a *CodecValueError for the given codec ID.
func assertValueError(t *testing.T, err error, codecID string) {
	t.Helper()
	_ = valueErrorReason(t, err, codecID)
}

// valueErrorReason checks err is a *CodecValueError for the given codec ID and returns its Reason.
func valueErrorReason(t *testing.T, err error, codecID string) string {
	t.Helper()
	var ve *CodecValueError
	if !errors.As(err, &ve) {
		t.Fatalf("want *CodecValueError for %q, got %v", codecID, err)
	}
	if ve.Codec != codecID {
		t.Errorf("CodecValueError.Codec = %q, want %q", ve.Codec, codecID)
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("error does not wrap ErrCodecValue: %v", err)
	}
	return ve.Reason
}

// mustPanic runs fn and returns the recovered value; it fails the test if fn does not panic.
func mustPanic(t *testing.T, fn func()) (recovered any) {
	t.Helper()
	defer func() {
		recovered = recover()
		if recovered == nil {
			t.Error("expected panic")
		}
	}()
	fn()
	return nil
}

func TestNumericCodecs_ExactVectors(t *testing.T) {
	t.Run("uint16", func(t *testing.T) {
		assertVector(t, MustNewUint16Codec(Layout16_21), uint16(0x0102), []uint16{0x0102})
		assertVector(t, MustNewUint16Codec(Layout16_12), uint16(0x0102), []uint16{0x0201})
	})
	t.Run("int16", func(t *testing.T) {
		assertVector(t, MustNewInt16Codec(Layout16_21), int16(-2), []uint16{0xFFFE})
		assertVector(t, MustNewInt16Codec(Layout16_12), int16(-2), []uint16{0xFEFF})
		assertVector(t, MustNewInt16Codec(Layout16_21), int16(-32768), []uint16{0x8000})
	})
	t.Run("uint32", func(t *testing.T) {
		const v = uint32(0x01020304)
		assertVector(t, MustNewUint32Codec(Layout32_4321), v, []uint16{0x0102, 0x0304})
		assertVector(t, MustNewUint32Codec(Layout32_3412), v, []uint16{0x0201, 0x0403})
		assertVector(t, MustNewUint32Codec(Layout32_2143), v, []uint16{0x0304, 0x0102})
		assertVector(t, MustNewUint32Codec(Layout32_1234), v, []uint16{0x0403, 0x0201})
	})
	t.Run("int32", func(t *testing.T) {
		assertVector(t, MustNewInt32Codec(Layout32_4321), int32(-2), []uint16{0xFFFF, 0xFFFE})
		assertVector(t, MustNewInt32Codec(Layout32_1234), int32(-2), []uint16{0xFEFF, 0xFFFF})
		assertVector(t, MustNewInt32Codec(Layout32_2143), int32(-2147483648), []uint16{0x0000, 0x8000})
	})
	t.Run("float32", func(t *testing.T) {
		// 1.0 == 0x3F800000
		assertVector(t, MustNewFloat32Codec(Layout32_4321), float32(1), []uint16{0x3F80, 0x0000})
		assertVector(t, MustNewFloat32Codec(Layout32_3412), float32(1), []uint16{0x803F, 0x0000})
		assertVector(t, MustNewFloat32Codec(Layout32_2143), float32(1), []uint16{0x0000, 0x3F80})
		assertVector(t, MustNewFloat32Codec(Layout32_1234), float32(1), []uint16{0x0000, 0x803F})
	})
	t.Run("uint48", func(t *testing.T) {
		const v = uint64(0x010203040506)
		assertVector(t, MustNewUint48Codec(Layout48_654321), v, []uint16{0x0102, 0x0304, 0x0506})
		assertVector(t, MustNewUint48Codec(Layout48_563412), v, []uint16{0x0201, 0x0403, 0x0605})
		assertVector(t, MustNewUint48Codec(Layout48_214365), v, []uint16{0x0506, 0x0304, 0x0102})
		assertVector(t, MustNewUint48Codec(Layout48_123456), v, []uint16{0x0605, 0x0403, 0x0201})
	})
	t.Run("int48", func(t *testing.T) {
		assertVector(t, MustNewInt48Codec(Layout48_654321), int64(-2), []uint16{0xFFFF, 0xFFFF, 0xFFFE})
		assertVector(t, MustNewInt48Codec(Layout48_123456), int64(-2), []uint16{0xFEFF, 0xFFFF, 0xFFFF})
		assertVector(t, MustNewInt48Codec(Layout48_654321), int64(-1)<<47, []uint16{0x8000, 0x0000, 0x0000})
		assertVector(t, MustNewInt48Codec(Layout48_654321), int64(1)<<47-1, []uint16{0x7FFF, 0xFFFF, 0xFFFF})
	})
	t.Run("uint64", func(t *testing.T) {
		const v = uint64(0x0102030405060708)
		assertVector(t, MustNewUint64Codec(Layout64_87654321), v, []uint16{0x0102, 0x0304, 0x0506, 0x0708})
		assertVector(t, MustNewUint64Codec(Layout64_78563412), v, []uint16{0x0201, 0x0403, 0x0605, 0x0807})
		assertVector(t, MustNewUint64Codec(Layout64_21436587), v, []uint16{0x0708, 0x0506, 0x0304, 0x0102})
		assertVector(t, MustNewUint64Codec(Layout64_12345678), v, []uint16{0x0807, 0x0605, 0x0403, 0x0201})
	})
	t.Run("int64", func(t *testing.T) {
		assertVector(t, MustNewInt64Codec(Layout64_87654321), int64(-2), []uint16{0xFFFF, 0xFFFF, 0xFFFF, 0xFFFE})
		assertVector(t, MustNewInt64Codec(Layout64_21436587), int64(-2), []uint16{0xFFFE, 0xFFFF, 0xFFFF, 0xFFFF})
	})
	t.Run("float64", func(t *testing.T) {
		// 1.0 == 0x3FF0000000000000
		assertVector(t, MustNewFloat64Codec(Layout64_87654321), float64(1), []uint16{0x3FF0, 0, 0, 0})
		assertVector(t, MustNewFloat64Codec(Layout64_21436587), float64(1), []uint16{0, 0, 0, 0x3FF0})
		assertVector(t, MustNewFloat64Codec(Layout64_12345678), float64(1), []uint16{0, 0, 0, 0xF03F})
	})
	t.Run("int16_sign_magnitude", func(t *testing.T) {
		c := NewInt16SignMagnitudeCodec()
		assertVector(t, c, int16(-1), []uint16{0x8001})
		assertVector(t, c, int16(32767), []uint16{0x7FFF})
		assertVector(t, c, int16(-32767), []uint16{0xFFFF})
	})
}

// numericCtor describes one layout-parameterised numeric constructor pair.
type numericCtor struct {
	name   string
	regs   uint16
	good   RegisterLayout
	bad    RegisterLayout
	newErr func(RegisterLayout) error
	must   func(RegisterLayout) codecMeta
}

func numericCtors() []numericCtor {
	return []numericCtor{
		{"uint16", 1, Layout16_12, Layout32_4321,
			func(l RegisterLayout) error { _, err := NewUint16Codec(l); return err },
			func(l RegisterLayout) codecMeta { return MustNewUint16Codec(l) }},
		{"int16", 1, Layout16_21, Layout32_4321,
			func(l RegisterLayout) error { _, err := NewInt16Codec(l); return err },
			func(l RegisterLayout) codecMeta { return MustNewInt16Codec(l) }},
		{"uint32", 2, Layout32_3412, Layout16_21,
			func(l RegisterLayout) error { _, err := NewUint32Codec(l); return err },
			func(l RegisterLayout) codecMeta { return MustNewUint32Codec(l) }},
		{"int32", 2, Layout32_2143, Layout48_654321,
			func(l RegisterLayout) error { _, err := NewInt32Codec(l); return err },
			func(l RegisterLayout) codecMeta { return MustNewInt32Codec(l) }},
		{"float32", 2, Layout32_1234, Layout64_87654321,
			func(l RegisterLayout) error { _, err := NewFloat32Codec(l); return err },
			func(l RegisterLayout) codecMeta { return MustNewFloat32Codec(l) }},
		{"uint48", 3, Layout48_563412, Layout32_4321,
			func(l RegisterLayout) error { _, err := NewUint48Codec(l); return err },
			func(l RegisterLayout) codecMeta { return MustNewUint48Codec(l) }},
		{"int48", 3, Layout48_214365, Layout64_87654321,
			func(l RegisterLayout) error { _, err := NewInt48Codec(l); return err },
			func(l RegisterLayout) codecMeta { return MustNewInt48Codec(l) }},
		{"uint64", 4, Layout64_78563412, Layout48_654321,
			func(l RegisterLayout) error { _, err := NewUint64Codec(l); return err },
			func(l RegisterLayout) codecMeta { return MustNewUint64Codec(l) }},
		{"int64", 4, Layout64_21436587, Layout32_4321,
			func(l RegisterLayout) error { _, err := NewInt64Codec(l); return err },
			func(l RegisterLayout) codecMeta { return MustNewInt64Codec(l) }},
		{"float64", 4, Layout64_12345678, Layout16_21,
			func(l RegisterLayout) error { _, err := NewFloat64Codec(l); return err },
			func(l RegisterLayout) codecMeta { return MustNewFloat64Codec(l) }},
	}
}

func TestNumericConstructors_MetaAndLayoutValidation(t *testing.T) {
	for _, tc := range numericCtors() {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.newErr(tc.good); err != nil {
				t.Fatalf("New with matching layout: %v", err)
			}
			assertMeta(t, tc.must(tc.good), tc.name+"/layout:"+tc.good.String(), tc.name, tc.regs)

			err := tc.newErr(tc.bad)
			var le *CodecLayoutError
			if !errors.As(err, &le) {
				t.Fatalf("New with %d-register layout: want *CodecLayoutError, got %v", tc.bad.RegisterCount(), err)
			}
			if le.Codec != tc.name || le.Layout != tc.bad {
				t.Errorf("CodecLayoutError = {Codec:%q Layout:%s}, want {Codec:%q Layout:%s}", le.Codec, le.Layout, tc.name, tc.bad)
			}
			if !errors.Is(err, ErrCodecLayout) {
				t.Errorf("error does not wrap ErrCodecLayout: %v", err)
			}

			r := mustPanic(t, func() { tc.must(tc.bad) })
			perr, ok := r.(error)
			if !ok || !errors.Is(perr, ErrCodecLayout) {
				t.Errorf("Must panic value = %v, want error wrapping ErrCodecLayout", r)
			}
		})
	}
}

func TestInt16SignMagnitudeCodec_Meta(t *testing.T) {
	assertMeta(t, NewInt16SignMagnitudeCodec(), "int16_sign_magnitude", "int16_sign_magnitude", 1)
}

func TestNumericCodecs_DirectDecodeWrongRegisterCount(t *testing.T) {
	assertDirectCountError(t, MustNewUint16Codec(Layout16_21))
	assertDirectCountError(t, MustNewInt16Codec(Layout16_21))
	assertDirectCountError(t, NewInt16SignMagnitudeCodec())
	assertDirectCountError(t, MustNewUint32Codec(Layout32_4321))
	assertDirectCountError(t, MustNewInt32Codec(Layout32_4321))
	assertDirectCountError(t, MustNewFloat32Codec(Layout32_4321))
	assertDirectCountError(t, MustNewUint48Codec(Layout48_654321))
	assertDirectCountError(t, MustNewUint64Codec(Layout64_87654321))
	assertDirectCountError(t, MustNewFloat64Codec(Layout64_87654321))

	assertDirectCountError(t, MustNewInt48Codec(Layout48_654321))
	assertDirectCountError(t, MustNewInt64Codec(Layout64_87654321))
}

// 48-bit codecs must reject values that do not fit in 48 bits instead of
// silently truncating them.
func TestNumericCodecs_48BitEncodeRange(t *testing.T) {
	u := MustNewUint48Codec(Layout48_654321)
	if regs, err := u.EncodeRegisters(1<<48 - 1); err != nil || regs[0] != 0xFFFF || regs[1] != 0xFFFF || regs[2] != 0xFFFF {
		t.Errorf("uint48 max: regs=%04X err=%v", regs, err)
	}
	for _, v := range []uint64{1 << 48, 1<<64 - 1} {
		_, err := u.EncodeRegisters(v)
		var ve *CodecValueError
		if !errors.As(err, &ve) || ve.Codec != u.ID() {
			t.Errorf("uint48 encode %d: want CodecValueError for %s, got %v", v, u.ID(), err)
		}
	}

	i := MustNewInt48Codec(Layout48_654321)
	for _, v := range []int64{-(1 << 47), -1, 0, 1<<47 - 1} {
		regs, err := i.EncodeRegisters(v)
		if err != nil {
			t.Errorf("int48 encode %d: %v", v, err)
			continue
		}
		if got, err := i.DecodeRegisters(regs); err != nil || got != v {
			t.Errorf("int48 round trip %d: got %d err=%v", v, got, err)
		}
	}
	for _, v := range []int64{1 << 47, -(1 << 47) - 1, 1<<63 - 1, -(1 << 63)} {
		_, err := i.EncodeRegisters(v)
		var ve *CodecValueError
		if !errors.As(err, &ve) || ve.Codec != i.ID() {
			t.Errorf("int48 encode %d: want CodecValueError for %s, got %v", v, i.ID(), err)
		}
	}
}

// assertLayoutMismatch exercises the defensive permutation checks: a codec
// value whose layout width disagrees with its register count (not constructible
// through the exported constructors) must fail with ErrEncodingError in both
// directions rather than produce garbage.
func assertLayoutMismatch[T any](t *testing.T, c Codec[T]) {
	t.Helper()
	var zero T
	if _, err := c.DecodeRegisters(make([]uint16, c.RegisterSpec().Count)); !errors.Is(err, ErrEncodingError) {
		t.Errorf("%s: decode: want ErrEncodingError, got %v", c.ID(), err)
	}
	if _, err := c.EncodeRegisters(zero); !errors.Is(err, ErrEncodingError) {
		t.Errorf("%s: encode: want ErrEncodingError, got %v", c.ID(), err)
	}
}

func TestNumericCodecs_InconsistentLayoutIsRejected(t *testing.T) {
	assertLayoutMismatch[uint16](t, uint16Codec{layout: Layout32_4321})
	assertLayoutMismatch[int16](t, int16Codec{layout: Layout32_4321})
	assertLayoutMismatch[uint32](t, uint32Codec{layout: Layout16_21})
	assertLayoutMismatch[int32](t, int32Codec{layout: Layout16_21})
	assertLayoutMismatch[float32](t, float32Codec{layout: Layout16_21})
	assertLayoutMismatch[uint64](t, uint48Codec{layout: Layout16_21})
	assertLayoutMismatch[int64](t, int48Codec{layout: Layout16_21})
	assertLayoutMismatch[uint64](t, uint64Codec{layout: Layout16_21})
	assertLayoutMismatch[int64](t, int64Codec{layout: Layout16_21})
	assertLayoutMismatch[float64](t, float64Codec{layout: Layout16_21})
}

func TestMustLayoutForName(t *testing.T) {
	cases := []struct {
		regs uint16
		name string
		want RegisterLayout
	}{
		{1, "21", Layout16_21},
		{1, "12", Layout16_12},
		{2, "3412", Layout32_3412},
		{3, "214365", Layout48_214365},
		{4, "12345678", Layout64_12345678},
	}
	for _, tc := range cases {
		if got := mustLayoutForName(tc.regs, tc.name); got != tc.want {
			t.Errorf("mustLayoutForName(%d, %q) = %s, want %s", tc.regs, tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		regs uint16
		name string
	}{{2, "21"}, {1, "4321"}, {5, "21"}} {
		r := mustPanic(t, func() { mustLayoutForName(tc.regs, tc.name) })
		if s, _ := r.(string); s == "" {
			t.Errorf("mustLayoutForName(%d, %q): panic value = %v, want message string", tc.regs, tc.name, r)
		}
	}
}
