package modbus

import (
	"testing"
)

func TestCodecFamily_String(t *testing.T) {
	if s := CodecFamilyInteger.String(); s != "integer" {
		t.Errorf("CodecFamilyInteger.String() = %q, want \"integer\"", s)
	}
	if s := CodecFamilyUnknown.String(); s != "unknown" {
		t.Errorf("CodecFamilyUnknown.String() = %q", s)
	}
	if s := CodecFamily(255).String(); s != "unknown" {
		t.Errorf("unknown CodecFamily: got %q", s)
	}
}

func TestCodecValueKind_String(t *testing.T) {
	if s := CodecValueUint32.String(); s != "uint32" {
		t.Errorf("CodecValueUint32.String() = %q, want \"uint32\"", s)
	}
	if s := CodecValueFloat64.String(); s != "float64" {
		t.Errorf("CodecValueFloat64.String() = %q", s)
	}
	if s := CodecValueKind(255).String(); s != "unknown" {
		t.Errorf("unknown CodecValueKind: got %q", s)
	}
}
