# Limitations

What go-modbus does not do, or does differently from what one might expect.
What it implements, function by function, is in
[INTEROPERABILITY.md](INTEROPERABILITY.md).

## Scope not implemented

- **Server transports.** The server listens on Modbus TCP and TLS only. RTU,
  ASCII, UDP and the tunnelled serial framings are client-only.
- **Vendor and user-defined function codes.** The client has no call that
  sends an arbitrary PDU, and the server answers them with exception 1
  (illegal function).
- **FC43 sub-type 13.** The CANopen general reference is not supported
  ([API.md](API.md)); only MEI type 14 is.
- **SunSpec points.** The `sunspec` package detects the marker and lists model
  headers. It does not decode points or schemas: that is
  [go-sunspec](https://github.com/otfabric/go-sunspec).
- **CP56Time2a flags.** The codec ignores the status bits on decode and writes
  them as zero ([CODECS.md](CODECS.md)).

## Verification

- Run against eight independent implementations in CI (libmodbus, PyModbus,
  digitalpetri/modbus, NModbus, tokio-modbus, j2mod, jsmodbus,
  modbus-esp8266) and a ninth, modbus4j, from a local build only, because
  its image is GPL-3.0 and not published. Versions and gaps are in
  [INTEROPERABILITY.md](INTEROPERABILITY.md).
- Both directions on Modbus TCP. The client only on Modbus UDP (j2mod,
  modbus4j) and RTU over TCP (j2mod, jsmodbus, modbus-esp8266, modbus4j):
  the server has Modbus TCP only.
- TLS, RTU and ASCII on a serial line, RTU over UDP and ASCII over TCP are
  covered by this repository's own tests, not by an independent
  implementation.
- Certification: none. No conformance test of the Modbus Organization has been
  attempted, and nothing here is a claim of conformance.

## Security

Only `tcp+tls://` protects the connection. Authorization on the server is the
handler's job. See [SECURITY.md](SECURITY.md).

## Known deviations

- The server's exception order: a request for a unit the handler would reject
  with exception 11 is answered with exception 1 or 3 when it also has an
  unknown function code or an invalid quantity, because the handler sees a
  request only after those checks. The specification leaves this open;
  libmodbus, PyModbus, digitalpetri/modbus, NModbus and tokio-modbus
  servers answer 11 ([INTEROPERABILITY.md](INTEROPERABILITY.md)).
- The server does not filter unit identifiers: that is the handler's job.
- The serial framings (`rtu`, `ascii`, `rtuovertcp`, `rtuoverudp`,
  `asciiovertcp`) carry no transaction ID. After a failed exchange the client
  discards what arrives for a short window (3 × t3.5 for RTU, 20 ms for ASCII)
  before the next request; an answer that arrives later than that can still be
  read as the answer to the next request.
- Modbus/UDP is not standardised: `udp` and `rtuoverudp` assume one ADU per
  datagram. Bytes left in a datagram are dropped when the next request is
  written, and a datagram longer than 260 bytes is cut off by the socket.
