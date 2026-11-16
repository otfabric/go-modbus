// SPDX-License-Identifier: MIT

package modbus

import (
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/otfabric/go-serial"
)

// These tests check that Config.SerialVerifySettings and
// Config.SerialUncheckedAddress reach the serial.Config the serial transports
// open their port with. openSerialPort is replaced by a recorder, so no
// device is opened.

// recordedSerialPort is the serial.Port the recorder returns.
type recordedSerialPort struct{}

func (recordedSerialPort) Read([]byte) (int, error)    { return 0, io.EOF }
func (recordedSerialPort) Write(b []byte) (int, error) { return len(b), nil }
func (recordedSerialPort) Close() error                { return nil }

// serialOpenRecorder records the serial.Config of every open.
type serialOpenRecorder struct {
	mu   sync.Mutex
	cfgs []serial.Config
}

func (r *serialOpenRecorder) opened(t *testing.T) []serial.Config {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]serial.Config(nil), r.cfgs...)
}

// recordSerialOpens replaces openSerialPort until the test ends. Each open is
// recorded and returns a recordedSerialPort, or err when err is not nil.
func recordSerialOpens(t *testing.T, err error) *serialOpenRecorder {
	t.Helper()
	rec := &serialOpenRecorder{}
	prev := openSerialPort
	openSerialPort = func(cfg *serial.Config) (serial.Port, error) {
		rec.mu.Lock()
		rec.cfgs = append(rec.cfgs, *cfg)
		rec.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return recordedSerialPort{}, nil
	}
	t.Cleanup(func() { openSerialPort = prev })
	return rec
}

// serialSettingCases are the four combinations of the two settings.
var serialSettingCases = []struct {
	name             string
	verifySettings   bool
	uncheckedAddress bool
}{
	{"both off", false, false},
	{"VerifySettings on", true, false},
	{"UncheckedAddress on", false, true},
	{"both on", true, true},
}

func TestSerialWrapper_PassesVerifySettingsAndUncheckedAddress(t *testing.T) {
	for _, tc := range serialSettingCases {
		t.Run(tc.name, func(t *testing.T) {
			rec := recordSerialOpens(t, nil)
			spw := newSerialPortWrapper(&serialPortConfig{
				Device:           "/dev/ttyUSB0",
				VerifySettings:   tc.verifySettings,
				UncheckedAddress: tc.uncheckedAddress,
			})
			if err := spw.Open(); err != nil {
				t.Fatalf("Open = %v, want nil", err)
			}
			t.Cleanup(func() { _ = spw.Close() })

			cfgs := rec.opened(t)
			if len(cfgs) != 1 {
				t.Fatalf("%d opens, want 1", len(cfgs))
			}
			got := cfgs[0]
			if got.Address != "/dev/ttyUSB0" {
				t.Errorf("Address = %q, want /dev/ttyUSB0", got.Address)
			}
			if got.VerifySettings != tc.verifySettings {
				t.Errorf("VerifySettings = %v, want %v", got.VerifySettings, tc.verifySettings)
			}
			if got.UncheckedAddress != tc.uncheckedAddress {
				t.Errorf("UncheckedAddress = %v, want %v", got.UncheckedAddress, tc.uncheckedAddress)
			}
		})
	}
}

func TestClientSerialSettings_ReachSerialConfig(t *testing.T) {
	builds := []struct {
		name  string
		build func(url string, verify, unchecked bool) Config
	}{
		{"Config", func(url string, verify, unchecked bool) Config {
			return Config{URL: url, SerialVerifySettings: verify, SerialUncheckedAddress: unchecked}
		}},
		{"NewConfig", func(url string, verify, unchecked bool) Config {
			return NewConfig(TransportConfig{URL: url, SerialVerifySettings: verify, SerialUncheckedAddress: unchecked},
				ExecutionConfig{}, ObservabilityConfig{})
		}},
	}
	transports := []struct {
		url      string
		dataBits serial.DataBits
	}{
		{"rtu://COM10", serial.DataBits8},
		{"ascii://COM10", serial.DataBits7},
	}

	for _, tr := range transports {
		for _, b := range builds {
			for _, tc := range serialSettingCases {
				t.Run(tr.url+"/"+b.name+"/"+tc.name, func(t *testing.T) {
					rec := recordSerialOpens(t, nil)
					mc, err := New(b.build(tr.url, tc.verifySettings, tc.uncheckedAddress))
					if err != nil {
						t.Fatalf("New = %v", err)
					}
					if err := mc.Open(); err != nil {
						t.Fatalf("Open = %v, want nil", err)
					}
					t.Cleanup(func() { _ = mc.Close() })

					cfgs := rec.opened(t)
					if len(cfgs) != 1 {
						t.Fatalf("%d opens, want 1", len(cfgs))
					}
					got := cfgs[0]
					if got.Address != "COM10" || got.DataBits != tr.dataBits {
						t.Errorf("Address, DataBits = %q, %v; want COM10, %v", got.Address, got.DataBits, tr.dataBits)
					}
					if got.VerifySettings != tc.verifySettings {
						t.Errorf("VerifySettings = %v, want %v", got.VerifySettings, tc.verifySettings)
					}
					if got.UncheckedAddress != tc.uncheckedAddress {
						t.Errorf("UncheckedAddress = %v, want %v", got.UncheckedAddress, tc.uncheckedAddress)
					}
				})
			}
		}
	}
}

// TestClientSerialSettings_ConfigErrorReachesOpen pins what Config's doc
// comments say a caller gets when the port refuses its settings or address:
// go-serial's *serial.ConfigError, through errors.As, and a closed client.
func TestClientSerialSettings_ConfigErrorReachesOpen(t *testing.T) {
	for _, url := range []string{"rtu:///dev/ttyUSB0", "ascii:///dev/ttyUSB0"} {
		t.Run(url, func(t *testing.T) {
			refused := &serial.ConfigError{Field: "Parity", Value: serial.ParityNone, Reason: "port reports N, want E", Err: serial.ErrInvalidConfig}
			recordSerialOpens(t, refused)
			mc, err := New(Config{URL: url, Parity: ParityEven, SerialVerifySettings: true})
			if err != nil {
				t.Fatalf("New = %v", err)
			}
			err = mc.Open()
			var cfgErr *serial.ConfigError
			if !errors.As(err, &cfgErr) || cfgErr.Field != "Parity" {
				t.Fatalf("Open = %v, want the *serial.ConfigError for Parity", err)
			}
			if !errors.Is(err, serial.ErrInvalidConfig) {
				t.Errorf("Open = %v, does not match serial.ErrInvalidConfig", err)
			}
			if mc.Info().IsOpen {
				t.Error("client is open after the failed Open")
			}
		})
	}
}
