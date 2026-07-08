// SPDX-License-Identifier: MIT

package codec

import (
	"testing"
)

func TestAvailableCodecDescriptors_ReturnsCopy(t *testing.T) {
	all := AvailableCodecDescriptors()
	if all == nil {
		t.Fatal("AvailableCodecDescriptors() must not return nil")
	}
	// Second call returns independent copy (e.g. for deep-copy of Layouts)
	all2 := AvailableCodecDescriptors()
	if len(all) != len(all2) {
		t.Errorf("two calls returned different lengths: %d vs %d", len(all), len(all2))
	}
}

func TestCodecDescriptorByID_NotFound(t *testing.T) {
	_, ok := CodecDescriptorByID("nonexistent/id")
	if ok {
		t.Error("expected false for nonexistent ID")
	}
}

func TestCodecDescriptorsForRegisterCount_ReturnsOnlyMatchingCount(t *testing.T) {
	got := CodecDescriptorsForRegisterCount(2)
	if got == nil {
		got = []CodecDescriptor{}
	}
	for i, d := range got {
		if d.RegisterSpec.Count != 2 {
			t.Errorf("descriptor[%d] %s: RegisterSpec.Count = %d, want 2", i, d.ID, d.RegisterSpec.Count)
		}
	}
}

func TestCodecCandidatesForRegisterCount_ReturnsOnlyMatchingCount(t *testing.T) {
	got := CodecCandidatesForRegisterCount(2)
	if got == nil {
		got = []CodecCandidate{}
	}
	for i, c := range got {
		d, ok := CodecDescriptorByID(c.CodecID)
		if !ok {
			t.Errorf("candidate[%d] CodecID %q not found in registry", i, c.CodecID)
			continue
		}
		if d.RegisterSpec.Count != 2 {
			t.Errorf("candidate[%d] %s: descriptor RegisterSpec.Count = %d, want 2", i, c.CodecID, d.RegisterSpec.Count)
		}
	}
}

func TestFindCodecDescriptors_ZeroQuery_ReturnsAll(t *testing.T) {
	got := FindCodecDescriptors(CodecQuery{})
	all := AvailableCodecDescriptors()
	if len(got) != len(all) {
		t.Errorf("zero query: got %d descriptors, want %d (same as Available)", len(got), len(all))
	}
}

func TestFindCodecDescriptors_WithFilters(t *testing.T) {
	got := FindCodecDescriptors(CodecQuery{
		RegisterCount: 2,
		Family:        CodecFamilyInteger,
	})
	for i, d := range got {
		if d.RegisterSpec.Count != 2 {
			t.Errorf("descriptor[%d] RegisterSpec.Count = %d, want 2", i, d.RegisterSpec.Count)
		}
		if d.Family != CodecFamilyInteger {
			t.Errorf("descriptor[%d] Family = %v, want CodecFamilyInteger", i, d.Family)
		}
	}
}

func TestRegisteredDescriptors_NoDuplicateIDs(t *testing.T) {
	all := AvailableCodecDescriptors()
	seen := make(map[string]struct{}, len(all))
	for _, d := range all {
		if _, exists := seen[d.ID]; exists {
			t.Errorf("duplicate descriptor ID: %q", d.ID)
		}
		seen[d.ID] = struct{}{}
	}
}

func TestRegisteredDescriptors_RepresentativeRoundTrips(t *testing.T) {
	families := map[CodecFamily]struct {
		id   string
		regs []uint16
	}{
		CodecFamilyInteger: {"uint16/layout:21", []uint16{0x1234}},
		CodecFamilyFloat:   {"float32/layout:4321", []uint16{0x4048, 0xF5C3}},
		CodecFamilyText:    {"ascii/registers:2", []uint16{0x4142, 0x4344}},
	}
	for fam, tc := range families {
		rc, ok, err := RuntimeCodecByID(tc.id)
		if err != nil {
			t.Errorf("family %v (id=%s): error: %v", fam, tc.id, err)
			continue
		}
		if !ok || rc == nil {
			t.Errorf("family %v (id=%s): not found", fam, tc.id)
			continue
		}
		decoded, err := DecodeRegistersAny(tc.regs, rc)
		if err != nil {
			t.Errorf("family %v (id=%s): decode: %v", fam, tc.id, err)
			continue
		}
		encoded, err := EncodeRegistersAny(decoded, rc)
		if err != nil {
			t.Errorf("family %v (id=%s): encode: %v", fam, tc.id, err)
			continue
		}
		if len(encoded) != len(tc.regs) {
			t.Errorf("family %v (id=%s): round-trip length %d != %d", fam, tc.id, len(encoded), len(tc.regs))
		}
		for i := range encoded {
			if encoded[i] != tc.regs[i] {
				t.Errorf("family %v (id=%s): reg[%d] = 0x%04X, want 0x%04X", fam, tc.id, i, encoded[i], tc.regs[i])
			}
		}
	}
}

// TestRegistryWithOneDescriptor verifies the registry returns a descriptor
// once registered (used when we add numeric codecs in Phase 4).
func TestRegistryWithOneDescriptor(t *testing.T) {
	// Save and restore registeredDescriptors and registeredIDs so we don't leak state
	savedDesc := registeredDescriptors
	savedIDs := registeredIDs
	defer func() {
		registeredDescriptors = savedDesc
		registeredIDs = savedIDs
	}()

	registeredDescriptors = nil
	registeredIDs = make(map[string]struct{})
	registerCodecDescriptor(CodecDescriptor{
		ID:           "uint32/layout:4321",
		Name:         "uint32",
		Family:       CodecFamilyInteger,
		ValueKind:    CodecValueUint32,
		RegisterSpec: RegisterSpec{Count: 2},
		ByteSpec:     ByteSpec{Count: 4},
		Layouts: []RegisterLayoutDescriptor{
			{Name: "4321", Common: true, Layout: Layout32_4321},
		},
	})

	all := AvailableCodecDescriptors()
	if len(all) != 1 {
		t.Fatalf("AvailableCodecDescriptors() len = %d, want 1", len(all))
	}
	if all[0].ID != "uint32/layout:4321" {
		t.Errorf("ID = %q, want \"uint32/layout:4321\"", all[0].ID)
	}

	d, ok := CodecDescriptorByID("uint32/layout:4321")
	if !ok {
		t.Fatal("CodecDescriptorByID: expected true")
	}
	if d.Name != "uint32" {
		t.Errorf("Name = %q, want \"uint32\"", d.Name)
	}

	forCount := CodecDescriptorsForRegisterCount(2)
	if len(forCount) != 1 {
		t.Errorf("CodecDescriptorsForRegisterCount(2) len = %d, want 1", len(forCount))
	}

	candidates := CodecCandidatesForRegisterCount(2)
	if len(candidates) != 1 {
		t.Fatalf("CodecCandidatesForRegisterCount(2) len = %d, want 1", len(candidates))
	}
	if candidates[0].CodecID != "uint32/layout:4321" {
		t.Errorf("CodecID = %q", candidates[0].CodecID)
	}
	if candidates[0].LayoutName != "4321" {
		t.Errorf("LayoutName = %q, want \"4321\"", candidates[0].LayoutName)
	}
}
