// SPDX-License-Identifier: MIT

package main

const operationsHelp = `Available operations:

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

Register types (rh/ri/wr):
  uint16, int16, uint32, int32, float32, uint64, int64, float64, bytes
  (wr also accepts: string)

Scan targets:
  c/coils, di/discreteInputs, h/hr/holding/holdingRegisters,
  i/ir/input/inputRegisters, s/sid

Supported transports:
  rtu:///path/to/device           Modbus RTU (serial)
  rtuovertcp://host:port          RTU over TCP
  rtuoverudp://host:port          RTU over UDP
  tcp://host:port                 Modbus TCP (MBAP)
  tcp+tls://host:port             Modbus TCP over TLS (requires --cert, --key, --ca)
  udp://host:port                 Modbus TCP over UDP

Register endianness and word order:
  Use --endianness <big|little> (default: big, per Modbus spec).
  For multi-register values (32/64-bit), use --word-order <highfirst|lowfirst>
  (default: highfirst, i.e. most significant word first).`

const operationsExamples = `  # Read 6 uint32 holding registers and 11 coils, then set coil 3
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

  # Generate shell completion (bash, zsh, fish, powershell)
  modbus-cli completion bash > /etc/bash_completion.d/modbus-cli
  modbus-cli completion zsh > "${fpath[1]}/_modbus-cli"`

func decodeString(in []byte) (out string) {
	var dec []byte
	var b byte

	for idx := range in {
		if in[idx] >= 0x20 && in[idx] <= 0x7e {
			b = in[idx]
		} else {
			b = '.'
		}

		dec = append(dec, b)
	}

	out = string(dec)

	return
}
