package main

import (
	"flag"
	"fmt"
	"os"
)

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

func displayHelp() {
	flag.CommandLine.SetOutput(os.Stdout)

	fmt.Println(
		`This tool is a modbus command line interface client meant to allow quick and easy
interaction with modbus devices (e.g. for probing or troubleshooting).

Available options:`)
	flag.PrintDefaults()
	fmt.Printf(
		`

Commands must be given as trailing arguments after any options.

Example: modbus-cli --target=tcp://somehost:502 --timeout=3s rh:uint16:0x100+5 wc:12:true
         Read 6 holding registers at address 0x100 then set the coil at address 12 to true
         on modbus/tcp device somehost port 502, with a timeout of 3s.

Available commands:
* <rc|readCoils>:<addr>[+additional quantity]
  Read coil at address <addr>, plus any additional coils if specified.

  rc:0x100+199         reads 200 coils starting at address 0x100 (hex)
  rc:300               reads 1 coil at address 300 (decimal)

* <rdi|readDiscreteInputs>:<addr>[+additional quantity]
  Read discrete input at address <addr>, plus any additional discrete inputs if specified.

  rdi:0x100+199        reads 200 discrete inputs starting at address 0x100 (hex)
  rdi:300              reads 1 discrete input at address 300 (decimal)

* <rh|readHoldingRegisters>:<type>:<addr>[+additional quantity]
  Read holding registers at address <addr>, plus any additional registers if specified,
  decoded as <type> which should be one of:
  - uint16:            unsigned 16-bit integer,
  - int16:             signed 16-bit integer,
  - uint32:            unsigned 32-bit integer (2 contiguous modbus registers),
  - int32:             signed 32-bit integer (2 contiguous modbus registers),
  - float32:           32-bit floating point number (2 contiguous modbus registers),
  - uint64:            unsigned 64-bit integer (4 contiguous modbus registers),
  - int64:             signed 64-bit integer (4 contiguous modbus registers),
  - float64:           64-bit floating point number (4 contiguous modbus registers),
  - bytes:             string of bytes (2 bytes per modbus register).

  rh:int16:0x300+1     reads 2 consecutive 16-bit signed integers at addresses 0x300 and 0x301
  rh:uint32:20         reads a 32-bit unsigned integer at addresses 20-21 (2 modbus registers)
  rh:float32:500+10    reads 11 32-bit floating point numbers at addresses 500-521
                       (11 * 32bit make for 22 16-bit contiguous modbus registers)

* <ri|readInputRegister|readInputRegisters>:<type>:<addr>[+additional quantity]
  Read input registers at address <addr>, plus any additional registers if specified, decoded
  in the same way as explained above.

  ri:uint16:0x300+1    reads 2 consecutive 16-bit unsigned integers at addresses 0x300 and 0x301
  ri:int32:20          reads a 32-bit signed integer at addresses 20-21 (2 modbus registers)

* <wc|writeCoil>:<addr>:<value>
  Set the coil at address <addr> to either true or false, depending on <value>.

  wc:1:true            writes true to the coil at address 1
  wc:2:false           writes false to the coil at address 2

* <wr:writeRegister>:<type>:<addr>:<value>
  Write <value> to register(s) at address <addr>, using the encoding given by <type>.

  wr:int16:0xf100:-10  writes -10 as a 16-bit signed integer at address 0xf100
                       (1 modbus register)
  wr:int32:0xff00:0xff writes 0xff as a 32-bit signed integer at addresses 0xff00-0xff01
                       (2 consecutive modbus registers)
  wr:float64:100:-3.2  writes -3.2 as a 64-bit float at addresses 100-103
                       (4 consecutive modbus registers)
  wr:bytes:5:fafbfcfd  writes 0xfafbfcfd as a 4-byte string at addresses 5-6
                       (2 consecutive modbus registers)

* sleep:<duration>
  Pause for <duration>, specified as a golang duration string.

  sleep:300s           sleeps for 300 seconds
  sleep:3m             sleeps for 3 minutes
  sleep:3ms            sleeps for 3 milliseconds

* <setUnitId|suid|sid>:<unit id>
  Switch to unit id (slave id) <unit id> for subsequent requests.

  sid:10               selects unit id #10

* repeat
  Restart execution of the given commands.

  rh:uint32:100 sleep:1s repeat  reads a 32-bit unsigned integer at addresses 100-101 and
                                 pauses for one second, forever in a loop.

* date
  Print the current date and time (can be useful for long-running scripts).

* scan:<type>
  Perform a modbus "scan" of the modbus type <type>, which can be one of:
  - "c", "coils",
  - "di", "discreteInputs",
  - "hr", "holdingRegisters",
  - "ir", "inputRegisters",
  - "s", "sid".

  scan:hr              scans the device for holding registers.
  scan:di              scans the device for discrete inputs.

  Read requests are made over the entire address space (65535 addresses).
  Addresses for which a non-error response is received are listed, along with the value received.
  Errors other than Illegal Data Address and Illegal Function are also shown, as they should
  not happen in sane implementations.

  scan:sid             scans the target for devices.

  Scans all unit IDs (0 to 255) using a single read input register request. Addresses responding
  positively or with non-timeout errors are shown, while timeouts and gateway timeouts are ignored.
  This command can be used to find active nodes on RS485 buses, behind gateways or in composite
  devices.

* ping:<count>[:interval]
  Executes <count> modbus reads (1 holding register at address 0x0000), either back to back or
  separated by [interval] if specified, then prints timing and outcome statistics.
  This command can be used to troubleshoot network or serial connections.

Register endianness and word order:
  The endianness of holding/input registers can be specified with --endianness <big|little> and
  defaults to big endian (as per the modbus spec).
  For constructs spanning multiple consecutive registers (namely [u]int32, float32, [u]int64 and
  float64), the word order can be set with --word-order <highfirst|lowfirst> and arbitrarily
  defaults to highfirst (i.e. most significant word first).

Supported transports and associated target schemes:
  - Modbus RTU using a local serial device:               rtu:///path/to/device
  - Modbus RTU over TCP (RTU framing over a TCP socket):  rtuovertcp://host:port
  - Modbus RTU over UDP (RTU framing over an UDP socket): rtuoverudp://host:port
  - Modbus TCP (MBAP):                                    tcp://host:port
  - Modbus TCP over TLS (MBAPS or Modbus Security):       tcp+tls://host:port
  - Modbus TCP over UDP (MBAP over UDP):                  udp://host:port
Note that UDP transports are not part of the Modbus protocol specification.

Examples:
  $ modbus-cli --target tcp://10.100.0.10:502 rh:uint32:0x100+5 rc:0+10 wc:3:true
  Connect to 10.100.0.10 port 502, read 6 consecutive 32-bit unsigned integers at addresses
  0x100-0x10b (12 modbus registers) and 11 coils at addresses 0-10, then set the coil at
  address 3 to true.

  $ modbus-cli --target rtu:///dev/ttyUSB0 --speed 19200 suid:2 rh:uint16:0+7 \
    wr:uint16:0x2:0x0605 suid:3 ri:int16:0+1 sleep:1s repeat
  Open serial port /dev/ttyUSB0 at a speed of 19200 bps and repeat forever:
    select unit id (slave id) 2, read holding registers at addresses 0-7 as 16 bit unsigned
    integers, write 0x605 as a 16-bit unsigned integer at address 2,
    change for unit id 3, read input registers 0-1 as 16-bit signed integers,
    pause for 1s.

  $ modbus-cli --target tcp://somehost:502 scan:hr scan:ir scan:di scan:coils
  Connect to somehost port 502 and perform a scan of all modbus types (namely
  holding registers, input registers, discrete inputs and coils).

  $ modbus-cli --target tcp+tls://securehost:802 --cert client.cert.pem --key client.key.pem \
    --ca ca.cert.pem rh:uint32:0x3000
  Connect to securehost port 802 using modbus/TCP over TLS, using client.cert.pem and
  client.key.pem to authenticate to the server (client auth) and ca.cert.pem to authenticate
  the server, then read holding registers 0x3000-0x3001 as a 32-bit unsigned integer.
  Note that ca.cert.pem can either be a CA (Certificate Authority) or the server (leaf)
  certificate.
`)
}
