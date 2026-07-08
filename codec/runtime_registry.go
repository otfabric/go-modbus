// SPDX-License-Identifier: MIT

package codec

import (
	"fmt"
	"strings"
)

// RuntimeCodecFromDescriptor builds a RuntimeCodec from a concrete codec descriptor.
// The descriptor must represent a fully concrete codec (e.g. "uint32/layout:4321",
// "ascii/registers:4", "ip_addr"). Unknown or abstract descriptors return an error.
// Descriptor metadata is used only to identify the codec; the actual layout/count
// comes from the descriptor's Layouts, RegisterSpec, or ByteSpec.
func RuntimeCodecFromDescriptor(desc CodecDescriptor) (RuntimeCodec, error) {
	return buildRuntimeCodecFromDescriptor(desc)
}

// RuntimeCodecByID looks up a registered codec by ID and returns a RuntimeCodec.
// Returns (nil, false, nil) when the ID is not registered.
// Returns (nil, false, err) when the codec is registered but construction fails.
func RuntimeCodecByID(id string) (RuntimeCodec, bool, error) {
	desc, ok := CodecDescriptorByID(id)
	if !ok {
		return nil, false, nil
	}
	rc, err := buildRuntimeCodecFromDescriptor(desc)
	if err != nil {
		return nil, false, err
	}
	return rc, true, nil
}

// MustRuntimeCodecByID is like RuntimeCodecByID but panics if the ID is unknown
// or if construction fails. Use for known-good IDs (e.g. from config or tests).
func MustRuntimeCodecByID(id string) RuntimeCodec {
	rc, ok, err := RuntimeCodecByID(id)
	if err != nil {
		panic(err)
	}
	if !ok {
		panic("modbus: unknown runtime codec ID: " + id)
	}
	return rc
}

// RuntimeCodecsForRegisterCount returns runtime codecs for all registered
// descriptors whose RegisterSpec.Count equals count. Returned codecs are
// directly usable for DecodeRegistersAny, EncodeRegistersAny, and runtime
// plan execution. Returns an error if any descriptor fails to instantiate.
func RuntimeCodecsForRegisterCount(count uint16) ([]RuntimeCodec, error) {
	descs := CodecDescriptorsForRegisterCount(count)
	out := make([]RuntimeCodec, 0, len(descs))
	for _, d := range descs {
		rc, err := RuntimeCodecFromDescriptor(d)
		if err != nil {
			return nil, fmt.Errorf("runtime codec for %q: %w", d.ID, err)
		}
		out = append(out, rc)
	}
	return out, nil
}

// RuntimeCodecsForByteCount returns runtime codecs for all registered
// descriptors whose ByteSpec.Count equals count.
func RuntimeCodecsForByteCount(count uint16) ([]RuntimeCodec, error) {
	descs := CodecDescriptorsForByteCount(count)
	out := make([]RuntimeCodec, 0, len(descs))
	for _, d := range descs {
		rc, err := RuntimeCodecFromDescriptor(d)
		if err != nil {
			return nil, fmt.Errorf("runtime codec for %q: %w", d.ID, err)
		}
		out = append(out, rc)
	}
	return out, nil
}

// FindRuntimeCodecs returns runtime codecs for all descriptors matching the query.
// Zero values in CodecQuery mean "no filter" for that field.
func FindRuntimeCodecs(q CodecQuery) ([]RuntimeCodec, error) {
	descs := FindCodecDescriptors(q)
	out := make([]RuntimeCodec, 0, len(descs))
	for _, d := range descs {
		rc, err := RuntimeCodecFromDescriptor(d)
		if err != nil {
			return nil, fmt.Errorf("runtime codec for %q: %w", d.ID, err)
		}
		out = append(out, rc)
	}
	return out, nil
}

// buildRuntimeCodecFromDescriptor instantiates a RuntimeCodec from a descriptor.
// It handles all built-in concrete descriptor IDs registered by the codec packages.
func buildRuntimeCodecFromDescriptor(desc CodecDescriptor) (RuntimeCodec, error) {
	id := desc.ID
	kind := desc.ValueKind

	// Numeric codecs: require exactly one layout in descriptor
	if strings.Contains(id, "/layout:") {
		if len(desc.Layouts) == 0 {
			return nil, fmt.Errorf("%w: descriptor %q has no layout", ErrCodecLayout, id)
		}
		layout := desc.Layouts[0].Layout
		switch {
		case strings.HasPrefix(id, "uint16/"):
			c, err := NewUint16Codec(layout)
			if err != nil {
				return nil, err
			}
			return AsRuntimeCodec(c, kind), nil
		case strings.HasPrefix(id, "int16/"):
			c, err := NewInt16Codec(layout)
			if err != nil {
				return nil, err
			}
			return AsRuntimeCodec(c, kind), nil
		case strings.HasPrefix(id, "uint32/"):
			c, err := NewUint32Codec(layout)
			if err != nil {
				return nil, err
			}
			return AsRuntimeCodec(c, kind), nil
		case strings.HasPrefix(id, "int32/"):
			c, err := NewInt32Codec(layout)
			if err != nil {
				return nil, err
			}
			return AsRuntimeCodec(c, kind), nil
		case strings.HasPrefix(id, "float32/"):
			c, err := NewFloat32Codec(layout)
			if err != nil {
				return nil, err
			}
			return AsRuntimeCodec(c, kind), nil
		case strings.HasPrefix(id, "uint48/"):
			c, err := NewUint48Codec(layout)
			if err != nil {
				return nil, err
			}
			return AsRuntimeCodec(c, kind), nil
		case strings.HasPrefix(id, "int48/"):
			c, err := NewInt48Codec(layout)
			if err != nil {
				return nil, err
			}
			return AsRuntimeCodec(c, kind), nil
		case strings.HasPrefix(id, "uint64/"):
			c, err := NewUint64Codec(layout)
			if err != nil {
				return nil, err
			}
			return AsRuntimeCodec(c, kind), nil
		case strings.HasPrefix(id, "int64/"):
			c, err := NewInt64Codec(layout)
			if err != nil {
				return nil, err
			}
			return AsRuntimeCodec(c, kind), nil
		case strings.HasPrefix(id, "float64/"):
			c, err := NewFloat64Codec(layout)
			if err != nil {
				return nil, err
			}
			return AsRuntimeCodec(c, kind), nil
		default:
			return nil, fmt.Errorf("%w: unknown numeric codec ID %q", ErrUnknownCodec, id)
		}
	}

	// Sign-magnitude 16-bit
	if id == "int16_sign_magnitude" {
		return AsRuntimeCodec(NewInt16SignMagnitudeCodec(), kind), nil
	}

	// Decimal limb (M10k) codecs: uint32_m10k/order:low_to_high, etc.
	if strings.HasPrefix(id, "uint32_m10k/order:") {
		orderStr := strings.TrimPrefix(id, "uint32_m10k/order:")
		order, err := decimalLimbOrderFromID(orderStr)
		if err != nil {
			return nil, err
		}
		c, err := NewUint32M10kCodec(order)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	}
	if strings.HasPrefix(id, "uint48_m10k/order:") {
		orderStr := strings.TrimPrefix(id, "uint48_m10k/order:")
		order, err := decimalLimbOrderFromID(orderStr)
		if err != nil {
			return nil, err
		}
		c, err := NewUint48M10kCodec(order)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	}
	if strings.HasPrefix(id, "uint64_m10k/order:") {
		orderStr := strings.TrimPrefix(id, "uint64_m10k/order:")
		order, err := decimalLimbOrderFromID(orderStr)
		if err != nil {
			return nil, err
		}
		c, err := NewUint64M10kCodec(order)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	}
	if strings.HasPrefix(id, "int32_m10k/order:") {
		orderStr := strings.TrimPrefix(id, "int32_m10k/order:")
		order, err := decimalLimbOrderFromID(orderStr)
		if err != nil {
			return nil, err
		}
		c, err := NewInt32M10kCodec(order)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	}
	if strings.HasPrefix(id, "int48_m10k/order:") {
		orderStr := strings.TrimPrefix(id, "int48_m10k/order:")
		order, err := decimalLimbOrderFromID(orderStr)
		if err != nil {
			return nil, err
		}
		c, err := NewInt48M10kCodec(order)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	}
	if strings.HasPrefix(id, "int64_m10k/order:") {
		orderStr := strings.TrimPrefix(id, "int64_m10k/order:")
		order, err := decimalLimbOrderFromID(orderStr)
		if err != nil {
			return nil, err
		}
		c, err := NewInt64M10kCodec(order)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	}

	// Text codecs: id like "ascii/registers:4"
	n := desc.RegisterSpec.Count
	switch {
	case strings.HasPrefix(id, "ascii/registers:"):
		c, err := NewAsciiCodec(n)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	case strings.HasPrefix(id, "ascii_fixed/registers:"):
		c, err := NewAsciiFixedCodec(n)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	case strings.HasPrefix(id, "ascii_reverse/registers:"):
		c, err := NewAsciiReverseCodec(n)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	case strings.HasPrefix(id, "bcd/registers:"):
		c, err := NewBCDCodec(n)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	case strings.HasPrefix(id, "packed_bcd/registers:"):
		c, err := NewPackedBCDCodec(n)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	case strings.HasPrefix(id, "utf16be/registers:"):
		c, err := NewUTF16BECodec(n)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	case strings.HasPrefix(id, "utf16le/registers:"):
		c, err := NewUTF16LECodec(n)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	case strings.HasPrefix(id, "signed_packed_bcd/registers:"):
		c, err := NewSignedPackedBCDCodec(n)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	case strings.HasPrefix(id, "packed_bcd_reverse/registers:"):
		c, err := NewPackedBCDReverseCodec(n)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	}

	// Bytes/network codecs: id like "bytes/bytes:4" or fixed "ip_addr"
	switch {
	case strings.HasPrefix(id, "bytes/bytes:"):
		byteCount := desc.ByteSpec.Count
		c, err := NewBytesCodec(byteCount)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	case strings.HasPrefix(id, "uint8_slice/bytes:"):
		byteCount := desc.ByteSpec.Count
		c, err := NewUint8SliceCodec(byteCount)
		if err != nil {
			return nil, err
		}
		return AsRuntimeCodec(c, kind), nil
	case id == "ip_addr":
		return AsRuntimeCodec(NewIPAddrCodec(), kind), nil
	case id == "ipv6_addr":
		return AsRuntimeCodec(NewIPv6AddrCodec(), kind), nil
	case id == "eui48":
		return AsRuntimeCodec(NewEUI48Codec(), kind), nil
	case id == "eui64":
		return AsRuntimeCodec(NewEUI64Codec(), kind), nil
	}

	// Time codecs: datetime2_s2000, datetime3_s2000, datetime_ymdhms_*, datetime_iec870_*
	switch id {
	case "datetime2_s2000":
		return AsRuntimeCodec(NewDateTime2S2000Codec(), kind), nil
	case "datetime3_s2000":
		return AsRuntimeCodec(NewDateTime3S2000Codec(), kind), nil
	case "datetime_ymdhms_utc":
		return AsRuntimeCodec(NewDateTimeYMDhmsUTCCodec(), kind), nil
	case "datetime_ymdhms_local":
		return AsRuntimeCodec(NewDateTimeYMDhmsLocalCodec(), kind), nil
	case "datetime_ymdhms":
		return AsRuntimeCodec(NewDateTimeYMDhmsCodec(), kind), nil
	case "datetime_iec870_utc":
		return AsRuntimeCodec(NewDateTimeIEC870UTCCodec(), kind), nil
	case "datetime_iec870_local":
		return AsRuntimeCodec(NewDateTimeIEC870LocalCodec(), kind), nil
	case "datetime_iec870":
		return AsRuntimeCodec(NewDateTimeIEC870Codec(), kind), nil
	}

	return nil, fmt.Errorf("%w: unknown codec ID %q", ErrUnknownCodec, id)
}
