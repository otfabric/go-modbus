package modbus

// Discovery exposes a curated subset of common codec widths (e.g. text/UTF-16:
// 1, 2, 3, 4, 6, 8, 12, 16, 20, 32, 48, 64 registers; bytes: 2, 4, 6, 8, 10, 12,
// 14, 16, 20, 24, 32, 48, 64 bytes). Not every valid width is registered;
// constructors accept any valid width. Constructors define what is valid;
// registration defines what is discoverable. Use discovery for UI/CLI or when
// you do not know the width in advance.

// descriptorCopy returns a deep copy of d so callers cannot mutate shared Layouts.
func descriptorCopy(d CodecDescriptor) CodecDescriptor {
	out := d
	if len(d.Layouts) > 0 {
		out.Layouts = make([]RegisterLayoutDescriptor, len(d.Layouts))
		copy(out.Layouts, d.Layouts)
	}
	return out
}

// AvailableCodecDescriptors returns a copy of all registered codec descriptors.
// Layouts are deep-copied so returned descriptors are immutable to the caller.
func AvailableCodecDescriptors() []CodecDescriptor {
	out := make([]CodecDescriptor, len(registeredDescriptors))
	for i, d := range registeredDescriptors {
		out[i] = descriptorCopy(d)
	}
	return out
}

// CodecDescriptorsForRegisterCount returns descriptors whose RegisterSpec.Count
// equals count. Returned descriptors are deep-copied.
func CodecDescriptorsForRegisterCount(count uint16) []CodecDescriptor {
	var out []CodecDescriptor
	for _, d := range registeredDescriptors {
		if d.RegisterSpec.Count == count {
			out = append(out, descriptorCopy(d))
		}
	}
	return out
}

// CodecDescriptorsForByteCount returns descriptors whose ByteSpec.Count equals count.
// Returned descriptors are deep-copied.
func CodecDescriptorsForByteCount(count uint16) []CodecDescriptor {
	var out []CodecDescriptor
	for _, d := range registeredDescriptors {
		if d.ByteSpec.Count == count {
			out = append(out, descriptorCopy(d))
		}
	}
	return out
}

// CodecDescriptorByID returns the descriptor with the given ID, if registered.
// The returned descriptor is a deep copy.
func CodecDescriptorByID(id string) (CodecDescriptor, bool) {
	for _, d := range registeredDescriptors {
		if d.ID == id {
			return descriptorCopy(d), true
		}
	}
	return CodecDescriptor{}, false
}

// CodecCandidatesForRegisterCount returns one candidate per descriptor whose
// RegisterSpec.Count equals count. CodecID is the descriptor's ID; LayoutName
// is the layout name when the descriptor has exactly one layout, otherwise empty.
func CodecCandidatesForRegisterCount(count uint16) []CodecCandidate {
	var out []CodecCandidate
	for _, d := range registeredDescriptors {
		if d.RegisterSpec.Count != count {
			continue
		}
		c := CodecCandidate{CodecID: d.ID}
		if len(d.Layouts) == 1 {
			c.LayoutName = d.Layouts[0].Name
		}
		out = append(out, c)
	}
	return out
}

// CodecCandidatesForByteCount returns one candidate per descriptor whose
// ByteSpec.Count equals count.
func CodecCandidatesForByteCount(count uint16) []CodecCandidate {
	var out []CodecCandidate
	for _, d := range registeredDescriptors {
		if d.ByteSpec.Count != count {
			continue
		}
		c := CodecCandidate{CodecID: d.ID}
		if len(d.Layouts) == 1 {
			c.LayoutName = d.Layouts[0].Name
		}
		out = append(out, c)
	}
	return out
}

// FindCodecDescriptors returns descriptors matching the query. Zero values in
// CodecQuery mean "no filter" for that field. Returned descriptors are deep-copied.
func FindCodecDescriptors(q CodecQuery) []CodecDescriptor {
	var out []CodecDescriptor
	for _, d := range registeredDescriptors {
		if q.RegisterCount != 0 && d.RegisterSpec.Count != q.RegisterCount {
			continue
		}
		if q.ByteCount != 0 && d.ByteSpec.Count != q.ByteCount {
			continue
		}
		if q.Family != 0 && d.Family != q.Family {
			continue
		}
		if q.ValueKind != 0 && d.ValueKind != q.ValueKind {
			continue
		}
		out = append(out, descriptorCopy(d))
	}
	return out
}
