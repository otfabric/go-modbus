// SPDX-License-Identifier: MIT

package codec

import (
	"errors"
	"math"
	"testing"
)

func TestM10k_ExactVectors(t *testing.T) {
	lo, hi := DecimalLimbLowToHigh, DecimalLimbHighToLow

	// 12345678 = 1234*10^4 + 5678
	assertVector(t, MustNewUint32M10kCodec(lo), uint32(12345678), []uint16{5678, 1234})
	assertVector(t, MustNewUint32M10kCodec(hi), uint32(12345678), []uint16{1234, 5678})
	assertVector(t, MustNewUint32M10kCodec(hi), uint32(99_999_999), []uint16{9999, 9999})

	// 123456789012 = 1234*10^8 + 5678*10^4 + 9012
	assertVector(t, MustNewUint48M10kCodec(lo), uint64(123456789012), []uint16{9012, 5678, 1234})
	assertVector(t, MustNewUint48M10kCodec(hi), uint64(123456789012), []uint16{1234, 5678, 9012})

	// 1234567890123456 = 1234*10^12 + 5678*10^8 + 9012*10^4 + 3456
	assertVector(t, MustNewUint64M10kCodec(lo), uint64(1234567890123456), []uint16{3456, 9012, 5678, 1234})
	assertVector(t, MustNewUint64M10kCodec(hi), uint64(1234567890123456), []uint16{1234, 5678, 9012, 3456})

	// Signed: lower limbs are 0..9999, only the most-significant limb carries the sign.
	// -1 = -1*10^4 + 9999 ; -12345678 = -1235*10^4 + 4322
	assertVector(t, MustNewInt32M10kCodec(lo), int32(-1), []uint16{9999, 0xFFFF})
	assertVector(t, MustNewInt32M10kCodec(hi), int32(-12345678), []uint16{0xFB2D, 4322})
	assertVector(t, MustNewInt32M10kCodec(lo), int32(-99_990_000), []uint16{0, 0xD8F1})

	assertVector(t, MustNewInt48M10kCodec(lo), int64(-1), []uint16{9999, 9999, 0xFFFF})
	assertVector(t, MustNewInt48M10kCodec(hi), int64(123456789012), []uint16{1234, 5678, 9012})

	assertVector(t, MustNewInt64M10kCodec(lo), int64(-1), []uint16{9999, 9999, 9999, 0xFFFF})
	assertVector(t, MustNewInt64M10kCodec(hi), int64(-1), []uint16{0xFFFF, 9999, 9999, 9999})
}

// m10kCtor describes one order-parameterised M10k constructor pair.
type m10kCtor struct {
	name   string
	regs   uint16
	newErr func(DecimalLimbOrder) error
	must   func(DecimalLimbOrder) codecMeta
}

func m10kCtors() []m10kCtor {
	return []m10kCtor{
		{"uint32_m10k", 2,
			func(o DecimalLimbOrder) error { _, err := NewUint32M10kCodec(o); return err },
			func(o DecimalLimbOrder) codecMeta { return MustNewUint32M10kCodec(o) }},
		{"uint48_m10k", 3,
			func(o DecimalLimbOrder) error { _, err := NewUint48M10kCodec(o); return err },
			func(o DecimalLimbOrder) codecMeta { return MustNewUint48M10kCodec(o) }},
		{"uint64_m10k", 4,
			func(o DecimalLimbOrder) error { _, err := NewUint64M10kCodec(o); return err },
			func(o DecimalLimbOrder) codecMeta { return MustNewUint64M10kCodec(o) }},
		{"int32_m10k", 2,
			func(o DecimalLimbOrder) error { _, err := NewInt32M10kCodec(o); return err },
			func(o DecimalLimbOrder) codecMeta { return MustNewInt32M10kCodec(o) }},
		{"int48_m10k", 3,
			func(o DecimalLimbOrder) error { _, err := NewInt48M10kCodec(o); return err },
			func(o DecimalLimbOrder) codecMeta { return MustNewInt48M10kCodec(o) }},
		{"int64_m10k", 4,
			func(o DecimalLimbOrder) error { _, err := NewInt64M10kCodec(o); return err },
			func(o DecimalLimbOrder) codecMeta { return MustNewInt64M10kCodec(o) }},
	}
}

func TestM10kConstructors_MetaAndOrderValidation(t *testing.T) {
	for _, tc := range m10kCtors() {
		t.Run(tc.name, func(t *testing.T) {
			assertMeta(t, tc.must(DecimalLimbLowToHigh), tc.name+"/order:low_to_high", tc.name, tc.regs)
			assertMeta(t, tc.must(DecimalLimbHighToLow), tc.name+"/order:high_to_low", tc.name, tc.regs)

			for _, bad := range []DecimalLimbOrder{0, 3, 255} {
				reason := valueErrorReason(t, tc.newErr(bad), tc.name)
				if reason != "invalid decimal limb order" {
					t.Errorf("order %d: Reason = %q", bad, reason)
				}
				r := mustPanic(t, func() { tc.must(bad) })
				if perr, ok := r.(error); !ok || !errors.Is(perr, ErrCodecValue) {
					t.Errorf("order %d: Must panic value = %v, want error wrapping ErrCodecValue", bad, r)
				}
			}
		})
	}
}

func TestM10k_DirectDecodeWrongRegisterCount(t *testing.T) {
	for _, o := range []DecimalLimbOrder{DecimalLimbLowToHigh, DecimalLimbHighToLow} {
		assertDirectCountError(t, MustNewUint32M10kCodec(o))
		assertDirectCountError(t, MustNewUint48M10kCodec(o))
		assertDirectCountError(t, MustNewUint64M10kCodec(o))
		assertDirectCountError(t, MustNewInt32M10kCodec(o))
		assertDirectCountError(t, MustNewInt48M10kCodec(o))
		assertDirectCountError(t, MustNewInt64M10kCodec(o))
	}
}

func TestM10k_DecodeRejectsLimbAbove9999(t *testing.T) {
	t.Run("unsigned", func(t *testing.T) {
		cases := []struct {
			order DecimalLimbOrder
			regs  []uint16
		}{
			{DecimalLimbLowToHigh, []uint16{0, 10000, 0}},
			{DecimalLimbLowToHigh, []uint16{0, 0, 0xFFFF}},
			{DecimalLimbHighToLow, []uint16{0, 0, 10000}},
			{DecimalLimbHighToLow, []uint16{10000, 0, 0}},
		}
		for _, tc := range cases {
			c := MustNewUint48M10kCodec(tc.order)
			_, err := DecodeRegisters(tc.regs, c)
			assertValueError(t, err, c.ID())
		}
	})
	t.Run("signed lower limb", func(t *testing.T) {
		// Only the most-significant limb is signed; a lower limb outside 0..9999 is invalid.
		cases := []struct {
			order DecimalLimbOrder
			regs  []uint16
		}{
			{DecimalLimbLowToHigh, []uint16{10000, 1}},
			{DecimalLimbHighToLow, []uint16{1, 10000}},
			{DecimalLimbLowToHigh, []uint16{0xFFFF, 0}},
		}
		for _, tc := range cases {
			c := MustNewInt32M10kCodec(tc.order)
			_, err := DecodeRegisters(tc.regs, c)
			assertValueError(t, err, c.ID())
		}
	})
	t.Run("signed MS limb", func(t *testing.T) {
		for _, ms := range []uint16{10000, 0x8000, 0xD8F0} { // 10000, -32768, -10000
			c := MustNewInt32M10kCodec(DecimalLimbHighToLow)
			_, err := DecodeRegisters([]uint16{ms, 0}, c)
			assertValueError(t, err, c.ID())
		}
	})
}

func TestM10k_EncodeRejectsOutOfRange(t *testing.T) {
	u32 := MustNewUint32M10kCodec(DecimalLimbHighToLow)
	_, err := EncodeRegisters(uint32(math.MaxUint32), u32)
	assertValueError(t, err, u32.ID())

	u48 := MustNewUint48M10kCodec(DecimalLimbLowToHigh)
	_, err = EncodeRegisters(uint64(1_000_000_000_000), u48)
	assertValueError(t, err, u48.ID())
	assertVector(t, u48, uint64(999_999_999_999), []uint16{9999, 9999, 9999})

	i32 := MustNewInt32M10kCodec(DecimalLimbLowToHigh)
	for _, v := range []int32{math.MaxInt32, math.MinInt32, 100_000_000, -100_000_000} {
		_, err = EncodeRegisters(v, i32)
		assertValueError(t, err, i32.ID())
	}
	// Boundaries that must still encode: +/-(9999*10^4 + 9999) and -9999*10^4.
	assertVector(t, i32, int32(99_999_999), []uint16{9999, 9999})
	assertVector(t, i32, int32(-99_990_000), []uint16{0, 0xD8F1})

	i64 := MustNewInt64M10kCodec(DecimalLimbHighToLow)
	for _, v := range []int64{math.MaxInt64, math.MinInt64, 10_000_000_000_000_000} {
		_, err = EncodeRegisters(v, i64)
		assertValueError(t, err, i64.ID())
	}
}

// The limb helpers are shared by all M10k codecs; their own guards are not
// reachable through the constructors (which validate the order and fix the
// register count), so they are exercised directly here.
func TestM10kHelpers_Guards(t *testing.T) {
	const id = "m10k/test"

	_, err := decodeUintM10k([]uint16{1, 2}, DecimalLimbOrder(9), id)
	if reason := valueErrorReason(t, err, id); reason != "invalid decimal limb order" {
		t.Errorf("decodeUintM10k: Reason = %q", reason)
	}

	_, err = encodeUintM10k(1, 2, DecimalLimbOrder(9), id)
	if reason := valueErrorReason(t, err, id); reason != "invalid decimal limb order" {
		t.Errorf("encodeUintM10k: Reason = %q", reason)
	}

	_, err = decodeIntM10k(nil, DecimalLimbLowToHigh, id)
	if reason := valueErrorReason(t, err, id); reason != "need at least one register" {
		t.Errorf("decodeIntM10k: Reason = %q", reason)
	}

	_, err = encodeIntM10k(1, 0, DecimalLimbLowToHigh, id)
	if reason := valueErrorReason(t, err, id); reason != "register count must be >= 1" {
		t.Errorf("encodeIntM10k: Reason = %q", reason)
	}

	// Single-register signed: the only limb is the signed MS limb.
	regs, err := encodeIntM10k(-9999, 1, DecimalLimbLowToHigh, id)
	if err != nil || len(regs) != 1 || regs[0] != 0xD8F1 {
		t.Errorf("encodeIntM10k(-9999, 1) = %04X, %v; want [D8F1]", regs, err)
	}
	v, err := decodeIntM10k([]uint16{0xD8F1}, DecimalLimbHighToLow, id)
	if err != nil || v != -9999 {
		t.Errorf("decodeIntM10k([D8F1]) = %d, %v; want -9999", v, err)
	}
}

func TestDecimalLimbOrder_String_Invalid(t *testing.T) {
	for _, o := range []DecimalLimbOrder{0, 3, 255} {
		if got := o.String(); got != "unknown" {
			t.Errorf("DecimalLimbOrder(%d).String() = %q, want \"unknown\"", o, got)
		}
	}
}

func TestDecimalLimbOrderFromID(t *testing.T) {
	for id, want := range map[string]DecimalLimbOrder{
		"low_to_high": DecimalLimbLowToHigh,
		"high_to_low": DecimalLimbHighToLow,
	} {
		got, err := decimalLimbOrderFromID(id)
		if err != nil || got != want {
			t.Errorf("decimalLimbOrderFromID(%q) = %v, %v; want %v", id, got, err, want)
		}
	}
	for _, id := range []string{"", "unknown", "LOW_TO_HIGH", "sideways"} {
		got, err := decimalLimbOrderFromID(id)
		if !errors.Is(err, ErrUnknownCodec) || got != 0 {
			t.Errorf("decimalLimbOrderFromID(%q) = %v, %v; want 0, ErrUnknownCodec", id, got, err)
		}
	}
}
