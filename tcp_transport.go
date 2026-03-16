package modbus

import (
	"fmt"
	"net"
	"time"

	inttrans "github.com/otfabric/go-modbus/internal/transport"
)

// newTCPTransport returns a TCP transport implementing inttrans.Transport.
func newTCPTransport(socket net.Conn, timeout time.Duration, l Logger) inttrans.Transport {
	return inttrans.NewTCP(socket, timeout, newLogger(fmt.Sprintf("tcp-transport(%s)", socket.RemoteAddr()), l))
}
