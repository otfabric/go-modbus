package modbus

import (
	"fmt"
	"net"
	"time"
)

const maxTCPFrameLength = 260

// udpSockWrapper wraps a net.UDPConn (UDP socket) to present a stream-like
// (net.Conn-compatible) interface on top of a datagram socket. This allows
// the TCP/MBAP transport layer to read byte-by-byte from what is actually
// a datagram socket, which is necessary because the Modbus/UDP framing is
// not standardized and different vendors use different conventions.
//
// Limitations:
//   - Modbus/UDP is non-standard; this wrapper is best-effort.
//   - The wrapper treats datagrams as a byte stream by buffering leftovers
//     from partially consumed datagrams. This only works correctly when each
//     request/response maps cleanly to a single datagram with no loss,
//     reordering, or multiplexing.
//   - It is not recommended for high-reliability production use unless
//     specifically validated against the target device(s).
//   - There is no concurrency protection; the transport layer serializes access.
type udpSockWrapper struct {
	leftoverCount int
	rxbuf         []byte
	sock          *net.UDPConn
}

func newUDPSockWrapper(sock net.Conn) (*udpSockWrapper, error) {
	udpConn, ok := sock.(*net.UDPConn)
	if !ok {
		return nil, fmt.Errorf("expected *net.UDPConn, got %T", sock)
	}
	return &udpSockWrapper{
		rxbuf: make([]byte, maxTCPFrameLength),
		sock:  udpConn,
	}, nil
}

func (usw *udpSockWrapper) Read(buf []byte) (rlen int, err error) {
	var copied int

	if usw.leftoverCount > 0 {
		// if we're holding onto any bytes from a previous datagram,
		// use them to satisfy the read (potentially partially)
		copied = copy(buf, usw.rxbuf[0:usw.leftoverCount])

		if usw.leftoverCount > copied {
			// move any leftover bytes to the beginning of the buffer
			copy(usw.rxbuf, usw.rxbuf[copied:usw.leftoverCount])
		}
		// make a note of how many leftover bytes we have in the buffer
		usw.leftoverCount -= copied
	} else {
		// read up to maxTCPFrameLength bytes from the socket
		rlen, err = usw.sock.Read(usw.rxbuf)
		if err != nil {
			return
		}
		// copy as many bytes as possible to satisfy the read
		copied = copy(buf, usw.rxbuf[0:rlen])

		if rlen > copied {
			// move any leftover bytes to the beginning of the buffer
			copy(usw.rxbuf, usw.rxbuf[copied:rlen])
		}
		// make a note of how many leftover bytes we have in the buffer
		usw.leftoverCount = rlen - copied
	}

	rlen = copied

	return
}

func (usw *udpSockWrapper) Close() (err error) {
	err = usw.sock.Close()

	return
}

func (usw *udpSockWrapper) Write(buf []byte) (wlen int, err error) {
	wlen, err = usw.sock.Write(buf)

	return
}

func (usw *udpSockWrapper) SetDeadline(deadline time.Time) (err error) {
	err = usw.sock.SetDeadline(deadline)

	return
}

func (usw *udpSockWrapper) SetReadDeadline(deadline time.Time) (err error) {
	err = usw.sock.SetReadDeadline(deadline)

	return
}

func (usw *udpSockWrapper) SetWriteDeadline(deadline time.Time) (err error) {
	err = usw.sock.SetWriteDeadline(deadline)

	return
}

func (usw *udpSockWrapper) LocalAddr() (addr net.Addr) {
	addr = usw.sock.LocalAddr()

	return
}

func (usw *udpSockWrapper) RemoteAddr() (addr net.Addr) {
	addr = usw.sock.RemoteAddr()

	return
}
