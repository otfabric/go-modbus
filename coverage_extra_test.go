// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// NewConfig
// ---------------------------------------------------------------------------

func TestNewConfig(t *testing.T) {
	cfg := NewConfig(
		TransportConfig{URL: "tcp://plc:502", DialTimeout: 5 * time.Second},
		ExecutionConfig{Timeout: 3 * time.Second, MaxConns: 4, MinConns: 2},
		ObservabilityConfig{Logger: NopLogger()},
	)
	if cfg.URL != "tcp://plc:502" {
		t.Errorf("URL = %q, want tcp://plc:502", cfg.URL)
	}
	if cfg.DialTimeout != 5*time.Second {
		t.Errorf("DialTimeout = %v, want 5s", cfg.DialTimeout)
	}
	if cfg.Timeout != 3*time.Second {
		t.Errorf("Timeout = %v, want 3s", cfg.Timeout)
	}
	if cfg.MaxConns != 4 {
		t.Errorf("MaxConns = %d, want 4", cfg.MaxConns)
	}
	if cfg.MinConns != 2 {
		t.Errorf("MinConns = %d, want 2", cfg.MinConns)
	}
	if cfg.Logger == nil {
		t.Error("Logger should be non-nil")
	}
}

// ---------------------------------------------------------------------------
// Client.Info / transportKind
// ---------------------------------------------------------------------------

func TestClientInfo_TCP(t *testing.T) {
	c, err := New(Config{URL: "tcp://127.0.0.1:502"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	info := c.Info()
	if info.IsOpen {
		t.Error("expected IsOpen=false before Open")
	}
	if info.Transport != TransportTCP {
		t.Errorf("Transport = %q, want %q", info.Transport, TransportTCP)
	}
	if info.Endpoint != "127.0.0.1:502" {
		t.Errorf("Endpoint = %q, want 127.0.0.1:502", info.Endpoint)
	}
}

func TestClientInfo_AllTransportKinds(t *testing.T) {
	tests := []struct {
		url  string
		kind TransportKind
	}{
		{"rtu:///dev/ttyUSB0", TransportRTU},
		{"rtuovertcp://h:502", TransportRTUOverTCP},
		{"rtuoverudp://h:502", TransportRTUOverUDP},
		{"tcp://h:502", TransportTCP},
		{"udp://h:502", TransportTCPOverUDP},
	}
	for _, tc := range tests {
		c, err := New(Config{URL: tc.url})
		if err != nil {
			t.Fatalf("New(%q): %v", tc.url, err)
		}
		info := c.Info()
		if info.Transport != tc.kind {
			t.Errorf("URL %q: Transport = %q, want %q", tc.url, info.Transport, tc.kind)
		}
	}
}

func TestClientInfo_IsOpenAfterOpen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	c, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()

	info := c.Info()
	if !info.IsOpen {
		t.Error("expected IsOpen=true after Open")
	}
}

// ---------------------------------------------------------------------------
// ProbeOutcome.String
// ---------------------------------------------------------------------------

func TestProbeOutcomeString(t *testing.T) {
	tests := []struct {
		o    ProbeOutcome
		want string
	}{
		{ProbeSupported, "supported"},
		{ProbeException, "exception"},
		{ProbeTimeout, "timeout"},
		{ProbeTransportError, "transport_error"},
		{ProbeValidationFailed, "validation_failed"},
		{ProbeOutcome(99), "ProbeOutcome(99)"},
	}
	for _, tc := range tests {
		if got := tc.o.String(); got != tc.want {
			t.Errorf("ProbeOutcome(%d).String() = %q, want %q", tc.o, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// ProbeFunction
// ---------------------------------------------------------------------------

func TestProbeFunction_Supported(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				for {
					frame, err := readMBAPFrame(c)
					if err != nil {
						return
					}
					txid, unitID, fc := frame[0:2], frame[6], frame[7]
					_ = writeMBAPNormal(c, txid, unitID, fc, []byte{0x02, 0x00, 0x00})
				}
			}(sock)
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	result, err := client.ProbeFunction(context.Background(), 1, FCReadHoldingRegisters)
	if err != nil {
		t.Fatalf("ProbeFunction: %v", err)
	}
	if result.Outcome != ProbeSupported {
		t.Errorf("Outcome = %v, want ProbeSupported", result.Outcome)
	}
	if !result.Supported {
		t.Error("Supported should be true")
	}
}

func TestProbeFunction_Exception(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				for {
					frame, err := readMBAPFrame(c)
					if err != nil {
						return
					}
					txid, unitID, fc := frame[0:2], frame[6], frame[7]
					_ = writeMBAPException(c, txid, unitID, fc, byte(exIllegalFunction))
				}
			}(sock)
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	result, err := client.ProbeFunction(context.Background(), 1, FCReadHoldingRegisters)
	if err != nil {
		t.Fatalf("ProbeFunction: %v", err)
	}
	if result.Outcome != ProbeException {
		t.Errorf("Outcome = %v, want ProbeException", result.Outcome)
	}
	if result.Supported {
		t.Error("Supported should be false")
	}
}

func TestProbeFunction_Timeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				time.Sleep(5 * time.Second)
			}(sock)
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	result, err := client.ProbeFunction(context.Background(), 1, FCReadHoldingRegisters)
	if err != nil {
		t.Fatalf("ProbeFunction: %v", err)
	}
	if result.Outcome != ProbeTimeout {
		t.Errorf("Outcome = %v, want ProbeTimeout", result.Outcome)
	}
}

func TestProbeFunction_UnsupportedFC(t *testing.T) {
	client, err := New(Config{URL: "tcp://127.0.0.1:502", Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.ProbeFunction(context.Background(), 1, FunctionCode(0xFF))
	if err == nil {
		t.Fatal("expected error for unsupported FC")
	}
	var pe *ParameterError
	if !errors.As(err, &pe) {
		t.Errorf("expected *ParameterError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// NewSlogLogger / NewSlogFieldLogger (public wrappers)
// ---------------------------------------------------------------------------

func TestNewSlogLogger(t *testing.T) {
	l := NewSlogLogger(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if l == nil {
		t.Fatal("NewSlogLogger should return non-nil")
	}
}

func TestNewSlogFieldLogger_Public(t *testing.T) {
	fl := NewSlogFieldLogger(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if fl == nil {
		t.Fatal("NewSlogFieldLogger should return non-nil")
	}
}

// ---------------------------------------------------------------------------
// DiagnosticLoopback / DiagnosticRegister / BusMessageCount
// ---------------------------------------------------------------------------

func diagTestServer(t *testing.T) (addr string, close func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				for {
					frame, err := readMBAPFrame(c)
					if err != nil {
						return
					}
					txid, unitID, fc := frame[0:2], frame[6], frame[7]
					if fc != byte(FCDiagnostics) {
						_ = writeMBAPException(c, txid, unitID, fc, byte(exIllegalFunction))
						continue
					}
					// Echo the sub-function + 2-byte data
					payload := frame[8:]
					if len(payload) < 2 {
						_ = writeMBAPException(c, txid, unitID, fc, byte(exIllegalDataValue))
						continue
					}
					subFunc := payload[0:2]
					respData := []byte{0x00, 0x42} // example response value
					_ = writeMBAPNormal(c, txid, unitID, fc, append(subFunc, respData...))
				}
			}(sock)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestDiagnosticLoopback(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()

	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	val, err := client.DiagnosticLoopback(context.Background(), 1, 0x1234)
	if err != nil {
		t.Fatalf("DiagnosticLoopback: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("value = 0x%04X, want 0x0042", val)
	}
}

func TestDiagnosticRegister(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()

	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	val, err := client.DiagnosticRegister(context.Background(), 1)
	if err != nil {
		t.Fatalf("DiagnosticRegister: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("value = 0x%04X, want 0x0042", val)
	}
}

func TestBusMessageCount(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()

	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	val, err := client.BusMessageCount(context.Background(), 1)
	if err != nil {
		t.Fatalf("BusMessageCount: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("value = 0x%04X, want 0x0042", val)
	}
}

// ---------------------------------------------------------------------------
// DiagnosticSubFunction.String
// ---------------------------------------------------------------------------

func TestDiagnosticSubFunctionString(t *testing.T) {
	tests := []struct {
		sf   DiagnosticSubFunction
		want string
	}{
		{DiagReturnQueryData, "ReturnQueryData"},
		{DiagRestartCommunications, "RestartCommunications"},
		{DiagReturnDiagnosticRegister, "ReturnDiagnosticRegister"},
		{DiagChangeASCIIInputDelimiter, "ChangeASCIIInputDelimiter"},
		{DiagForceListenOnlyMode, "ForceListenOnlyMode"},
		{DiagClearCountersAndDiagnosticReg, "ClearCountersAndDiagnosticReg"},
		{DiagReturnBusMessageCount, "ReturnBusMessageCount"},
		{DiagReturnBusCommunicationErrorCount, "ReturnBusCommunicationErrorCount"},
		{DiagReturnBusExceptionErrorCount, "ReturnBusExceptionErrorCount"},
		{DiagReturnServerMessageCount, "ReturnServerMessageCount"},
		{DiagReturnServerNoResponseCount, "ReturnServerNoResponseCount"},
		{DiagReturnServerNAKCount, "ReturnServerNAKCount"},
		{DiagReturnServerBusyCount, "ReturnServerBusyCount"},
		{DiagReturnBusCharacterOverrunCount, "ReturnBusCharacterOverrunCount"},
		{DiagClearOverrunCounterAndFlag, "ClearOverrunCounterAndFlag"},
		{DiagnosticSubFunction(0xFFFF), "DiagnosticSubFunction(0xFFFF)"},
	}
	for _, tc := range tests {
		if got := tc.sf.String(); got != tc.want {
			t.Errorf("DiagnosticSubFunction(0x%04X).String() = %q, want %q", uint16(tc.sf), got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// MaskWriteRegister (client)
// ---------------------------------------------------------------------------

func TestMaskWriteRegister_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				for {
					frame, err := readMBAPFrame(c)
					if err != nil {
						return
					}
					txid, unitID, fc := frame[0:2], frame[6], frame[7]
					if fc != byte(FCMaskWriteRegister) {
						_ = writeMBAPException(c, txid, unitID, fc, byte(exIllegalFunction))
						continue
					}
					// Echo the 6-byte payload (addr + andMask + orMask)
					_ = writeMBAPNormal(c, txid, unitID, fc, frame[8:14])
				}
			}(sock)
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	err = client.MaskWriteRegister(context.Background(), 1, 0x0010, 0x00F2, 0x0025)
	if err != nil {
		t.Fatalf("MaskWriteRegister: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ReadHoldingRegister / ReadHoldingRegisters / ReadInputRegister / ReadInputRegisters
// ---------------------------------------------------------------------------

func TestReadHoldingRegister_Aliases(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				for {
					frame, err := readMBAPFrame(c)
					if err != nil {
						return
					}
					txid, unitID, fc := frame[0:2], frame[6], frame[7]
					if fc == byte(FCReadHoldingRegisters) || fc == byte(FCReadInputRegisters) {
						_ = writeMBAPNormal(c, txid, unitID, fc, []byte{0x02, 0x00, 0x42})
					} else {
						_ = writeMBAPException(c, txid, unitID, fc, byte(exIllegalFunction))
					}
				}
			}(sock)
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx := context.Background()

	val, err := client.ReadHoldingRegister(ctx, 1, 0)
	if err != nil {
		t.Fatalf("ReadHoldingRegister: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("ReadHoldingRegister = 0x%04X, want 0x0042", val)
	}

	vals, err := client.ReadHoldingRegisters(ctx, 1, 0, 1)
	if err != nil {
		t.Fatalf("ReadHoldingRegisters: %v", err)
	}
	if len(vals) != 1 || vals[0] != 0x0042 {
		t.Errorf("ReadHoldingRegisters = %v, want [0x0042]", vals)
	}

	val, err = client.ReadInputRegister(ctx, 1, 0)
	if err != nil {
		t.Fatalf("ReadInputRegister: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("ReadInputRegister = 0x%04X, want 0x0042", val)
	}

	vals, err = client.ReadInputRegisters(ctx, 1, 0, 1)
	if err != nil {
		t.Fatalf("ReadInputRegisters: %v", err)
	}
	if len(vals) != 1 || vals[0] != 0x0042 {
		t.Errorf("ReadInputRegisters = %v, want [0x0042]", vals)
	}
}

// ---------------------------------------------------------------------------
// AttemptMetrics bridge
// ---------------------------------------------------------------------------

type testAttemptMetrics struct {
	attempts atomic.Int64
	dials    atomic.Int64
}

func (m *testAttemptMetrics) OnRequest(uint8, FunctionCode)                     {}
func (m *testAttemptMetrics) OnResponse(uint8, FunctionCode, time.Duration)     {}
func (m *testAttemptMetrics) OnError(uint8, FunctionCode, time.Duration, error) {}
func (m *testAttemptMetrics) OnTimeout(uint8, FunctionCode, time.Duration)      {}
func (m *testAttemptMetrics) OnAttempt(_ uint8, _ FunctionCode, _ int, _ time.Duration, _ error) {
	m.attempts.Add(1)
}
func (m *testAttemptMetrics) OnRetryDial(_ int, _ time.Duration, _ error) {
	m.dials.Add(1)
}

func TestAttemptMetrics_Called(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				for {
					frame, err := readMBAPFrame(c)
					if err != nil {
						return
					}
					txid, unitID, fc := frame[0:2], frame[6], frame[7]
					_ = writeMBAPNormal(c, txid, unitID, fc, []byte{0x02, 0x00, 0x01})
				}
			}(sock)
		}
	}()

	am := &testAttemptMetrics{}
	client, err := New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
		Metrics: am,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.ReadRegisters(context.Background(), 1, 0, 1, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	if am.attempts.Load() != 1 {
		t.Errorf("OnAttempt called %d times, want 1", am.attempts.Load())
	}
}

// ---------------------------------------------------------------------------
// Server FC22 (MaskWriteRegister) and FC23 (ReadWriteMultipleRegisters)
// ---------------------------------------------------------------------------

type fc22fc23Handler struct {
	tcpTestHandler
}

func (h *fc22fc23Handler) HandleMaskWrite(_ context.Context, req *MaskWriteRequest) error {
	if req.Addr >= uint16(len(h.holding)) {
		return ErrIllegalDataAddress
	}
	h.holding[req.Addr] = (h.holding[req.Addr] & req.AndMask) | (req.OrMask & ^req.AndMask)
	return nil
}

func (h *fc22fc23Handler) HandleReadWriteRegisters(_ context.Context, req *ReadWriteRegistersRequest) ([]uint16, error) {
	if req.WriteAddr+uint16(len(req.WriteValues)) > uint16(len(h.holding)) {
		return nil, ErrIllegalDataAddress
	}
	if req.ReadAddr+req.ReadQty > uint16(len(h.holding)) {
		return nil, ErrIllegalDataAddress
	}
	for i, v := range req.WriteValues {
		h.holding[req.WriteAddr+uint16(i)] = v
	}
	out := make([]uint16, req.ReadQty)
	for i := range out {
		out[i] = h.holding[req.ReadAddr+uint16(i)]
	}
	return out, nil
}

func TestServerFC22_MaskWriteRegister(t *testing.T) {
	handler := &fc22fc23Handler{}
	handler.holding[0] = 0x00FF

	server, err := NewServer(&ServerConfig{
		URL:        "tcp://localhost:0",
		MaxClients: 2,
	}, handler)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = server.Stop() }()

	server.lock.Lock()
	addr := server.tcpListener.Addr().String()
	server.lock.Unlock()

	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	err = client.MaskWriteRegister(ctx, 9, 0, 0x00F0, 0x000F)
	if err != nil {
		t.Fatalf("MaskWriteRegister: %v", err)
	}

	val, err := client.ReadRegister(ctx, 9, 0, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegister: %v", err)
	}
	expected := uint16((0x00FF & 0x00F0) | (0x000F & ^uint16(0x00F0)))
	if val != expected {
		t.Errorf("register value = 0x%04X, want 0x%04X", val, expected)
	}
}

func TestServerFC23_ReadWriteMultipleRegisters(t *testing.T) {
	handler := &fc22fc23Handler{}
	handler.holding[0] = 0x1111
	handler.holding[1] = 0x2222

	server, err := NewServer(&ServerConfig{
		URL:        "tcp://localhost:0",
		MaxClients: 2,
	}, handler)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = server.Stop() }()

	server.lock.Lock()
	addr := server.tcpListener.Addr().String()
	server.lock.Unlock()

	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	vals, err := client.ReadWriteMultipleRegisters(ctx, 9, 0, 2, 2, []uint16{0xAAAA, 0xBBBB})
	if err != nil {
		t.Fatalf("ReadWriteMultipleRegisters: %v", err)
	}
	if len(vals) != 2 || vals[0] != 0x1111 || vals[1] != 0x2222 {
		t.Errorf("read values = %v, want [0x1111, 0x2222]", vals)
	}

	verify, err := client.ReadRegisters(ctx, 9, 2, 2, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	if len(verify) != 2 || verify[0] != 0xAAAA || verify[1] != 0xBBBB {
		t.Errorf("written values = %v, want [0xAAAA, 0xBBBB]", verify)
	}
}

func TestServerFC22_NotImplemented(t *testing.T) {
	handler := &tcpTestHandler{}

	server, err := NewServer(&ServerConfig{
		URL:        "tcp://localhost:0",
		MaxClients: 2,
	}, handler)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = server.Stop() }()

	server.lock.Lock()
	addr := server.tcpListener.Addr().String()
	server.lock.Unlock()

	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	err = client.MaskWriteRegister(context.Background(), 9, 0, 0x00FF, 0x0000)
	if !errors.Is(err, ErrIllegalFunction) {
		t.Errorf("expected ErrIllegalFunction, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// NewExponentialBackoff
// ---------------------------------------------------------------------------

func TestNewExponentialBackoff(t *testing.T) {
	rp := NewExponentialBackoff(ExponentialBackoffConfig{
		BaseDelay:      100 * time.Millisecond,
		MaxDelay:       2 * time.Second,
		MaxAttempts:    3,
		RetryOnTimeout: true,
	})
	if rp == nil {
		t.Fatal("NewExponentialBackoff should return non-nil")
	}
	retry, delay := rp.ShouldRetry(0, ErrRequestTimedOut)
	if !retry {
		t.Error("expected retry on timeout")
	}
	if delay < 50*time.Millisecond {
		t.Errorf("delay too small: %v", delay)
	}
}

// ---------------------------------------------------------------------------
// OnRetryDial (attempt bridge under retry)
// ---------------------------------------------------------------------------

func TestOnRetryDial_CalledOnRetry(t *testing.T) {
	var connCount atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				n := connCount.Add(1)
				if n == 1 {
					_ = c.Close()
					return
				}
				for {
					frame, err := readMBAPFrame(c)
					if err != nil {
						return
					}
					txid, unitID, fc := frame[0:2], frame[6], frame[7]
					_ = writeMBAPNormal(c, txid, unitID, fc, []byte{0x02, 0x00, 0x01})
				}
			}(sock)
		}
	}()

	am := &testAttemptMetrics{}
	client, err := New(Config{
		URL:         "tcp://" + ln.Addr().String(),
		Timeout:     2 * time.Second,
		RetryPolicy: ExponentialBackoff(10*time.Millisecond, 100*time.Millisecond, 3),
		Metrics:     am,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.ReadRegisters(context.Background(), 1, 0, 1, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	if am.dials.Load() < 1 {
		t.Errorf("OnRetryDial called %d times, want >= 1", am.dials.Load())
	}
}

// ---------------------------------------------------------------------------
// Transport wrapper coverage: UDP client lifecycle
// ---------------------------------------------------------------------------

func TestUDPClientLifecycle(t *testing.T) {
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := ln.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 8 {
				continue
			}
			txid := buf[0:2]
			unitID := buf[6]
			fc := buf[7]
			resp := append([]byte{txid[0], txid[1], 0, 0, 0, 5, unitID, fc, 0x02, 0x00, 0x01}, []byte{}...)
			_, _ = ln.WriteTo(resp, addr)
		}
	}()

	client, err := New(Config{URL: "udp://" + ln.LocalAddr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}

	_, err = client.ReadRegisters(context.Background(), 1, 0, 1, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}

	if err := client.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Server: safeDispatch panic recovery
// ---------------------------------------------------------------------------

type panicHandler struct{}

func (h *panicHandler) HandleCoils(_ context.Context, _ *CoilsRequest) ([]bool, error) {
	panic("test panic in handler")
}
func (h *panicHandler) HandleDiscreteInputs(_ context.Context, _ *DiscreteInputsRequest) ([]bool, error) {
	return nil, ErrIllegalFunction
}
func (h *panicHandler) HandleHoldingRegisters(_ context.Context, _ *HoldingRegistersRequest) ([]uint16, error) {
	return nil, ErrIllegalFunction
}
func (h *panicHandler) HandleInputRegisters(_ context.Context, _ *InputRegistersRequest) ([]uint16, error) {
	return nil, ErrIllegalFunction
}

// ---------------------------------------------------------------------------
// FC08 Diagnostic convenience wrappers (all were at 0% coverage)
// ---------------------------------------------------------------------------

func TestDiagnosticForceListenOnlyMode(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.DiagnosticForceListenOnlyMode(context.Background(), 1); err != nil {
		t.Fatalf("DiagnosticForceListenOnlyMode: %v", err)
	}
}

func TestDiagnosticClearCounters(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.DiagnosticClearCounters(context.Background(), 1); err != nil {
		t.Fatalf("DiagnosticClearCounters: %v", err)
	}
}

func TestDiagnosticBusCommunicationErrorCount(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	val, err := client.DiagnosticBusCommunicationErrorCount(context.Background(), 1)
	if err != nil {
		t.Fatalf("DiagnosticBusCommunicationErrorCount: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("value = 0x%04X, want 0x0042", val)
	}
}

func TestDiagnosticBusExceptionErrorCount(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	val, err := client.DiagnosticBusExceptionErrorCount(context.Background(), 1)
	if err != nil {
		t.Fatalf("DiagnosticBusExceptionErrorCount: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("value = 0x%04X, want 0x0042", val)
	}
}

func TestDiagnosticServerMessageCount(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	val, err := client.DiagnosticServerMessageCount(context.Background(), 1)
	if err != nil {
		t.Fatalf("DiagnosticServerMessageCount: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("value = 0x%04X, want 0x0042", val)
	}
}

func TestDiagnosticServerNoResponseCount(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	val, err := client.DiagnosticServerNoResponseCount(context.Background(), 1)
	if err != nil {
		t.Fatalf("DiagnosticServerNoResponseCount: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("value = 0x%04X, want 0x0042", val)
	}
}

func TestDiagnosticServerNAKCount(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	val, err := client.DiagnosticServerNAKCount(context.Background(), 1)
	if err != nil {
		t.Fatalf("DiagnosticServerNAKCount: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("value = 0x%04X, want 0x0042", val)
	}
}

func TestDiagnosticServerBusyCount(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	val, err := client.DiagnosticServerBusyCount(context.Background(), 1)
	if err != nil {
		t.Fatalf("DiagnosticServerBusyCount: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("value = 0x%04X, want 0x0042", val)
	}
}

func TestDiagnosticBusCharacterOverrunCount(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	val, err := client.DiagnosticBusCharacterOverrunCount(context.Background(), 1)
	if err != nil {
		t.Fatalf("DiagnosticBusCharacterOverrunCount: %v", err)
	}
	if val != 0x0042 {
		t.Errorf("value = 0x%04X, want 0x0042", val)
	}
}

func TestDiagnosticClearOverrunCounterAndFlag(t *testing.T) {
	addr, closeFn := diagTestServer(t)
	defer closeFn()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.DiagnosticClearOverrunCounterAndFlag(context.Background(), 1); err != nil {
		t.Fatalf("DiagnosticClearOverrunCounterAndFlag: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ValidateConfig / ValidateServerConfig
// ---------------------------------------------------------------------------

func TestValidateConfig_Valid(t *testing.T) {
	if err := ValidateConfig(Config{URL: "tcp://localhost:502", Timeout: time.Second}); err != nil {
		t.Errorf("ValidateConfig should pass for valid config, got: %v", err)
	}
}

func TestValidateConfig_Invalid(t *testing.T) {
	err := ValidateConfig(Config{URL: ""})
	if err == nil {
		t.Error("ValidateConfig should fail for empty URL")
	}
}

func TestValidateServerConfig_Valid(t *testing.T) {
	h := &panicHandler{}
	if err := ValidateServerConfig(&ServerConfig{URL: "tcp://localhost:0", MaxClients: 1}, h); err != nil {
		t.Errorf("ValidateServerConfig should pass, got: %v", err)
	}
}

func TestValidateServerConfig_NilHandler(t *testing.T) {
	err := ValidateServerConfig(&ServerConfig{URL: "tcp://localhost:0"}, nil)
	if err == nil {
		t.Error("ValidateServerConfig should fail for nil handler")
	}
}

// ---------------------------------------------------------------------------
// protocol_validate.go edge cases — overflow paths
// ---------------------------------------------------------------------------

func TestValidateReadBitsRange_Overflow(t *testing.T) {
	err := validateReadBitsRange(0xFFF0, 20)
	if err == nil {
		t.Error("expected error for addr+quantity overflow")
	}
}

func TestValidateWriteBitsRange_Overflow(t *testing.T) {
	err := validateWriteBitsRange(0xFFF0, 20)
	if err == nil {
		t.Error("expected error for addr+quantity overflow")
	}
}

func TestValidateReadRegsRange_Overflow(t *testing.T) {
	err := validateReadRegsRange(0xFFF0, 20)
	if err == nil {
		t.Error("expected error for addr+quantity overflow")
	}
}

func TestValidateWriteRegsRange_Overflow(t *testing.T) {
	err := validateWriteRegsRange(0xFFF0, 20)
	if err == nil {
		t.Error("expected error for addr+quantity overflow")
	}
}

func TestValidateReadBitsRange_ZeroQuantity(t *testing.T) {
	err := validateReadBitsRange(0, 0)
	if err == nil {
		t.Error("expected error for zero quantity")
	}
}

func TestValidateWriteBitsRange_ZeroQuantity(t *testing.T) {
	err := validateWriteBitsRange(0, 0)
	if err == nil {
		t.Error("expected error for zero quantity")
	}
}

// ---------------------------------------------------------------------------
// UDP and TLS wrapper net.Conn stubs
// ---------------------------------------------------------------------------

func TestUDPSockWrapper_NetConnMethods(t *testing.T) {
	sock, err := net.Dial("udp", "127.0.0.1:53")
	if err != nil {
		t.Skipf("cannot dial UDP: %v", err)
	}
	defer func() { _ = sock.Close() }()
	usw := &udpSockWrapper{sock: sock.(*net.UDPConn)}
	if err := usw.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetReadDeadline: %v", err)
	}
	if err := usw.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetWriteDeadline: %v", err)
	}
	if usw.LocalAddr() == nil {
		t.Error("LocalAddr returned nil")
	}
	if usw.RemoteAddr() == nil {
		t.Error("RemoteAddr returned nil")
	}
}

func TestTLSSockWrapper_NetConnMethods(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, _ := ln.Accept()
		if c != nil {
			time.Sleep(time.Second)
			_ = c.Close()
		}
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	tsw := &tlsSockWrapper{sock: conn}
	if err := tsw.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetReadDeadline: %v", err)
	}
	if err := tsw.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetWriteDeadline: %v", err)
	}
	if tsw.LocalAddr() == nil {
		t.Error("LocalAddr returned nil")
	}
	if tsw.RemoteAddr() == nil {
		t.Error("RemoteAddr returned nil")
	}
}

// ---------------------------------------------------------------------------
// Panic recovery
// ---------------------------------------------------------------------------

func TestServerSafeDispatch_PanicRecovery(t *testing.T) {
	server, err := NewServer(&ServerConfig{
		URL:        "tcp://localhost:0",
		MaxClients: 2,
	}, &panicHandler{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = server.Stop() }()

	server.lock.Lock()
	addr := server.tcpListener.Addr().String()
	server.lock.Unlock()

	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.ReadCoils(context.Background(), 1, 0, 1)
	if !errors.Is(err, ErrServerDeviceFailure) {
		t.Errorf("expected ErrServerDeviceFailure after panic, got: %v", err)
	}
}
