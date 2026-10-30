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

- Run against five independent implementations (libmodbus, PyModbus,
  digitalpetri/modbus, NModbus, tokio-modbus; versions in
  [INTEROPERABILITY.md](INTEROPERABILITY.md)), **on Modbus TCP only**.
- TLS, RTU, ASCII, UDP and the tunnelled framings are covered by this
  repository's own tests, not by an independent implementation.
- Certification: none. No conformance test of the Modbus Organization has been
  attempted, and nothing here is a claim of conformance.

## Security

Only `tcp+tls://` protects the connection. Authorization on the server is the
handler's job. See [SECURITY.md](SECURITY.md).

## Known deviations

- The server's exception order: a request for a unit the handler would reject
  with exception 11 is answered with exception 1 or 3 when it also has an
  unknown function code or an invalid quantity, because the handler sees a
  request only after those checks. The specification leaves this open; the
  five reference servers answer 11 ([INTEROPERABILITY.md](INTEROPERABILITY.md)).
- The server does not filter unit identifiers: that is the handler's job.
