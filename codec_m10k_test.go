package modbus

import (
	"testing"
)

func TestUint32M10k_LowToHigh_RoundTrip(t *testing.T) {
	c, err := NewUint32M10kCodec(DecimalLimbLowToHigh)
	if err != nil {
		t.Fatal(err)
	}
	v := uint32(12345678) // 1234*10000 + 5678
	regs, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 2 {
		t.Fatalf("len(regs) = %d, want 2", len(regs))
	}
	if regs[0] != 5678 || regs[1] != 1234 {
		t.Errorf("regs = %v, want [5678, 1234]", regs)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != v {
		t.Errorf("DecodeRegisters = %d, want %d", got, v)
	}
	if c.ID() != "uint32_m10k/order:low_to_high" {
		t.Errorf("ID = %q", c.ID())
	}
}

func TestUint32M10k_HighToLow_RoundTrip(t *testing.T) {
	c, err := NewUint32M10kCodec(DecimalLimbHighToLow)
	if err != nil {
		t.Fatal(err)
	}
	v := uint32(12345678)
	regs, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatal(err)
	}
	// high_to_low: r0 = value/10000, r1 = value%10000
	if regs[0] != 1234 || regs[1] != 5678 {
		t.Errorf("regs = %v, want [1234, 5678]", regs)
	}
	got, _ := DecodeRegisters(regs, c)
	if got != v {
		t.Errorf("DecodeRegisters = %d, want %d", got, v)
	}
	if c.ID() != "uint32_m10k/order:high_to_low" {
		t.Errorf("ID = %q", c.ID())
	}
}

func TestUint32M10k_RejectOverflow(t *testing.T) {
	c, _ := NewUint32M10kCodec(DecimalLimbLowToHigh)
	_, err := EncodeRegisters(uint32(100_000_000), c)
	if err == nil {
		t.Fatal("expected error for value > 99_999_999")
	}
}

func TestUint32M10k_RejectInvalidLimb(t *testing.T) {
	c, _ := NewUint32M10kCodec(DecimalLimbLowToHigh)
	_, err := DecodeRegisters([]uint16{10000, 0}, c)
	if err == nil {
		t.Fatal("expected error for limb > 9999")
	}
}

func TestUint48M10k_LowToHigh_RoundTrip(t *testing.T) {
	c, err := NewUint48M10kCodec(DecimalLimbLowToHigh)
	if err != nil {
		t.Fatal(err)
	}
	v := uint64(123456789012) // 9012 + 5678*10000 + 1234*10000^2
	regs, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 3 {
		t.Fatalf("len(regs) = %d, want 3", len(regs))
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != v {
		t.Errorf("got %d, want %d", got, v)
	}
}

func TestUint48M10k_HighToLow_RoundTrip(t *testing.T) {
	c, err := NewUint48M10kCodec(DecimalLimbHighToLow)
	if err != nil {
		t.Fatal(err)
	}
	v := uint64(123456789012)
	regs, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := DecodeRegisters(regs, c)
	if got != v {
		t.Errorf("got %d, want %d", got, v)
	}
}

func TestUint64M10k_LowToHigh_RoundTrip(t *testing.T) {
	c, err := NewUint64M10kCodec(DecimalLimbLowToHigh)
	if err != nil {
		t.Fatal(err)
	}
	v := uint64(9999999999999999)
	regs, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 4 {
		t.Fatalf("len(regs) = %d, want 4", len(regs))
	}
	got, _ := DecodeRegisters(regs, c)
	if got != v {
		t.Errorf("got %d, want %d", got, v)
	}
}

func TestUint64M10k_RejectOverflow(t *testing.T) {
	c, _ := NewUint64M10kCodec(DecimalLimbLowToHigh)
	_, err := EncodeRegisters(uint64(10_000_000_000_000_000), c)
	if err == nil {
		t.Fatal("expected error for value >= 10000^4")
	}
}

func TestUint32M10k_RejectInvalidOrder(t *testing.T) {
	_, err := NewUint32M10kCodec(DecimalLimbOrder(0))
	if err == nil {
		t.Fatal("expected error for invalid order")
	}
}

func TestDecimalLimbOrder_String(t *testing.T) {
	if DecimalLimbLowToHigh.String() != "low_to_high" {
		t.Errorf("LowToHigh String = %q", DecimalLimbLowToHigh.String())
	}
	if DecimalLimbHighToLow.String() != "high_to_low" {
		t.Errorf("HighToLow String = %q", DecimalLimbHighToLow.String())
	}
}

func TestM10k_RuntimeByID(t *testing.T) {
	rc, ok, err := RuntimeCodecByID("uint32_m10k/order:low_to_high")
	if err != nil || !ok || rc == nil {
		t.Fatalf("RuntimeCodecByID: ok=%v err=%v rc=%v", ok, err, rc)
	}
	if rc.ID() != "uint32_m10k/order:low_to_high" {
		t.Errorf("ID = %q", rc.ID())
	}
	regs, err := EncodeRegistersAny(uint32(12345678), rc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegistersAny(regs, rc)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got.(uint32); !ok || v != 12345678 {
		t.Errorf("round-trip = %v (%T)", got, got)
	}
}

func TestInt32M10k_LowToHigh_PositiveRoundTrip(t *testing.T) {
	c, err := NewInt32M10kCodec(DecimalLimbLowToHigh)
	if err != nil {
		t.Fatal(err)
	}
	v := int32(12345678) // lo=5678, ms=1234
	regs, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 2 {
		t.Fatalf("len(regs) = %d, want 2", len(regs))
	}
	if regs[0] != 5678 || regs[1] != 1234 {
		t.Errorf("regs = %v, want [5678, 1234]", regs)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != v {
		t.Errorf("DecodeRegisters = %d, want %d", got, v)
	}
}

func TestInt32M10k_LowToHigh_NegativeRoundTrip(t *testing.T) {
	c, err := NewInt32M10kCodec(DecimalLimbLowToHigh)
	if err != nil {
		t.Fatal(err)
	}
	// int32 M10k range: min -9999*10000+0 = -99990000, max 9999*10000+9999 = 99999999
	v := int32(-99985000) // -9999*10000 + 5000
	regs, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != v {
		t.Errorf("DecodeRegisters = %d, want %d", got, v)
	}
}

func TestInt32M10k_HighToLow_NegativeRoundTrip(t *testing.T) {
	c, err := NewInt32M10kCodec(DecimalLimbHighToLow)
	if err != nil {
		t.Fatal(err)
	}
	v := int32(-99985000) // -9999*10000 + 5000
	regs, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatal(err)
	}
	// high_to_low: first reg = ms = -9999, second = lo = 5000
	if int16(regs[0]) != -9999 {
		t.Errorf("regs[0] (ms) = %d, want -9999", int16(regs[0]))
	}
	if regs[1] != 5000 {
		t.Errorf("regs[1] (lo) = %d, want 5000", regs[1])
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != v {
		t.Errorf("DecodeRegisters = %d, want %d", got, v)
	}
}

func TestInt32M10k_RejectMSLimbOutOfRange(t *testing.T) {
	c, _ := NewInt32M10kCodec(DecimalLimbLowToHigh)
	// 10000 in the MS limb is invalid (must be -9999..9999)
	_, err := DecodeRegisters([]uint16{0, 10000}, c)
	if err == nil {
		t.Fatal("expected error for MS limb 10000")
	}
}

func TestInt48M10k_NegativeRoundTrip(t *testing.T) {
	c, err := NewInt48M10kCodec(DecimalLimbLowToHigh)
	if err != nil {
		t.Fatal(err)
	}
	// int48 M10k: 3 regs, value = ms*10000^2 + mid*10000 + lo; ms in [-9999..9999], others 0..9999
	v := int64(-123400000000) // -1234*10000^2 + 0*10000 + 0
	regs, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != v {
		t.Errorf("got %d, want %d", got, v)
	}
}

func TestInt64M10k_NegativeRoundTrip(t *testing.T) {
	c, err := NewInt64M10kCodec(DecimalLimbHighToLow)
	if err != nil {
		t.Fatal(err)
	}
	// int64 M10k range: min -9999*10000^3 = -9999000000000000, max 9999999999999999
	v := int64(-9999000000000000)
	regs, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != v {
		t.Errorf("got %d, want %d", got, v)
	}
}

func TestInt32M10k_RuntimeByID(t *testing.T) {
	rc, ok, err := RuntimeCodecByID("int32_m10k/order:low_to_high")
	if err != nil || !ok || rc == nil {
		t.Fatalf("RuntimeCodecByID: ok=%v err=%v", ok, err)
	}
	// -12345 = -1*10000 + 7655 (ms=-1, lo=7655)
	regs, err := EncodeRegistersAny(int32(-12345), rc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegistersAny(regs, rc)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got.(int32); !ok || v != -12345 {
		t.Errorf("round-trip = %v (%T)", got, got)
	}
}
