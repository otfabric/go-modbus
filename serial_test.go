package modbus

import (
	"errors"
	"testing"
	"time"
)

// These tests exercise the serialPortWrapper error and configuration-mapping
// paths that do not require real serial hardware. The success path (an actually
// opened port) can only be covered on a host with a serial device present.

func TestSerialWrapper_NilConfig(t *testing.T) {
	spw := &serialPortWrapper{}
	if err := spw.Open(); err == nil {
		t.Fatal("Open with nil config: expected error, got nil")
	}
}

func TestSerialWrapper_NotOpen(t *testing.T) {
	spw := newSerialPortWrapper(&serialPortConfig{Device: "/dev/does-not-exist"})

	if _, err := spw.Read(make([]byte, 4)); !errors.Is(err, ErrSerialPortNotOpen) {
		t.Errorf("Read not open = %v, want ErrSerialPortNotOpen", err)
	}
	if _, err := spw.Write([]byte{0x01}); !errors.Is(err, ErrSerialPortNotOpen) {
		t.Errorf("Write not open = %v, want ErrSerialPortNotOpen", err)
	}
	if err := spw.Close(); err != nil {
		t.Errorf("Close not open = %v, want nil", err)
	}
	if err := spw.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetDeadline = %v, want nil", err)
	}
}

func TestSerialWrapper_UnsupportedParams(t *testing.T) {
	cases := []struct {
		name string
		conf *serialPortConfig
	}{
		{"data bits", &serialPortConfig{Device: "/dev/x", DataBits: 9}},
		{"stop bits", &serialPortConfig{Device: "/dev/x", DataBits: 8, StopBits: 3}},
		{"parity", &serialPortConfig{Device: "/dev/x", DataBits: 8, StopBits: 1, Parity: Parity(99)}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			spw := newSerialPortWrapper(tc.conf)
			if err := spw.Open(); err == nil {
				t.Fatalf("Open with unsupported %s: expected error, got nil", tc.name)
			}
		})
	}
}

// TestSerialWrapper_OpenConfigArms drives every valid data-bit/stop-bit/parity
// switch arm. Open ultimately fails because the device does not exist, but the
// configuration mapping is fully exercised before serial.Open is reached.
func TestSerialWrapper_OpenConfigArms(t *testing.T) {
	dataBits := []uint{5, 6, 7, 8}
	stopBits := []uint{1, 2}
	parities := []Parity{ParityNone, ParityEven, ParityOdd}

	for _, db := range dataBits {
		for _, sb := range stopBits {
			for _, p := range parities {
				conf := &serialPortConfig{
					Device:   "/dev/nonexistent-modbus-serial-test",
					Speed:    9600,
					DataBits: db,
					StopBits: sb,
					Parity:   p,
				}
				spw := newSerialPortWrapper(conf)
				if err := spw.Open(); err == nil {
					// Very unlikely, but clean up if a device happened to open.
					_ = spw.Close()
					t.Skipf("unexpected successful open of test device (db=%d sb=%d p=%d)", db, sb, p)
				}
			}
		}
	}
}
