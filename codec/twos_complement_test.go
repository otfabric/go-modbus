// SPDX-License-Identifier: MIT

package codec

import (
	"math"
	"math/big"
	"testing"
	"time"
)

// The two's-complement helpers must reproduce Go's native same-width
// conversions bit for bit. The oracle uses wider arithmetic, not conversions.

func TestTwosComplement16Exhaustive(t *testing.T) {
	for u := 0; u <= math.MaxUint16; u++ {
		want := u
		if u > math.MaxInt16 {
			want = u - 1<<16
		}
		got := uint16ToInt16(uint16(u))
		if int(got) != want {
			t.Fatalf("uint16ToInt16(%#04x) = %d, want %d", u, got, want)
		}
		if back := int16ToUint16(got); int(back) != u {
			t.Fatalf("int16ToUint16(%d) = %#04x, want %#04x", got, back, u)
		}
	}
}

func TestTwosComplement32Edges(t *testing.T) {
	for _, tc := range []struct {
		v int32
		u uint32
	}{
		{math.MinInt32, 0x80000000}, {math.MinInt32 + 1, 0x80000001}, {-2, 0xFFFFFFFE}, {-1, 0xFFFFFFFF},
		{0, 0}, {1, 1}, {math.MaxInt32 - 1, 0x7FFFFFFE}, {math.MaxInt32, 0x7FFFFFFF},
	} {
		if got := int32ToUint32(tc.v); got != tc.u {
			t.Errorf("int32ToUint32(%d) = %#08x, want %#08x", tc.v, got, tc.u)
		}
		if got := uint32ToInt32(tc.u); got != tc.v {
			t.Errorf("uint32ToInt32(%#08x) = %d, want %d", tc.u, got, tc.v)
		}
	}
}

func TestTwosComplement64Edges(t *testing.T) {
	two64 := new(big.Int).Lsh(big.NewInt(1), 64)
	for _, v := range []int64{math.MinInt64, math.MinInt64 + 1, -1 << 47, -2, -1, 0, 1, 1<<47 - 1, math.MaxInt64 - 1, math.MaxInt64} {
		want := big.NewInt(v)
		if v < 0 {
			want.Add(want, two64)
		}
		u := int64ToUint64(v)
		if new(big.Int).SetUint64(u).Cmp(want) != 0 {
			t.Errorf("int64ToUint64(%d) = %#016x, want %s", v, u, want.Text(16))
		}
		if back := uint64ToInt64(u); back != v {
			t.Errorf("uint64ToInt64(%#016x) = %d, want %d", u, back, v)
		}
	}
	if got := uint64ToInt64(math.MaxUint64); got != -1 {
		t.Errorf("uint64ToInt64(MaxUint64) = %d, want -1", got)
	}
}

func TestSignedCodecExtremesRoundTrip(t *testing.T) {
	i16 := []struct {
		v    int16
		regs uint16
	}{{math.MinInt16, 0x8000}, {-1, 0xFFFF}, {0, 0}, {math.MaxInt16, 0x7FFF}}
	for _, tc := range i16 {
		c := MustNewInt16Codec(Layout16_21)
		regs, err := c.EncodeRegisters(tc.v)
		if err != nil || regs[0] != tc.regs {
			t.Fatalf("int16 encode %d = %v, %v; want [%#04x]", tc.v, regs, err, tc.regs)
		}
		got, err := c.DecodeRegisters(regs)
		if err != nil || got != tc.v {
			t.Fatalf("int16 decode %v = %d, %v; want %d", regs, got, err, tc.v)
		}
	}

	c32 := MustNewInt32Codec(Layout32_4321)
	for _, v := range []int32{math.MinInt32, -1, 0, math.MaxInt32} {
		regs, err := c32.EncodeRegisters(v)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := c32.DecodeRegisters(regs); err != nil || got != v {
			t.Fatalf("int32 round trip %d = %d, %v", v, got, err)
		}
	}
	if regs, _ := c32.EncodeRegisters(math.MinInt32); regs[0] != 0x8000 || regs[1] != 0 {
		t.Fatalf("int32 MinInt32 regs = %v, want [0x8000 0]", regs)
	}

	c48 := MustNewInt48Codec(Layout48_654321)
	for _, v := range []int64{minInt48, minInt48 + 1, -1, 0, maxInt48 - 1, maxInt48} {
		regs, err := c48.EncodeRegisters(v)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := c48.DecodeRegisters(regs); err != nil || got != v {
			t.Fatalf("int48 round trip %d = %d, %v", v, got, err)
		}
	}
	if regs, _ := c48.EncodeRegisters(minInt48); regs[0] != 0x8000 || regs[1] != 0 || regs[2] != 0 {
		t.Fatalf("int48 min regs = %v, want [0x8000 0 0]", regs)
	}

	c64 := MustNewInt64Codec(Layout64_87654321)
	for _, v := range []int64{math.MinInt64, -1, 0, math.MaxInt64} {
		regs, err := c64.EncodeRegisters(v)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := c64.DecodeRegisters(regs); err != nil || got != v {
			t.Fatalf("int64 round trip %d = %d, %v", v, got, err)
		}
	}
	u64 := MustNewUint64Codec(Layout64_87654321)
	regs, err := u64.EncodeRegisters(math.MaxUint64)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c64.DecodeRegisters(regs); err != nil || got != -1 {
		t.Fatalf("int64 decode of MaxUint64 pattern = %d, %v; want -1", got, err)
	}
}

func TestInt16SignMagnitudeExtremes(t *testing.T) {
	c := NewInt16SignMagnitudeCodec()
	for _, tc := range []struct {
		v    int16
		regs uint16
	}{{-math.MaxInt16, 0xFFFF}, {math.MaxInt16, 0x7FFF}, {-1, 0x8001}, {0, 0}} {
		regs, err := c.EncodeRegisters(tc.v)
		if err != nil || regs[0] != tc.regs {
			t.Fatalf("encode %d = %v, %v; want [%#04x]", tc.v, regs, err, tc.regs)
		}
		if got, err := c.DecodeRegisters(regs); err != nil || got != tc.v {
			t.Fatalf("decode %v = %d, %v; want %d", regs, got, err, tc.v)
		}
	}
	if _, err := c.EncodeRegisters(math.MinInt16); err == nil {
		t.Fatal("encode MinInt16: want magnitude error")
	}
}

func TestDateTimeYMDhmsEncodeFieldsAtEdges(t *testing.T) {
	c := NewDateTimeYMDhmsUTCCodec()
	for _, tm := range []time.Time{
		time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(65535, 12, 31, 23, 59, 59, 0, time.UTC),
	} {
		regs, err := c.EncodeRegisters(tm)
		if err != nil {
			t.Fatal(err)
		}
		want := []int{tm.Year(), int(tm.Month()), tm.Day(), tm.Hour(), tm.Minute(), tm.Second()}
		for i := range want {
			if int(regs[i]) != want[i] {
				t.Fatalf("%v: reg %d = %d, want %d", tm, i, regs[i], want[i])
			}
		}
		got, err := c.DecodeRegisters(regs)
		if err != nil || !got.Equal(tm) {
			t.Fatalf("round trip %v = %v, %v", tm, got, err)
		}
	}
}

func TestCP56Time2aEncodeByteFields(t *testing.T) {
	// 59.999 s = 59999 ms = 0xEA5F. 2127-12-31 is a Wednesday (3), 2128-01-02 a
	// Sunday (7): day 31 | 3<<5 = 0x7F, day 2 | 7<<5 = 0xE2.
	for _, tc := range []struct {
		tm   time.Time
		want []byte
	}{
		{time.Date(2127, 12, 31, 23, 59, 59, 999*int(time.Millisecond), time.UTC), []byte{0x5F, 0xEA, 59, 23, 0x7F, 12, 127}},
		{time.Date(2000, 1, 2, 0, 0, 0, 0, time.UTC), []byte{0, 0, 0, 0, 0xE2, 1, 0}},
	} {
		b, err := encodeCP56Time2a(tc.tm, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		for i := range tc.want {
			if b[i] != tc.want[i] {
				t.Fatalf("%v: byte %d = %#02x, want %#02x (all %x)", tc.tm, i, b[i], tc.want[i], b)
			}
		}
	}
}

func TestM10kSignedEncodeLimbsAtEdges(t *testing.T) {
	for _, tc := range []struct {
		v    int64
		want []uint16
	}{
		{-99_990_000, []uint16{0, 0xD8F1}}, // MS limb -9999
		{99_999_999, []uint16{9999, 9999}},
		{-1, []uint16{9999, 0xFFFF}}, // -1 = 9999 + (-1)*10000
	} {
		regs, err := encodeIntM10k(tc.v, 2, DecimalLimbLowToHigh, "test")
		if err != nil {
			t.Fatal(err)
		}
		if regs[0] != tc.want[0] || regs[1] != tc.want[1] {
			t.Fatalf("encode %d = %v, want %v", tc.v, regs, tc.want)
		}
	}
}
