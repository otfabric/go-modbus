// SPDX-License-Identifier: MIT

package modbus

type transportType uint

const (
	modbusRTU          transportType = 1
	modbusRTUOverTCP   transportType = 2
	modbusRTUOverUDP   transportType = 3
	modbusTCP          transportType = 4
	modbusTCPOverTLS   transportType = 5
	modbusTCPOverUDP   transportType = 6
	modbusASCII        transportType = 7
	modbusASCIIOverTCP transportType = 8
)
