package modbus

import (
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-serial"
	serialmodbus "github.com/otfabric/go-serial/modbus"
)

// serialPortWrapper wraps a serial.Port (i.e. physical port) to
// 1) satisfy the rtuLink interface and
// 2) add Read() deadline/timeout support.
type serialPortWrapper struct {
	conf     *serialPortConfig
	port     serial.Port
	deadline time.Time
}

type serialPortConfig struct {
	Device   string
	Speed    uint
	DataBits uint
	Parity   Parity
	StopBits uint
}

// ErrSerialPortNotOpen is returned by Read and Write when the serial port has not been opened or has been closed.
// Use errors.Is(err, ErrSerialPortNotOpen) to detect.
var ErrSerialPortNotOpen = errors.New("modbus: serial port not open")

func newSerialPortWrapper(conf *serialPortConfig) (spw *serialPortWrapper) {
	spw = &serialPortWrapper{
		conf: conf,
	}

	return
}

func (spw *serialPortWrapper) Open() error {
	if spw.conf == nil {
		return fmt.Errorf("modbus: nil serial port config")
	}
	if spw.port != nil {
		return fmt.Errorf("modbus: serial port already open")
	}
	// Start from Modbus RTU defaults (19200 8E1), then override with client config.
	cfg := serialmodbus.DefaultModbusRTUConfig(spw.conf.Device)
	cfg.Timeout = 10 * time.Millisecond

	if spw.conf.Speed != 0 {
		cfg.BaudRate = serial.BaudRate(spw.conf.Speed)
	}

	if spw.conf.DataBits != 0 {
		switch spw.conf.DataBits {
		case 5:
			cfg.DataBits = serial.DataBits5
		case 6:
			cfg.DataBits = serial.DataBits6
		case 7:
			cfg.DataBits = serial.DataBits7
		case 8:
			cfg.DataBits = serial.DataBits8
		default:
			return fmt.Errorf("modbus: unsupported serial data bits %d", spw.conf.DataBits)
		}
	}

	if spw.conf.StopBits != 0 {
		switch spw.conf.StopBits {
		case 1:
			cfg.StopBits = serial.StopBits1
		case 2:
			cfg.StopBits = serial.StopBits2
		default:
			return fmt.Errorf("modbus: unsupported serial stop bits %d", spw.conf.StopBits)
		}
	}

	switch spw.conf.Parity {
	case ParityEven:
		cfg.Parity = serial.ParityEven
	case ParityOdd:
		cfg.Parity = serial.ParityOdd
	case ParityNone:
		cfg.Parity = serial.ParityNone
	default:
		return fmt.Errorf("modbus: unsupported serial parity %v", spw.conf.Parity)
	}

	port, err := serial.Open(&cfg)
	if err != nil {
		return err
	}
	spw.deadline = time.Time{}
	spw.port = port
	return nil
}

// Close closes the serial port. It is safe to call if Open() failed; in that case port is nil and Close returns nil.
// The wrapper clears its port reference after Close returns so that later Read/Write calls return ErrSerialPortNotOpen.
func (spw *serialPortWrapper) Close() error {
	if spw.port == nil {
		return nil
	}
	err := spw.port.Close()
	spw.port = nil
	return err
}

// Reads bytes from the underlying serial port.
// If Read() is called after the deadline, a timeout error is returned without
// attempting to read from the serial port.
// If Read() is called before the deadline, a read attempt to the serial port
// is made. At this point, one of two things can happen:
//   - the serial port's receive buffer has one or more bytes and port.Read()
//     returns immediately (partial or full read),
//   - the serial port's receive buffer is empty: port.Read() blocks for
//     up to 10ms and returns serial.ErrTimeout. The serial timeout error is
//     masked and Read() returns with no data.
//
// As the higher-level methods use io.ReadFull(), Read() will be called
// as many times as necessary until either enough bytes have been read or an
// error is returned (ErrRequestTimedOut or any other i/o error).
func (spw *serialPortWrapper) Read(rxbuf []byte) (int, error) {
	if spw.port == nil {
		return 0, ErrSerialPortNotOpen
	}
	if !spw.deadline.IsZero() && time.Now().After(spw.deadline) {
		return 0, ErrRequestTimedOut
	}

	n, err := spw.port.Read(rxbuf)
	if err != nil && errors.Is(err, serial.ErrTimeout) {
		return n, nil
	}
	return n, err
}

// Write sends the bytes over the wire.
func (spw *serialPortWrapper) Write(txbuf []byte) (int, error) {
	if spw.port == nil {
		return 0, ErrSerialPortNotOpen
	}
	return spw.port.Write(txbuf)
}

// SetDeadline sets the deadline for Read. Zero time means no deadline yet.
func (spw *serialPortWrapper) SetDeadline(deadline time.Time) error {
	spw.deadline = deadline
	return nil
}
