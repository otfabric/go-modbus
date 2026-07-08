// SPDX-License-Identifier: MIT

package codec

import "fmt"

//
// Runtime codec interfaces (MIGRATE_PLAN Phase 1)
//
// RuntimeDecoder and RuntimeEncoder provide type-erased decode/encode for CLI,
// descriptor-driven decoding, and batch plans. They do not replace typed Codec[T].
//

// RuntimeDecoder decodes raw registers into an any value. Used for discovery,
// CLI selection, and batch decode plans where the concrete type is not known at compile time.
type RuntimeDecoder interface {
	ID() string
	Name() string
	RegisterSpec() RegisterSpec
	ByteSpec() ByteSpec
	ValueKind() CodecValueKind
	DecodeRegistersAny(regs []uint16) (any, error)
}

// RuntimeEncoder encodes an any value into raw registers. Callers must pass a value
// whose type matches the codec; otherwise EncodeRegistersAny returns a *CodecValueError (never panics).
// For interface types (e.g. net.IP), a typed nil (var ip net.IP = nil) may pass the
// type assertion while an untyped nil may not; CLI/runtime callers should be aware when passing nil.
type RuntimeEncoder interface {
	ID() string
	Name() string
	RegisterSpec() RegisterSpec
	ByteSpec() ByteSpec
	ValueKind() CodecValueKind
	EncodeRegistersAny(value any) ([]uint16, error)
}

// RuntimeCodec is a combined runtime decoder and encoder.
type RuntimeCodec interface {
	RuntimeDecoder
	RuntimeEncoder
}

//
// Adapters: wrap typed Decoder[T] / Encoder[T] / Codec[T] as RuntimeDecoder / RuntimeEncoder / RuntimeCodec
//

type runtimeDecoderAdapter[T any] struct {
	inner Decoder[T]
	kind  CodecValueKind
}

func (a *runtimeDecoderAdapter[T]) ID() string                 { return a.inner.ID() }
func (a *runtimeDecoderAdapter[T]) Name() string               { return a.inner.Name() }
func (a *runtimeDecoderAdapter[T]) RegisterSpec() RegisterSpec { return a.inner.RegisterSpec() }
func (a *runtimeDecoderAdapter[T]) ByteSpec() ByteSpec         { return a.inner.ByteSpec() }
func (a *runtimeDecoderAdapter[T]) ValueKind() CodecValueKind  { return a.kind }
func (a *runtimeDecoderAdapter[T]) DecodeRegistersAny(regs []uint16) (any, error) {
	v, err := a.inner.DecodeRegisters(regs)
	if err != nil {
		return nil, err
	}
	return v, nil
}

type runtimeEncoderAdapter[T any] struct {
	inner Encoder[T]
	kind  CodecValueKind
}

func (a *runtimeEncoderAdapter[T]) ID() string                 { return a.inner.ID() }
func (a *runtimeEncoderAdapter[T]) Name() string               { return a.inner.Name() }
func (a *runtimeEncoderAdapter[T]) RegisterSpec() RegisterSpec { return a.inner.RegisterSpec() }
func (a *runtimeEncoderAdapter[T]) ByteSpec() ByteSpec         { return a.inner.ByteSpec() }
func (a *runtimeEncoderAdapter[T]) ValueKind() CodecValueKind  { return a.kind }
func (a *runtimeEncoderAdapter[T]) EncodeRegistersAny(value any) ([]uint16, error) {
	t, ok := value.(T)
	if !ok {
		return nil, &CodecValueError{
			Codec:  a.inner.ID(),
			Reason: fmt.Sprintf("wrong value type: got %T", value),
		}
	}
	return a.inner.EncodeRegisters(t)
}

type runtimeCodecAdapter[T any] struct {
	inner Codec[T]
	kind  CodecValueKind
}

func (a *runtimeCodecAdapter[T]) ID() string                 { return a.inner.ID() }
func (a *runtimeCodecAdapter[T]) Name() string               { return a.inner.Name() }
func (a *runtimeCodecAdapter[T]) RegisterSpec() RegisterSpec { return a.inner.RegisterSpec() }
func (a *runtimeCodecAdapter[T]) ByteSpec() ByteSpec         { return a.inner.ByteSpec() }
func (a *runtimeCodecAdapter[T]) ValueKind() CodecValueKind  { return a.kind }
func (a *runtimeCodecAdapter[T]) DecodeRegistersAny(regs []uint16) (any, error) {
	v, err := a.inner.DecodeRegisters(regs)
	if err != nil {
		return nil, err
	}
	return v, nil
}
func (a *runtimeCodecAdapter[T]) EncodeRegistersAny(value any) ([]uint16, error) {
	t, ok := value.(T)
	if !ok {
		return nil, &CodecValueError{
			Codec:  a.inner.ID(),
			Reason: fmt.Sprintf("wrong value type: got %T", value),
		}
	}
	return a.inner.EncodeRegisters(t)
}

//
// Package-level adapter constructors
//

// AsRuntimeDecoder wraps a typed Decoder[T] as a RuntimeDecoder. The kind is stored for discovery/CLI.
func AsRuntimeDecoder[T any](d Decoder[T], kind CodecValueKind) RuntimeDecoder {
	return &runtimeDecoderAdapter[T]{inner: d, kind: kind}
}

// AsRuntimeEncoder wraps a typed Encoder[T] as a RuntimeEncoder. Wrong type passed to
// EncodeRegistersAny returns *CodecValueError and never panics.
func AsRuntimeEncoder[T any](e Encoder[T], kind CodecValueKind) RuntimeEncoder {
	return &runtimeEncoderAdapter[T]{inner: e, kind: kind}
}

// AsRuntimeCodec wraps a typed Codec[T] as a RuntimeCodec. Wrong type passed to
// EncodeRegistersAny returns *CodecValueError and never panics.
func AsRuntimeCodec[T any](c Codec[T], kind CodecValueKind) RuntimeCodec {
	return &runtimeCodecAdapter[T]{inner: c, kind: kind}
}

//
// Offline runtime decode/encode (no client; for tests and batch plans)
//

// DecodeRegistersAny decodes raw registers using the given runtime decoder.
// It validates register count against the codec's RegisterSpec before calling the codec.
func DecodeRegistersAny(regs []uint16, codec RuntimeDecoder) (any, error) {
	if codec == nil {
		return nil, &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	spec := codec.RegisterSpec()
	if err := ValidateRegisterSpec(spec, regs, codec.ID()); err != nil {
		return nil, err
	}
	return codec.DecodeRegistersAny(regs)
}

// EncodeRegistersAny encodes a value to raw registers using the given runtime encoder.
// Wrong dynamic type returns *CodecValueError (never panics).
func EncodeRegistersAny(value any, codec RuntimeEncoder) ([]uint16, error) {
	if codec == nil {
		return nil, &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	regs, err := codec.EncodeRegistersAny(value)
	if err != nil {
		return nil, err
	}
	if err := ValidateRegisterSpec(codec.RegisterSpec(), regs, codec.ID()); err != nil {
		return nil, err
	}
	return regs, nil
}
