package modbus

import (
	"errors"
	"testing"
	"time"
)

func TestDateTime2S2000_RoundTrip(t *testing.T) {
	codec := NewDateTime2S2000Codec()
	// 2000-01-01 00:00:00 UTC + 12345 seconds
	want := epochS2000.Add(12345 * time.Second)
	regs, err := codec.EncodeRegisters(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 2 {
		t.Fatalf("expected 2 registers, got %d", len(regs))
	}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

func TestDateTime2S2000_ExactEpoch(t *testing.T) {
	codec := NewDateTime2S2000Codec()
	regs := []uint16{0, 0}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(epochS2000) {
		t.Errorf("decode 0 seconds: got %v, want epoch 2000-01-01 00:00:00 UTC", got)
	}
	enc, err := codec.EncodeRegisters(epochS2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) != 2 || enc[0] != 0 || enc[1] != 0 {
		t.Errorf("encode epoch: got %v", enc)
	}
}

func TestDateTime2S2000_RejectBeforeEpoch(t *testing.T) {
	codec := NewDateTime2S2000Codec()
	before := time.Date(1999, 12, 31, 23, 59, 59, 0, time.UTC)
	_, err := codec.EncodeRegisters(before)
	if err == nil {
		t.Error("expected error for time before 2000-01-01")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTime2S2000_RegisterCountMismatch(t *testing.T) {
	codec := NewDateTime2S2000Codec()
	for _, regs := range [][]uint16{{0x1234}, {0x1234, 0x5678, 0x9abc}} {
		_, err := codec.DecodeRegisters(regs)
		if err == nil {
			t.Errorf("expected error for %d registers", len(regs))
		}
		if !errors.Is(err, ErrCodecRegisterCount) {
			t.Errorf("expected ErrCodecRegisterCount, got %v", err)
		}
	}
}

// Raw registers layout 4321: high word first, big-endian. So 0x0000, 0x0001 = 1 second after epoch.
func TestDateTime2S2000_RawDecodeOneSecond(t *testing.T) {
	codec := NewDateTime2S2000Codec()
	regs := []uint16{0x0000, 0x0001}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	want := epochS2000.Add(1 * time.Second)
	if !got.Equal(want) {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

func TestDateTime2S2000_MaxValue(t *testing.T) {
	codec := NewDateTime2S2000Codec()
	// Max uint32 seconds after epoch
	maxSec := uint32(0xFFFFFFFF)
	want := time.Unix(epochS2000.Unix()+int64(maxSec), 0).UTC()
	regs, err := codec.EncodeRegisters(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

func TestDateTime2S2000_RejectAboveMax(t *testing.T) {
	codec := NewDateTime2S2000Codec()
	// One second past uint32 max
	tooBig := time.Unix(epochS2000.Unix()+int64(0xFFFFFFFF)+1, 0).UTC()
	_, err := codec.EncodeRegisters(tooBig)
	if err == nil {
		t.Error("expected error for seconds exceeding uint32")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTime3S2000_ExactEpoch(t *testing.T) {
	codec := NewDateTime3S2000Codec()
	regs := []uint16{0, 0, 0}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(epochS2000) {
		t.Errorf("decode 0 seconds: got %v, want epoch 2000-01-01 00:00:00 UTC", got)
	}
}

func TestDateTime3S2000_RoundTrip(t *testing.T) {
	codec := NewDateTime3S2000Codec()
	want := epochS2000.Add(1000000 * time.Second)
	regs, err := codec.EncodeRegisters(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 3 {
		t.Fatalf("expected 3 registers, got %d", len(regs))
	}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

func TestDateTime3S2000_RegisterCountMismatch(t *testing.T) {
	codec := NewDateTime3S2000Codec()
	for _, regs := range [][]uint16{{0x1234, 0x5678}, {0x1234, 0x5678, 0x9abc, 0xdef0}} {
		_, err := codec.DecodeRegisters(regs)
		if err == nil {
			t.Errorf("expected error for %d registers", len(regs))
		}
		if !errors.Is(err, ErrCodecRegisterCount) {
			t.Errorf("expected ErrCodecRegisterCount, got %v", err)
		}
	}
}

// Raw 48-bit: three big-endian words. 0, 0, 1 = 1 second after epoch.
func TestDateTime3S2000_RawDecodeOneSecond(t *testing.T) {
	codec := NewDateTime3S2000Codec()
	regs := []uint16{0x0000, 0x0000, 0x0001}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	want := epochS2000.Add(1 * time.Second)
	if !got.Equal(want) {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

func TestDateTime3S2000_MaxValue(t *testing.T) {
	codec := NewDateTime3S2000Codec()
	const max48 = 0xFFFFFFFFFFFF
	want := time.Unix(epochS2000.Unix()+int64(max48), 0).UTC()
	regs, err := codec.EncodeRegisters(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

func TestDateTime3S2000_RejectAboveMax(t *testing.T) {
	codec := NewDateTime3S2000Codec()
	const max48 = 0xFFFFFFFFFFFF
	tooBig := time.Unix(epochS2000.Unix()+int64(max48)+1, 0).UTC()
	_, err := codec.EncodeRegisters(tooBig)
	if err == nil {
		t.Error("expected error for seconds exceeding 48-bit range")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeYMDhmsUTC_RoundTrip(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	want := time.Date(2026, 3, 17, 14, 30, 45, 0, time.UTC)
	regs, err := codec.EncodeRegisters(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 6 {
		t.Fatalf("expected 6 registers, got %d", len(regs))
	}
	if regs[0] != 2026 || regs[1] != 3 || regs[2] != 17 || regs[3] != 14 || regs[4] != 30 || regs[5] != 45 {
		t.Errorf("registers: got %v", regs)
	}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

func TestDateTimeYMDhms_InvalidMonth(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2026, 13, 1, 0, 0, 0}
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for month 13")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeYMDhms_ValidLeapYear(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2024, 2, 29, 12, 0, 0}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDateTimeYMDhms_InvalidNonLeapFeb29(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2023, 2, 29, 0, 0, 0}
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for 2023-02-29 (not a leap year)")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeYMDhms_InvalidFeb31(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2024, 2, 31, 0, 0, 0}
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for 2024-02-31 (invalid calendar date)")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeYMDhms_InvalidFeb30(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2024, 2, 30, 0, 0, 0}
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for 2024-02-30 (invalid calendar date)")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeYMDhms_Invalid2025Feb29(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2025, 2, 29, 0, 0, 0}
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for 2025-02-29 (not a leap year)")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeYMDhms_InvalidMonth0(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2024, 0, 1, 0, 0, 0}
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for month 0")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeYMDhms_InvalidDay0(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2024, 1, 0, 0, 0, 0}
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for day 0")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeYMDhms_InvalidDay32(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2024, 1, 32, 0, 0, 0}
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for day 32")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeYMDhms_RegisterCountMismatch(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2026, 3, 17, 14, 30} // 5 registers
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for wrong register count")
	}
	if !errors.Is(err, ErrCodecRegisterCount) {
		t.Errorf("expected ErrCodecRegisterCount, got %v", err)
	}
}

func TestDateTimeYMDhms_InvalidApril31(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2024, 4, 31, 12, 0, 0}
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for 2024-04-31 (invalid calendar date)")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeYMDhms_DefaultMatchesUTC(t *testing.T) {
	defaultCodec := NewDateTimeYMDhmsCodec()
	utcCodec := NewDateTimeYMDhmsUTCCodec()
	regs := []uint16{2026, 3, 17, 14, 30, 0}
	gotDefault, err := defaultCodec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	gotUTC, err := utcCodec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if !gotDefault.Equal(gotUTC) {
		t.Errorf("default codec should behave like UTC: got default %v, utc %v", gotDefault, gotUTC)
	}
}

func TestDateTimeYMDhms_Valid235959(t *testing.T) {
	codec := NewDateTimeYMDhmsUTCCodec()
	want := time.Date(2024, 6, 15, 23, 59, 59, 0, time.UTC)
	regs, err := codec.EncodeRegisters(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDateTimeIEC870UTC_RoundTrip(t *testing.T) {
	codec := NewDateTimeIEC870UTCCodec()
	want := time.Date(2025, 9, 21, 11, 37, 0, 998*int(time.Millisecond), time.UTC)
	regs, err := codec.EncodeRegisters(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 4 {
		t.Fatalf("expected 4 registers, got %d", len(regs))
	}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

func TestDateTimeIEC870_MillisecondPrecision(t *testing.T) {
	codec := NewDateTimeIEC870UTCCodec()
	want := time.Date(2020, 1, 15, 12, 30, 45, 123*int(time.Millisecond), time.UTC)
	regs, err := codec.EncodeRegisters(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if got.UnixMilli() != want.UnixMilli() {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

// cp56Regs builds 4 registers from 7 CP56Time2a bytes (ms_lo, ms_hi, min, hour, day+dow, month, year 0-99).
func cp56Regs(ms int, min, hour, day, month, year int) []uint16 {
	dow := 1
	if day >= 1 && day <= 31 {
		// 2024-01-15 = Monday = 1
		t := time.Date(2000+year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		dow = int(t.Weekday())
		if dow == 0 {
			dow = 7
		}
	}
	b := []byte{
		byte(ms), byte(ms >> 8),
		byte(min & 0x3F), byte(hour & 0x1F),
		byte((day & 0x1F) | (dow&0x07)<<5),
		byte(month & 0x0F), byte(year & 0x7F),
	}
	return bytesToUint16s(BigEndian, append(b, 0))
}

func TestDateTimeIEC870_InvalidMonthDecode(t *testing.T) {
	codec := NewDateTimeIEC870UTCCodec()
	regs := cp56Regs(0, 0, 0, 1, 13, 24) // month 13
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for month 13")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeIEC870_InvalidDayDecode(t *testing.T) {
	codec := NewDateTimeIEC870UTCCodec()
	regs := cp56Regs(0, 0, 0, 0, 1, 24) // day 0
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for day 0")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeIEC870_InvalidCalendarDateDecode(t *testing.T) {
	codec := NewDateTimeIEC870UTCCodec()
	// 2023-02-29 is not a valid date
	regs := cp56Regs(0, 0, 0, 29, 2, 23)
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for 2023-02-29 (invalid calendar date)")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeIEC870_EncodeRejectYear1999(t *testing.T) {
	codec := NewDateTimeIEC870UTCCodec()
	tm := time.Date(1999, 6, 15, 12, 0, 0, 0, time.UTC)
	_, err := codec.EncodeRegisters(tm)
	if err == nil {
		t.Error("expected error for year 1999")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeIEC870_EncodeRejectYear2128(t *testing.T) {
	codec := NewDateTimeIEC870UTCCodec()
	tm := time.Date(2128, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := codec.EncodeRegisters(tm)
	if err == nil {
		t.Error("expected error for year 2128")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Fatalf("expected ErrCodecValue, got %v", err)
	}
}

func TestDateTimeIEC870_YearPrecedenceDecode(t *testing.T) {
	// Regression: year := 2000 + int(b[6])&0x7F was wrong (operator precedence).
	// Correct: year := 2000 + (int(b[6]) & 0x7F). So 0x37 (55) -> 2055.
	codec := NewDateTimeIEC870UTCCodec()
	regs := cp56Regs(500, 30, 14, 15, 6, 55) // 2055-06-15 14:30:00.500
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2055, 6, 15, 14, 30, 0, 500*int(time.Millisecond), time.UTC)
	if !got.Equal(want) {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

func TestDateTimeIEC870_RegisterCountMismatch(t *testing.T) {
	codec := NewDateTimeIEC870UTCCodec()
	regs := []uint16{0x1234, 0x5678, 0x9abc} // 3 registers
	_, err := codec.DecodeRegisters(regs)
	if err == nil {
		t.Error("expected error for wrong register count")
	}
	if !errors.Is(err, ErrCodecRegisterCount) {
		t.Errorf("expected ErrCodecRegisterCount, got %v", err)
	}
}

func TestDateTimeIEC870_IgnorePaddingByte(t *testing.T) {
	codec := NewDateTimeIEC870UTCCodec()
	// Same first 7 bytes (2024-06-15 12:00:00.000), vary 8th byte; decode must be unchanged
	base := cp56Regs(0, 0, 12, 15, 6, 24)
	if len(base) != 4 {
		t.Fatalf("cp56Regs: expected 4 registers, got %d", len(base))
	}
	raw := uint16sToBytes(BigEndian, base)
	firstDecode, err := codec.DecodeRegisters(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, pad := range []byte{0x00, 0xFF, 0x5A} {
		raw[7] = pad
		regs := bytesToUint16s(BigEndian, raw)
		got, err := codec.DecodeRegisters(regs)
		if err != nil {
			t.Fatalf("pad 0x%02x: %v", pad, err)
		}
		if !got.Equal(firstDecode) {
			t.Errorf("pad 0x%02x: decode %v should equal %v", pad, got, firstDecode)
		}
	}
}

func TestDateTimeIEC870_DecodeYear2000(t *testing.T) {
	codec := NewDateTimeIEC870UTCCodec()
	// year byte 0 => 2000
	regs := cp56Regs(0, 0, 0, 1, 1, 0)
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

func TestDateTimeIEC870_DecodeYear2127(t *testing.T) {
	codec := NewDateTimeIEC870UTCCodec()
	// year byte 127 => 2127
	regs := cp56Regs(0, 59, 23, 31, 12, 127)
	got, err := codec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2127, 12, 31, 23, 59, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("decode: got %v, want %v", got, want)
	}
}

func TestDateTimeIEC870_DefaultMatchesUTC(t *testing.T) {
	defaultCodec := NewDateTimeIEC870Codec()
	utcCodec := NewDateTimeIEC870UTCCodec()
	tm := time.Date(2025, 6, 10, 14, 30, 0, 0, time.UTC)
	regs, err := utcCodec.EncodeRegisters(tm)
	if err != nil {
		t.Fatal(err)
	}
	gotDefault, err := defaultCodec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	gotUTC, err := utcCodec.DecodeRegisters(regs)
	if err != nil {
		t.Fatal(err)
	}
	if !gotDefault.Equal(gotUTC) {
		t.Errorf("default codec should behave like UTC: got default %v, utc %v", gotDefault, gotUTC)
	}
}

func TestStrictDateTime_ValidLeapDay(t *testing.T) {
	got, err := strictDateTime(2024, 2, 29, 0, 0, 0, 0, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStrictDateTime_InvalidNonLeapFeb29(t *testing.T) {
	_, err := strictDateTime(2023, 2, 29, 0, 0, 0, 0, time.UTC)
	if err == nil {
		t.Error("expected error for 2023-02-29")
	}
}

func TestStrictDateTime_NilLocation(t *testing.T) {
	_, err := strictDateTime(2024, 1, 15, 0, 0, 0, 0, nil)
	if err == nil {
		t.Error("expected error for nil location")
	}
}

func TestTimeCodecs_DescriptorSpecs(t *testing.T) {
	cases := []struct {
		id    string
		count uint16
		bytes uint16
	}{
		{"datetime2_s2000", 2, 4},
		{"datetime3_s2000", 3, 6},
		{"datetime_ymdhms_utc", 6, 12},
		{"datetime_ymdhms_local", 6, 12},
		{"datetime_ymdhms", 6, 12},
		{"datetime_iec870_utc", 4, 8},
		{"datetime_iec870_local", 4, 8},
		{"datetime_iec870", 4, 8},
	}
	for _, tc := range cases {
		d, ok := CodecDescriptorByID(tc.id)
		if !ok {
			t.Errorf("%s: descriptor not found", tc.id)
			continue
		}
		if d.RegisterSpec.Count != tc.count {
			t.Errorf("%s: RegisterSpec.Count = %d, want %d", tc.id, d.RegisterSpec.Count, tc.count)
		}
		if d.ByteSpec.Count != tc.bytes {
			t.Errorf("%s: ByteSpec.Count = %d, want %d", tc.id, d.ByteSpec.Count, tc.bytes)
		}
	}
}

func TestTimeCodecs_RuntimeRoundTrip(t *testing.T) {
	// Smoke test: runtime codec encode then decode per family; each codec has a suitable want (precision/range).
	cases := []struct {
		id   string
		want time.Time
	}{
		{"datetime2_s2000", epochS2000.Add(12345 * time.Second)},
		{"datetime_ymdhms_utc", time.Date(2026, 3, 17, 14, 30, 45, 0, time.UTC)},
		{"datetime_iec870_utc", time.Date(2025, 9, 21, 11, 37, 0, 998*int(time.Millisecond), time.UTC)},
	}
	for _, tc := range cases {
		rc, ok, err := RuntimeCodecByID(tc.id)
		if err != nil || !ok || rc == nil {
			t.Fatalf("%s: %v (ok=%v)", tc.id, err, ok)
		}
		regs, err := rc.EncodeRegistersAny(tc.want)
		if err != nil {
			t.Fatalf("%s EncodeRegistersAny: %v", tc.id, err)
		}
		dec, err := rc.DecodeRegistersAny(regs)
		if err != nil {
			t.Fatalf("%s DecodeRegistersAny: %v", tc.id, err)
		}
		got, ok := dec.(time.Time)
		if !ok {
			t.Fatalf("%s: DecodeRegistersAny returned %T, not time.Time", tc.id, dec)
		}
		if !got.Equal(tc.want) {
			t.Errorf("%s: round-trip got %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestTimeCodecs_RuntimeByID(t *testing.T) {
	ids := []string{
		"datetime2_s2000", "datetime3_s2000",
		"datetime_ymdhms_utc", "datetime_ymdhms_local", "datetime_ymdhms",
		"datetime_iec870_utc", "datetime_iec870_local", "datetime_iec870",
	}
	for _, id := range ids {
		rc, ok, err := RuntimeCodecByID(id)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		if !ok {
			t.Errorf("%s: not found", id)
			continue
		}
		if rc == nil {
			t.Errorf("%s: nil codec", id)
		}
	}
}

func TestTimeCodecs_DescriptorFamily(t *testing.T) {
	ids := []string{"datetime2_s2000", "datetime_ymdhms_utc", "datetime_iec870_utc"}
	for _, id := range ids {
		d, ok := CodecDescriptorByID(id)
		if !ok {
			t.Errorf("%s: descriptor not found", id)
			continue
		}
		if d.Family != CodecFamilyTime {
			t.Errorf("%s: Family = %v, want CodecFamilyTime", id, d.Family)
		}
		if d.ValueKind != CodecValueTime {
			t.Errorf("%s: ValueKind = %v, want CodecValueTime", id, d.ValueKind)
		}
	}
}
