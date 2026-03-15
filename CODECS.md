# Codecs — Typed register read/write

This document lists and explains all **codecs** implemented in `github.com/otfabric/modbus`. Codecs provide typed encode/decode between Modbus registers (`[]uint16`) and Go values. Use them with **ReadWithCodec** / **WriteWithCodec** (or **ReadWithRuntimeCodec** / **WriteWithRuntimeCodec** when the type is not known at compile time).

- **Transport** remains register-native: you read/write raw registers or raw bytes; the codec interprets them.
- **Layout** (byte and word order) is part of the codec. Numeric codecs take a `RegisterLayout`; text and byte codecs have a fixed or parameterized width.
- **Discovery** is supported via `CodecDescriptor`, `CodecCandidate`, and registry functions; each codec has a stable **ID** for CLI and tooling.

**Codec design discipline.** The bar for adding a new codec is high: it must have a **stable binary/register-level representation**, be **reversible**, and **not** embed scaling or business meaning. Vendor-specific or ambiguous formats are better handled by higher-level logic than as transport codecs. The suite is intentionally broad but strict; avoid “custom parser disguised as codec.”

**Discovery philosophy.** Constructors define what is **valid** (e.g. even byte count, register count ≥ 1). The **registry** defines what is **discoverable** by default (e.g. which text widths, which byte counts). Discovery widths are **curated** for common device schemas and CLI usability—not an exhaustive list. Use constructors directly when you need a width not in the discovery set.

For the full API (interfaces, discovery, runtime codecs, batch decode), see [API.md § 11](API.md#11-codec-api).

---

## Table of contents

1. [Register layouts](#1-register-layouts)
2. [Numeric codecs](#2-numeric-codecs)
3. [Text codecs](#3-text-codecs)
4. [Bytes and network codecs](#4-bytes-and-network-codecs)
5. [Discovery and stable IDs](#5-discovery-and-stable-ids)
6. [Usage](#6-usage)

---

## 1. Register layouts

A **RegisterLayout** describes how the bytes of a multi-register value are ordered. Positions are 1-based: 1 = least-significant byte, higher numbers = more significant. Layouts are immutable; use **NewRegisterLayout** or the common variables below.

| Variable | Registers | Byte positions | Typical use |
|----------|-----------|----------------|-------------|
| `Layout16_21` | 1 | 2, 1 | Big-endian 16-bit (high byte first) |
| `Layout16_12` | 1 | 1, 2 | Little-endian 16-bit |
| `Layout32_4321` | 2 | 4,3,2,1 | Big-endian 32-bit (ABCD) |
| `Layout32_3412` | 2 | 3,4,1,2 | Byte-swap within words |
| `Layout32_2143` | 2 | 2,1,4,3 | Word-swap 32-bit (CDAB) |
| `Layout32_1234` | 2 | 1,2,3,4 | Little-endian 32-bit |
| `Layout48_654321` | 3 | 6,5,4,3,2,1 | Big-endian 48-bit |
| `Layout48_563412` | 3 | 5,6,3,4,1,2 | Byte/word permutation 48-bit |
| `Layout48_214365` | 3 | 2,1,4,3,6,5 | Word order 48-bit |
| `Layout48_123456` | 3 | 1,2,3,4,5,6 | Little-endian 48-bit |
| `Layout64_87654321` | 4 | 8..1 | Big-endian 64-bit |
| `Layout64_78563412` | 4 | 7,8,5,6,3,4,1,2 | Byte/word permutation 64-bit |
| `Layout64_21436587` | 4 | 2,1,4,3,6,5,8,7 | Word order 64-bit |
| `Layout64_12345678` | 4 | 1..8 | Little-endian 64-bit |

For 32-, 48-, and 64-bit values the library provides **four canonical layout variants** per width (big-endian, byte-swap, word-swap, little-endian) for real-world Modbus device compatibility. Numeric codecs (integer and float) **require** a layout that matches their width: 1 register for 16-bit, 2 for 32-bit, 3 for 48-bit, 4 for 64-bit. Invalid layout returns an error from the constructor.

---

## 2. Numeric codecs

All numeric codecs take a **RegisterLayout** and return `(Codec[T], error)`; **MustNew** variants panic on error.

### 16-bit (1 register)

| Constructor | Go type | Layout |
|-------------|---------|--------|
| `NewUint16Codec(layout)` | `uint16` | 1 register (e.g. `Layout16_21`, `Layout16_12`) |
| `MustNewUint16Codec(layout)` | `uint16` | same |
| `NewInt16Codec(layout)` | `int16` | same |
| `NewInt16SignMagnitudeCodec()` | `int16` | **Special-purpose legacy encoding:** bit 15 = sign, bits 0–14 = magnitude. **Not two's complement.** Independent of `RegisterLayout` (single 16-bit semantic word). ID: `int16_sign_magnitude`. Use only when the device uses sign-magnitude. |

### 32-bit (2 registers)

| Constructor | Go type | Layout |
|-------------|---------|--------|
| `NewUint32Codec(layout)` | `uint32` | 2 registers (e.g. `Layout32_4321`, `Layout32_3412`, `Layout32_2143`, `Layout32_1234`) |
| `MustNewUint32Codec(layout)` | `uint32` | same |
| `NewInt32Codec(layout)` | `int32` | same |
| `NewFloat32Codec(layout)` | `float32` | same |

### 48-bit (3 registers)

Unsigned range 0–2⁴⁸−1; signed uses 47-bit magnitude, sign-extended to `int64`.

| Constructor | Go type | Layout |
|-------------|---------|--------|
| `NewUint48Codec(layout)` | `uint64` | 3 registers (e.g. `Layout48_654321`, `Layout48_563412`, `Layout48_214365`, `Layout48_123456`) |
| `MustNewUint48Codec(layout)` | `uint64` | same |
| `NewInt48Codec(layout)` | `int64` | same |

### 64-bit (4 registers)

| Constructor | Go type | Layout |
|-------------|---------|--------|
| `NewUint64Codec(layout)` | `uint64` | 4 registers (e.g. `Layout64_87654321`, `Layout64_78563412`, `Layout64_21436587`, `Layout64_12345678`) |
| `MustNewUint64Codec(layout)` | `uint64` | same |
| `NewInt64Codec(layout)` | `int64` | same |
| `NewFloat64Codec(layout)` | `float64` | same |

**Stable ID format:** `uint16/layout:21`, `uint32/layout:4321`, `int32/layout:2143`, `float64/layout:21436587`, etc. The layout suffix is the layout’s `String()` (e.g. `"4321"`).

### Decimal limb (M10k) codecs — family `decimal_limb`

**Important:** M10k is **not** a `RegisterLayout`, **not** BCD, and **not** packed decimal bytes. Each 16-bit register holds one **base-10000 limb value** (0–9999 unsigned; signed uses one MS limb in −9999..9999). The full number is reconstructed as a base-10000 integer. Used by some Schneider / power-monitoring devices. Order is controlled by **DecimalLimbOrder**, not byte/word layout.

| Constructor | Go type | Registers | Order | Range (unsigned / signed) |
|-------------|---------|-----------|-------|---------------------------|
| `NewUint32M10kCodec(order)` | `uint32` | 2 | `DecimalLimbLowToHigh` or `DecimalLimbHighToLow` | 0 .. 99_999_999 |
| `MustNewUint32M10kCodec(order)` | `uint32` | 2 | same | same |
| `NewInt32M10kCodec(order)` | `int32` | 2 | same | −99_990_000 .. 99_999_999 |
| `MustNewInt32M10kCodec(order)` | `int32` | 2 | same | same |
| `NewUint48M10kCodec(order)` | `uint64` | 3 | same | 0 .. 999_999_999_999 |
| `MustNewUint48M10kCodec(order)` | `uint64` | 3 | same | same |
| `NewInt48M10kCodec(order)` | `int64` | 3 | same | −999_900_000_000 .. 999_999_999_999 |
| `MustNewInt48M10kCodec(order)` | `int64` | 3 | same | same |
| `NewUint64M10kCodec(order)` | `uint64` | 4 | same | 0 .. 9_999_999_999_999_999 |
| `MustNewUint64M10kCodec(order)` | `uint64` | 4 | same | same |
| `NewInt64M10kCodec(order)` | `int64` | 4 | same | −9_999_000_000_000_000 .. 9_999_999_999_999_999 |
| `MustNewInt64M10kCodec(order)` | `int64` | 4 | same | same |

**Signed semantics:** Only the **most-significant limb** is signed (−9999 .. 9999); all other limbs are unsigned 0 .. 9999. The MS limb is transported as a 16-bit signed integer (`int16(reg)` on decode; `uint16(int16(ms))` on encode). Same register order as unsigned (low_to_high = first reg LSB; high_to_low = first reg MSB).

**Orders:**

- **DecimalLimbLowToHigh** — First register is least-significant limb. Decode: `value = r0 + r1*10000 + …`. Schneider equivalents: 2143, 21-65, 21-87.
- **DecimalLimbHighToLow** — First register is most-significant limb. Decode: `value = r1 + r0*10000` (2 regs). Schneider equivalents: 4321, 65-21, 87-21.

**Stable IDs:** `uint32_m10k/order:low_to_high`, `uint32_m10k/order:high_to_low`, `int32_m10k/order:low_to_high`, `int32_m10k/order:high_to_low`, and similarly for `uint48_m10k`, `int48_m10k`, `uint64_m10k`, `int64_m10k`.

---

## 3. Text codecs

Text codecs take a **register count** (number of 16-bit registers). ASCII/BCD codecs use two characters per register (high byte, low byte); UTF-16 codecs use one UTF-16 code unit per register. All return `(Codec[string], error)` and reject `registerCount == 0`.

| Constructor | Description | Decode | Encode |
|-------------|-------------|--------|--------|
| `NewAsciiCodec(registerCount)` | ASCII, high byte first per register | Trims trailing spaces | Right-pads with space |
| `NewAsciiFixedCodec(registerCount)` | Fixed-width ASCII | No trim | Right-pads with NUL |
| `NewAsciiReverseCodec(registerCount)` | ASCII, low byte first per register | Trims trailing spaces | Same byte order |
| `NewUTF16BECodec(registerCount)` | UTF-16 big-endian (one code unit per register) | Full width; see UTF-16 contract below | Right-pads with NUL code units |
| `NewUTF16LECodec(registerCount)` | UTF-16 little-endian (low byte first per register) | Full width; see UTF-16 contract below | Right-pads with NUL code units |
| `NewBCDCodec(registerCount)` | One byte per BCD digit (0–9) | Decimal digit string | Pads with leading zeros; rejects non-digits |
| `NewPackedBCDCodec(registerCount)` | Two BCD digits per byte | Decimal digit string | Pads with leading zeros; rejects non-digits |
| `NewSignedPackedBCDCodec(registerCount)` | Packed BCD with trailing sign nibble; see sign nibble rules below | Signed digit string (e.g. `-1234`) | Optional leading `-`; digits only |
| `NewPackedBCDReverseCodec(registerCount)` | **Same packed BCD semantics** as `NewPackedBCDCodec`; only **byte order within each 16-bit register** is low-byte first | Decimal digit string | Same byte order |

**UTF-16 decode/encode contract:** Decode preserves **full width**; it does not stop at the first NUL. Embedded NUL code units survive as runes. Trailing NULs are preserved (no silent trim unless the codec name says fixed/trimmed). Encoding truncates overlong input to the codec width, then right-pads with NUL code units. The implementation uses Go’s `unicode/utf16`: lone surrogate halves are passed through (no U+FFFD replacement); invalid UTF-16 on decode is not replaced. For strict validation of invalid UTF-16, validate the string before encode or after decode in application code.

**Signed packed BCD sign nibble rules:** The **trailing** (least-significant) nibble is the sign. **Decode:** last nibble **0xC, 0xD, or 0xF** → negative (magnitude from all other nibbles as digits); last nibble **0x0–0x9** → positive (that nibble is the last digit). **Encode:** negative → exactly **0xC** in the trailing nibble (canonical); positive → all nibbles are digits (no sign nibble). Positive values with a sign nibble on wire are accepted on decode (0x0–0x9 in last nibble); encode never emits 0xD or 0xF. Non-digit nibbles (0xA, 0xB, 0xE) in the last position are invalid on decode.

**Stable ID format:** `ascii/registers:4`, `utf16be/registers:4`, `utf16le/registers:2`, `bcd/registers:2`, `packed_bcd/registers:4`, `signed_packed_bcd/registers:2`, `packed_bcd_reverse/registers:4`. Discovery registers these for register counts 1, 2, 3, 4, 6, 8, 12, 16, 20, 32, 48, 64.

---

## 4. Bytes and network codecs

### Raw bytes

| Constructor | Go type | Parameter | Constraint |
|-------------|---------|-----------|------------|
| `NewBytesCodec(byteCount)` | `[]byte` | Byte count | Must be **even** (register-backed); 0 or odd returns error |
| `NewUint8SliceCodec(byteCount)` | `[]uint8` | Byte count | Same |

Use for opaque binary fields. Bytes are in **wire order** (no reordering).

**Stable ID format:** `bytes/bytes:6`, `uint8_slice/bytes:4`. Discovery registers byte counts 2, 4, 6, 8, 10, 12, 14, 16, 20, 24, 32, 48, 64.

### Network and hardware addresses

| Constructor | Go type | Size | Description |
|-------------|---------|------|-------------|
| `NewIPAddrCodec()` | `net.IP` | 4 bytes (2 registers) | IPv4 in wire order |
| `NewIPv6AddrCodec()` | `net.IP` | 16 bytes (8 registers) | IPv6 in wire order (rejects IPv4) |
| `NewEUI48Codec()` | `net.HardwareAddr` | 6 bytes (3 registers) | MAC/EUI-48 in wire order |
| `NewEUI64Codec()` | `net.HardwareAddr` | 8 bytes (4 registers) | EUI-64 in wire order |

**Stable IDs:** `ip_addr`, `ipv6_addr`, `eui48`, `eui64`.

---

## 5. Discovery and stable IDs

- **CodecDescriptor** — Metadata (ID, name, family, value kind, register/byte spec, optional layouts). Use **AvailableCodecDescriptors()**, **CodecDescriptorsForRegisterCount**, **CodecDescriptorsForByteCount**, **CodecDescriptorByID**, **FindCodecDescriptors**.
- **CodecCandidate** — Flattened descriptor + layout name for “this codec with this layout”. Use **CodecCandidatesForRegisterCount**, **CodecCandidatesForByteCount**.
- **Stable ID** — Used by **RuntimeCodecByID**, **MustRuntimeCodecByID**, and CLI. Examples: `uint32/layout:4321`, `float64/layout:21436587`, `uint32_m10k/order:low_to_high`, `int32_m10k/order:high_to_low`, `int16_sign_magnitude`, `ascii/registers:4`, `bytes/bytes:6`, `ip_addr`, `eui48`, `eui64`.

Families: `integer`, `float`, `text`, `bcd`, `bytes`, `network`, `hardware_address`, `decimal_limb`. Value kinds: `uint16`, `int16`, `uint32`, `int32`, … `string`, `byte_slice`, `ip`, `hardware_addr`.

---

## 6. Usage

**Typed (compile-time type known):**

```go
codec, err := NewUint32Codec(Layout32_4321)
if err != nil { ... }
v, err := ReadWithCodec(client, ctx, unitID, addr, HoldingRegister, codec)
// or
err = WriteWithCodec(client, ctx, unitID, addr, value, codec)
```

**Convenience for uint32:** **ReadUint32WithLayout** / **WriteUint32WithLayout** take a layout and call the codec internally.

**Runtime (type unknown at compile time, e.g. CLI or descriptor-driven):**

```go
rc, ok, err := RuntimeCodecByID("uint32/layout:4321")
if err != nil || !ok { ... }
anyVal, err := ReadWithRuntimeCodec(client, ctx, unitID, addr, HoldingRegister, rc)
```

**Offline (tests, tooling):**

```go
decoded, err := DecodeRegisters(regs, codec)
encoded, err := EncodeRegisters(value, codec)
```

**Date/time.** The library does not currently provide date/time codecs (e.g. epoch seconds, CP56Time2a). Such codecs would require narrowly defined, reversible register semantics; they may be added later with explicit contracts. Until then, use numeric codecs or application-level parsing.

For full API details, see [API.md § 11 Codec API](API.md#11-codec-api).
