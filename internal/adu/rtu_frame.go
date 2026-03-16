package adu

// MaxRTUFrameLength is the maximum length of an RTU ADU (256 bytes per spec).
const MaxRTUFrameLength = 256

// AssembleRTUFrame builds an RTU frame: unitID, fc, payload, then CRC (LSB first).
func AssembleRTUFrame(unitID uint8, fc byte, payload []byte) []byte {
	var c crc
	adu := make([]byte, 0, 1+1+len(payload)+2)
	adu = append(adu, unitID, fc)
	adu = append(adu, payload...)
	c.init()
	c.add(adu)
	adu = append(adu, c.value()...)
	return adu
}

// ValidateRTUCRC checks that the last two bytes of frame are the CRC of frame[:len(frame)-2].
// Frame must have at least 4 bytes (unitID, fc, 2-byte CRC).
func ValidateRTUCRC(frame []byte) bool {
	if len(frame) < 4 {
		return false
	}
	n := len(frame) - 2
	var c crc
	c.init()
	c.add(frame[:n])
	return c.isEqual(frame[n], frame[n+1])
}

// ParseRTUFrame extracts unitID, function code, and payload from a full RTU frame after CRC validation.
// Caller must ensure ValidateRTUCRC(frame) is true.
func ParseRTUFrame(frame []byte) (unitID uint8, fc byte, payload []byte) {
	if len(frame) < 4 {
		return 0, 0, nil
	}
	n := len(frame) - 2
	return frame[0], frame[1], append([]byte(nil), frame[2:n]...)
}
