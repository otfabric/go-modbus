package codec

import (
	"fmt"
	"math"
)

// Decimal limb (M10k) codecs: each 16-bit register holds one base-10000 limb (0–9999).
// Value = sum of limbs × 10000^position. Order is controlled by DecimalLimbOrder, not RegisterLayout.
// This is a distinct codec family (CodecFamilyDecimalLimb), not binary byte permutation.

const m10kBase uint64 = 10000

func decodeUintM10k(regs []uint16, order DecimalLimbOrder, codecID string) (uint64, error) {
	var value uint64
	var factor uint64 = 1

	switch order {
	case DecimalLimbLowToHigh:
		for i := 0; i < len(regs); i++ {
			if regs[i] > 9999 {
				return 0, &CodecValueError{
					Codec:  codecID,
					Reason: fmt.Sprintf("register %d has value %d, must be in 0..9999", i, regs[i]),
				}
			}
			value += uint64(regs[i]) * factor
			factor *= m10kBase
		}
		return value, nil
	case DecimalLimbHighToLow:
		for i := len(regs) - 1; i >= 0; i-- {
			if regs[i] > 9999 {
				return 0, &CodecValueError{
					Codec:  codecID,
					Reason: fmt.Sprintf("register %d has value %d, must be in 0..9999", i, regs[i]),
				}
			}
			value += uint64(regs[i]) * factor
			factor *= m10kBase
		}
		return value, nil
	default:
		return 0, &CodecValueError{Codec: codecID, Reason: "invalid decimal limb order"}
	}
}

func encodeUintM10k(value uint64, registerCount uint16, order DecimalLimbOrder, codecID string) ([]uint16, error) {
	max := uint64(1)
	for i := uint16(0); i < registerCount; i++ {
		max *= m10kBase
	}
	if value >= max {
		return nil, &CodecValueError{
			Codec:  codecID,
			Reason: fmt.Sprintf("value %d exceeds max encodable value %d", value, max-1),
		}
	}

	limbs := make([]uint16, registerCount)
	v := value
	for i := uint16(0); i < registerCount; i++ {
		limbs[i] = uint16(v % m10kBase)
		v /= m10kBase
	}

	switch order {
	case DecimalLimbLowToHigh:
		return limbs, nil
	case DecimalLimbHighToLow:
		out := make([]uint16, registerCount)
		for i := range limbs {
			out[len(limbs)-1-i] = limbs[i]
		}
		return out, nil
	default:
		return nil, &CodecValueError{Codec: codecID, Reason: "invalid decimal limb order"}
	}
}

const m10kSignedMSMin = -9999
const m10kSignedMSMax = 9999

func decodeIntM10k(regs []uint16, order DecimalLimbOrder, codecID string) (int64, error) {
	n := len(regs)
	if n == 0 {
		return 0, &CodecValueError{Codec: codecID, Reason: "need at least one register"}
	}
	msIndex := 0
	if order == DecimalLimbLowToHigh {
		msIndex = n - 1
	}
	// Build lower limbs in LSB-first order for accumulation.
	var lowerLimbs []uint16
	if order == DecimalLimbLowToHigh {
		lowerLimbs = regs[:n-1]
	} else {
		lowerLimbs = make([]uint16, n-1)
		for i := range lowerLimbs {
			lowerLimbs[i] = regs[n-1-i]
		}
	}
	var unsignedPart uint64
	var factor uint64 = 1
	for i := range lowerLimbs {
		if lowerLimbs[i] > 9999 {
			return 0, &CodecValueError{
				Codec:  codecID,
				Reason: fmt.Sprintf("limb value %d must be in 0..9999", lowerLimbs[i]),
			}
		}
		unsignedPart += uint64(lowerLimbs[i]) * factor
		factor *= m10kBase
	}
	ms := int16(regs[msIndex])
	if ms < m10kSignedMSMin || ms > m10kSignedMSMax {
		return 0, &CodecValueError{
			Codec:  codecID,
			Reason: fmt.Sprintf("signed MS limb (register %d) has value %d, must be in -9999..9999", msIndex, ms),
		}
	}
	msPower := uint64(1)
	for i := 0; i < n-1; i++ {
		msPower *= m10kBase
	}
	return int64(unsignedPart) + int64(ms)*int64(msPower), nil
}

func encodeIntM10k(value int64, registerCount uint16, order DecimalLimbOrder, codecID string) ([]uint16, error) {
	n := int(registerCount)
	if n == 0 {
		return nil, &CodecValueError{Codec: codecID, Reason: "register count must be >= 1"}
	}
	// Peel lower limbs (0..n-2) as unsigned 0..9999; remaining quotient is signed MS limb.
	// Use (v - remainder) / base so quotient is correct for negative values.
	limbs := make([]int64, n)
	v := value
	for i := 0; i < n-1; i++ {
		limbs[i] = v % int64(m10kBase)
		if limbs[i] < 0 {
			limbs[i] += int64(m10kBase)
		}
		v = (v - limbs[i]) / int64(m10kBase)
	}
	msLimb := v
	if msLimb < m10kSignedMSMin || msLimb > m10kSignedMSMax {
		return nil, &CodecValueError{
			Codec:  codecID,
			Reason: fmt.Sprintf("value %d exceeds encodable range (MS limb %d must be in -9999..9999)", value, msLimb),
		}
	}
	out := make([]uint16, registerCount)
	for i := 0; i < n-1; i++ {
		out[i] = uint16(limbs[i])
	}
	out[n-1] = uint16(int16(msLimb))

	if order == DecimalLimbHighToLow {
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// uint32M10kCodec: 2 registers, base-10000 limbs.
type uint32M10kCodec struct{ order DecimalLimbOrder }

func (c uint32M10kCodec) ID() string                 { return "uint32_m10k/order:" + c.order.String() }
func (c uint32M10kCodec) Name() string               { return "uint32_m10k" }
func (c uint32M10kCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 2} }
func (c uint32M10kCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: 4} }

func (c uint32M10kCodec) DecodeRegisters(regs []uint16) (uint32, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	v, err := decodeUintM10k(regs, c.order, c.ID())
	if err != nil {
		return 0, err
	}
	if v > math.MaxUint32 {
		return 0, &CodecValueError{Codec: c.ID(), Reason: "decoded value overflows uint32"}
	}
	return uint32(v), nil
}

func (c uint32M10kCodec) EncodeRegisters(v uint32) ([]uint16, error) {
	return encodeUintM10k(uint64(v), 2, c.order, c.ID())
}

// uint48M10kCodec: 3 registers, base-10000 limbs. Go type uint64.
type uint48M10kCodec struct{ order DecimalLimbOrder }

func (c uint48M10kCodec) ID() string                 { return "uint48_m10k/order:" + c.order.String() }
func (c uint48M10kCodec) Name() string               { return "uint48_m10k" }
func (c uint48M10kCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 3} }
func (c uint48M10kCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: 6} }

func (c uint48M10kCodec) DecodeRegisters(regs []uint16) (uint64, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	return decodeUintM10k(regs, c.order, c.ID())
}

func (c uint48M10kCodec) EncodeRegisters(v uint64) ([]uint16, error) {
	return encodeUintM10k(v, 3, c.order, c.ID())
}

// uint64M10kCodec: 4 registers, base-10000 limbs.
type uint64M10kCodec struct{ order DecimalLimbOrder }

func (c uint64M10kCodec) ID() string                 { return "uint64_m10k/order:" + c.order.String() }
func (c uint64M10kCodec) Name() string               { return "uint64_m10k" }
func (c uint64M10kCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 4} }
func (c uint64M10kCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: 8} }

func (c uint64M10kCodec) DecodeRegisters(regs []uint16) (uint64, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	return decodeUintM10k(regs, c.order, c.ID())
}

func (c uint64M10kCodec) EncodeRegisters(v uint64) ([]uint16, error) {
	return encodeUintM10k(v, 4, c.order, c.ID())
}

// int32M10kCodec: 2 registers, only the most-significant limb is signed (-9999..9999).
type int32M10kCodec struct{ order DecimalLimbOrder }

func (c int32M10kCodec) ID() string                 { return "int32_m10k/order:" + c.order.String() }
func (c int32M10kCodec) Name() string               { return "int32_m10k" }
func (c int32M10kCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 2} }
func (c int32M10kCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: 4} }

func (c int32M10kCodec) DecodeRegisters(regs []uint16) (int32, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	v, err := decodeIntM10k(regs, c.order, c.ID())
	if err != nil {
		return 0, err
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, &CodecValueError{Codec: c.ID(), Reason: "decoded value overflows int32"}
	}
	return int32(v), nil
}

func (c int32M10kCodec) EncodeRegisters(v int32) ([]uint16, error) {
	return encodeIntM10k(int64(v), 2, c.order, c.ID())
}

// int48M10kCodec: 3 registers, only the most-significant limb is signed. Go type int64.
type int48M10kCodec struct{ order DecimalLimbOrder }

func (c int48M10kCodec) ID() string                 { return "int48_m10k/order:" + c.order.String() }
func (c int48M10kCodec) Name() string               { return "int48_m10k" }
func (c int48M10kCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 3} }
func (c int48M10kCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: 6} }

func (c int48M10kCodec) DecodeRegisters(regs []uint16) (int64, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	return decodeIntM10k(regs, c.order, c.ID())
}

func (c int48M10kCodec) EncodeRegisters(v int64) ([]uint16, error) {
	return encodeIntM10k(v, 3, c.order, c.ID())
}

// int64M10kCodec: 4 registers, only the most-significant limb is signed.
type int64M10kCodec struct{ order DecimalLimbOrder }

func (c int64M10kCodec) ID() string                 { return "int64_m10k/order:" + c.order.String() }
func (c int64M10kCodec) Name() string               { return "int64_m10k" }
func (c int64M10kCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 4} }
func (c int64M10kCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: 8} }

func (c int64M10kCodec) DecodeRegisters(regs []uint16) (int64, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	return decodeIntM10k(regs, c.order, c.ID())
}

func (c int64M10kCodec) EncodeRegisters(v int64) ([]uint16, error) {
	return encodeIntM10k(v, 4, c.order, c.ID())
}

// Constructors

// NewUint32M10kCodec returns a 32-bit decimal-limb (M10k) codec. Order controls whether the first register is the least- or most-significant limb.
func NewUint32M10kCodec(order DecimalLimbOrder) (Codec[uint32], error) {
	if order != DecimalLimbLowToHigh && order != DecimalLimbHighToLow {
		return nil, &CodecValueError{Codec: "uint32_m10k", Reason: "invalid decimal limb order"}
	}
	return uint32M10kCodec{order: order}, nil
}

// MustNewUint32M10kCodec is like NewUint32M10kCodec but panics on error.
func MustNewUint32M10kCodec(order DecimalLimbOrder) Codec[uint32] {
	c, err := NewUint32M10kCodec(order)
	if err != nil {
		panic(err)
	}
	return c
}

// NewUint48M10kCodec returns a 48-bit decimal-limb (M10k) codec; Go type uint64.
func NewUint48M10kCodec(order DecimalLimbOrder) (Codec[uint64], error) {
	if order != DecimalLimbLowToHigh && order != DecimalLimbHighToLow {
		return nil, &CodecValueError{Codec: "uint48_m10k", Reason: "invalid decimal limb order"}
	}
	return uint48M10kCodec{order: order}, nil
}

// MustNewUint48M10kCodec is like NewUint48M10kCodec but panics on error.
func MustNewUint48M10kCodec(order DecimalLimbOrder) Codec[uint64] {
	c, err := NewUint48M10kCodec(order)
	if err != nil {
		panic(err)
	}
	return c
}

// NewUint64M10kCodec returns a 64-bit decimal-limb (M10k) codec.
func NewUint64M10kCodec(order DecimalLimbOrder) (Codec[uint64], error) {
	if order != DecimalLimbLowToHigh && order != DecimalLimbHighToLow {
		return nil, &CodecValueError{Codec: "uint64_m10k", Reason: "invalid decimal limb order"}
	}
	return uint64M10kCodec{order: order}, nil
}

// MustNewUint64M10kCodec is like NewUint64M10kCodec but panics on error.
func MustNewUint64M10kCodec(order DecimalLimbOrder) Codec[uint64] {
	c, err := NewUint64M10kCodec(order)
	if err != nil {
		panic(err)
	}
	return c
}

// NewInt32M10kCodec returns a signed 32-bit decimal-limb (M10k) codec. Only the most-significant limb is signed (-9999..9999); other limbs are 0..9999.
func NewInt32M10kCodec(order DecimalLimbOrder) (Codec[int32], error) {
	if order != DecimalLimbLowToHigh && order != DecimalLimbHighToLow {
		return nil, &CodecValueError{Codec: "int32_m10k", Reason: "invalid decimal limb order"}
	}
	return int32M10kCodec{order: order}, nil
}

// MustNewInt32M10kCodec is like NewInt32M10kCodec but panics on error.
func MustNewInt32M10kCodec(order DecimalLimbOrder) Codec[int32] {
	c, err := NewInt32M10kCodec(order)
	if err != nil {
		panic(err)
	}
	return c
}

// NewInt48M10kCodec returns a signed 48-bit decimal-limb (M10k) codec; Go type int64. Only the MS limb is signed.
func NewInt48M10kCodec(order DecimalLimbOrder) (Codec[int64], error) {
	if order != DecimalLimbLowToHigh && order != DecimalLimbHighToLow {
		return nil, &CodecValueError{Codec: "int48_m10k", Reason: "invalid decimal limb order"}
	}
	return int48M10kCodec{order: order}, nil
}

// MustNewInt48M10kCodec is like NewInt48M10kCodec but panics on error.
func MustNewInt48M10kCodec(order DecimalLimbOrder) Codec[int64] {
	c, err := NewInt48M10kCodec(order)
	if err != nil {
		panic(err)
	}
	return c
}

// NewInt64M10kCodec returns a signed 64-bit decimal-limb (M10k) codec. Only the MS limb is signed.
func NewInt64M10kCodec(order DecimalLimbOrder) (Codec[int64], error) {
	if order != DecimalLimbLowToHigh && order != DecimalLimbHighToLow {
		return nil, &CodecValueError{Codec: "int64_m10k", Reason: "invalid decimal limb order"}
	}
	return int64M10kCodec{order: order}, nil
}

// MustNewInt64M10kCodec is like NewInt64M10kCodec but panics on error.
func MustNewInt64M10kCodec(order DecimalLimbOrder) Codec[int64] {
	c, err := NewInt64M10kCodec(order)
	if err != nil {
		panic(err)
	}
	return c
}

func decimalLimbOrderFromID(id string) (DecimalLimbOrder, error) {
	switch id {
	case "low_to_high":
		return DecimalLimbLowToHigh, nil
	case "high_to_low":
		return DecimalLimbHighToLow, nil
	default:
		return 0, fmt.Errorf("%w: invalid decimal limb order %q", ErrUnknownCodec, id)
	}
}

func init() {
	registerM10kDescriptors()
}

func registerM10kDescriptors() {
	for _, order := range []DecimalLimbOrder{DecimalLimbLowToHigh, DecimalLimbHighToLow} {
		registerCodecDescriptor(CodecDescriptor{
			ID:           "uint32_m10k/order:" + order.String(),
			Name:         "uint32_m10k",
			Family:       CodecFamilyDecimalLimb,
			ValueKind:    CodecValueUint32,
			RegisterSpec: RegisterSpec{Count: 2},
			ByteSpec:     ByteSpec{Count: 4},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           "int32_m10k/order:" + order.String(),
			Name:         "int32_m10k",
			Family:       CodecFamilyDecimalLimb,
			ValueKind:    CodecValueInt32,
			RegisterSpec: RegisterSpec{Count: 2},
			ByteSpec:     ByteSpec{Count: 4},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           "uint48_m10k/order:" + order.String(),
			Name:         "uint48_m10k",
			Family:       CodecFamilyDecimalLimb,
			ValueKind:    CodecValueUint48,
			RegisterSpec: RegisterSpec{Count: 3},
			ByteSpec:     ByteSpec{Count: 6},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           "int48_m10k/order:" + order.String(),
			Name:         "int48_m10k",
			Family:       CodecFamilyDecimalLimb,
			ValueKind:    CodecValueInt48,
			RegisterSpec: RegisterSpec{Count: 3},
			ByteSpec:     ByteSpec{Count: 6},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           "uint64_m10k/order:" + order.String(),
			Name:         "uint64_m10k",
			Family:       CodecFamilyDecimalLimb,
			ValueKind:    CodecValueUint64,
			RegisterSpec: RegisterSpec{Count: 4},
			ByteSpec:     ByteSpec{Count: 8},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           "int64_m10k/order:" + order.String(),
			Name:         "int64_m10k",
			Family:       CodecFamilyDecimalLimb,
			ValueKind:    CodecValueInt64,
			RegisterSpec: RegisterSpec{Count: 4},
			ByteSpec:     ByteSpec{Count: 8},
			Layouts:      nil,
		})
	}
}
