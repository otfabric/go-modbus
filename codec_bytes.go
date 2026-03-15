package modbus

import (
	"fmt"
	"net"
)

// bytesCodec: raw bytes in wire order (high byte first per register). byteCount must be even.
type bytesCodec struct{ byteCount uint16 }

func (c bytesCodec) ID() string                 { return fmt.Sprintf("bytes/bytes:%d", c.byteCount) }
func (c bytesCodec) Name() string               { return "bytes" }
func (c bytesCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: c.byteCount / 2} }
func (c bytesCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: c.byteCount} }

func (c bytesCodec) DecodeRegisters(regs []uint16) ([]byte, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return nil, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	out := make([]byte, len(raw))
	copy(out, raw)
	return out, nil
}

func (c bytesCodec) EncodeRegisters(b []byte) ([]uint16, error) {
	if uint16(len(b)) != c.byteCount {
		return nil, &CodecValueError{Codec: c.ID(), Reason: fmt.Sprintf("expected %d bytes, got %d", c.byteCount, len(b))}
	}
	return bytesToUint16s(BigEndian, b), nil
}

// uint8SliceCodec: same as bytesCodec but typed as []uint8 for API clarity.
type uint8SliceCodec struct{ byteCount uint16 }

func (c uint8SliceCodec) ID() string                 { return fmt.Sprintf("uint8_slice/bytes:%d", c.byteCount) }
func (c uint8SliceCodec) Name() string               { return "uint8_slice" }
func (c uint8SliceCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: c.byteCount / 2} }
func (c uint8SliceCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: c.byteCount} }

func (c uint8SliceCodec) DecodeRegisters(regs []uint16) ([]uint8, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return nil, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	out := make([]uint8, len(raw))
	copy(out, raw)
	return out, nil
}

func (c uint8SliceCodec) EncodeRegisters(b []uint8) ([]uint16, error) {
	if uint16(len(b)) != c.byteCount {
		return nil, &CodecValueError{Codec: c.ID(), Reason: fmt.Sprintf("expected %d bytes, got %d", c.byteCount, len(b))}
	}
	return bytesToUint16s(BigEndian, b), nil
}

// ipAddrCodec: 4 bytes (2 registers), IPv4 in raw wire order.
type ipAddrCodec struct{}

func (c ipAddrCodec) ID() string                 { return "ip_addr" }
func (c ipAddrCodec) Name() string               { return "ip_addr" }
func (c ipAddrCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 2} }
func (c ipAddrCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: 4} }

func (c ipAddrCodec) DecodeRegisters(regs []uint16) (net.IP, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return nil, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	ip := make(net.IP, 4)
	copy(ip, raw)
	return ip, nil
}

func (c ipAddrCodec) EncodeRegisters(ip net.IP) ([]uint16, error) {
	if ip == nil {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "IPv4 address must not be nil"}
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "not a valid IPv4 address (use net.IP.To4() for 16-byte representations)"}
	}
	b := make([]byte, 4)
	copy(b, ip4)
	return bytesToUint16s(BigEndian, b), nil
}

// ipv6AddrCodec: 16 bytes (8 registers), IPv6 in raw wire order.
type ipv6AddrCodec struct{}

func (c ipv6AddrCodec) ID() string                 { return "ipv6_addr" }
func (c ipv6AddrCodec) Name() string               { return "ipv6_addr" }
func (c ipv6AddrCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 8} }
func (c ipv6AddrCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: 16} }

func (c ipv6AddrCodec) DecodeRegisters(regs []uint16) (net.IP, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return nil, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	ip := make(net.IP, 16)
	copy(ip, raw)
	return ip, nil
}

func (c ipv6AddrCodec) EncodeRegisters(ip net.IP) ([]uint16, error) {
	if ip == nil {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "IPv6 address must not be nil"}
	}
	if ip.To4() != nil {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "IPv6 codec requires 16-byte address; use IPv4 codec for IPv4"}
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "not a valid IPv6 address"}
	}
	b := make([]byte, 16)
	copy(b, ip16)
	return bytesToUint16s(BigEndian, b), nil
}

// eui48Codec: 6 bytes (3 registers), MAC/EUI-48 in raw wire order.
type eui48Codec struct{}

func (c eui48Codec) ID() string                 { return "eui48" }
func (c eui48Codec) Name() string               { return "eui48" }
func (c eui48Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 3} }
func (c eui48Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 6} }

func (c eui48Codec) DecodeRegisters(regs []uint16) (net.HardwareAddr, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return nil, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	hw := make(net.HardwareAddr, 6)
	copy(hw, raw)
	return hw, nil
}

func (c eui48Codec) EncodeRegisters(mac net.HardwareAddr) ([]uint16, error) {
	if mac == nil || len(mac) != 6 {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "EUI-48 address must be exactly 6 bytes"}
	}
	b := make([]byte, 6)
	copy(b, mac)
	return bytesToUint16s(BigEndian, b), nil
}

// eui64Codec: 8 bytes (4 registers), EUI-64 in raw wire order.
type eui64Codec struct{}

func (c eui64Codec) ID() string                 { return "eui64" }
func (c eui64Codec) Name() string               { return "eui64" }
func (c eui64Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 4} }
func (c eui64Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 8} }

func (c eui64Codec) DecodeRegisters(regs []uint16) (net.HardwareAddr, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return nil, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	hw := make(net.HardwareAddr, 8)
	copy(hw, raw)
	return hw, nil
}

func (c eui64Codec) EncodeRegisters(addr net.HardwareAddr) ([]uint16, error) {
	if addr == nil || len(addr) != 8 {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "EUI-64 address must be exactly 8 bytes"}
	}
	b := make([]byte, 8)
	copy(b, addr)
	return bytesToUint16s(BigEndian, b), nil
}

func bytesCodecRejectOdd(byteCount uint16) error {
	if byteCount == 0 || byteCount%2 != 0 {
		return fmt.Errorf("%w: byte count must be positive and even for register-backed bytes", ErrCodecValue)
	}
	return nil
}

// NewBytesCodec returns a codec for raw bytes in wire order. byteCount must be even (transport is register-based). Rejects 0 and odd byteCount.
func NewBytesCodec(byteCount uint16) (Codec[[]byte], error) {
	if err := bytesCodecRejectOdd(byteCount); err != nil {
		return nil, err
	}
	return bytesCodec{byteCount: byteCount}, nil
}

// NewUint8SliceCodec returns a codec for []uint8 in wire order. byteCount must be even. Rejects 0 and odd byteCount.
func NewUint8SliceCodec(byteCount uint16) (Codec[[]uint8], error) {
	if err := bytesCodecRejectOdd(byteCount); err != nil {
		return nil, err
	}
	return uint8SliceCodec{byteCount: byteCount}, nil
}

// NewIPAddrCodec returns a codec for IPv4 (4 bytes, 2 registers) in raw wire order.
func NewIPAddrCodec() Codec[net.IP] {
	return ipAddrCodec{}
}

// NewIPv6AddrCodec returns a codec for IPv6 (16 bytes, 8 registers) in raw wire order.
func NewIPv6AddrCodec() Codec[net.IP] {
	return ipv6AddrCodec{}
}

// NewEUI48Codec returns a codec for EUI-48/MAC (6 bytes, 3 registers) in raw wire order.
func NewEUI48Codec() Codec[net.HardwareAddr] {
	return eui48Codec{}
}

// NewEUI64Codec returns a codec for EUI-64 (8 bytes, 4 registers) in raw wire order.
func NewEUI64Codec() Codec[net.HardwareAddr] {
	return eui64Codec{}
}

func init() {
	registerBytesDescriptors()
}

func registerBytesDescriptors() {
	for _, n := range []uint16{2, 4, 6, 8, 10, 12, 14, 16, 20, 24, 32, 48, 64} {
		registerCodecDescriptor(CodecDescriptor{
			ID:           fmt.Sprintf("bytes/bytes:%d", n),
			Name:         "bytes",
			Family:       CodecFamilyBytes,
			ValueKind:    CodecValueByteSlice,
			RegisterSpec: RegisterSpec{Count: n / 2},
			ByteSpec:     ByteSpec{Count: n},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           fmt.Sprintf("uint8_slice/bytes:%d", n),
			Name:         "uint8_slice",
			Family:       CodecFamilyBytes,
			ValueKind:    CodecValueUint8Slice,
			RegisterSpec: RegisterSpec{Count: n / 2},
			ByteSpec:     ByteSpec{Count: n},
			Layouts:      nil,
		})
	}
	registerCodecDescriptor(CodecDescriptor{
		ID:           "ip_addr",
		Name:         "ip_addr",
		Family:       CodecFamilyNetwork,
		ValueKind:    CodecValueIP,
		RegisterSpec: RegisterSpec{Count: 2},
		ByteSpec:     ByteSpec{Count: 4},
		Layouts:      nil,
	})
	registerCodecDescriptor(CodecDescriptor{
		ID:           "ipv6_addr",
		Name:         "ipv6_addr",
		Family:       CodecFamilyNetwork,
		ValueKind:    CodecValueIP,
		RegisterSpec: RegisterSpec{Count: 8},
		ByteSpec:     ByteSpec{Count: 16},
		Layouts:      nil,
	})
	registerCodecDescriptor(CodecDescriptor{
		ID:           "eui48",
		Name:         "eui48",
		Family:       CodecFamilyHardwareAddress,
		ValueKind:    CodecValueHardwareAddr,
		RegisterSpec: RegisterSpec{Count: 3},
		ByteSpec:     ByteSpec{Count: 6},
		Layouts:      nil,
	})
	registerCodecDescriptor(CodecDescriptor{
		ID:           "eui64",
		Name:         "eui64",
		Family:       CodecFamilyHardwareAddress,
		ValueKind:    CodecValueHardwareAddr,
		RegisterSpec: RegisterSpec{Count: 4},
		ByteSpec:     ByteSpec{Count: 8},
		Layouts:      nil,
	})
}
