package modbus

import (
	"errors"
	"net"
	"testing"
)

func TestNewBytesCodec_RejectOddAndZero(t *testing.T) {
	_, err := NewBytesCodec(0)
	if err == nil {
		t.Fatal("expected error for byteCount 0")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("expected ErrCodecValue, got %v", err)
	}
	_, err = NewBytesCodec(3)
	if err == nil {
		t.Fatal("expected error for odd byteCount")
	}
	c, err := NewBytesCodec(4)
	if err != nil {
		t.Fatal(err)
	}
	if c.RegisterSpec().Count != 2 {
		t.Errorf("RegisterSpec().Count = %d, want 2", c.RegisterSpec().Count)
	}
}

func TestNewUint8SliceCodec_RejectOdd(t *testing.T) {
	_, err := NewUint8SliceCodec(1)
	if err == nil {
		t.Fatal("expected error for odd byteCount")
	}
}

func TestBytesCodec_RoundTrip(t *testing.T) {
	c, err := NewBytesCodec(6)
	if err != nil {
		t.Fatal(err)
	}
	b := []byte{0x00, 0x1A, 0x2B, 0x3C, 0x4D, 0x5E}
	regs, err := EncodeRegisters(b, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(b) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(b))
	}
	for i := range b {
		if got[i] != b[i] {
			t.Errorf("got[%d] = 0x%02x, want 0x%02x", i, got[i], b[i])
		}
	}
}

func TestBytesCodec_EncodeWrongLength(t *testing.T) {
	c := mustBytesCodec(t, 4)
	_, err := EncodeRegisters([]byte{1, 2, 3}, c)
	if err == nil {
		t.Fatal("expected error for wrong length")
	}
}

func TestIPAddrCodec_RoundTrip(t *testing.T) {
	c := NewIPAddrCodec()
	ip := net.IP{192, 168, 1, 10}
	regs, err := EncodeRegisters(ip, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("len(got) = %d", len(got))
	}
	for i := range ip {
		if got[i] != ip[i] {
			t.Errorf("got[%d] = %d, want %d", i, got[i], ip[i])
		}
	}
}

func TestIPAddrCodec_AcceptParseIP(t *testing.T) {
	// net.ParseIP often returns 16-byte form for IPv4; codec must accept via To4().
	c := NewIPAddrCodec()
	ip := net.ParseIP("192.168.1.10")
	if ip == nil {
		t.Fatal("ParseIP failed")
	}
	regs, err := EncodeRegisters(ip, c)
	if err != nil {
		t.Fatalf("EncodeRegisters(net.ParseIP result): %v", err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	want := net.IP{192, 168, 1, 10}
	for i := 0; i < 4; i++ {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestIPAddrCodec_RejectWrongLength(t *testing.T) {
	c := NewIPAddrCodec()
	_, err := EncodeRegisters(net.IP{192, 168, 1}, c)
	if err == nil {
		t.Fatal("expected error for 3-byte IP")
	}
	_, err = EncodeRegisters(net.IP(nil), c)
	if err == nil {
		t.Fatal("expected error for nil IP")
	}
}

func TestIPv6AddrCodec_RoundTrip(t *testing.T) {
	c := NewIPv6AddrCodec()
	ip := net.IP{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	regs, err := EncodeRegisters(ip, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 16 {
		t.Fatalf("len(got) = %d", len(got))
	}
	for i := range ip {
		if got[i] != ip[i] {
			t.Errorf("got[%d] = 0x%02x, want 0x%02x", i, got[i], ip[i])
		}
	}
}

func TestIPv6AddrCodec_RejectWrongLength(t *testing.T) {
	c := NewIPv6AddrCodec()
	_, err := EncodeRegisters(net.IP{1, 2, 3, 4}, c)
	if err == nil {
		t.Fatal("expected error for 4-byte IP")
	}
}

func TestIPv6AddrCodec_RejectIPv4(t *testing.T) {
	c := NewIPv6AddrCodec()
	ip := net.ParseIP("192.168.1.10")
	if ip == nil {
		t.Fatal("ParseIP failed")
	}
	_, err := EncodeRegisters(ip, c)
	if err == nil {
		t.Fatal("expected error when encoding IPv4 with IPv6 codec")
	}
}

func TestEUI48Codec_RoundTrip(t *testing.T) {
	c := NewEUI48Codec()
	mac := net.HardwareAddr{0x00, 0x1A, 0x2B, 0x3C, 0x4D, 0x5E}
	regs, err := EncodeRegisters(mac, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("len(got) = %d", len(got))
	}
	for i := range mac {
		if got[i] != mac[i] {
			t.Errorf("got[%d] = 0x%02x, want 0x%02x", i, got[i], mac[i])
		}
	}
}

func TestEUI48Codec_RejectWrongLength(t *testing.T) {
	c := NewEUI48Codec()
	_, err := EncodeRegisters(net.HardwareAddr{1, 2, 3}, c)
	if err == nil {
		t.Fatal("expected error for 3-byte MAC")
	}
	_, err = EncodeRegisters(net.HardwareAddr(nil), c)
	if err == nil {
		t.Fatal("expected error for nil MAC")
	}
}

func TestEUI64Codec_RoundTrip(t *testing.T) {
	c := NewEUI64Codec()
	addr := net.HardwareAddr{0x00, 0x1A, 0x2B, 0x3C, 0x4D, 0x5E, 0x6F, 0x70}
	regs, err := EncodeRegisters(addr, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 {
		t.Fatalf("len(got) = %d", len(got))
	}
	for i := range addr {
		if got[i] != addr[i] {
			t.Errorf("got[%d] = 0x%02x, want 0x%02x", i, got[i], addr[i])
		}
	}
}

func TestEUI64Codec_RejectWrongLength(t *testing.T) {
	c := NewEUI64Codec()
	_, err := EncodeRegisters(net.HardwareAddr{1, 2, 3, 4, 5, 6}, c)
	if err == nil {
		t.Fatal("expected error for 6-byte address")
	}
	_, err = EncodeRegisters(net.HardwareAddr(nil), c)
	if err == nil {
		t.Fatal("expected error for nil address")
	}
}

func mustBytesCodec(t *testing.T, byteCount uint16) Codec[[]byte] {
	t.Helper()
	c, err := NewBytesCodec(byteCount)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDescriptorConsistency_BytesAndAddressCodecs(t *testing.T) {
	all := AvailableCodecDescriptors()
	for _, d := range all {
		switch d.Family {
		case CodecFamilyBytes:
			if d.ValueKind == CodecValueByteSlice {
				c, err := NewBytesCodec(d.ByteSpec.Count)
				if err != nil {
					t.Errorf("descriptor %s: %v", d.ID, err)
					continue
				}
				if c.RegisterSpec() != d.RegisterSpec || c.ByteSpec() != d.ByteSpec {
					t.Errorf("descriptor %s: spec mismatch", d.ID)
				}
			} else if d.ValueKind == CodecValueUint8Slice {
				c, err := NewUint8SliceCodec(d.ByteSpec.Count)
				if err != nil {
					t.Errorf("descriptor %s: %v", d.ID, err)
					continue
				}
				if c.RegisterSpec() != d.RegisterSpec || c.ByteSpec() != d.ByteSpec {
					t.Errorf("descriptor %s: spec mismatch", d.ID)
				}
			}
		case CodecFamilyNetwork:
			switch d.ID {
			case "ip_addr":
				c := NewIPAddrCodec()
				if c.RegisterSpec() != d.RegisterSpec || c.ByteSpec() != d.ByteSpec {
					t.Errorf("descriptor %s: spec mismatch", d.ID)
				}
			case "ipv6_addr":
				c := NewIPv6AddrCodec()
				if c.RegisterSpec() != d.RegisterSpec || c.ByteSpec() != d.ByteSpec {
					t.Errorf("descriptor %s: spec mismatch", d.ID)
				}
			}
		case CodecFamilyHardwareAddress:
			switch d.ID {
			case "eui48":
				c := NewEUI48Codec()
				if c.RegisterSpec() != d.RegisterSpec || c.ByteSpec() != d.ByteSpec {
					t.Errorf("descriptor %s: spec mismatch", d.ID)
				}
			case "eui64":
				c := NewEUI64Codec()
				if c.RegisterSpec() != d.RegisterSpec || c.ByteSpec() != d.ByteSpec {
					t.Errorf("descriptor %s: spec mismatch", d.ID)
				}
			}
		case CodecFamilyDecimalLimb, CodecFamilyUnknown, CodecFamilyInteger, CodecFamilyFloat, CodecFamilyText, CodecFamilyBCD, CodecFamilyVendorSpecific:
			// Not under test in this function; skip.
		}
	}
}
