// SPDX-License-Identifier: MIT

package codec

import "math"

// ValidateRegisterSpec returns an error if the number of registers in regs does
// not match spec.Count. codecID is used in the error for diagnostics (e.g. codec.ID());
// if empty, "codec" is used. Use this before calling DecodeRegisters or after
// EncodeRegisters to enforce the codec contract.
func ValidateRegisterSpec(spec RegisterSpec, regs []uint16, codecID string) error {
	if len(regs) != int(spec.Count) {
		if codecID == "" {
			codecID = "codec"
		}
		return &CodecRegisterCountError{
			Codec:    codecID,
			Expected: spec,
			Actual:   saturatedCount(len(regs)),
		}
	}
	return nil
}

// ValidateByteSpec returns an error if the number of bytes in b does not match
// spec.Count. codecID is used in the error for diagnostics (e.g. codec.ID());
// if empty, "codec" is used. Used for offline tooling and test helpers when
// working with byte-oriented views of codec data; transport remains register-native.
func ValidateByteSpec(spec ByteSpec, b []byte, codecID string) error {
	if len(b) != int(spec.Count) {
		if codecID == "" {
			codecID = "codec"
		}
		return &CodecByteCountError{
			Codec:    codecID,
			Expected: spec,
			Actual:   saturatedCount(len(b)),
		}
	}
	return nil
}

// saturatedCount reports a length in the uint16 Actual field of the count
// errors: a length beyond 65535 shows as 65535, never as its value modulo 2^16.
func saturatedCount(n int) uint16 {
	if n < 0 || n > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(n)
}
