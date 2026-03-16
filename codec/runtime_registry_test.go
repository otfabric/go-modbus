package codec

import (
	"testing"
)

func TestRuntimeCodecByID_KnownIDSucceeds(t *testing.T) {
	rc, ok, err := RuntimeCodecByID("uint32/layout:4321")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok true for known ID")
	}
	if rc == nil {
		t.Fatal("expected non-nil RuntimeCodec")
	}
	if rc.ID() != "uint32/layout:4321" {
		t.Errorf("ID() = %q, want %q", rc.ID(), "uint32/layout:4321")
	}
	if rc.ValueKind() != CodecValueUint32 {
		t.Errorf("ValueKind() = %v, want CodecValueUint32", rc.ValueKind())
	}
	// Quick decode sanity check
	got, err := rc.DecodeRegistersAny([]uint16{0x1234, 0x5678})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got.(uint32); !ok || v != 0x12345678 {
		t.Errorf("DecodeRegistersAny = %v (%T), want uint32(0x12345678)", got, got)
	}
}

func TestRuntimeCodecByID_UnknownIDReturnsNotFound(t *testing.T) {
	rc, ok, err := RuntimeCodecByID("nonexistent/codec:id")
	if err != nil {
		t.Fatalf("unexpected error for unknown ID: %v", err)
	}
	if ok {
		t.Fatal("expected ok false for unknown ID")
	}
	if rc != nil {
		t.Errorf("expected nil RuntimeCodec, got %v", rc)
	}
}

func TestRuntimeCodecFromDescriptor_FromCodecDescriptorByID(t *testing.T) {
	desc, ok := CodecDescriptorByID("ascii/registers:4")
	if !ok {
		t.Fatal("expected descriptor for ascii/registers:4")
	}
	rc, err := RuntimeCodecFromDescriptor(desc)
	if err != nil {
		t.Fatalf("RuntimeCodecFromDescriptor: %v", err)
	}
	if rc.ID() != "ascii/registers:4" {
		t.Errorf("ID() = %q, want ascii/registers:4", rc.ID())
	}
	if rc.RegisterSpec().Count != 4 {
		t.Errorf("RegisterSpec().Count = %d, want 4", rc.RegisterSpec().Count)
	}
}

func TestMustRuntimeCodecByID_KnownID(t *testing.T) {
	rc := MustRuntimeCodecByID("ip_addr")
	if rc == nil || rc.ID() != "ip_addr" {
		t.Errorf("MustRuntimeCodecByID(ip_addr) = %v", rc)
	}
}

func TestMustRuntimeCodecByID_UnknownIDPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for unknown ID")
		}
	}()
	MustRuntimeCodecByID("no/such/id")
}

// TestRuntimeRegistry_EveryDescriptorInstantiable ensures every concrete descriptor
// returned by AvailableCodecDescriptors can be instantiated as a RuntimeCodec, and
// that the instantiated codec's ID, RegisterSpec, ByteSpec, and ValueKind match
// the descriptor (parity test to catch descriptor/runtime drift).
func TestRuntimeRegistry_EveryDescriptorInstantiable(t *testing.T) {
	all := AvailableCodecDescriptors()
	if len(all) == 0 {
		t.Skip("no descriptors registered (e.g. in a test that clears registry)")
	}
	for i, d := range all {
		rc, err := RuntimeCodecFromDescriptor(d)
		if err != nil {
			t.Errorf("descriptor[%d] ID=%q: cannot instantiate runtime codec: %v", i, d.ID, err)
			continue
		}
		if rc.ID() != d.ID {
			t.Errorf("descriptor[%d] ID=%q: instantiated codec has ID %q", i, d.ID, rc.ID())
		}
		if got := rc.RegisterSpec(); got != d.RegisterSpec {
			t.Errorf("descriptor[%d] ID=%q: RegisterSpec %+v != descriptor %+v", i, d.ID, got, d.RegisterSpec)
		}
		if got := rc.ByteSpec(); got != d.ByteSpec {
			t.Errorf("descriptor[%d] ID=%q: ByteSpec %+v != descriptor %+v", i, d.ID, got, d.ByteSpec)
		}
		if rc.ValueKind() != d.ValueKind {
			t.Errorf("descriptor[%d] ID=%q: ValueKind %v != descriptor %v", i, d.ID, rc.ValueKind(), d.ValueKind)
		}
	}
}

func TestRuntimeCodecsForRegisterCount(t *testing.T) {
	// 2 registers: uint32, int32, float32 with various layouts
	codecs, err := RuntimeCodecsForRegisterCount(2)
	if err != nil {
		t.Fatalf("RuntimeCodecsForRegisterCount(2): %v", err)
	}
	if len(codecs) == 0 {
		t.Fatal("expected at least one codec for 2 registers")
	}
	for _, rc := range codecs {
		if rc.RegisterSpec().Count != 2 {
			t.Errorf("codec %q: RegisterSpec().Count = %d, want 2", rc.ID(), rc.RegisterSpec().Count)
		}
		// Directly usable: decode with dummy regs
		_, err := DecodeRegistersAny([]uint16{0, 0}, rc)
		if err != nil {
			t.Errorf("codec %q DecodeRegistersAny: %v", rc.ID(), err)
		}
	}
}

func TestRuntimeCodecsForByteCount(t *testing.T) {
	// 4 bytes: bytes/bytes:4, uint8_slice/bytes:4, ip_addr
	codecs, err := RuntimeCodecsForByteCount(4)
	if err != nil {
		t.Fatalf("RuntimeCodecsForByteCount(4): %v", err)
	}
	if len(codecs) == 0 {
		t.Fatal("expected at least one codec for 4 bytes")
	}
	for _, rc := range codecs {
		if rc.ByteSpec().Count != 4 {
			t.Errorf("codec %q: ByteSpec().Count = %d, want 4", rc.ID(), rc.ByteSpec().Count)
		}
	}
}

func TestFindRuntimeCodecs_ByFamily(t *testing.T) {
	codecs, err := FindRuntimeCodecs(CodecQuery{Family: CodecFamilyFloat})
	if err != nil {
		t.Fatalf("FindRuntimeCodecs(Family=Float): %v", err)
	}
	for _, rc := range codecs {
		if rc.ValueKind() != CodecValueFloat32 && rc.ValueKind() != CodecValueFloat64 {
			t.Errorf("codec %q: ValueKind %v not float", rc.ID(), rc.ValueKind())
		}
	}
}

func TestRuntimeCodec_NameAndByteSpec(t *testing.T) {
	// Cover Name() and ByteSpec() on runtime codecs (used by discovery/tooling).
	rc, ok, _ := RuntimeCodecByID("uint32/layout:4321")
	if !ok || rc == nil {
		t.Fatal("need uint32 codec")
	}
	if rc.Name() == "" {
		t.Error("Name() should be non-empty")
	}
	_ = rc.ByteSpec()
	// Text codec
	rc2, _, _ := RuntimeCodecByID("ascii/registers:4")
	if rc2 != nil && rc2.Name() == "" {
		t.Error("ascii codec Name() should be non-empty")
	}
	// Bytes codec
	rc3, _, _ := RuntimeCodecByID("ip_addr")
	if rc3 != nil {
		_ = rc3.ByteSpec()
	}
}

func TestFindRuntimeCodecs_ByRegisterCountAndValueKind(t *testing.T) {
	codecs, err := FindRuntimeCodecs(CodecQuery{RegisterCount: 2, ValueKind: CodecValueUint32})
	if err != nil {
		t.Fatalf("FindRuntimeCodecs: %v", err)
	}
	for _, rc := range codecs {
		if rc.RegisterSpec().Count != 2 || rc.ValueKind() != CodecValueUint32 {
			t.Errorf("codec %q: count=%d kind=%v", rc.ID(), rc.RegisterSpec().Count, rc.ValueKind())
		}
		// Encode then decode round-trip. Use 12345678 so it fits both binary uint32 and uint32_m10k (max 99_999_999).
		testVal := uint32(12345678)
		regs, err := EncodeRegistersAny(testVal, rc)
		if err != nil {
			t.Errorf("codec %q EncodeRegistersAny: %v", rc.ID(), err)
			continue
		}
		got, err := DecodeRegistersAny(regs, rc)
		if err != nil {
			t.Errorf("codec %q DecodeRegistersAny: %v", rc.ID(), err)
			continue
		}
		if v, ok := got.(uint32); !ok || v != testVal {
			t.Errorf("codec %q round-trip = %v (%T), want %d", rc.ID(), got, got, testVal)
		}
	}
}
