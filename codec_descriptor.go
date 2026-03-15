package modbus

//
// Codec family and value kind (for discovery filtering and grouping)
//

// CodecFamily classifies a codec for discovery and CLI grouping.
type CodecFamily uint8

const (
	CodecFamilyUnknown CodecFamily = iota
	CodecFamilyInteger
	CodecFamilyFloat
	CodecFamilyText
	CodecFamilyBCD
	CodecFamilyBytes
	CodecFamilyNetwork
	CodecFamilyHardwareAddress
	CodecFamilyDecimalLimb
	CodecFamilyVendorSpecific
)

var codecFamilyNames = map[CodecFamily]string{
	CodecFamilyUnknown:         "unknown",
	CodecFamilyInteger:         "integer",
	CodecFamilyFloat:           "float",
	CodecFamilyText:            "text",
	CodecFamilyBCD:             "bcd",
	CodecFamilyBytes:           "bytes",
	CodecFamilyNetwork:         "network",
	CodecFamilyHardwareAddress: "hardware_address",
	CodecFamilyDecimalLimb:     "decimal_limb",
	CodecFamilyVendorSpecific:  "vendor_specific",
}

// DecimalLimbOrder controls how base-10000 limbs are ordered across registers.
// Used by M10k (modulo-10000) codecs; distinct from byte-order RegisterLayout.
type DecimalLimbOrder uint8

const (
	// DecimalLimbLowToHigh: first register is least-significant limb.
	// Schneider equivalents: 2143, 21-65, 21-87.
	DecimalLimbLowToHigh DecimalLimbOrder = iota + 1
	// DecimalLimbHighToLow: first register is most-significant limb.
	// Schneider equivalents: 4321, 65-21, 87-21.
	DecimalLimbHighToLow
)

func (o DecimalLimbOrder) String() string {
	switch o {
	case DecimalLimbLowToHigh:
		return "low_to_high"
	case DecimalLimbHighToLow:
		return "high_to_low"
	default:
		return "unknown"
	}
}

func (f CodecFamily) String() string {
	if s, ok := codecFamilyNames[f]; ok {
		return s
	}
	return "unknown"
}

// CodecValueKind is the type of value a codec produces or consumes.
type CodecValueKind uint8

const (
	CodecValueUnknown CodecValueKind = iota
	CodecValueUint16
	CodecValueInt16
	CodecValueUint32
	CodecValueInt32
	CodecValueUint48
	CodecValueInt48
	CodecValueUint64
	CodecValueInt64
	CodecValueFloat32
	CodecValueFloat64
	CodecValueString
	CodecValueByteSlice
	CodecValueUint8Slice
	CodecValueIP
	CodecValueHardwareAddr
)

var codecValueKindNames = map[CodecValueKind]string{
	CodecValueUnknown:      "unknown",
	CodecValueUint16:       "uint16",
	CodecValueInt16:        "int16",
	CodecValueUint32:       "uint32",
	CodecValueInt32:        "int32",
	CodecValueUint48:       "uint48",
	CodecValueInt48:        "int48",
	CodecValueUint64:       "uint64",
	CodecValueInt64:        "int64",
	CodecValueFloat32:      "float32",
	CodecValueFloat64:      "float64",
	CodecValueString:       "string",
	CodecValueByteSlice:    "byte_slice",
	CodecValueUint8Slice:   "uint8_slice",
	CodecValueIP:           "ip",
	CodecValueHardwareAddr: "hardware_addr",
}

func (k CodecValueKind) String() string {
	if s, ok := codecValueKindNames[k]; ok {
		return s
	}
	return "unknown"
}

//
// Layout and descriptor types
//

// RegisterLayoutDescriptor describes a layout variant for discovery (e.g. "4321", "2143").
type RegisterLayoutDescriptor struct {
	Name   string
	Common bool
	Layout RegisterLayout
}

// CodecDescriptor is metadata for a codec option. Descriptors are derived from
// the registration table (see codec_registration.go), not hand-authored.
// Layouts is nil for layout-less codecs (ASCII, BCD, IP, EUI48, etc.).
//
// In the current implementation each concrete option (e.g. a specific layout or
// width) is registered as its own descriptor; CodecCandidate then provides a
// lightweight view (CodecID + LayoutName) for discovery/CLI without duplicating
// full descriptor data.
type CodecDescriptor struct {
	ID           string
	Name         string
	Family       CodecFamily
	ValueKind    CodecValueKind
	RegisterSpec RegisterSpec
	ByteSpec     ByteSpec
	Layouts      []RegisterLayoutDescriptor // nil for layout-less codecs
}

// CodecCandidate is a lightweight concrete selectable option for discovery/CLI.
// CodecID must equal the CodecDescriptor.ID of the option it refers to (same
// namespace). LayoutName is empty for layout-less codecs. Candidates mirror
// the registered descriptors rather than expanding from a separate family list.
type CodecCandidate struct {
	CodecID    string
	LayoutName string
}

// CodecQuery filters descriptors by register count, byte count, family, and value kind.
// Zero values mean "no filter" for that field.
type CodecQuery struct {
	RegisterCount uint16
	ByteCount     uint16
	Family        CodecFamily
	ValueKind     CodecValueKind
}
