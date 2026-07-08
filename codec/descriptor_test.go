// SPDX-License-Identifier: MIT

package codec

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
	if s := CodecFamilyTime.String(); s != "time" {
		t.Errorf("CodecFamilyTime.String() = %q, want \"time\"", s)
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
	if s := CodecValueTime.String(); s != "time" {
		t.Errorf("CodecValueTime.String() = %q, want \"time\"", s)
	}
	if s := CodecValueKind(255).String(); s != "unknown" {
		t.Errorf("unknown CodecValueKind: got %q", s)
	}
}
