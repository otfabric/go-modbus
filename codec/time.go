// SPDX-License-Identifier: MIT

package codec

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"
)

// epochS2000 is the epoch for "seconds since 2000" (s2000) codecs: 2000-01-01 00:00:00 UTC.
// It is a var (not const) because time.Date returns a value and Go constants cannot be of struct type.
var epochS2000 = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// strictDateTime builds a time in loc and returns an error if the components would be normalized (e.g. Feb 31).
func strictDateTime(year, month, day, hour, min, sec, nsec int, loc *time.Location) (time.Time, error) {
	if loc == nil {
		return time.Time{}, fmt.Errorf("modbus: time location must not be nil")
	}
	tt := time.Date(year, time.Month(month), day, hour, min, sec, nsec, loc)
	if tt.Year() != year || int(tt.Month()) != month || tt.Day() != day ||
		tt.Hour() != hour || tt.Minute() != min || tt.Second() != sec || tt.Nanosecond() != nsec {
		return time.Time{}, fmt.Errorf("invalid calendar date/time")
	}
	return tt, nil
}

// dateTime2S2000Codec: 2 registers, uint32 seconds since 2000-01-01 00:00:00 UTC.
// Layout 4321 (big-endian, high word first).
type dateTime2S2000Codec struct{ layout RegisterLayout }

func (c dateTime2S2000Codec) ID() string                 { return "datetime2_s2000" }
func (c dateTime2S2000Codec) Name() string               { return "datetime2_s2000" }
func (c dateTime2S2000Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 2} }
func (c dateTime2S2000Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 4} }

func (c dateTime2S2000Codec) DecodeRegisters(regs []uint16) (time.Time, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return time.Time{}, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	canonical, err := PermuteBytesDecode(raw, c.layout)
	if err != nil {
		return time.Time{}, err
	}
	sec := binary.BigEndian.Uint32(canonical)
	return time.Unix(epochS2000.Unix()+int64(sec), 0).UTC(), nil
}

func (c dateTime2S2000Codec) EncodeRegisters(t time.Time) ([]uint16, error) {
	t = t.UTC()
	delta := t.Unix() - epochS2000.Unix()
	if delta < 0 {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "time before 2000-01-01 00:00:00 UTC"}
	}
	if uint64(delta) > 0xFFFFFFFF {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "seconds since 2000 exceeds uint32 range"}
	}
	canonical := make([]byte, 4)
	binary.BigEndian.PutUint32(canonical, uint32(delta))
	raw, err := PermuteBytesEncode(canonical, c.layout)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, raw), nil
}

// dateTime3S2000Codec: 3 registers, 48-bit seconds since 2000-01-01 00:00:00 UTC.
// Layout 654321 (big-endian, high word first).
type dateTime3S2000Codec struct{ layout RegisterLayout }

func (c dateTime3S2000Codec) ID() string                 { return "datetime3_s2000" }
func (c dateTime3S2000Codec) Name() string               { return "datetime3_s2000" }
func (c dateTime3S2000Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 3} }
func (c dateTime3S2000Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 6} }

func (c dateTime3S2000Codec) DecodeRegisters(regs []uint16) (time.Time, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return time.Time{}, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	canonical, err := PermuteBytesDecode(raw, c.layout)
	if err != nil {
		return time.Time{}, err
	}
	sec := canonicalToUint48(canonical)
	if sec > math.MaxInt64 {
		return time.Time{}, &CodecValueError{Codec: c.ID(), Reason: "seconds since 2000 exceeds int64 range"}
	}
	return time.Unix(epochS2000.Unix()+int64(sec), 0).UTC(), nil
}

func (c dateTime3S2000Codec) EncodeRegisters(t time.Time) ([]uint16, error) {
	t = t.UTC()
	delta := t.Unix() - epochS2000.Unix()
	if delta < 0 {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "time before 2000-01-01 00:00:00 UTC"}
	}
	const max48 = 0xFFFFFFFFFFFF
	if uint64(delta) > max48 {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "seconds since 2000 exceeds 48-bit range"}
	}
	canonical := uint48ToCanonical(uint64(delta))
	raw, err := PermuteBytesEncode(canonical, c.layout)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, raw), nil
}

// NewDateTime2S2000Codec returns a codec for 2-register seconds-since-2000 (uint32). Uses layout 4321.
func NewDateTime2S2000Codec() Codec[time.Time] {
	return dateTime2S2000Codec{layout: Layout32_4321}
}

// NewDateTime3S2000Codec returns a codec for 3-register seconds-since-2000 (48-bit). Uses layout 654321.
func NewDateTime3S2000Codec() Codec[time.Time] {
	return dateTime3S2000Codec{layout: Layout48_654321}
}

// dateTimeYMDhmsCodec: 6 registers = Year, Month, Day, Hour, Minute, Second (each uint16). Interpreted in loc.
type dateTimeYMDhmsCodec struct {
	id  string
	loc *time.Location
}

func (c dateTimeYMDhmsCodec) ID() string                 { return c.id }
func (c dateTimeYMDhmsCodec) Name() string               { return c.id }
func (c dateTimeYMDhmsCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 6} }
func (c dateTimeYMDhmsCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: 12} }

func (c dateTimeYMDhmsCodec) DecodeRegisters(regs []uint16) (time.Time, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return time.Time{}, err
	}
	year := int(regs[0])
	month := int(regs[1])
	day := int(regs[2])
	hour := int(regs[3])
	min := int(regs[4])
	sec := int(regs[5])
	if month < 1 || month > 12 {
		return time.Time{}, &CodecValueError{Codec: c.ID(), Reason: fmt.Sprintf("month %d out of range 1-12", month)}
	}
	if day < 1 || day > 31 {
		return time.Time{}, &CodecValueError{Codec: c.ID(), Reason: fmt.Sprintf("day %d out of range 1-31", day)}
	}
	if hour < 0 || hour > 23 {
		return time.Time{}, &CodecValueError{Codec: c.ID(), Reason: fmt.Sprintf("hour %d out of range 0-23", hour)}
	}
	if min < 0 || min > 59 {
		return time.Time{}, &CodecValueError{Codec: c.ID(), Reason: fmt.Sprintf("minute %d out of range 0-59", min)}
	}
	if sec < 0 || sec > 59 {
		return time.Time{}, &CodecValueError{Codec: c.ID(), Reason: fmt.Sprintf("second %d out of range 0-59", sec)}
	}
	tt, err := strictDateTime(year, month, day, hour, min, sec, 0, c.loc)
	if err != nil {
		return time.Time{}, &CodecValueError{Codec: c.ID(), Reason: err.Error()}
	}
	return tt, nil
}

func (c dateTimeYMDhmsCodec) EncodeRegisters(t time.Time) ([]uint16, error) {
	if c.loc == nil {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "location must not be nil"}
	}
	t = t.In(c.loc)
	year := t.Year()
	if year < 0 || year > 65535 {
		return nil, &CodecValueError{Codec: c.ID(), Reason: fmt.Sprintf("year %d out of range 0-65535", year)}
	}
	month := int(t.Month())
	day := t.Day()
	hour := t.Hour()
	min := t.Minute()
	sec := t.Second()
	return []uint16{
		uint16(year),
		uint16(month),
		uint16(day),
		uint16(hour),
		uint16(min),
		uint16(sec),
	}, nil
}

// NewDateTimeYMDhmsUTCCodec returns a codec for 6-register YMDhms interpreted as UTC.
func NewDateTimeYMDhmsUTCCodec() Codec[time.Time] {
	return dateTimeYMDhmsCodec{id: "datetime_ymdhms_utc", loc: time.UTC}
}

// NewDateTimeYMDhmsLocalCodec returns a codec for 6-register YMDhms interpreted as local time.
func NewDateTimeYMDhmsLocalCodec() Codec[time.Time] {
	return dateTimeYMDhmsCodec{id: "datetime_ymdhms_local", loc: time.Local}
}

// NewDateTimeYMDhmsCodec returns a codec for 6-register YMDhms: naive Y/M/D/h/m/s tuple; library interprets it in UTC by default.
func NewDateTimeYMDhmsCodec() Codec[time.Time] {
	return dateTimeYMDhmsCodec{id: "datetime_ymdhms", loc: time.UTC}
}

// dateTimeIEC870Codec: 4 registers, 7 bytes IEC 60870-5 CP56Time2a. Registers use big-endian byte order
// within each register; the 7-byte payload occupies the first 7 bytes, byte 8 is padded zero. Interpreted in loc.
type dateTimeIEC870Codec struct {
	id  string
	loc *time.Location
}

func (c dateTimeIEC870Codec) ID() string                 { return c.id }
func (c dateTimeIEC870Codec) Name() string               { return c.id }
func (c dateTimeIEC870Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 4} }
func (c dateTimeIEC870Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 8} }

// decodeCP56Time2a decodes 7 bytes (IEC 60870-5 CP56Time2a) to time in loc.
// Bytes (LE ms): ms_lo, ms_hi (0-59999 = milliseconds within minute), min, hour, day+dow, month, year (0-99 = 2000-2099).
// CP56Time2a status/flag bits (invalid, summer time, substituted, etc.) are ignored; only the timestamp fields are decoded.
func decodeCP56Time2a(b []byte, loc *time.Location) (time.Time, error) {
	if loc == nil {
		return time.Time{}, fmt.Errorf("modbus: CP56Time2a location must not be nil")
	}
	if len(b) < 7 {
		return time.Time{}, fmt.Errorf("modbus: CP56Time2a requires 7 bytes")
	}
	ms := int(b[0]) | int(b[1])<<8
	min := int(b[2]) & 0x3F
	hour := int(b[3]) & 0x1F
	day := int(b[4]) & 0x1F
	month := int(b[5]) & 0x0F
	year := 2000 + (int(b[6]) & 0x7F)
	if month < 1 || month > 12 {
		return time.Time{}, fmt.Errorf("modbus: CP56Time2a month %d out of range 1-12", month)
	}
	if day < 1 || day > 31 {
		return time.Time{}, fmt.Errorf("modbus: CP56Time2a day %d out of range 1-31", day)
	}
	if hour > 23 || min > 59 || ms > 59999 {
		return time.Time{}, fmt.Errorf("modbus: CP56Time2a time out of range")
	}
	sec := ms / 1000
	nsec := (ms % 1000) * int(time.Millisecond)
	tt, err := strictDateTime(year, month, day, hour, min, sec, nsec, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("modbus: CP56Time2a %v", err)
	}
	return tt, nil
}

// encodeCP56Time2a encodes t (in loc) to 7 bytes CP56Time2a. Status/flag bits are left unset; only timestamp fields are written.
func encodeCP56Time2a(t time.Time, loc *time.Location) ([]byte, error) {
	if loc == nil {
		return nil, fmt.Errorf("modbus: CP56Time2a location must not be nil")
	}
	t = t.In(loc)
	year := t.Year()
	if year < 2000 || year > 2127 {
		return nil, fmt.Errorf("modbus: CP56Time2a year %d out of range 2000-2127", year)
	}
	ms := t.Second()*1000 + t.Nanosecond()/int(time.Millisecond)
	if ms < 0 || ms > 59999 {
		return nil, fmt.Errorf("modbus: CP56Time2a milliseconds %d out of range 0-59999", ms)
	}
	b := make([]byte, 7)
	b[0] = byte(ms)
	b[1] = byte(ms >> 8)
	b[2] = byte(t.Minute() & 0x3F)
	b[3] = byte(t.Hour() & 0x1F)
	day := t.Day()
	dow := int(t.Weekday())
	if dow == 0 {
		dow = 7
	}
	b[4] = byte(day&0x1F | (dow&0x07)<<5)
	b[5] = byte(int(t.Month()) & 0x0F)
	b[6] = byte(year - 2000)
	return b, nil
}

func (c dateTimeIEC870Codec) DecodeRegisters(regs []uint16) (time.Time, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return time.Time{}, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	tt, err := decodeCP56Time2a(raw[:7], c.loc)
	if err != nil {
		reason := strings.TrimPrefix(err.Error(), "modbus: ")
		return time.Time{}, &CodecValueError{Codec: c.ID(), Reason: reason}
	}
	return tt, nil
}

func (c dateTimeIEC870Codec) EncodeRegisters(t time.Time) ([]uint16, error) {
	if c.loc == nil {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "location must not be nil"}
	}
	b, err := encodeCP56Time2a(t, c.loc)
	if err != nil {
		reason := strings.TrimPrefix(err.Error(), "modbus: ")
		return nil, &CodecValueError{Codec: c.ID(), Reason: reason}
	}
	b = append(b, 0)
	return bytesToUint16s(BigEndian, b), nil
}

// NewDateTimeIEC870UTCCodec returns a codec for 4-register IEC 60870-5 CP56Time2a interpreted as UTC.
func NewDateTimeIEC870UTCCodec() Codec[time.Time] {
	return dateTimeIEC870Codec{id: "datetime_iec870_utc", loc: time.UTC}
}

// NewDateTimeIEC870LocalCodec returns a codec for 4-register IEC 60870-5 CP56Time2a interpreted as local time.
func NewDateTimeIEC870LocalCodec() Codec[time.Time] {
	return dateTimeIEC870Codec{id: "datetime_iec870_local", loc: time.Local}
}

// NewDateTimeIEC870Codec returns a codec for 4-register IEC 60870-5 CP56Time2a: timezone-unspecified wire value interpreted by library in UTC.
func NewDateTimeIEC870Codec() Codec[time.Time] {
	return dateTimeIEC870Codec{id: "datetime_iec870", loc: time.UTC}
}

func registerTimeDescriptors() {
	registerCodecDescriptor(CodecDescriptor{
		ID:           "datetime2_s2000",
		Name:         "datetime2_s2000",
		Family:       CodecFamilyTime,
		ValueKind:    CodecValueTime,
		RegisterSpec: RegisterSpec{Count: 2},
		ByteSpec:     ByteSpec{Count: 4},
		Layouts:      []RegisterLayoutDescriptor{{Name: "4321", Common: true, Layout: Layout32_4321}},
	})
	registerCodecDescriptor(CodecDescriptor{
		ID:           "datetime3_s2000",
		Name:         "datetime3_s2000",
		Family:       CodecFamilyTime,
		ValueKind:    CodecValueTime,
		RegisterSpec: RegisterSpec{Count: 3},
		ByteSpec:     ByteSpec{Count: 6},
		Layouts:      []RegisterLayoutDescriptor{{Name: "654321", Common: true, Layout: Layout48_654321}},
	})
	for _, d := range []struct {
		id   string
		name string
	}{
		{"datetime_ymdhms_utc", "datetime_ymdhms_utc"},
		{"datetime_ymdhms_local", "datetime_ymdhms_local"},
		{"datetime_ymdhms", "datetime_ymdhms"},
		{"datetime_iec870_utc", "datetime_iec870_utc"},
		{"datetime_iec870_local", "datetime_iec870_local"},
		{"datetime_iec870", "datetime_iec870"},
	} {
		spec := RegisterSpec{Count: 6}
		byteSpec := ByteSpec{Count: 12}
		if strings.HasPrefix(d.id, "datetime_iec870") {
			spec = RegisterSpec{Count: 4}
			byteSpec = ByteSpec{Count: 8}
		}
		registerCodecDescriptor(CodecDescriptor{
			ID:           d.id,
			Name:         d.name,
			Family:       CodecFamilyTime,
			ValueKind:    CodecValueTime,
			RegisterSpec: spec,
			ByteSpec:     byteSpec,
			Layouts:      nil,
		})
	}
}

func init() {
	registerTimeDescriptors()
}
