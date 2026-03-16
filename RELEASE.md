# Release v0.4.2

**Date:** 2026-03-17
**Previous release:** v0.4.1

## Summary

Patch release: **time codec family** for typed `time.Time` read/write. Three formats are supported — seconds since 2000 (s2000), calendar YMDhms (6 registers), and IEC 60870-5 CP56Time2a (4 registers) — with UTC, local, and default-UTC variants. All time codecs use **CodecFamilyTime** / **CodecValueTime**, strict calendar validation, and consistent **CodecValueError** on invalid values (including CP56 decode failures). Descriptor registration and runtime registry support discovery and CLI use.

## Changes

### Added

- **Time codecs** — `NewDateTime2S2000Codec()` (2 regs, uint32 seconds since 2000-01-01 UTC), `NewDateTime3S2000Codec()` (3 regs, 48-bit seconds), `NewDateTimeYMDhmsUTCCodec()`, `NewDateTimeYMDhmsLocalCodec()`, `NewDateTimeYMDhmsCodec()` (6 regs: year, month, day, hour, minute, second), `NewDateTimeIEC870UTCCodec()`, `NewDateTimeIEC870LocalCodec()`, `NewDateTimeIEC870Codec()` (4 regs, CP56Time2a, 7-byte payload + pad). Stable IDs: `datetime2_s2000`, `datetime3_s2000`, `datetime_ymdhms_utc`, `datetime_ymdhms_local`, `datetime_ymdhms`, `datetime_iec870_utc`, `datetime_iec870_local`, `datetime_iec870`.
- **CodecFamilyTime / CodecValueTime** — New family and value kind for time codecs; used by descriptors and `FindRuntimeCodecs` / discovery.
- **Layout metadata for s2000** — `datetime2_s2000` and `datetime3_s2000` descriptors expose layout 4321 and 654321 for tooling; YMDhms and IEC870 are structural formats and do not expose layout metadata.
- **Strict calendar validation** — Helper `strictDateTime` rejects invalid dates (e.g. Feb 31, April 31, non-leap Feb 29); YMDhms and CP56 decode paths use it. Coarse range checks (month 1–12, day 1–31, etc.) return targeted errors; normalisation failures yield a single “invalid calendar date/time” error.
- **CP56 behaviour** — Decode ignores status/flag bits and uses only timestamp fields; encode writes a clean timestamp (flag bits unset). Eighth byte of the 4-register frame is padding and ignored on decode. Millisecond precision (milliseconds within minute 0–59999) is preserved. Year 2000–2127; decode maps year byte 0→2000, 127→2127.
- **Nil-location guards** — `strictDateTime`, `decodeCP56Time2a`, `encodeCP56Time2a`, and the YMDhms/IEC870 codec `EncodeRegisters` methods reject nil `*time.Location` with a clear error instead of panicking.
- **CP56 decode errors as CodecValueError** — `dateTimeIEC870Codec.DecodeRegisters` wraps CP56 decode failures in `*CodecValueError` so `errors.Is(err, ErrCodecValue)` works for value-validation tests and callers.

### Documentation

- **CODECS.md** — §5 Time codecs: s2000, YMDhms, CP56Time2a; UTC/default/local semantics; DOW written on encode, ignored on decode.
- **README.md** — Supported Go types table includes `time.Time` via time codecs; codec list mentions time codecs.
- **API.md** — Time codec constructors and stable IDs already documented; aligned with CODECS.md.

### Unchanged

- No breaking changes. Serial transport, TCP/TLS, codec API, and all other behaviour unchanged.

---

# Release v0.4.1

**Date:** 2026-03-17
**Previous release:** v0.4.0

## Summary

Patch release: **serial transport for Modbus RTU** now uses [github.com/otfabric/go-serial](https://github.com/otfabric/go-serial) v0.1.1 instead of goburrow/serial. The RTU serial wrapper is hardened and preserves Modbus RTU defaults unless the caller explicitly overrides.

## Changes

### Dependency

- **Serial library** — Replaced `github.com/goburrow/serial` with `github.com/otfabric/go-serial` v0.1.1. RTU serial open uses `serialmodbus.DefaultModbusRTUConfig(device)` (19200 8E1) as the base; client config (Speed, DataBits, StopBits, Parity) overrides only when explicitly set. Unset parity keeps even parity (Modbus default).

### Serial wrapper behaviour

- **Validation** — Invalid DataBits (not 5–8), StopBits (not 1–2), or Parity (not None/Even/Odd) return an error instead of silently falling back. Nil config guarded in Open().
- **Nil safety** — Open(), Close(), Read(), and Write() guard against nil config or nil port. Close() clears the port reference after Close returns so later Read/Write return ErrSerialPortNotOpen. Close is idempotent when port is already nil.
- **ErrSerialPortNotOpen** — New sentinel returned by Read/Write when the port is not open or has been closed. Use `errors.Is(err, ErrSerialPortNotOpen)` to detect.
- **Double-open** — Open() returns an error if the wrapper already has an open port; call Close() first.
- **Deadline** — Zero deadline means “no deadline yet” (no immediate timeout). Open() resets deadline on success so close/reopen does not carry over an old deadline.
- **Style** — SetDeadline and Read use plain returns; Read/Write share the same sentinel for “not open”.

### Unchanged

- No API or behaviour change for TCP, TLS, RTU-over-TCP, or RTU-over-UDP. Codec API, discovery, and all other client/server behaviour unchanged.

---

# Release v0.4.0

**Date:** 2026-03-17
**Previous release:** v0.3.0

## Summary

**Breaking release.** The codec-first API is now the only way to perform typed register read/write. Client-wide encoding state and all legacy typed helpers have been removed. Use **ReadRegisters** / **WriteRegisters** and **ReadRawBytes** / **WriteRawBytes** for raw transport; use **ReadWithCodec** / **WriteWithCodec** (and runtime codec APIs) for typed access with explicit layout.

The **decimal limb (M10k) codec family** is built around **DecimalLimbOrder** and **CodecFamilyDecimalLimb**: unsigned and signed M10k codecs use order-based constructors and stable IDs `order:low_to_high` / `order:high_to_low`. Documentation and semantics are tightened (signed packed BCD sign nibble rules, UTF-16 contract, discovery philosophy, codec discipline); README, API, and CODECS are aligned with the codebase.

## Changes

### Removed (breaking)

- **SetEncoding** — No longer exists. Byte and word order are defined by the codec’s `RegisterLayout` (e.g. `NewUint32Codec(Layout32_4321)`).
- **ReadBytes / WriteBytes** — Removed. Use **ReadRawBytes** / **WriteRawBytes** for raw byte transport; use codecs for typed interpretation.
- **Legacy typed read helpers** — Removed: `ReadUint16`, `ReadUint16s`, `ReadUint16Pair`, `ReadUint32`, `ReadUint32s`, `ReadInt16`, `ReadInt16s`, `ReadInt32`, `ReadInt32s`, `ReadFloat32`, `ReadFloat32s`, `ReadUint48`, `ReadUint48s`, `ReadInt48`, `ReadInt48s`, `ReadUint64`, `ReadUint64s`, `ReadInt64`, `ReadInt64s`, `ReadFloat64`, `ReadFloat64s`, `ReadAscii`, `ReadAsciiFixed`, `ReadAsciiReverse`, `ReadBCD`, `ReadPackedBCD`, `ReadUint8s`, `ReadIPAddr`, `ReadIPv6Addr`, `ReadEUI48`. Use **ReadRegisters** / **ReadRawBytes** plus **ReadWithCodec** or **ReadWithRuntimeCodec** with the appropriate codec.
- **Legacy typed write helpers** — Removed: `WriteUint32`, `WriteUint32s`, `WriteInt16`, `WriteInt16s`, `WriteInt32`, `WriteInt32s`, `WriteFloat32`, `WriteFloat32s`, `WriteUint48`, `WriteUint48s`, `WriteInt48`, `WriteInt48s`, `WriteUint64`, `WriteUint64s`, `WriteInt64`, `WriteInt64s`, `WriteFloat64`, `WriteFloat64s`, `WriteAscii`, `WriteAsciiFixed`, `WriteAsciiReverse`, `WriteBCD`, `WritePackedBCD`, `WriteUint8s`, `WriteIPAddr`, `WriteIPv6Addr`, `WriteEUI48`. Use **WriteRegisters** / **WriteRawBytes** plus **WriteWithCodec** or **WriteWithRuntimeCodec** with the appropriate codec.

### Added

- **DecimalLimbOrder** — Type with `DecimalLimbLowToHigh` and `DecimalLimbHighToLow`; `String()` returns `"low_to_high"` / `"high_to_low"`.
- **CodecFamilyDecimalLimb** — Codec family `"decimal_limb"` for base-10000 limb codecs.
- **M10k unsigned codecs** — `NewUint32M10kCodec(order)`, `NewUint48M10kCodec(order)`, `NewUint64M10kCodec(order)` and `MustNew*` variants. Ranges: uint32 0..99_999_999; uint48 0..999_999_999_999; uint64 0..9_999_999_999_999_999.
- **M10k signed codecs** — `NewInt32M10kCodec(order)`, `NewInt48M10kCodec(order)`, `NewInt64M10kCodec(order)` and `MustNew*` variants. Only the most-significant limb is signed (−9999..9999); MS limb as `int16(reg)` on wire. Ranges: int32 −99_990_000..99_999_999; int48 −999_900_000_000..999_999_999_999; int64 −9_999_000_000_000_000..9_999_999_999_999_999.
- **M10k stable IDs** — `uint32_m10k/order:low_to_high`, `uint32_m10k/order:high_to_low`, and similarly for int32, uint48, int48, uint64, int64. Schneider mapping in CODECS.md.
- **Tests** — Signed packed BCD sign nibble rules; UTF-16 full-width with embedded NUL; M10k signed round-trip and runtime-by-ID.

### Changed (breaking for M10k callers)

- **M10k constructors** — Order-based only. Use `NewUint32M10kCodec(DecimalLimbLowToHigh)` or `DecimalLimbHighToLow` (and analogously for Uint48, Uint64, Int32, Int48, Int64). Old per-order names (e.g. `NewUint32M10k4321Codec`) are not provided.
- **M10k stable IDs** — Registry uses `order:low_to_high` and `order:high_to_low`; not `order:4321`, `order:2143`, etc. Update CLI or config that relied on the old IDs.

### Documentation and semantics

- **CODECS.md** — Codec design discipline; discovery philosophy; M10k “not layout, not BCD”; sign-magnitude and signed packed BCD sign nibble rules; UTF-16 contract; packed BCD reverse as byte-order variant; date/time out of scope.
- **API.md** — Discovery subset includes text/UTF-16 and bytes widths 48, 64; ReadRawBytes odd-quantity behaviour; pointers to CODECS for sign-magnitude and UTF-16/signed BCD.
- **README.md** — Supported Go types: decimal limb/M10k row; BCD variants note; discovery example fix.
- **codec_registry.go** — Comment updated for discovery subset.

### Unchanged

- Raw transport: **ReadRegister**, **ReadRegisters**, **ReadRawBytes**, **WriteRegister**, **WriteRegisters**, **WriteRawBytes**.
- Codec API: **ReadWithCodec**, **WriteWithCodec**, all codec constructors, **RegisterLayout**, discovery, runtime codecs, batch decode plans.
- Coils, discrete inputs, bitfield ops (**ReadRegisterBit**, **ReadRegisterBits**, **WriteRegisterBit**, **UpdateRegisterMask**), file records, FC23/FC24, device identification, SunSpec discovery, diagnostics, report server ID.
- **Endianness** and **WordOrder** types remain for layout naming (e.g. CLI); they are not used by the client for encoding.

---

# Release v0.3.0

**Date:** 2026-03-15
**Previous release:** v0.2.5

## Summary

Introduce a **codec-first API** for typed register read/write with explicit layout and discovery. Codecs own interpretation; transport remains register-native. **Runtime codec** APIs and **batch decode plans** support CLI, descriptor-driven, and query-based workflows. Legacy typed helpers and `SetEncoding` were deprecated and have been removed in v0.4.0.

## Changes

### Added

- **Codec interfaces** — `Decoder[T]`, `Encoder[T]`, and `Codec[T]` with `ID()`, `Name()`, `RegisterSpec()`, `ByteSpec()`, `DecodeRegisters`, `EncodeRegisters`. All codec instances are fixed-width (parameterized at construction).
- **RegisterLayout** — Immutable layout describing byte order across registers (1-based positions: 1 = LSB). `NewRegisterLayout`, `MustNewRegisterLayout`, getters `RegisterCount()`, `BytePositions()`, `String()`. Common vars: `Layout16_21`, `Layout16_12`, `Layout32_4321`, `Layout32_2143`, `Layout48_654321`, `Layout48_214365`, `Layout64_87654321`, `Layout64_21436587`.
- **Transport** — Package-level generic `ReadWithCodec[T](mc, ctx, unitID, addr, regType, codec)` and `WriteWithCodec[T](mc, ctx, unitID, addr, value, codec)`. Wire order (big-endian per register); codec owns layout. Convenience: `ReadUint32WithLayout`, `WriteUint32WithLayout`.
- **Numeric codecs** — `New*Codec(layout)` and `MustNew*Codec(layout)` for uint16, int16, uint32, int32, uint48, int48, uint64, int64, float32, float64. Constructors validate layout and return `(Codec[T], error)` or panic for `Must`.
- **Text codecs** — `NewAsciiCodec`, `NewAsciiFixedCodec`, `NewAsciiReverseCodec`, `NewBCDCodec`, `NewPackedBCDCodec` (register count = width). Full ASCII validation on encode; overlong input truncated; BCD truncation keeps rightmost digits.
- **Bytes and network codecs** — `NewBytesCodec(byteCount)`, `NewUint8SliceCodec(byteCount)` (even byte count required); `NewIPAddrCodec()`, `NewIPv6AddrCodec()` (IPv6 rejects IPv4), `NewEUI48Codec()`.
- **Offline helpers** — `DecodeRegisters`, `EncodeRegisters`, `ValidateRegisterSpec(spec, regs, codecID)`, `ValidateByteSpec(spec, b, codecID)` for tests and tooling.
- **Discovery (registry)** — `CodecDescriptor`, `CodecCandidate`, `CodecQuery`. `AvailableCodecDescriptors()`, `CodecDescriptorsForRegisterCount`, `CodecDescriptorsForByteCount`, `CodecDescriptorByID`, `CodecCandidatesForRegisterCount`, `CodecCandidatesForByteCount`, `FindCodecDescriptors`. Returned descriptors are deep-copied. Discovery exposes a curated subset of common widths; constructors accept any valid width.
- **Codec errors** — Sentinels: `ErrCodecRegisterCount`, `ErrCodecLayout`, `ErrCodecValue`, `ErrEncodingError`. Typed: `*CodecRegisterCountError`, `*CodecLayoutError`, `*CodecByteCountError`, `*CodecValueError` (all unwrap to the appropriate sentinel). `ReadWithCodec` returns `*CodecRegisterCountError` when `spec.Count == 0`.
- **Runtime codec API** — Type-erased `RuntimeDecoder`, `RuntimeEncoder`, `RuntimeCodec` for CLI and descriptor-driven use. Adapters: `AsRuntimeDecoder`, `AsRuntimeEncoder`, `AsRuntimeCodec`. Package helpers: `DecodeRegistersAny`, `EncodeRegistersAny`. Transport: `ReadWithRuntimeCodec`, `WriteWithRuntimeCodec`. Offline: `DecodeWithDescriptor`, `EncodeWithDescriptor`.
- **Runtime registry** — `RuntimeCodecFromDescriptor`, `RuntimeCodecByID`, `MustRuntimeCodecByID`; discovery: `RuntimeCodecsForRegisterCount`, `RuntimeCodecsForByteCount`, `FindRuntimeCodecs` returning `[]RuntimeCodec`. Every built-in descriptor is instantiable as a runtime codec.
- **Batch decode plan** — Single-window read with multiple decode items: `ReadWindow`, `RuntimeDecodeItem`, `RuntimeDecodePlan`, `RuntimeDecodedValue`, `RuntimeDecodeResult`. `ValidateRuntimeDecodePlan`, `ExecuteRuntimeDecodePlan` (online), `ExecuteRuntimeDecodePlanOffline`. Per-item decode failures are recorded without aborting the plan.

### Deprecated (will be removed in a future major version)

- **ReadBytes / WriteBytes** — Use `ReadRawBytes` / `WriteRawBytes` for raw byte transport and codecs for typed interpretation.
- **Legacy typed read/write helpers** — All of `ReadUint16(s)`, `ReadUint16Pair`, `ReadUint32(s)`, `ReadInt16(s)`, `ReadInt32(s)`, `ReadFloat32(s)`, `ReadUint48(s)`, `ReadInt48(s)`, `ReadUint64(s)`, `ReadInt64(s)`, `ReadFloat64(s)`, `ReadAscii`, `ReadAsciiFixed`, `ReadAsciiReverse`, `ReadBCD`, `ReadPackedBCD`, `ReadUint8s`, `ReadIPAddr`, `ReadIPv6Addr`, `ReadEUI48`, and the matching write helpers. **Migration:** Use `ReadRegisters`/`WriteRegisters` or `ReadRawBytes`/`WriteRawBytes` for raw access; use `ReadWithCodec`/`WriteWithCodec` for typed access; use runtime codec APIs for descriptor/CLI/query workflows.
- **SetEncoding** — Explicit layouts belong to codecs. Use codecs with the desired `RegisterLayout` or runtime codecs; do not depend on client-wide encoding state.

### Unchanged

- SunSpec discovery, bitfield ops (`ReadRegisterBit`, `WriteRegisterBit`, `UpdateRegisterMask`), and all existing client/server behaviour unchanged. Raw transport (`ReadRegisters`, `WriteRegisters`, `ReadRawBytes`, `WriteRawBytes`) unchanged.

---

# Release v0.2.5

**Date:** 2026-03-14
**Previous release:** v0.2.4

## Summary

Add bitfield and masked-register operations for devices that expose booleans and enums inside holding or input registers (status bits, alarm words, control words, mode enums). Read single or multiple bits from a register; write one bit or update a masked field without clobbering adjacent bits.

## Changes

### Added

- **ReadRegisterBit(ctx, unitID, addr, bitIndex, regType)** — Reads one register (FC03/FC04) and returns the bit at `bitIndex` (0 = LSB, 15 = MSB). Supports both holding and input registers.
- **ReadRegisterBits(ctx, unitID, addr, bitIndex, count, regType)** — Reads one register and returns `count` bits (1–16) starting at `bitIndex`. Use for multi-bit mode enums.
- **WriteRegisterBit(ctx, unitID, addr, bitIndex, value)** — Read-modify-write: reads holding register, sets or clears one bit, writes back (FC03 + FC16). Other bits unchanged.
- **UpdateRegisterMask(ctx, unitID, addr, mask, value)** — Read-modify-write: `newVal = (old & ^mask) | (value & mask)`. Only bits set in `mask` are updated; use for control words without affecting adjacent bits.

Invalid `bitIndex` (> 15) or invalid `ReadRegisterBits` range returns `ErrUnexpectedParameters`.

### Unchanged

- Coils and discrete inputs unchanged. New methods are additive.

---

# Release v0.2.4

**Date:** 2026-03-14
**Previous release:** v0.2.3

## Summary

Add typed write helpers that mirror the existing read helpers: signed integers (Int16/32/48/64), ASCII (normal, fixed-width, reverse), BCD and packed BCD, and raw/address types (Uint8s, IPAddr, IPv6Addr, EUI48). All use FC16 (Write Multiple Registers) with the same encoding conventions as the corresponding read methods.

## Changes

### Added

- **Signed integer writes** — `WriteInt16`, `WriteInt16s`, `WriteInt32`, `WriteInt32s`, `WriteInt48`, `WriteInt48s`, `WriteInt64`, `WriteInt64s`. Encoding follows `SetEncoding`; empty slice returns `ErrUnexpectedParameters`.
- **ASCII writes** — `WriteAscii` (trim trailing spaces, same layout as ReadAscii), `WriteAsciiFixed` (no trim), `WriteAsciiReverse` (same layout as ReadAsciiReverse).
- **BCD writes** — `WriteBCD` (one byte per digit), `WritePackedBCD` (two digits per byte; odd byte count padded for register alignment). Non-digit characters return an error.
- **Raw and address writes** — `WriteUint8s` (raw bytes, no reordering), `WriteIPAddr` (4 bytes from `net.IP.To4()`), `WriteIPv6Addr` (16 bytes), `WriteEUI48` (6 bytes from `net.HardwareAddr`). Invalid input returns `ErrUnexpectedParameters`.
- **Encoding helpers** (internal) — `uint48ToBytes`, `asciiToBytes`, `asciiToBytesReverse`, `bcdToBytes`, `packedBCDToBytes` for use by the write methods.

### Unchanged

- Existing write and read behaviour unchanged. New methods are additive.

---

# Release v0.2.3

**Date:** 2026-03-12
**Previous release:** v0.2.2

## Summary

Align the library with common Modbus/TCP and Wireshark dissector behaviour: spec-compliant MBAP length validation, standard port constants, additional function-code coverage, optional transaction-ID diagnostics, and clearer protocol error reporting.

## Changes

### Added

- **Standard port constants** — `PortModbusTCP` (502) and `PortModbusTLS` (802) for use in URLs or documentation. Modbus RTU over TCP has no standard port.
- **MBAP length validation** — TCP transport rejects MBAP length &lt; 2 or &gt; 254 and returns an error wrapping `ErrInvalidMBAPLength` (received length included in the message). Validation applied on both receive and send.
- **Function codes** — `FCReadExceptionStatus` (0x07), `FCGetCommEventCounters` (0x0B), `FCGetCommEventLog` (0x0C) added to known FCs and `KnownFunctionCodes()`. FC07 supported in RTU response length handling.
- **LastTransactionID()** — Client method returns the MBAP transaction ID of the last successful TCP response (0 for RTU/non-TCP). Useful for diagnostics and correlating with packet captures.
- **RTU PDU length rules** — Comment block in `expectedResponseLenth` documents response length rules per FC for spec/dissector alignment.

### Changed

- **TCP receive** — Frames with invalid MBAP length now return `ErrInvalidMBAPLength` (with value) instead of generic `ErrProtocolError`; log message includes expected range 2–254.
- **TCP send** — Requests that would produce MBAP length &gt; 254 are rejected before send with `ErrInvalidMBAPLength`.

### Unchanged

- All existing client/server behaviour and API contracts unchanged. New constants and `LastTransactionID()` are additive.

---

# Release v0.2.2

**Date:** 2026-03-12
**Previous release:** v0.2.1

## Summary

Export SunSpec protocol constants so downstream consumers (e.g. strategies parsing raw `ScanResult.Data`) can reference the canonical marker, end-model sentinel, and default base address values directly instead of maintaining mirrored copies.

## Changes

### Changed

- **SunSpec constants** — The following previously-unexported values are now exported:
  - `SunSpecMarkerReg0` (`0x5375`) / `SunSpecMarkerReg1` (`0x6E53`) — "SunS" marker registers.
  - `SunSpecEndModelID` (`0xFFFF`) / `SunSpecEndModelLength` (`0`) — end-of-chain sentinel.
  - `SunSpecDefaultBaseAddresses` (`[]uint16{0, 40000, 50000, 1, 39999, 40001, 49999, 50001}`) — default probe addresses.

### Unchanged

- All SunSpec discovery methods, types, and behaviour unchanged. This is a purely additive API change.

---

# Release v0.2.1

**Date:** 2026-03-12
**Previous release:** v0.2.0

## Summary

Relax SunSpec discovery **UnitID** handling so the full range **0–255** is accepted. SunSpec-enabled devices behind a Modbus gateway may use unit IDs outside the classic 1–247 range; validation no longer rejects them.

## Changes

### Changed

- **SunSpec options** — Removed UnitID range check (was 1–247). `SunSpecOptions.UnitID` now accepts 0–255. Zero still defaults to 1 for scanner ergonomics. Docs and API comments updated; invalid-options text no longer mentions UnitID.

### Unchanged

- All other SunSpec and FC03/FC04 helper behaviour unchanged.

---

# Release v0.2.0

**Date:** 2026-03-12
**Previous release:** v0.1.0

## Summary

Add minimal, transport-level **SunSpec discovery** APIs so callers can detect SunSpec devices, discover the SunSpec map base address, and enumerate **SunSpec model headers** (not full model metadata: no point decoding, names, or schema) without external SunSpec JSON or schema. These APIs are **read-only discovery helpers** and do not modify device state. Intended for device fingerprinting, protocol detection, and as a foundation for higher-level SunSpec libraries.

Default probe addresses are the official protocol candidates **0, 40000, 50000**, plus adjacent compatibility offsets (**1, 39999, 40001, 49999, 50001**) to tolerate 0-based vs 1-based addressing confusion found in vendor documentation and tooling. Reaching **MaxModels** stops enumeration and returns the models collected so far **without error** (normal truncation).

## Changes

### Added

- **SunSpec discovery (client)**  
  - `DetectSunSpec(ctx, opts)` — Probes candidate base addresses for the "SunS" marker; returns a structured result. "Not SunSpec" is not an error (`Detected: false`, `error == nil`). Uses the same request path as other client methods (lock per read, retries, metrics).
  - `ReadSunSpecModelHeaders(ctx, opts, baseAddress)` — Walks the model chain from `baseAddress+2`, returning model ID, length, and address ranges. Stops at end model (0xFFFF/0) or when guards trigger. Reaching MaxModels returns collected models without error. Malformed or non-progressing chains return partial results plus `ErrSunSpecModelChainInvalid`; exceeding `MaxAddressSpan` returns `ErrSunSpecModelChainLimitExceeded`. Invalid options (unsupported RegType, empty BaseAddresses) return `ErrUnexpectedParameters`.
  - `DiscoverSunSpec(ctx, opts)` — Convenience: runs detection then model-header enumeration; returns combined result. Includes partial model results when the chain read fails partway.
- **Types:** `SunSpecOptions`, `SunSpecProbeAttempt`, `SunSpecDetectionResult`, `SunSpecModelHeader`, `SunSpecDiscoveryResult`.
- **Sentinels:** `ErrSunSpecModelChainInvalid`, `ErrSunSpecModelChainLimitExceeded`.
- UnitID zero defaults to 1 for scanner ergonomics (documented tradeoff).
- **FC03/FC04 convenience read helpers** — Generic read helpers usable for SunSpec and other fixed-field protocols (no SunSpec-specific logic):
  - `ReadUint16Pair` — Exactly two registers as `[2]uint16`.
  - `ReadAsciiFixed` — Same ASCII layout as `ReadAscii` but trailing spaces preserved.
  - `ReadUint8s` — Raw bytes in wire order (no `SetEncoding`).
  - `ReadIPAddr` — 4 bytes as IPv4 `net.IP`.
  - `ReadIPv6Addr` — 16 bytes as IPv6 `net.IP`.
  - `ReadEUI48` — 6 bytes as MAC/EUI-48 `net.HardwareAddr`.
  Address and byte helpers use raw wire order and are unaffected by `SetEncoding`.

### Unchanged

- No point decoding, scale factors, or schema-driven parsing; no JSON model definitions. SunSpec semantics remain the responsibility of a separate SunSpec library.

---

# Release v0.1.0

**Date:** 2026-03-12
**Previous release:** v0.0.0

## Summary

Initial release.

---