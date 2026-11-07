# modbus-cli

A command-line Modbus client for probing and troubleshooting devices, built on
[go-modbus](../../README.md). It is a nested module of go-modbus, so the
library does not depend on cobra.

Install it with the one-line installer: see
[Install modbus-cli](../../README.md#install-modbus-cli). Contributors build it
from a checkout of go-modbus with `make build-cmd` (`./bin/modbus-cli`).

## Usage

```text
modbus-cli [flags] operation [operation...]
modbus-cli [command]
```

Operations are given as trailing arguments in a colon-separated notation and
run in order against one target:

```text
rc:<addr>[+qty]                 Read coils
rdi:<addr>[+qty]                Read discrete inputs
rh:<type>:<addr>[+qty]          Read holding registers
ri:<type>:<addr>[+qty]          Read input registers
wc:<addr>:<true|false>          Write coil
wr:<type>:<addr>:<value>        Write register
scan:<target>                   Scan address space
ping:<count>[:<interval>]       Ping device
sleep:<duration>                Pause execution
suid:<id> / sid:<id>            Set unit ID for subsequent operations
repeat                          Restart all operations from the beginning
date                            Print current date and time
```

Register types (`rh`, `ri`, `wr`): `uint16`, `int16`, `uint32`, `int32`,
`float32`, `uint64`, `int64`, `float64`, `bytes`; `wr` also accepts `string`.
`+qty` reads `qty` values after the first one (`bytes`: `qty` bytes). One
request reads at most 2000 coils or discrete inputs or 125 registers (62
32-bit values, 31 64-bit values, 250 bytes) and writes at most 123 registers
(246 bytes); ranges past address 0xffff are rejected. `wr:bytes` takes a hex
string, with or without a `0x` prefix.

Scan targets: `c`/`coils`, `di`/`discreteInputs`,
`h`/`hr`/`holding`/`holdingRegisters`, `i`/`ir`/`input`/`inputRegisters`,
`s`/`sid`.

Transports, selected by the scheme of `--target`:

| Target | Transport |
|---|---|
| `rtu:///path/to/device` | Modbus RTU (serial) |
| `rtuovertcp://host:port` | RTU over TCP |
| `rtuoverudp://host:port` | RTU over UDP |
| `ascii:///path/to/device` | Modbus ASCII (serial) |
| `asciiovertcp://host:port` | ASCII over TCP |
| `tcp://host:port` | Modbus TCP (MBAP) |
| `tcp+tls://host:port` | Modbus TCP over TLS (needs `--cert`, `--key`, `--ca`) |
| `udp://host:port` | Modbus TCP over UDP |

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--target` | | Target device to connect to (for example `tcp://somehost:502`); required to run operations |
| `--timeout` | `3s` | Request timeout (for example `3s`, `500ms`); must be greater than 0 |
| `--unit-id` | `1` | Unit (slave) ID, 0–255 |
| `--speed` | `19200` | Serial bus speed in bps (rtu, ascii) |
| `--data-bits` | `0` | Data bits per character: 0 (auto: 8 for rtu, 7 for ascii), 5–8 |
| `--parity` | `none` | Parity bit: `none`, `even`, `odd` (rtu, ascii) |
| `--stop-bits` | `0` | Stop bits: 0 (auto), 1, 2 (rtu, ascii) |
| `--endianness` | `big` | Register endianness, `big` or `little`; applies to all typed values, 16-bit ones included, but not to `bytes` |
| `--word-order` | `highfirst` | Word order of 32- and 64-bit values: `highfirst` (`hf`), `lowfirst` (`lf`) |
| `--cert` | | TLS client certificate path |
| `--key` | | TLS client key path |
| `--ca` | | TLS CA/server certificate path |
| `--json` | `false` | Output as line-delimited JSON |
| `--fail-fast` | `false` | Stop on the first operation error |

## Commands

| Command | What it does |
|---|---|
| `modbus-cli version` | Print the version, tag, commit, build date, Go version and platform on one line; with `--json` as one line of JSON |
| `modbus-cli completion bash\|zsh\|fish\|powershell` | Generate the shell completion script ([install](../../README.md#shell-completion)) |
| `modbus-cli help [command]` | Help about any command |

## Examples

```bash
# Read 6 uint32 holding registers and 11 coils, then set coil 3
modbus-cli --target tcp://10.100.0.10:502 rh:uint32:0x100+5 rc:0+10 wc:3:true

# Serial RTU: read, write, switch unit ID, loop forever
modbus-cli --target rtu:///dev/ttyUSB0 --speed 19200 \
  suid:2 rh:uint16:0+7 wr:uint16:0x2:0x0605 \
  suid:3 ri:int16:0+1 sleep:1s repeat

# Scan all register types
modbus-cli --target tcp://somehost:502 scan:hr scan:ir scan:di scan:coils

# TLS mutual authentication
modbus-cli --target tcp+tls://securehost:802 \
  --cert client.cert.pem --key client.key.pem --ca ca.cert.pem \
  rh:uint32:0x3000

# Ping a device 10 times with 500ms interval
modbus-cli --target tcp://somehost:502 ping:10:500ms
```

## Version

```text
$ modbus-cli version
modbus-cli 1.13.0 (tag v1.13.0, commit 1802d0f9afe8642d1e278120b76fcdfac967cb9e, built 2026-11-05T15:41:49Z, go-serial 0.2.1, go1.26.0, linux/amd64)
$ modbus-cli version --json
{"name":"modbus-cli","version":"1.13.0","tag":"v1.13.0","commit":"1802d0f9afe8642d1e278120b76fcdfac967cb9e","built":"2026-11-05T15:41:49Z","libraries":[{"name":"go-serial","version":"0.2.1"}],"go":"go1.26.0","platform":"linux/amd64"}
```

The OT Fabric libraries it is built on follow the build date (not go-modbus
itself, whose version is the version). A build that was not released
(`go build`, `go install`) prints `dev` or the
module version of `go install …@vX.Y.Z`, the VCS revision (with `-dirty` for
uncommitted changes), and no tag.

## Output

`--json` writes line-delimited JSON, one object per result, for `jq` and
other tools; scan and ping operations produce structured JSON too:

```bash
modbus-cli --target tcp://10.0.0.1:502 --json rh:uint16:0x100+3
```

Without `--fail-fast` the CLI runs all operations and exits non-zero if any
failed; with it, it stops at the first error. The exit code tells usage errors
(2), connection failures (4), timeouts (5), Modbus exceptions (6), partial
runs (7) and interrupts (130) apart; the full table is in
[docs/exitcodes.md](docs/exitcodes.md).
