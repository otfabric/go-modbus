package protocol

// Response is a minimal view of a Modbus response used by detection probes.
type Response struct {
	FunctionCode FunctionCode
	Payload      []byte
}

// IsValidModbusException returns true when res is a well-formed Modbus exception:
// function code equals reqFC | 0x80 and payload is a single byte in the valid exception code range (0x01–0x0B).
func IsValidModbusException(reqFC FunctionCode, res Response) bool {
	return res.FunctionCode == FunctionCode(uint8(reqFC)|0x80) &&
		len(res.Payload) == 1 &&
		res.Payload[0] >= 0x01 && res.Payload[0] <= 0x0b
}

// DetectionProbe is one entry in the probe set used by SupportsFunction.
type DetectionProbe struct {
	FC       FunctionCode
	Payload  []byte
	Validate func(reqFC FunctionCode, res Response) bool
}

var allDetectionProbes []DetectionProbe
var probeByFC map[FunctionCode]DetectionProbe

func init() {
	allDetectionProbes = []DetectionProbe{
		{
			FC:      FCDiagnostics,
			Payload: []byte{0x00, 0x00, 0x12, 0x34},
			Validate: func(reqFC FunctionCode, res Response) bool {
				return IsValidModbusException(reqFC, res)
			},
		},
		{
			FC:      FCEncapsulatedInterface,
			Payload: []byte{byte(MEIReadDeviceIdentification), ReadDeviceIDBasic, 0x00},
			Validate: func(reqFC FunctionCode, res Response) bool {
				if IsValidModbusException(reqFC, res) {
					return true
				}
				return res.FunctionCode == reqFC && len(res.Payload) >= 6
			},
		},
		{
			FC:      FCReadHoldingRegisters,
			Payload: []byte{0, 0, 0, 1},
			Validate: func(reqFC FunctionCode, res Response) bool {
				if IsValidModbusException(reqFC, res) {
					return true
				}
				return res.FunctionCode == reqFC && len(res.Payload) == 3 && res.Payload[0] == 2
			},
		},
		{
			FC:      FCReadInputRegisters,
			Payload: []byte{0, 0, 0, 1},
			Validate: func(reqFC FunctionCode, res Response) bool {
				if IsValidModbusException(reqFC, res) {
					return true
				}
				return res.FunctionCode == reqFC && len(res.Payload) == 3 && res.Payload[0] == 2
			},
		},
		{
			FC:      FCReadCoils,
			Payload: []byte{0, 0, 0, 1},
			Validate: func(reqFC FunctionCode, res Response) bool {
				if IsValidModbusException(reqFC, res) {
					return true
				}
				return res.FunctionCode == reqFC && len(res.Payload) == 2 && res.Payload[0] == 1
			},
		},
		{
			FC:      FCReadDiscreteInputs,
			Payload: []byte{0, 0, 0, 1},
			Validate: func(reqFC FunctionCode, res Response) bool {
				if IsValidModbusException(reqFC, res) {
					return true
				}
				return res.FunctionCode == reqFC && len(res.Payload) == 2 && res.Payload[0] == 1
			},
		},
		{
			FC:      FCReportServerID,
			Payload: nil,
			Validate: func(reqFC FunctionCode, res Response) bool {
				if IsValidModbusException(reqFC, res) {
					return true
				}
				if res.FunctionCode != reqFC || len(res.Payload) < 2 {
					return false
				}
				byteCount := res.Payload[0]
				return int(byteCount) == len(res.Payload)-1 && byteCount >= 2
			},
		},
		{
			FC:      FCReadFIFOQueue,
			Payload: []byte{0, 0},
			Validate: func(reqFC FunctionCode, res Response) bool {
				if IsValidModbusException(reqFC, res) {
					return true
				}
				return res.FunctionCode == reqFC && len(res.Payload) >= 4
			},
		},
		{
			FC:      FCReadFileRecord,
			Payload: []byte{7, 0x06, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01},
			Validate: func(reqFC FunctionCode, res Response) bool {
				if IsValidModbusException(reqFC, res) {
					return true
				}
				if res.FunctionCode != reqFC || len(res.Payload) < 4 {
					return false
				}
				byteCount := res.Payload[0]
				return int(byteCount) == len(res.Payload)-1 && byteCount == 3
			},
		},
	}
	probeByFC = make(map[FunctionCode]DetectionProbe, len(allDetectionProbes))
	for _, p := range allDetectionProbes {
		probeByFC[p.FC] = p
	}
}

// AllDetectionProbes returns a copy of the full ordered probe table for device detection.
func AllDetectionProbes() []DetectionProbe {
	out := make([]DetectionProbe, len(allDetectionProbes))
	copy(out, allDetectionProbes)
	return out
}

// GetProbeForFC returns the detection probe for the given function code, if defined.
func GetProbeForFC(fc FunctionCode) (DetectionProbe, bool) {
	p, ok := probeByFC[fc]
	return p, ok
}
