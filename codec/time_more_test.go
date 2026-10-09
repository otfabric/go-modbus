// SPDX-License-Identifier: MIT

package codec

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTimeCodecs_MetaAndRegisterCount(t *testing.T) {
	cases := []struct {
		c    Codec[time.Time]
		id   string
		regs uint16
	}{
		{NewDateTime2S2000Codec(), "datetime2_s2000", 2},
		{NewDateTime3S2000Codec(), "datetime3_s2000", 3},
		{NewDateTimeYMDhmsCodec(), "datetime_ymdhms", 6},
		{NewDateTimeYMDhmsUTCCodec(), "datetime_ymdhms_utc", 6},
		{NewDateTimeYMDhmsLocalCodec(), "datetime_ymdhms_local", 6},
		{NewDateTimeIEC870Codec(), "datetime_iec870", 4},
		{NewDateTimeIEC870UTCCodec(), "datetime_iec870_utc", 4},
		{NewDateTimeIEC870LocalCodec(), "datetime_iec870_local", 4},
	}
	for _, tc := range cases {
		assertMeta(t, tc.c, tc.id, tc.id, tc.regs)
		assertDirectCountError(t, tc.c)
	}
}

func TestTimeCodecs_ExactVectors(t *testing.T) {
	cases := []struct {
		c    Codec[time.Time]
		t    time.Time
		regs []uint16
	}{
		// 0x00010002 seconds after the epoch = 65538 s = 18h12m18s.
		{NewDateTime2S2000Codec(), time.Date(2000, 1, 1, 18, 12, 18, 0, time.UTC), []uint16{0x0001, 0x0002}},
		{NewDateTime3S2000Codec(), time.Date(2000, 1, 1, 18, 12, 18, 0, time.UTC), []uint16{0x0000, 0x0001, 0x0002}},
		// 0x0001_0000_0000 s = 4294967296 s = 49710 days 6h28m16s after the epoch.
		{NewDateTime3S2000Codec(), time.Date(2000, 1, 1, 6, 28, 16, 0, time.UTC).AddDate(0, 0, 49710), []uint16{0x0001, 0x0000, 0x0000}},
		{NewDateTimeYMDhmsUTCCodec(), time.Date(2024, 2, 29, 23, 59, 58, 0, time.UTC), []uint16{2024, 2, 29, 23, 59, 58}},
		// 2024-01-15 is a Monday (dow 1): day byte = 15 | 1<<5 = 0x2F. 30.250 s = 30250 ms = 0x762A (LE on wire).
		{NewDateTimeIEC870UTCCodec(), time.Date(2024, 1, 15, 10, 45, 30, 250_000_000, time.UTC), []uint16{0x2A76, 0x2D0A, 0x2F01, 0x1800}},
		// 2023-12-31 is a Sunday, which CP56Time2a encodes as dow 7: 31 | 7<<5 = 0xFF.
		{NewDateTimeIEC870Codec(), time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC), []uint16{0x0000, 0x0000, 0xFF0C, 0x1700}},
	}
	for _, tc := range cases {
		got, err := EncodeRegisters(tc.t, tc.c)
		if err != nil {
			t.Errorf("%s: encode %s: %v", tc.c.ID(), tc.t, err)
			continue
		}
		if !slices.Equal(got, tc.regs) {
			t.Errorf("%s: encode %s = %04X, want %04X", tc.c.ID(), tc.t, got, tc.regs)
		}
		back, err := DecodeRegisters(tc.regs, tc.c)
		if err != nil {
			t.Errorf("%s: decode %04X: %v", tc.c.ID(), tc.regs, err)
			continue
		}
		if !back.Equal(tc.t) {
			t.Errorf("%s: decode %04X = %s, want %s", tc.c.ID(), tc.regs, back, tc.t)
		}
	}
}

func TestTimeCodecs_EncodeConvertsToCodecLocation(t *testing.T) {
	// 01:30 at UTC+2 on Jan 1 is 23:30 UTC on Dec 31 of the previous year.
	east := time.FixedZone("east", 2*3600)
	in := time.Date(2025, 1, 1, 1, 30, 0, 0, east)

	regs, err := EncodeRegisters(in, NewDateTimeYMDhmsUTCCodec())
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint16{2024, 12, 31, 23, 30, 0}; !slices.Equal(regs, want) {
		t.Errorf("ymdhms_utc: got %v, want %v", regs, want)
	}

	inZone := dateTimeYMDhmsCodec{id: "datetime_ymdhms_east", loc: east}
	regs, err = EncodeRegisters[time.Time](in.UTC(), inZone)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint16{2025, 1, 1, 1, 30, 0}; !slices.Equal(regs, want) {
		t.Errorf("ymdhms in zone: got %v, want %v", regs, want)
	}
	back, err := DecodeRegisters[time.Time](regs, inZone)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Equal(in) {
		t.Errorf("ymdhms in zone: decoded %s, want %s", back, in)
	}
}

func TestDateTimeS2000_RejectBeforeEpoch(t *testing.T) {
	before := time.Date(1999, 12, 31, 23, 59, 59, 0, time.UTC)
	for _, c := range []Codec[time.Time]{NewDateTime2S2000Codec(), NewDateTime3S2000Codec()} {
		_, err := EncodeRegisters(before, c)
		if reason := valueErrorReason(t, err, c.ID()); reason != "time before 2000-01-01 00:00:00 UTC" {
			t.Errorf("%s: Reason = %q", c.ID(), reason)
		}
	}
}

func TestDateTimeS2000_InconsistentLayoutIsRejected(t *testing.T) {
	valid := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	c2 := dateTime2S2000Codec{layout: Layout16_21}
	if _, err := c2.DecodeRegisters(make([]uint16, 2)); !errors.Is(err, ErrEncodingError) {
		t.Errorf("datetime2 decode: want ErrEncodingError, got %v", err)
	}
	if _, err := c2.EncodeRegisters(valid); !errors.Is(err, ErrEncodingError) {
		t.Errorf("datetime2 encode: want ErrEncodingError, got %v", err)
	}

	c3 := dateTime3S2000Codec{layout: Layout32_4321}
	if _, err := c3.DecodeRegisters(make([]uint16, 3)); !errors.Is(err, ErrEncodingError) {
		t.Errorf("datetime3 decode: want ErrEncodingError, got %v", err)
	}
	if _, err := c3.EncodeRegisters(valid); !errors.Is(err, ErrEncodingError) {
		t.Errorf("datetime3 encode: want ErrEncodingError, got %v", err)
	}
}

func TestDateTimeYMDhms_DecodeRejectsOutOfRangeFields(t *testing.T) {
	c := NewDateTimeYMDhmsUTCCodec()
	cases := []struct {
		regs   []uint16
		reason string
	}{
		{[]uint16{2024, 13, 1, 0, 0, 0}, "month 13 out of range 1-12"},
		{[]uint16{2024, 1, 32, 0, 0, 0}, "day 32 out of range 1-31"},
		{[]uint16{2024, 1, 15, 24, 0, 0}, "hour 24 out of range 0-23"},
		{[]uint16{2024, 1, 15, 0xFFFF, 0, 0}, "hour 65535 out of range 0-23"},
		{[]uint16{2024, 1, 15, 23, 60, 0}, "minute 60 out of range 0-59"},
		{[]uint16{2024, 1, 15, 23, 59, 60}, "second 60 out of range 0-59"},
		{[]uint16{2023, 2, 29, 0, 0, 0}, "invalid calendar date/time"},
	}
	for _, tc := range cases {
		got, err := DecodeRegisters(tc.regs, c)
		if !got.IsZero() {
			t.Errorf("%v: decoded %s alongside error", tc.regs, got)
		}
		if reason := valueErrorReason(t, err, c.ID()); reason != tc.reason {
			t.Errorf("%v: Reason = %q, want %q", tc.regs, reason, tc.reason)
		}
	}
}

func TestDateTimeYMDhms_EncodeRejectsYearOutsideRegisterRange(t *testing.T) {
	c := NewDateTimeYMDhmsUTCCodec()
	for year, want := range map[int]string{
		65536: "year 65536 out of range 0-65535",
		-1:    "year -1 out of range 0-65535",
	} {
		_, err := EncodeRegisters(time.Date(year, 6, 1, 0, 0, 0, 0, time.UTC), c)
		if reason := valueErrorReason(t, err, c.ID()); reason != want {
			t.Errorf("year %d: Reason = %q, want %q", year, reason, want)
		}
	}
	// The extremes of the register range must still encode.
	for _, year := range []int{0, 65535} {
		regs, err := EncodeRegisters(time.Date(year, 6, 1, 0, 0, 0, 0, time.UTC), c)
		if err != nil || regs[0] != uint16(year) {
			t.Errorf("year %d: got %v, %v", year, regs, err)
		}
	}
}

func TestDateTimeCodecs_NilLocationIsRejected(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

	ymd := dateTimeYMDhmsCodec{id: "ymd/nil"}
	_, err := ymd.EncodeRegisters(now)
	if reason := valueErrorReason(t, err, "ymd/nil"); reason != "location must not be nil" {
		t.Errorf("ymd encode: Reason = %q", reason)
	}
	_, err = ymd.DecodeRegisters([]uint16{2024, 1, 15, 10, 0, 0})
	if reason := valueErrorReason(t, err, "ymd/nil"); !strings.Contains(reason, "location must not be nil") {
		t.Errorf("ymd decode: Reason = %q", reason)
	}

	iec := dateTimeIEC870Codec{id: "iec/nil"}
	_, err = iec.EncodeRegisters(now)
	if reason := valueErrorReason(t, err, "iec/nil"); reason != "location must not be nil" {
		t.Errorf("iec encode: Reason = %q", reason)
	}
	_, err = iec.DecodeRegisters(cp56Regs(0, 0, 10, 15, 1, 24))
	if reason := valueErrorReason(t, err, "iec/nil"); reason != "CP56Time2a location must not be nil" {
		t.Errorf("iec decode: Reason = %q", reason)
	}

	if _, err := encodeCP56Time2a(now, nil); err == nil || !strings.Contains(err.Error(), "location must not be nil") {
		t.Errorf("encodeCP56Time2a(nil loc): got %v", err)
	}
	if _, err := decodeCP56Time2a(make([]byte, 7), nil); err == nil || !strings.Contains(err.Error(), "location must not be nil") {
		t.Errorf("decodeCP56Time2a(nil loc): got %v", err)
	}
}

func TestDecodeCP56Time2a_RequiresSevenBytes(t *testing.T) {
	for _, n := range []int{0, 6} {
		got, err := decodeCP56Time2a(make([]byte, n), time.UTC)
		if err == nil || !strings.Contains(err.Error(), "requires 7 bytes") || !got.IsZero() {
			t.Errorf("len %d: got %s, %v", n, got, err)
		}
	}
}

func TestDateTimeIEC870_DecodeRejectsOutOfRangeTime(t *testing.T) {
	c := NewDateTimeIEC870UTCCodec()
	cases := []struct {
		name string
		regs []uint16
	}{
		{"hour 24", cp56Regs(0, 0, 24, 15, 1, 24)},
		{"hour 31", cp56Regs(0, 0, 31, 15, 1, 24)},
		{"minute 60", cp56Regs(0, 60, 10, 15, 1, 24)},
		{"ms 60000", cp56Regs(60000, 0, 10, 15, 1, 24)},
		{"ms 65535", cp56Regs(0xFFFF, 0, 10, 15, 1, 24)},
	}
	for _, tc := range cases {
		got, err := DecodeRegisters(tc.regs, c)
		if !got.IsZero() {
			t.Errorf("%s: decoded %s alongside error", tc.name, got)
		}
		if reason := valueErrorReason(t, err, c.ID()); reason != "CP56Time2a time out of range" {
			t.Errorf("%s: Reason = %q", tc.name, reason)
		}
	}
	// Upper bounds that must still decode: 23:59:59.999.
	got, err := DecodeRegisters(cp56Regs(59999, 59, 23, 15, 1, 24), c)
	want := time.Date(2024, 1, 15, 23, 59, 59, 999_000_000, time.UTC)
	if err != nil || !got.Equal(want) {
		t.Errorf("23:59:59.999: got %s, %v; want %s", got, err, want)
	}
}

func TestDateTimeIEC870_DecodeIgnoresFlagBits(t *testing.T) {
	// Invalid (min bit 7), summer-time (hour bit 7), reserved month/year high bits
	// and the day-of-week field are all masked off.
	regs := bytesToUint16s(BigEndian, []byte{
		0x2A, 0x76, // 30250 ms
		0x80 | 45, // IV flag + minute 45
		0x80 | 10, // SU flag + hour 10
		0xE0 | 15, // dow 7 (wrong on purpose) + day 15
		0xF0 | 1,  // reserved bits + month 1
		0x80 | 24, // reserved bit + year 24
		0xAB,      // padding byte
	})
	got, err := DecodeRegisters(regs, NewDateTimeIEC870UTCCodec())
	want := time.Date(2024, 1, 15, 10, 45, 30, 250_000_000, time.UTC)
	if err != nil || !got.Equal(want) {
		t.Errorf("got %s, %v; want %s", got, err, want)
	}
}
