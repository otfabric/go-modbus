// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// reversed returns b in reverse order, which is how the e2e device answers
// e2eVendorFC.
func reversed(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

// SendRawRequest against our server: vendor function codes through the raw
// request handler, standard ones through their own handlers, byte for byte
// what the typed methods decode.
func TestE2E_SendRawRequest(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		raw := func(what string, fc FunctionCode, payload []byte) []byte {
			t.Helper()
			res, err := c.SendRawRequest(ctx, e2eUnit, fc, payload)
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
			if res.FunctionCode != fc {
				t.Fatalf("%s: response function code 0x%02X, want 0x%02X", what, uint8(res.FunctionCode), uint8(fc))
			}
			return res.Payload
		}

		// A vendor function code.
		in := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
		if got := raw("vendor", e2eVendorFC, in); !bytes.Equal(got, reversed(in)) {
			t.Errorf("vendor: payload % X, want % X", got, reversed(in))
		}
		if call := e2eOneCall(t, "vendor", dev); call.Kind != "raw" || call.FC != e2eVendorFC ||
			call.UnitID != e2eUnit || !bytes.Equal(call.Data, in) {
			t.Errorf("vendor: handler saw %+v", call)
		}
		// Empty request and response payloads.
		if got := raw("vendor, empty", e2eVendorFC, nil); len(got) != 0 {
			t.Errorf("vendor, empty: payload % X, want none", got)
		}
		e2eOneCall(t, "vendor, empty", dev)
		// The largest PDU both ways.
		in = make([]byte, 252)
		for i := range in {
			in[i] = byte(i)
		}
		if got := raw("vendor, 252 bytes", e2eVendorFC, in); !bytes.Equal(got, reversed(in)) {
			t.Errorf("vendor, 252 bytes: %d bytes back, want the reversed request", len(got))
		}
		e2eOneCall(t, "vendor, 252 bytes", dev)

		// Standard function codes give exactly what the typed methods decode.
		dev.setHolding(10, 0x1234, 0x5678, 0x9ABC)
		if got, want := raw("FC03", FCReadHoldingRegisters, []byte{0x00, 0x0A, 0x00, 0x03}),
			[]byte{0x06, 0x12, 0x34, 0x56, 0x78, 0x9A, 0xBC}; !bytes.Equal(got, want) {
			t.Errorf("FC03: payload % X, want % X", got, want)
		}
		if regs, err := c.ReadHoldingRegisters(ctx, e2eUnit, 10, 3); err != nil || !reflect.DeepEqual(regs, []uint16{0x1234, 0x5678, 0x9ABC}) {
			t.Errorf("ReadHoldingRegisters = %04X, %v", regs, err)
		}
		dev.setCoils(0, true, false, true, true)
		if got, want := raw("FC01", FCReadCoils, []byte{0x00, 0x00, 0x00, 0x04}), []byte{0x01, 0x0D}; !bytes.Equal(got, want) {
			t.Errorf("FC01: payload % X, want % X", got, want)
		}
		if got, want := raw("FC08", FCDiagnostics, []byte{0x00, 0x00, 0xCA, 0xFE}), []byte{0x00, 0x00, 0xCA, 0xFE}; !bytes.Equal(got, want) {
			t.Errorf("FC08: payload % X, want % X", got, want)
		}
		if got, want := raw("FC11", FCReportServerID, nil), []byte{0x05, 'G', 'O', 'M', 'B', 0xFF}; !bytes.Equal(got, want) {
			t.Errorf("FC17: payload % X, want % X", got, want)
		}
		if got, want := raw("FC07", FCReadExceptionStatus, nil), []byte{0xA5}; !bytes.Equal(got, want) {
			t.Errorf("FC07: payload % X, want % X", got, want)
		}
		if got := raw("FC06", FCWriteSingleRegister, []byte{0x03, 0x84, 0xBE, 0xEF}); !bytes.Equal(got, []byte{0x03, 0x84, 0xBE, 0xEF}) {
			t.Errorf("FC06: payload % X, want the echo", got)
		}
		if v := dev.holdingAt(900, 1)[0]; v != 0xBEEF {
			t.Errorf("FC06: register 900 = 0x%04X, want 0xBEEF", v)
		}
		dev.takeCalls()

		// Exceptions are *ExceptionError, as for every method.
		_, err := c.SendRawRequest(ctx, e2eUnit, e2eVendorFailFC, []byte{0x00})
		e2eWantException(t, "vendor exception", err, e2eVendorFailFC, exIllegalDataValue)
		e2eOneCall(t, "vendor exception", dev)
		_, err = c.SendRawRequest(ctx, e2eUnit, 0x65, nil)
		e2eWantException(t, "unhandled function code", err, 0x65, exIllegalFunction)
		_, err = c.SendRawRequest(ctx, e2eUnit, FCReadHoldingRegisters, []byte{0x00, 0x00, 0x00, 0x00})
		e2eWantException(t, "FC03 quantity 0", err, FCReadHoldingRegisters, exIllegalDataValue)
		if !errors.Is(err, ErrIllegalDataValue) {
			t.Errorf("FC03 quantity 0: errors.Is(ErrIllegalDataValue) = false for %v", err)
		}
		if calls := dev.takeCalls(); len(calls) != 0 {
			t.Errorf("handlers saw %+v, want nothing", calls)
		}

		// Invalid arguments never reach the wire.
		_, err = c.SendRawRequest(ctx, e2eUnit, 0x80|e2eVendorFC, nil)
		e2eWantParamError(t, "exception function code", err, dev)
		_, err = c.SendRawRequest(ctx, e2eUnit, e2eVendorFC, make([]byte, 253))
		e2eWantParamError(t, "253-byte payload", err, dev)

		// Nothing above disturbed the connection.
		if n := p.conns(); n != 1 {
			t.Errorf("server connections = %d, want 1", n)
		}
	})
}

// Raw requests are refused by AllowedFunctionCodes and reported to metrics and
// the PDU tap like every other request.
func TestE2E_SendRawRequest_Observed(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		m := &e2eMetrics{}
		rec := &tapRecorder{}
		p := e2eStart(t, kind, dev, e2eOpts{client: func(c *Config) {
			c.Metrics = m
			c.PDUTap = rec
			c.AllowedFunctionCodes = []FunctionCode{e2eVendorFC, e2eVendorFailFC, FCReadInputRegisters}
		}})
		c := p.client
		ctx := context.Background()

		check := func(what string, wantEvents []string, wantFC FunctionCode, wantEx []PDUExchange) {
			t.Helper()
			events := m.take()
			var kinds []string
			for _, e := range events {
				kinds = append(kinds, e.Kind)
			}
			if !reflect.DeepEqual(kinds, wantEvents) {
				t.Errorf("%s: metrics %v, want %v", what, events, wantEvents)
			}
			for _, e := range events {
				if e.FC != wantFC || e.Unit != e2eUnit {
					t.Errorf("%s: metrics event %v, want unit %d fc 0x%02X", what, e, e2eUnit, uint8(wantFC))
				}
			}
			got := rec.take()
			if len(got) != len(wantEx) {
				t.Fatalf("%s: %d exchanges tapped, want %d", what, len(got), len(wantEx))
			}
			for i := range got {
				if got[i].UnitID != e2eUnit || !bytes.Equal(got[i].Request, wantEx[i].Request) ||
					!bytes.Equal(got[i].Response, wantEx[i].Response) || got[i].Received.IsZero() {
					t.Errorf("%s: tapped %+v, want %+v", what, got[i], wantEx[i])
				}
			}
		}

		if _, err := c.SendRawRequest(ctx, e2eUnit, e2eVendorFC, []byte{0x0A, 0x0B, 0x0C}); err != nil {
			t.Fatalf("vendor: %v", err)
		}
		check("vendor", []string{"request", "response"}, e2eVendorFC,
			[]PDUExchange{{Request: []byte{0x41, 0x0A, 0x0B, 0x0C}, Response: []byte{0x41, 0x0C, 0x0B, 0x0A}}})

		dev.setInput(0, 0x0102)
		if _, err := c.SendRawRequest(ctx, e2eUnit, FCReadInputRegisters, []byte{0x00, 0x00, 0x00, 0x01}); err != nil {
			t.Fatalf("FC04: %v", err)
		}
		check("FC04", []string{"request", "response"}, FCReadInputRegisters,
			[]PDUExchange{{Request: []byte{0x04, 0x00, 0x00, 0x00, 0x01}, Response: []byte{0x04, 0x02, 0x01, 0x02}}})

		_, err := c.SendRawRequest(ctx, e2eUnit, e2eVendorFailFC, nil)
		e2eWantException(t, "vendor exception", err, e2eVendorFailFC, exIllegalDataValue)
		check("vendor exception", []string{"request", "error"}, e2eVendorFailFC,
			[]PDUExchange{{Request: []byte{0x42}, Response: []byte{0xC2, 0x03}}})
		dev.takeCalls()

		// Not on the list: refused before anything is sent.
		for _, fc := range []FunctionCode{0x43, FCReadHoldingRegisters} {
			if _, err := c.SendRawRequest(ctx, e2eUnit, fc, []byte{0x00, 0x00, 0x00, 0x01}); !errors.Is(err, ErrFunctionCodeNotAllowed) {
				t.Errorf("FC 0x%02X: want ErrFunctionCodeNotAllowed, got %v", uint8(fc), err)
			}
			check(fmt.Sprintf("FC 0x%02X refused", uint8(fc)), []string{"request", "error"}, fc, nil)
		}
		// Invalid arguments are reported nowhere.
		if _, err := c.SendRawRequest(ctx, e2eUnit, 0, nil); !errors.Is(err, ErrUnexpectedParameters) {
			t.Errorf("FC 0: want ErrUnexpectedParameters, got %v", err)
		}
		check("FC 0", nil, 0, nil)
		if calls := dev.takeCalls(); len(calls) != 0 {
			t.Errorf("handlers saw %+v, want nothing", calls)
		}
	})
}

// Many clients, and many goroutines on one pooled client, send raw requests
// at once while the device fails some of them. Every response must answer its
// own request. Run with -race.
func TestE2E_SendRawRequest_Parallel(t *testing.T) {
	const clients, rounds = 8, 40
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		var n atomic.Uint64
		dev.setHook(func(context.Context, e2eCall) error {
			if n.Add(1)%7 == 0 {
				return ErrServerDeviceBusy
			}
			return nil
		})
		for i := 0; i < clients; i++ {
			dev.setHolding(100+i, uint16(0xA000+i))
		}
		p := e2eStart(t, kind, dev, e2eOpts{
			server: func(c *ServerConfig) { c.MaxClients = 2*clients + 1 },
			client: func(c *Config) { c.MaxConns = 4 },
		})
		ctx := context.Background()
		busy := func(err error) bool { return errors.Is(err, ErrServerDeviceBusy) }

		var wg sync.WaitGroup
		errs := make(chan error, 2*clients*rounds*2)
		run := func(i int, c *Client) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				in := bytes.Repeat([]byte{byte(i), byte(r)}, 1+(i*rounds+r)%60)
				res, err := c.SendRawRequest(ctx, e2eUnit, e2eVendorFC, in)
				switch {
				case err == nil && !bytes.Equal(res.Payload, reversed(in)):
					errs <- fmt.Errorf("worker %d round %d: payload % X for request % X", i, r, res.Payload, in)
				case err != nil && !busy(err):
					errs <- fmt.Errorf("worker %d round %d vendor: %w", i, r, err)
				}
				addr := uint16(100 + i%clients)
				res, err = c.SendRawRequest(ctx, e2eUnit, FCReadHoldingRegisters, []byte{0x00, byte(addr), 0x00, 0x01})
				want := []byte{0x02, 0xA0, byte(i % clients)}
				switch {
				case err == nil && !bytes.Equal(res.Payload, want):
					errs <- fmt.Errorf("worker %d round %d: FC03 payload % X, want % X", i, r, res.Payload, want)
				case err != nil && !busy(err):
					errs <- fmt.Errorf("worker %d round %d FC03: %w", i, r, err)
				}
			}
		}
		// One client each...
		for i := 0; i < clients; i++ {
			c := p.newClient(nil)
			if err := c.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}
			wg.Add(1)
			go run(i, c)
		}
		// ...and as many goroutines sharing the pair's client.
		for i := clients; i < 2*clients; i++ {
			wg.Add(1)
			go run(i, p.client)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		if got := n.Load(); got != 2*clients*rounds*2 {
			t.Errorf("device saw %d requests, want %d", got, 2*clients*rounds*2)
		}
	})
}

// rawSerialDevice answers the raw requests of TestSendRawRequest_SerialFraming
// and TestSendRawRequest_OverPTY. It returns the response function code and
// payload.
func rawSerialDevice(fc byte, payload []byte) (byte, []byte) {
	switch fc {
	case 0x41: // vendor: the payload reversed, any length
		return fc, reversed(payload)
	case 0x42: // vendor: Illegal Data Value
		return fc | 0x80, []byte{0x03}
	case 0x44: // vendor: an empty response
		return fc, nil
	case 0x45: // vendor: the largest response
		return fc, bytes.Repeat([]byte{0x3C}, 252)
	case 0x09: // a standard code the library does not implement: echo
		return fc, payload
	case 0x03:
		return fc, []byte{0x04, 0x12, 0x34, 0x56, 0x78}
	}
	return fc | 0x80, []byte{0x01}
}

// rawSerialCase is one exchange with rawSerialDevice.
type rawSerialCase struct {
	name    string
	fc      FunctionCode
	payload []byte
	want    []byte // nil with wantErr
	wantErr error
}

var rawSerialCases = []rawSerialCase{
	{"vendor", 0x41, []byte{0x01, 0x02, 0x03}, []byte{0x03, 0x02, 0x01}, nil},
	{"vendor, one byte", 0x41, []byte{0x7E}, []byte{0x7E}, nil},
	{"vendor, empty request and response", 0x41, nil, []byte{}, nil},
	{"vendor, empty response", 0x44, []byte{0x01, 0x02}, []byte{}, nil},
	{"vendor, 252-byte request", 0x41, bytes.Repeat([]byte{0x01, 0x02}, 126), bytes.Repeat([]byte{0x02, 0x01}, 126), nil},
	{"vendor, 252-byte response", 0x45, nil, bytes.Repeat([]byte{0x3C}, 252), nil},
	{"vendor exception", 0x42, []byte{0x00}, nil, ErrIllegalDataValue},
	{"unanswered vendor function code", 0x46, nil, nil, ErrIllegalFunction},
	{"unimplemented standard function code", 0x09, []byte{0xAB, 0xCD}, []byte{0xAB, 0xCD}, nil},
	{"standard function code", 0x03, []byte{0x00, 0x00, 0x00, 0x02}, []byte{0x04, 0x12, 0x34, 0x56, 0x78}, nil},
}

// runRawSerialCases runs rawSerialCases on c, each followed by a typed read
// that shows the line is still in step.
func runRawSerialCases(t *testing.T, c *Client) {
	t.Helper()
	ctx := context.Background()
	for _, tc := range rawSerialCases {
		res, err := c.SendRawRequest(ctx, 0x11, tc.fc, tc.payload)
		if tc.wantErr != nil {
			var exc *ExceptionError
			if !errors.Is(err, tc.wantErr) || !errors.As(err, &exc) || exc.FunctionCode != tc.fc {
				t.Errorf("%s: got %v, want %v", tc.name, err, tc.wantErr)
			}
		} else if err != nil || res.FunctionCode != tc.fc || !bytes.Equal(res.Payload, tc.want) {
			t.Errorf("%s: got %+v, %v; want payload % X", tc.name, res, err, tc.want)
		}
		if regs, err := c.ReadHoldingRegisters(ctx, 0x11, 0, 2); err != nil || !reflect.DeepEqual(regs, []uint16{0x1234, 0x5678}) {
			t.Errorf("%s: ReadHoldingRegisters after it = %04X, %v", tc.name, regs, err)
		}
	}
}

// readRTURequest reads one RTU request frame of unknown length from r: bytes
// up to the first point where the CRC matches.
func readRTURequest(r io.Reader) ([]byte, error) {
	var frame []byte
	b := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		frame = append(frame, b[0])
		if len(frame) >= 4 && adu.ValidateRTUCRC(frame) {
			return frame, nil
		}
	}
}

// answerRTU returns rawSerialDevice's answer to the RTU request frame req.
func answerRTU(req []byte) []byte {
	unit, fc, payload := adu.ParseRTUFrame(req)
	resFC, resPayload := rawSerialDevice(fc, payload)
	return adu.AssembleRTUFrame(unit, resFC, resPayload)
}

// SendRawRequest over the transports whose framing differs from MBAP: RTU, where
// the response length comes from the function code or from inter-frame silence,
// and ASCII, delimited by CR LF.
func TestSendRawRequest_SerialFraming(t *testing.T) {
	t.Run("rtuovertcp", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		var mu sync.Mutex
		var seen [][]byte
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer func() { _ = conn.Close() }()
					for {
						req, err := readRTURequest(conn)
						if err != nil {
							return
						}
						mu.Lock()
						seen = append(seen, req)
						mu.Unlock()
						if _, err := conn.Write(answerRTU(req)); err != nil {
							return
						}
					}
				}()
			}
		}()
		c, err := New(Config{URL: "rtuovertcp://" + ln.Addr().String(), Speed: 115200, Timeout: 2 * time.Second})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		runRawSerialCases(t, c)

		mu.Lock()
		defer mu.Unlock()
		if want := adu.AssembleRTUFrame(0x11, 0x41, []byte{0x01, 0x02, 0x03}); len(seen) == 0 || !bytes.Equal(seen[0], want) {
			t.Errorf("first request frame % X, want % X", seen, want)
		}
	})

	t.Run("rtuoverudp", func(t *testing.T) {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("ListenPacket: %v", err)
		}
		t.Cleanup(func() { _ = pc.Close() })
		go func() {
			buf := make([]byte, 512)
			for {
				n, from, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				_, _ = pc.WriteTo(answerRTU(buf[:n]), from)
			}
		}()
		c, err := New(Config{URL: "rtuoverudp://" + pc.LocalAddr().String(), Speed: 115200, Timeout: 2 * time.Second})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		runRawSerialCases(t, c)
	})

	t.Run("asciiovertcp", func(t *testing.T) {
		addr := startASCIISlave(t, func(req *adu.Request) *adu.Response {
			fc, payload := rawSerialDevice(req.FunctionCode, req.Payload)
			return &adu.Response{UnitID: req.UnitID, FunctionCode: fc, Payload: payload}
		})
		c, err := New(Config{URL: "asciiovertcp://" + addr, Timeout: 2 * time.Second})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		runRawSerialCases(t, c)
	})
}
