# Interoperability: otfabric/go-modbus

What the library implements of the Modbus Application Protocol
Specification V1.1b3 ("the specification" below), how its server answers
requests that are not valid, and which other implementations it has been run
against. Use it to compare against the device or the master on the other
side.

Legend: **yes** implemented and tested · **opt** implemented when the
application provides the optional handler · **no** not implemented.

## Transports

| Item | Client | Server |
|------|--------|--------|
| Modbus TCP (MBAP), IPv4 and IPv6 | yes | yes |
| Modbus over TLS (MBAPS), role from the client certificate | yes | yes |
| Modbus RTU, Modbus ASCII (serial line) | yes | no |
| RTU over TCP, RTU over UDP, ASCII over TCP, Modbus UDP | yes | no |

The reference implementations below speak Modbus TCP, so that is the
transport the interop suite covers. The others are covered by this
repository's own tests.

## Function codes

| FC | Name | Client | Server |
|----|------|--------|--------|
| 01 | Read Coils | yes, 1..2000 | yes |
| 02 | Read Discrete Inputs | yes, 1..2000 | yes |
| 03 | Read Holding Registers | yes, 1..125 | yes |
| 04 | Read Input Registers | yes, 1..125 | yes |
| 05 | Write Single Coil | yes | yes |
| 06 | Write Single Register | yes | yes |
| 07 | Read Exception Status | yes | opt |
| 08 | Diagnostics | yes | no |
| 11 | Get Comm Event Counter | yes | opt |
| 12 | Get Comm Event Log | yes | opt |
| 15 | Write Multiple Coils | yes, 1..1968 | yes |
| 16 | Write Multiple Registers | yes, 1..123 | yes |
| 17 | Report Server ID | yes | no |
| 20, 21 | Read / Write File Record | yes | no |
| 22 | Mask Write Register | yes | opt |
| 23 | Read/Write Multiple Registers | yes, read 1..125, write 1..121 | opt |
| 24 | Read FIFO Queue | yes | no |
| 43/14 | Read Device Identification | yes, all categories, follows "more follows" | opt, stream and individual access, pagination |

A function code the server does not implement, an optional one whose handler
the application does not provide, and an MEI type other than 14 are answered
with exception 1 (illegal function).

## How the client treats a response

- The transaction identifier, the protocol identifier, the unit identifier
  and the function code of a response are checked. A response that fails a
  check is never handed to the caller as data.
- The byte count of a read response must match its length and the quantity
  requested; the echo of a write response (address and value, or address and
  quantity; address and both masks for FC22) must match the request. A
  mismatch is a `*ProtocolError`.
- An exception response is an `*ExceptionError` with the function code and
  the exception code, and leaves the connection in use. Every other failure
  on Modbus TCP closes the connection; the next request opens a new one.
- A quantity the protocol does not allow is refused before anything is sent
  (`*ParameterError`). The library has no call that sends an arbitrary PDU.

## How the server treats a request

The checks are made in this order, and the first that fails decides the
answer:

| Request | Answer |
|---------|--------|
| MBAP header invalid (protocol identifier, length) | connection closed |
| Function code not implemented, or its handler not provided | exception 1 |
| PDU too short to hold the fields of its function, or FC01..06 and FC22 with a length other than the one the function has | connection closed |
| Quantity 0 or above the maximum of the function (FC01, 02, 03, 04, 15, 16, 23) | exception 3 |
| FC15, FC16, FC23: byte count that is not the one the quantity asks for | exception 3 |
| FC05: value other than `0x0000` and `0xFF00` | exception 3 |
| FC43/14: read device ID code outside 1..4 | exception 3 |
| Address range that runs beyond 65535 | exception 2 |
| FC15, FC16, FC23: byte count right, but the data has another length | connection closed |
| Anything else | the `RequestHandler` decides: values, or an error that maps to an exception |

Two consequences for an application that models a device:

- The address space, the unit identifiers it answers to and the exceptions 2,
  4, 6, 10 and 11 are the handler's. The server does not filter unit
  identifiers by itself.
- The handler sees a request only after the checks above. A request for a
  unit the handler would reject with exception 11 is therefore answered with
  exception 1 or 3 when it also has an unknown function code or an invalid
  quantity. The specification leaves this open; the five reference servers
  below answer 11 in that case.

FC43/14: stream access from an object identifier the device does not have
starts at object 0; individual access to such an object is exception 2. The
conformity level, when the handler does not give one, is that of the highest
category the device has objects for, with the bit for individual access
(`0x81`, `0x82`, `0x83`).

## Verification status

Besides its own tests the library is run against five independent
implementations, as client and as server, on every push. The reference
builds are the images of
[otfabric/modbus-interop](https://github.com/otfabric/modbus-interop) v0.1.0:

| Reference | Version | go-modbus client → reference server | reference client → go-modbus server |
|-----------|---------|-------------------------------------|-------------------------------------|
| libmodbus (C) | v3.2.0 | pass ¹ | pass ¹ |
| PyModbus (Python) | 3.15.0 | pass | pass |
| digitalpetri/modbus (Java) | 2.1.6 | pass ¹ | pass ² |
| NModbus (C#) | 3.0.83 | pass ¹ ³ | pass ³ |
| tokio-modbus (Rust) | 0.17.0 | pass | pass |

¹ without Read Device Identification: the stack has no FC43 in that role.
² FC43 through the client's raw operation only: the stack has no call for it.
³ without Mask Write Register: NModbus has no FC22.

The suite reads what each reference declares (`print-capabilities`) and
skips what it lacks.

The suite (`make interop`, package `interop`) covers FC01 to FC06, FC15,
FC16, FC22, FC23 and FC43/14 on Modbus TCP:

- **go-modbus client → reference servers.** Every table of the reference
  device read in full, at the boundaries of its block, across byte
  boundaries and with the largest request of each function, the values
  compared with the fixture of the image and with the rules the fixture is
  generated by. Every write function with the largest request, read back on
  a second connection together with everything that must not have changed,
  and compared with what the server reports it received. FC23 with
  overlapping ranges (write before read) and with an invalid read range
  (nothing written). Device identification in every category, from a later
  object, and by individual access. Exceptions 1, 2 and 11. Two thousand
  requests on one connection; sixteen connections at once and a pooled
  client shared by 32 goroutines.
- **reference clients → go-modbus server.** 92 operations per client (fewer
  where a client lacks an operation) against a go-modbus device that serves the same fixture by the same
  rules: every operation the client has, `--repeat` up to 1000 requests on
  one connection, raw PDUs, exceptions 1, 2, 3 and 11. Each operation must
  have the outcome the fixture rules and the specification prescribe, the
  values must be those of the fixture, a write must leave the device in
  exactly the expected state, and the client's result document must equal,
  field by field, the one it gets from a reference server.

Exception 3 in the client direction is received from one stack only
(libmodbus, for FC05 with an invalid value sent with `WriteCoilRaw`): the
client cannot send a request with an invalid quantity, which is what the
reference servers answer with exception 3.

### What the suite found

In go-modbus, fixed in v1.3.0:

- The server closed the connection on an FC15, FC16 or FC23 request whose
  byte count did not match its quantity. The specification (figures 21, 22
  and 27) asks for exception 3, which is what libmodbus, PyModbus and
  digitalpetri/modbus answer.

In the reference stacks, as packaged by modbus-interop (none of these makes
a test fail; the suite records them where it meets them):

- **PyModbus 3.15.0** accepts FC05 with a value other than `0x0000` and
  `0xFF00` as ON and answers `0xFF00`. go-modbus reports the wrong echo as a
  protocol error. It answers FC43/14 stream access from an object the device
  does not have with no object at all.
- **tokio-modbus 0.17.0** closes the connection on FC05 with an invalid
  value and on an FC16 or FC23 byte count that does not match; **libmodbus
  v3.2.0** and tokio-modbus serve an FC15 request with one data byte too
  many.
- **libmodbus v3.2.0** frames a response by its function code, so its client
  cannot read the response to a function it does not know (FC43).

It has **not** been run against certified test equipment. Reports from the
field are welcome.
