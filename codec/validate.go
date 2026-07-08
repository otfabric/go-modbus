// SPDX-License-Identifier: MIT

package codec

// ValidateRegisterSpec returns an error if the number of registers in regs does
// not match spec.Count. codecID is used in the error for diagnostics (e.g. codec.ID());
// if empty, "codec" is used. Use this before calling DecodeRegisters or after
// EncodeRegisters to enforce the codec contract.
func ValidateRegisterSpec(spec RegisterSpec, regs []uint16, codecID string) error {
	if uint16(len(regs)) != spec.Count {
		if codecID == "" {
			codecID = "codec"
		}
		return &CodecRegisterCountError{
			Codec:    codecID,
			Expected: spec,
			Actual:   uint16(len(regs)),
		}
	}
	return nil
}

// ValidateByteSpec returns an error if the number of bytes in b does not match
// spec.Count. codecID is used in the error for diagnostics (e.g. codec.ID());
// if empty, "codec" is used. Used for offline tooling and test helpers when
// working with byte-oriented views of codec data; transport remains register-native.
func ValidateByteSpec(spec ByteSpec, b []byte, codecID string) error {
	if uint16(len(b)) != spec.Count {
		if codecID == "" {
			codecID = "codec"
		}
		return &CodecByteCountError{
			Codec:    codecID,
			Expected: spec,
			Actual:   uint16(len(b)),
		}
	}
	return nil
}
