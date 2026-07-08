// SPDX-License-Identifier: MIT

package codec

// registeredDescriptors is the source of truth for descriptor derivation.
// Codec constructors (in codec_numeric.go, codec_text.go, codec_bytes.go)
// register their descriptor metadata here so that discovery APIs can return
// derived views without hand-authoring a separate table. Descriptors must
// not be authored independently from codec definitions; tests verify
// that registered descriptors and instantiated codecs agree.
var registeredDescriptors []CodecDescriptor
var registeredIDs = make(map[string]struct{})

// registerCodecDescriptor adds a descriptor to the registry. It is called
// from init() or from codec constructors when built-in codecs are registered.
// Layout variants (e.g. uint32 with layout 4321 vs 2143) are registered as
// separate descriptors with distinct IDs (e.g. "uint32/layout:4321").
// Panics on duplicate ID (init-time guard).
func registerCodecDescriptor(d CodecDescriptor) {
	if _, exists := registeredIDs[d.ID]; exists {
		panic("codec: duplicate descriptor ID: " + d.ID)
	}
	registeredIDs[d.ID] = struct{}{}
	registeredDescriptors = append(registeredDescriptors, d)
}
