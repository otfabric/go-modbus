// SPDX-License-Identifier: MIT

package modbus

import (
	"fmt"
	"net"
	"time"

	inttrans "github.com/otfabric/go-modbus/internal/transport"
)

// newTCPTransport returns a Modbus/TCP (MBAP) transport on socket.
func newTCPTransport(socket net.Conn, timeout time.Duration, l Logger) *inttrans.TCP {
	return inttrans.NewTCP(socket, timeout, newLogger(fmt.Sprintf("tcp-transport(%s)", socket.RemoteAddr()), l))
}
