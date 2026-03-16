package modbus

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/modbus/codec"
)

// typedReadHandler is a minimal Modbus server handler that serves a fixed array
// of 12 holding registers for the typed-read integration tests.
type typedReadHandler struct {
	holding [12]uint16
}

func (h *typedReadHandler) HandleCoils(_ context.Context, _ *CoilsRequest) ([]bool, error) {
	return nil, ErrIllegalFunction
}

func (h *typedReadHandler) HandleDiscreteInputs(_ context.Context, _ *DiscreteInputsRequest) ([]bool, error) {
	return nil, ErrIllegalFunction
}

func (h *typedReadHandler) HandleInputRegisters(_ context.Context, _ *InputRegistersRequest) ([]uint16, error) {
	return nil, ErrIllegalFunction
}

func (h *typedReadHandler) HandleHoldingRegisters(_ context.Context, req *HoldingRegistersRequest) (res []uint16, err error) {
	if req.UnitID != 1 {
		err = ErrIllegalFunction
		return
	}

	if req.Addr+req.Quantity > uint16(len(h.holding)) {
		err = ErrIllegalDataAddress
		return
	}

	res = make([]uint16, req.Quantity)

	for i := range res {
		res[i] = h.holding[int(req.Addr)+i]
	}

	return
}

func startTypedReadServer(t *testing.T, h *typedReadHandler, url string) (*Server, *Client) {
	t.Helper()

	var server *Server
	var client *Client
	var err error

	server, err = NewServer(&ServerConfig{
		URL:        url,
		MaxClients: 1,
	}, h)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	if err = server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}

	client, err = New(Config{URL: url})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	if err = client.Open(); err != nil {
		t.Fatalf("failed to open client: %v", err)
	}

	return server, client
}

// TestReadUint16AndInt16 tests ReadUint16, ReadUint16s, ReadInt16, ReadInt16s.
func TestReadUint16AndInt16(t *testing.T) {
	h := &typedReadHandler{}
	// holding[0] = 0x1234 (positive uint16 / int16)
	// holding[1] = 0xFFFF → uint16: 65535, int16: -1
	// holding[2] = 0x8000 → uint16: 32768, int16: -32768
	h.holding[0] = 0x1234
	h.holding[1] = 0xFFFF
	h.holding[2] = 0x8000

	server, client := startTypedReadServer(t, h, "tcp://localhost:5506")
	defer func() { _ = client.Close(); _ = server.Stop() }()

	ctx := context.Background()

	// --- ReadRegister (was ReadUint16) ---
	u16, err := client.ReadRegister(ctx, 1, 0x0000, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegister: unexpected error: %v", err)
	}
	if u16 != 0x1234 {
		t.Errorf("ReadRegister: expected 0x1234, got 0x%04x", u16)
	}

	// --- ReadRegisters (was ReadUint16s) ---
	u16s, err := client.ReadRegisters(ctx, 1, 0x0000, 3, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: unexpected error: %v", err)
	}
	if len(u16s) != 3 {
		t.Fatalf("ReadRegisters: expected 3 values, got %d", len(u16s))
	}
	if u16s[0] != 0x1234 || u16s[1] != 0xFFFF || u16s[2] != 0x8000 {
		t.Errorf("ReadRegisters: unexpected values: %v", u16s)
	}

	// --- ReadRegister + int16 (was ReadInt16) ---
	reg, err := client.ReadRegister(ctx, 1, 0x0001, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegister: unexpected error: %v", err)
	}
	i16 := int16(reg)
	if i16 != -1 {
		t.Errorf("int16(register): expected -1, got %v", i16)
	}

	// --- ReadRegisters + int16 (was ReadInt16s) ---
	regs, err := client.ReadRegisters(ctx, 1, 0x0001, 2, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: unexpected error: %v", err)
	}
	if len(regs) != 2 {
		t.Fatalf("ReadRegisters: expected 2 values, got %d", len(regs))
	}
	i16s := []int16{int16(regs[0]), int16(regs[1])}
	if i16s[0] != -1 || i16s[1] != -32768 {
		t.Errorf("int16(registers): expected [-1, -32768], got %v", i16s)
	}
}

// TestReadInt32 tests ReadWithCodec for int32 (layout 4321 = BigEndian HighWordFirst).
func TestReadInt32(t *testing.T) {
	h := &typedReadHandler{}
	h.holding[0] = 0xFFFF
	h.holding[1] = 0xFFFF
	h.holding[2] = 0x0000
	h.holding[3] = 0x0001

	server, client := startTypedReadServer(t, h, "tcp://localhost:5508")
	defer func() { _ = client.Close(); _ = server.Stop() }()

	ctx := context.Background()
	enc := codec.MustNewInt32Codec(codec.Layout32_4321)

	i32, err := codec.ReadFromClient(client, ctx, 1, 0x0000, HoldingRegister, enc)
	if err != nil {
		t.Fatalf("ReadWithCodec int32: %v", err)
	}
	if i32 != -1 {
		t.Errorf("ReadWithCodec int32: expected -1, got %v", i32)
	}

	regs, err := client.ReadRegisters(ctx, 1, 0x0000, 4, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	var i32s []int32
	for i := 0; i < 2; i++ {
		v, decErr := codec.DecodeRegisters(regs[i*2:(i+1)*2], enc)
		if decErr != nil {
			t.Fatalf("DecodeRegisters: %v", decErr)
		}
		i32s = append(i32s, v)
	}
	if len(i32s) != 2 || i32s[0] != -1 || i32s[1] != 1 {
		t.Errorf("DecodeRegisters int32: expected [-1, 1], got %v", i32s)
	}
}

// TestReadInt64 tests ReadWithCodec for int64 (layout 87654321).
func TestReadInt64(t *testing.T) {
	h := &typedReadHandler{}
	// holding[0..3] = 0xFFFFFFFFFFFFFFFF → -1
	h.holding[0] = 0xFFFF
	h.holding[1] = 0xFFFF
	h.holding[2] = 0xFFFF
	h.holding[3] = 0xFFFF
	// holding[4..7] = 0x0000000000000001 → 1
	h.holding[4] = 0x0000
	h.holding[5] = 0x0000
	h.holding[6] = 0x0000
	h.holding[7] = 0x0001

	server, client := startTypedReadServer(t, h, "tcp://localhost:5510")
	defer func() { _ = client.Close(); _ = server.Stop() }()

	ctx := context.Background()
	enc := codec.MustNewInt64Codec(codec.Layout64_87654321)

	i64, err := codec.ReadFromClient(client, ctx, 1, 0x0000, HoldingRegister, enc)
	if err != nil {
		t.Fatalf("ReadWithCodec int64: %v", err)
	}
	if i64 != -1 {
		t.Errorf("ReadWithCodec int64: expected -1, got %v", i64)
	}

	regs, err := client.ReadRegisters(ctx, 1, 0x0000, 8, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	var i64s []int64
	for i := 0; i < 2; i++ {
		v, decErr := codec.DecodeRegisters(regs[i*4:(i+1)*4], enc)
		if decErr != nil {
			t.Fatalf("DecodeRegisters: %v", decErr)
		}
		i64s = append(i64s, v)
	}
	if len(i64s) != 2 || i64s[0] != -1 || i64s[1] != 1 {
		t.Errorf("DecodeRegisters int64: expected [-1, 1], got %v", i64s)
	}
}

// TestReadUint48 tests ReadWithCodec for uint48 (layout 654321).
func TestReadUint48(t *testing.T) {
	h := &typedReadHandler{}
	// First value: holding[0..2] = W0=0x0001 (MSW), W1=0x0002, W2=0x0003 → 0x000100020003.
	h.holding[0] = 0x0001
	h.holding[1] = 0x0002
	h.holding[2] = 0x0003
	// Second value: holding[3..5] = 0x000400050006.
	h.holding[3] = 0x0004
	h.holding[4] = 0x0005
	h.holding[5] = 0x0006

	server, client := startTypedReadServer(t, h, "tcp://localhost:5512")
	defer func() { _ = client.Close(); _ = server.Stop() }()

	ctx := context.Background()
	enc := codec.MustNewUint48Codec(codec.Layout48_654321)

	u48, err := codec.ReadFromClient(client, ctx, 1, 0x0000, HoldingRegister, enc)
	if err != nil {
		t.Fatalf("ReadWithCodec uint48: %v", err)
	}
	if u48 != 0x000100020003 {
		t.Errorf("ReadWithCodec uint48: expected 0x000100020003, got 0x%012x", u48)
	}

	regs, err := client.ReadRegisters(ctx, 1, 0x0000, 6, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	var u48s []uint64
	for i := 0; i < 2; i++ {
		v, decErr := codec.DecodeRegisters(regs[i*3:(i+1)*3], enc)
		if decErr != nil {
			t.Fatalf("DecodeRegisters: %v", decErr)
		}
		u48s = append(u48s, v)
	}
	if len(u48s) != 2 || u48s[0] != 0x000100020003 || u48s[1] != 0x000400050006 {
		t.Errorf("DecodeRegisters uint48: got [0x%012x, 0x%012x]", u48s[0], u48s[1])
	}
}

// TestReadInt48 tests ReadWithCodec for int48 (layout 654321).
func TestReadInt48(t *testing.T) {
	h := &typedReadHandler{}
	// First value: all 0xFFFF words → 0xFFFFFFFFFFFF → -1.
	h.holding[0] = 0xFFFF
	h.holding[1] = 0xFFFF
	h.holding[2] = 0xFFFF
	// Second value: 0x800000000000 → minimum signed 48-bit.
	h.holding[3] = 0x8000
	h.holding[4] = 0x0000
	h.holding[5] = 0x0000

	server, client := startTypedReadServer(t, h, "tcp://localhost:5514")
	defer func() { _ = client.Close(); _ = server.Stop() }()

	ctx := context.Background()
	enc := codec.MustNewInt48Codec(codec.Layout48_654321)

	i48, err := codec.ReadFromClient(client, ctx, 1, 0x0000, HoldingRegister, enc)
	if err != nil {
		t.Fatalf("ReadWithCodec int48: %v", err)
	}
	if i48 != -1 {
		t.Errorf("ReadWithCodec int48: expected -1, got %v", i48)
	}

	regs, err := client.ReadRegisters(ctx, 1, 0x0000, 6, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	var i48s []int64
	for i := 0; i < 2; i++ {
		v, decErr := codec.DecodeRegisters(regs[i*3:(i+1)*3], enc)
		if decErr != nil {
			t.Fatalf("DecodeRegisters: %v", decErr)
		}
		i48s = append(i48s, v)
	}
	const minInt48 = -140737488355328
	if len(i48s) != 2 || i48s[0] != -1 || i48s[1] != minInt48 {
		t.Errorf("DecodeRegisters int48: expected [-1, %v], got %v", minInt48, i48s)
	}
}

// TestReadAscii tests ReadWithCodec for ASCII (high byte first, trim spaces).
// "Hello " stored as [0x4865, 0x6C6C, 0x6F20] → "Hello" (trailing space stripped).
func TestReadAscii(t *testing.T) {
	h := &typedReadHandler{}
	h.holding[0] = 0x4865 // 'H','e'
	h.holding[1] = 0x6C6C // 'l','l'
	h.holding[2] = 0x6F20 // 'o',' '

	server, client := startTypedReadServer(t, h, "tcp://localhost:5516")
	defer func() { _ = client.Close(); _ = server.Stop() }()

	enc, err := codec.NewAsciiCodec(3)
	if err != nil {
		t.Fatalf("NewAsciiCodec: %v", err)
	}
	s, err := codec.ReadFromClient(client, context.Background(), 1, 0x0000, HoldingRegister, enc)
	if err != nil {
		t.Fatalf("ReadWithCodec Ascii: %v", err)
	}
	if s != "Hello" {
		t.Errorf("ReadWithCodec Ascii: expected \"Hello\", got %q", s)
	}
}

// TestReadAsciiReverse tests ReadWithCodec for AsciiReverse (low byte first).
// "Hello " stored reversed as [0x6548, 0x6C6C, 0x206F] → "Hello" (trailing space stripped).
func TestReadAsciiReverse(t *testing.T) {
	h := &typedReadHandler{}
	h.holding[0] = 0x6548 // 'e','H' → reversed → 'H','e'
	h.holding[1] = 0x6C6C // 'l','l' → reversed → 'l','l'
	h.holding[2] = 0x206F // ' ','o' → reversed → 'o',' '

	server, client := startTypedReadServer(t, h, "tcp://localhost:5518")
	defer func() { _ = client.Close(); _ = server.Stop() }()

	enc, err := codec.NewAsciiReverseCodec(3)
	if err != nil {
		t.Fatalf("NewAsciiReverseCodec: %v", err)
	}
	s, err := codec.ReadFromClient(client, context.Background(), 1, 0x0000, HoldingRegister, enc)
	if err != nil {
		t.Fatalf("ReadWithCodec AsciiReverse: %v", err)
	}
	if s != "Hello" {
		t.Errorf("ReadWithCodec AsciiReverse: expected \"Hello\", got %q", s)
	}
}

// TestReadBCD tests ReadWithCodec for BCD (one digit per byte).
// Registers [0x0102, 0x0304] → bytes [0x01,0x02,0x03,0x04] → "1234".
func TestReadBCD(t *testing.T) {
	h := &typedReadHandler{}
	h.holding[0] = 0x0102 // bytes: 0x01=digit 1, 0x02=digit 2
	h.holding[1] = 0x0304 // bytes: 0x03=digit 3, 0x04=digit 4

	server, client := startTypedReadServer(t, h, "tcp://localhost:5520")
	defer func() { _ = client.Close(); _ = server.Stop() }()

	enc, err := codec.NewBCDCodec(2)
	if err != nil {
		t.Fatalf("NewBCDCodec: %v", err)
	}
	s, err := codec.ReadFromClient(client, context.Background(), 1, 0x0000, HoldingRegister, enc)
	if err != nil {
		t.Fatalf("ReadWithCodec BCD: %v", err)
	}
	if s != "1234" {
		t.Errorf("ReadWithCodec BCD: expected \"1234\", got %q", s)
	}
}

// TestReadPackedBCD tests ReadWithCodec for PackedBCD (two nibbles per byte).
func TestReadPackedBCD(t *testing.T) {
	h := &typedReadHandler{}
	h.holding[0] = 0x1234
	h.holding[1] = 0x5678

	server, client := startTypedReadServer(t, h, "tcp://localhost:5522")
	defer func() { _ = client.Close(); _ = server.Stop() }()

	enc, err := codec.NewPackedBCDCodec(2)
	if err != nil {
		t.Fatalf("NewPackedBCDCodec: %v", err)
	}
	s, err := codec.ReadFromClient(client, context.Background(), 1, 0x0000, HoldingRegister, enc)
	if err != nil {
		t.Fatalf("ReadWithCodec PackedBCD: %v", err)
	}
	if s != "12345678" {
		t.Errorf("ReadWithCodec PackedBCD: expected \"12345678\", got %q", s)
	}
}

func TestReadWriteMultipleRegisters_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadWriteMultipleRegs) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			// FC23 response: 1 byte count + readQty*2 bytes. For readQty=2: [4, v1hi, v1lo, v2hi, v2lo]
			payload := []byte{0x04, 0x00, 0x01, 0x00, 0x02}
			_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
		}
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	vals, err := client.ReadWriteMultipleRegisters(context.Background(), 1, 0, 2, 10, []uint16{0x1234})
	if err != nil {
		t.Fatalf("ReadWriteMultipleRegisters: %v", err)
	}
	if len(vals) != 2 || vals[0] != 1 || vals[1] != 2 {
		t.Errorf("got %v, want [1, 2]", vals)
	}
}

func TestReadWriteMultipleRegisters_Exception(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
		}
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadWriteMultipleRegisters(context.Background(), 1, 0, 2, 10, []uint16{0x1234})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrIllegalDataAddress) {
		t.Errorf("want ErrIllegalDataAddress, got %v", err)
	}
}

func TestReadWriteMultipleRegisters_InvalidParams(t *testing.T) {
	client, err := New(Config{URL: "tcp://127.0.0.1:1", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_ = client.Open()
	defer func() { _ = client.Close() }()
	ctx := context.Background()
	if _, err := client.ReadWriteMultipleRegisters(ctx, 1, 0, 0, 0, []uint16{1}); err == nil {
		t.Error("readQty 0 should error")
	}
	if _, err := client.ReadWriteMultipleRegisters(ctx, 1, 0, 126, 0, []uint16{1}); err == nil {
		t.Error("readQty 126 should error")
	}
	if _, err := client.ReadWriteMultipleRegisters(ctx, 1, 0, 1, 0, nil); err == nil {
		t.Error("writeQty 0 should error")
	}
}

func TestReadFIFOQueue_Exception(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadFIFOQueue(context.Background(), 1, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrIllegalDataAddress) {
		t.Errorf("want ErrIllegalDataAddress, got %v", err)
	}
}

func TestReadFIFOQueue_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadFIFOQueue) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			// FC24 response: 2 bytes byte count (6 = 2 + fifoCount*2) + 2 bytes fifo count (2) + 4 bytes data
			payload := []byte{0x00, 0x06, 0x00, 0x02, 0x00, 0x01, 0x00, 0x02}
			_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
		}
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	vals, err := client.ReadFIFOQueue(context.Background(), 1, 0)
	if err != nil {
		t.Fatalf("ReadFIFOQueue: %v", err)
	}
	if len(vals) != 2 || vals[0] != 1 || vals[1] != 2 {
		t.Errorf("got %v, want [1, 2]", vals)
	}
}

type testMetrics struct {
	onRequest, onResponse, onError, onTimeout int
}

func (m *testMetrics) OnRequest(unitID uint8, fc FunctionCode) { m.onRequest++ }
func (m *testMetrics) OnResponse(unitID uint8, fc FunctionCode, d time.Duration) {
	m.onResponse++
}
func (m *testMetrics) OnError(unitID uint8, fc FunctionCode, d time.Duration, err error) {
	m.onError++
}
func (m *testMetrics) OnTimeout(unitID uint8, fc FunctionCode, d time.Duration) {
	m.onTimeout++
}

func TestClientMetrics_Callbacks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		_ = writeMBAPNormal(sock, txid, unitID, fc, []byte{0x02, 0x00, 0x01})
	}()
	metrics := &testMetrics{}
	client, err := New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
		Metrics: metrics,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegister: %v", err)
	}
	if metrics.onRequest != 1 || metrics.onResponse != 1 {
		t.Errorf("metrics: onRequest=%d onResponse=%d", metrics.onRequest, metrics.onResponse)
	}
	// Trigger OnError: server accepts then closes, so first request fails
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		sock, _ := ln2.Accept()
		if sock != nil {
			_ = sock.Close()
		}
	}()
	client2, _ := New(Config{
		URL:         "tcp://" + ln2.Addr().String(),
		Timeout:     2 * time.Second,
		Metrics:     metrics,
		RetryPolicy: NoRetry(),
	})
	if err := client2.Open(); err != nil {
		t.Fatalf("Open client2: %v", err)
	}
	defer func() { _ = client2.Close(); _ = ln2.Close() }()
	_, _ = client2.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	if metrics.onError != 1 {
		t.Errorf("expected OnError call, onError=%d", metrics.onError)
	}
}

func TestNewClient_TransportTypes(t *testing.T) {
	// Cover NewClient switch branches for rtu, rtuovertcp, rtuoverudp, udp (Open will fail but client is created).
	for _, url := range []string{"rtu:///dev/ttyUSB0", "rtuovertcp://127.0.0.1:502", "rtuoverudp://127.0.0.1:502", "udp://127.0.0.1:502"} {
		mc, err := New(Config{URL: url, Timeout: time.Second})
		if err != nil {
			t.Errorf("New(%q): %v", url, err)
			continue
		}
		if mc == nil {
			t.Errorf("New(%q): nil client", url)
		}
	}
	// URL without :// triggers "missing client type"
	_, err := New(Config{URL: "no-scheme", Timeout: time.Second})
	if err == nil {
		t.Error("expected error for URL without scheme")
	}
	if !errors.Is(err, ErrConfigurationError) {
		t.Errorf("want ErrConfigurationError, got %v", err)
	}
}

func TestLastObservedTransactionID(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		payload := []byte{0x02, 0x00, 0x01}
		_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegister: %v", err)
	}
	id := client.LastObservedTransactionID()
	if id == 0 {
		t.Error("LastObservedTransactionID should be non-zero after successful TCP request")
	}
}

func TestReadDiscreteInput(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadDiscreteInputs) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			// one coil: byte count 1, one byte 0x01
			_ = writeMBAPNormal(sock, txid, unitID, fc, []byte{0x01, 0x01})
		}
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	val, err := client.ReadDiscreteInput(context.Background(), 1, 0)
	if err != nil {
		t.Fatalf("ReadDiscreteInput: %v", err)
	}
	if !val {
		t.Error("expected true (0x01)")
	}
}

func TestWriteCoilRaw(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCWriteSingleCoil) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			// echo request addr + value
			_ = writeMBAPNormal(sock, txid, unitID, fc, frame[8:12])
		}
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	err = client.WriteCoilRaw(context.Background(), 1, 0, 0xFF00) // 0xFF00 = on
	if err != nil {
		t.Fatalf("WriteCoilValue: %v", err)
	}
}

func TestClientPool_TwoConns(t *testing.T) {
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
				frame, _ := readMBAPFrame(c)
				txid, unitID, fc := frame[0:2], frame[6], frame[7]
				_ = writeMBAPNormal(c, txid, unitID, fc, []byte{0x02, 0x00, 0x01})
			}(sock)
		}
	}()
	client, err := New(Config{
		URL:      "tcp://" + ln.Addr().String(),
		Timeout:  2 * time.Second,
		MaxConns: 2,
		MinConns: 0,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegister: %v", err)
	}
}

func TestClientPool_CloseDrainsPool(t *testing.T) {
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
				frame, _ := readMBAPFrame(c)
				txid, unitID, fc := frame[0:2], frame[6], frame[7]
				_ = writeMBAPNormal(c, txid, unitID, fc, []byte{0x02, 0x00, 0x01})
			}(sock)
		}
	}()
	client, err := New(Config{
		URL:      "tcp://" + ln.Addr().String(),
		Timeout:  2 * time.Second,
		MaxConns: 2,
		MinConns: 1,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, _ = client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	err = client.Close()
	if err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestClientPool_PreWarm(t *testing.T) {
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
				frame, _ := readMBAPFrame(c)
				txid, unitID, fc := frame[0:2], frame[6], frame[7]
				_ = writeMBAPNormal(c, txid, unitID, fc, []byte{0x02, 0x00, 0x01})
			}(sock)
		}
	}()
	client, err := New(Config{
		URL:      "tcp://" + ln.Addr().String(),
		Timeout:  2 * time.Second,
		MaxConns: 2,
		MinConns: 1,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegister: %v", err)
	}
}

func TestClientRetry_ContextCanceledDuringDelay(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock != nil {
			_ = sock.Close()
		}
	}()
	client, err := New(Config{
		URL:         "tcp://" + ln.Addr().String(),
		Timeout:     2 * time.Second,
		RetryPolicy: ExponentialBackoff(50*time.Millisecond, time.Second, 3),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so retry delay sees it
	_, err = client.ReadRegister(ctx, 1, 0, HoldingRegister)
	if err == nil {
		t.Fatal("expected error after context canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestClientRetry_SuccessOnSecondAttempt(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	first := true
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			if first {
				first = false
				_ = sock.Close()
				continue
			}
			defer func() { _ = sock.Close() }()
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			payload := []byte{0x02, 0x00, 0x01}
			_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
		}
	}()
	client, err := New(Config{
		URL:         "tcp://" + ln.Addr().String(),
		Timeout:     2 * time.Second,
		RetryPolicy: ExponentialBackoff(5*time.Millisecond, 100*time.Millisecond, 2),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegister after retry: %v", err)
	}
}

// TestOpen_SingleTransport_OneRequestSucceeds verifies that with MaxConns 0/1 only a single
// transport is used and one request succeeds (no pool).
func TestOpen_SingleTransport_OneRequestSucceeds(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		_ = writeMBAPNormal(sock, txid, unitID, fc, []byte{0x02, 0x00, 0x01})
	}()
	client, err := New(Config{
		URL:      "tcp://" + ln.Addr().String(),
		Timeout:  2 * time.Second,
		MaxConns: 0,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	v, err := client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegister: %v", err)
	}
	if v != 1 {
		t.Errorf("ReadRegister = %v, want 1", v)
	}
}

// TestPool_ConcurrentRequests_TwoGoroutines verifies that with a pool two goroutines can
// run requests concurrently and both get correct responses (no cross-talk).
func TestPool_ConcurrentRequests_TwoGoroutines(t *testing.T) {
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
				frame, err := readMBAPFrame(c)
				if err != nil {
					return
				}
				txid, unitID, fc := frame[0:2], frame[6], frame[7]
				// Return register value 0x1111 for addr 0, 0x2222 for addr 2
				addr := int(frame[8])<<8 | int(frame[9])
				var payload []byte
				if addr == 0 {
					payload = []byte{0x02, 0x11, 0x11}
				} else {
					payload = []byte{0x02, 0x22, 0x22}
				}
				_ = writeMBAPNormal(c, txid, unitID, fc, payload)
			}(sock)
		}
	}()
	client, err := New(Config{
		URL:      "tcp://" + ln.Addr().String(),
		Timeout:  2 * time.Second,
		MaxConns: 2,
		MinConns: 0,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	var wg sync.WaitGroup
	var v0, v2 uint16
	var err0, err2 error
	wg.Add(2)
	go func() {
		defer wg.Done()
		v0, err0 = client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	}()
	go func() {
		defer wg.Done()
		v2, err2 = client.ReadRegister(context.Background(), 1, 2, HoldingRegister)
	}()
	wg.Wait()
	if err0 != nil {
		t.Errorf("goroutine 0: %v", err0)
	} else if v0 != 0x1111 {
		t.Errorf("goroutine 0 value = 0x%x, want 0x1111", v0)
	}
	if err2 != nil {
		t.Errorf("goroutine 1: %v", err2)
	} else if v2 != 0x2222 {
		t.Errorf("goroutine 1 value = 0x%x, want 0x2222", v2)
	}
}

// TestRetry_PoolMode_SuccessAfterDiscard verifies that in pool mode a failed connection
// is discarded and a retry uses a new connection from the pool.
func TestRetry_PoolMode_SuccessAfterDiscard(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	first := true
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			if first {
				first = false
				_ = sock.Close()
				continue
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				frame, _ := readMBAPFrame(c)
				txid, unitID, fc := frame[0:2], frame[6], frame[7]
				_ = writeMBAPNormal(c, txid, unitID, fc, []byte{0x02, 0xAB, 0xCD})
			}(sock)
		}
	}()
	client, err := New(Config{
		URL:         "tcp://" + ln.Addr().String(),
		Timeout:     2 * time.Second,
		MaxConns:    2,
		MinConns:    0,
		RetryPolicy: ExponentialBackoff(5*time.Millisecond, 100*time.Millisecond, 2),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	v, err := client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegister after retry: %v", err)
	}
	if v != 0xABCD {
		t.Errorf("ReadRegister = 0x%x, want 0xABCD", v)
	}
}

// TestClose_WhileGoroutinesActive closes the client while goroutines are still issuing
// requests; they should either succeed or get an error without panic.
func TestClose_WhileGoroutinesActive(t *testing.T) {
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
				frame, _ := readMBAPFrame(c)
				txid, unitID, fc := frame[0:2], frame[6], frame[7]
				_ = writeMBAPNormal(c, txid, unitID, fc, []byte{0x02, 0x00, 0x01})
			}(sock)
		}
	}()
	client, err := New(Config{
		URL:      "tcp://" + ln.Addr().String(),
		Timeout:  5 * time.Second,
		MaxConns: 2,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(3)
	for i := 0; i < 3; i++ {
		go func() {
			defer wg.Done()
			_, _ = client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
		}()
	}
	_ = client.Close()
	wg.Wait()
}

func TestDiagnostics_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		if fc != byte(FCDiagnostics) {
			_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
			return
		}
		// FC08 response: 2 bytes subfunction echo + optional data
		payload := []byte{0x00, 0x00, 0x12, 0x34}
		_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	dr, err := client.Diagnostics(context.Background(), 1, DiagReturnQueryData, nil)
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if dr.SubFunction != DiagReturnQueryData {
		t.Errorf("SubFunction = %v", dr.SubFunction)
	}
	if len(dr.Data) != 2 || dr.Data[0] != 0x12 || dr.Data[1] != 0x34 {
		t.Errorf("Data = %v", dr.Data)
	}
}

func TestDiagnostics_Exception(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataValue))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.Diagnostics(context.Background(), 1, DiagReturnQueryData, nil)
	if err == nil {
		t.Fatal("expected exception error")
	}
	if !errors.Is(err, ErrIllegalDataValue) {
		t.Errorf("want ErrIllegalDataValue, got %v", err)
	}
}

func TestReportServerID_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		if fc != byte(FCReportServerID) {
			_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
			return
		}
		// FC11 response: 1 byte count + data
		payload := []byte{0x03, 0x01, 0xFF, 0x00}
		_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	rs, err := client.ReportServerID(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReportServerID: %v", err)
	}
	if len(rs.Data) != 3 {
		t.Errorf("Data length = %d, want 3", len(rs.Data))
	}
	if len(rs.Data) < 3 || rs.Data[0] != 0x01 || rs.Data[1] != 0xFF {
		t.Errorf("Data = %v", rs.Data)
	}
}

func TestReportServerID_Exception(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		_ = writeMBAPException(sock, txid, unitID, fc, byte(exServerDeviceBusy))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReportServerID(context.Background(), 1)
	if err == nil {
		t.Fatal("expected exception error")
	}
	if !errors.Is(err, ErrServerDeviceBusy) {
		t.Errorf("want ErrServerDeviceBusy, got %v", err)
	}
}

func TestClientMetrics_OnTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock != nil {
			// Never respond so client times out (do not close, just block)
			_, _ = sock.Read(make([]byte, 256))
		}
	}()
	metrics := &testMetrics{}
	client, err := New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 10 * time.Millisecond,
		Metrics: metrics,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, _ = client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	if metrics.onTimeout != 1 {
		t.Errorf("expected OnTimeout call, onTimeout=%d", metrics.onTimeout)
	}
}

func TestReadFileRecords_Exception(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataValue))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadFileRecords(context.Background(), 1, []FileRecordRequest{
		{FileNumber: 1, RecordNumber: 0, RecordLength: 1},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrIllegalDataValue) {
		t.Errorf("want ErrIllegalDataValue, got %v", err)
	}
}

func TestReadFileRecords_InvalidParams(t *testing.T) {
	client, _ := New(Config{URL: "tcp://127.0.0.1:1", Timeout: time.Second})
	_ = client.Open()
	defer func() { _ = client.Close() }()
	ctx := context.Background()

	_, err := client.ReadFileRecords(ctx, 1, nil)
	if err == nil {
		t.Fatal("nil requests should error")
	}
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("nil: got %v", err)
	}

	_, err = client.ReadFileRecords(ctx, 1, []FileRecordRequest{{FileNumber: 0, RecordNumber: 0, RecordLength: 1}})
	if err == nil {
		t.Error("FileNumber 0 should error")
	}
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("FileNumber 0: got %v", err)
	}

	_, err = client.ReadFileRecords(ctx, 1, []FileRecordRequest{{FileNumber: 1, RecordNumber: 0x2710, RecordLength: 1}})
	if err == nil {
		t.Error("RecordNumber > 0x270F should error")
	}

	_, err = client.ReadFileRecords(ctx, 1, []FileRecordRequest{{FileNumber: 1, RecordNumber: 0, RecordLength: 0}})
	if err == nil {
		t.Error("RecordLength 0 should error")
	}

	// maxFileByteCount is 0xF5, so 36 requests * 7 = 252 > 0xF5
	reqs := make([]FileRecordRequest, 36)
	for i := range reqs {
		reqs[i] = FileRecordRequest{FileNumber: 1, RecordNumber: 0, RecordLength: 1}
	}
	_, err = client.ReadFileRecords(ctx, 1, reqs)
	if err == nil {
		t.Error("too many sub-requests should error")
	}
}

func TestReadFileRecords_ProtocolError_TruncatedPayload(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		// respDataLen=4 but only 2 bytes payload -> len != 1+respDataLen
		payload := []byte{0x04, 0x03, 0x06, 0x00}
		_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadFileRecords(context.Background(), 1, []FileRecordRequest{
		{FileNumber: 1, RecordNumber: 0, RecordLength: 1},
	})
	if err == nil {
		t.Fatal("expected protocol error")
	}
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("want ErrProtocolError, got %v", err)
	}
}

func TestReadFileRecords_ProtocolError_WrongRefType(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		// refType 0x05 instead of 0x06
		payload := []byte{0x04, 0x03, 0x05, 0x00, 0x01}
		_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadFileRecords(context.Background(), 1, []FileRecordRequest{
		{FileNumber: 1, RecordNumber: 0, RecordLength: 1},
	})
	if err == nil {
		t.Fatal("expected protocol error")
	}
}

func TestReadFileRecords_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		if fc != byte(FCReadFileRecord) {
			_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
			return
		}
		// FC20 response: respDataLen=4, sub-response: fileRespLen=3 (refType+2 bytes), refType=6, data=0x0001
		payload := []byte{0x04, 0x03, 0x06, 0x00, 0x01}
		_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	recs, err := client.ReadFileRecords(context.Background(), 1, []FileRecordRequest{
		{FileNumber: 1, RecordNumber: 0, RecordLength: 1},
	})
	if err != nil {
		t.Fatalf("ReadFileRecords: %v", err)
	}
	if len(recs) != 1 || len(recs[0]) != 1 || recs[0][0] != 0x0001 {
		t.Errorf("records = %v", recs)
	}
}

// TestReadFileRecords_RequestContainsRefType06 verifies that the FC20 request includes
// reference type 0x06 for each sub-request per the protocol (7 bytes per sub-request).
func TestReadFileRecords_RequestContainsRefType06(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	var receivedPayload []byte
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		if fc != byte(FCReadFileRecord) {
			_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
			return
		}
		// Capture request payload: byteCount(1) + for each sub-request: refType(1) + file(2) + rec(2) + len(2) = 7
		receivedPayload = make([]byte, len(frame[8:]))
		copy(receivedPayload, frame[8:])
		// Respond with one sub-response: refType 6, one register 0x0001
		payload := []byte{0x04, 0x03, 0x06, 0x00, 0x01}
		_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ReadFileRecords(context.Background(), 1, []FileRecordRequest{
		{FileNumber: 1, RecordNumber: 0, RecordLength: 1},
	})
	if err != nil {
		t.Fatalf("ReadFileRecords: %v", err)
	}
	// Request must be: byteCount=7, refType=0x06, file=0x0001, rec=0x0000, len=0x0001
	if len(receivedPayload) < 8 {
		t.Fatalf("request payload too short: %d", len(receivedPayload))
	}
	if receivedPayload[0] != 7 {
		t.Errorf("byte count want 7, got %d", receivedPayload[0])
	}
	if receivedPayload[1] != 0x06 {
		t.Errorf("reference type want 0x06, got 0x%02x", receivedPayload[1])
	}
}

func TestWriteFileRecords_Exception(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	err = client.WriteFileRecords(context.Background(), 1, []FileRecord{
		{FileNumber: 1, RecordNumber: 0, Data: []uint16{0x1234}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrIllegalDataAddress) {
		t.Errorf("want ErrIllegalDataAddress, got %v", err)
	}
}

func TestWriteFileRecords_ProtocolError_EchoMismatch(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		// Echo but corrupt one byte so response doesn't match request
		payload := make([]byte, len(frame[8:]))
		copy(payload, frame[8:])
		if len(payload) > 9 {
			payload[9] ^= 0xFF
		}
		_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	err = client.WriteFileRecords(context.Background(), 1, []FileRecord{
		{FileNumber: 1, RecordNumber: 0, Data: []uint16{0x1234}},
	})
	if err == nil {
		t.Fatal("expected protocol error")
	}
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("want ErrProtocolError, got %v", err)
	}
}

func TestWriteFileRecords_Success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		frame, _ := readMBAPFrame(sock)
		txid, unitID, fc := frame[0:2], frame[6], frame[7]
		if fc != byte(FCWriteFileRecord) {
			_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
			return
		}
		// FC21 response is echo of request payload (PDU after unit id and FC)
		payload := frame[8:]
		_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
	}()
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	err = client.WriteFileRecords(context.Background(), 1, []FileRecord{
		{FileNumber: 1, RecordNumber: 0, Data: []uint16{0x1234}},
	})
	if err != nil {
		t.Fatalf("WriteFileRecords: %v", err)
	}
}

func TestWriteFileRecords_InvalidParams(t *testing.T) {
	client, _ := New(Config{URL: "tcp://127.0.0.1:1", Timeout: time.Second})
	_ = client.Open()
	defer func() { _ = client.Close() }()
	ctx := context.Background()

	err := client.WriteFileRecords(ctx, 1, nil)
	if err == nil {
		t.Fatal("nil records should error")
	}
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("nil: got %v", err)
	}
	err = client.WriteFileRecords(ctx, 1, []FileRecord{{FileNumber: 0, RecordNumber: 0, Data: []uint16{1}}})
	if err == nil {
		t.Error("FileNumber 0 should error")
	}
	err = client.WriteFileRecords(ctx, 1, []FileRecord{{FileNumber: 1, RecordNumber: 0x2710, Data: []uint16{1}}})
	if err == nil {
		t.Error("RecordNumber > 0x270F should error")
	}
	err = client.WriteFileRecords(ctx, 1, []FileRecord{{FileNumber: 1, RecordNumber: 0, Data: nil}})
	if err == nil {
		t.Error("empty Data should error")
	}
}

func TestDiagnosticSubFunction_String(t *testing.T) {
	if s := DiagReturnQueryData.String(); s != "ReturnQueryData" {
		t.Errorf("DiagReturnQueryData.String() = %q", s)
	}
	if s := DiagRestartCommunications.String(); s != "RestartCommunications" {
		t.Errorf("DiagRestartCommunications.String() = %q", s)
	}
	if s := DiagnosticSubFunction(0xFFFF).String(); s == "" {
		t.Error("unknown DiagnosticSubFunction should return non-empty string")
	}
}

func TestConcurrentClientRequests(t *testing.T) {
	handler := &typedReadHandler{}
	handler.holding[0] = 0x0042

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv, err := NewServer(&ServerConfig{URL: "tcp://" + ln.Addr().String(), MaxClients: 10}, handler)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	_ = ln.Close()
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Stop() }()

	client, err := New(Config{
		URL:     "tcp://" + srv.conf.URL,
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			val, readErr := client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
			if readErr != nil {
				errs <- readErr
				return
			}
			if val != 0x0042 {
				errs <- errors.New("unexpected value")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent request error: %v", e)
	}
}

func TestCloseDuringInFlightRequest(t *testing.T) {
	handler := &typedReadHandler{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()
	srv, err := NewServer(&ServerConfig{URL: "tcp://" + addr, MaxClients: 10}, handler)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	_ = ln.Close()
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Stop() }()

	client, err := New(Config{
		URL:     "tcp://" + addr,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = client.ReadRegister(context.Background(), 1, 0, HoldingRegister)
	}()

	time.Sleep(10 * time.Millisecond)
	_ = client.Close()
	wg.Wait()
}

func TestConcurrentOpenClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close()
		}
	}()

	client, err := New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = client.Open()
			_ = client.Close()
		}()
	}
	wg.Wait()
}
