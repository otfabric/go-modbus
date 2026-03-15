package modbus

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// sunSpecMarkerBytes is "SunS" as two big-endian registers: 0x5375, 0x6E53.
var sunSpecMarkerBytes = []byte{0x04, 0x53, 0x75, 0x6E, 0x53}

func writeMBAPRegisters(conn net.Conn, txid []byte, unitID, fc byte, regs []uint16) error {
	payload := []byte{byte(len(regs) * 2)}
	for _, r := range regs {
		payload = append(payload, byte(r>>8), byte(r))
	}
	return writeMBAPNormal(conn, txid, unitID, fc, payload)
}

// TestDetectSunSpec_FoundAtZero verifies detection when the server returns the SunS marker at base 0.
func TestDetectSunSpec_FoundAtZero(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if addr == 0 && qty == 2 {
				_ = writeMBAPNormal(sock, txid, unitID, fc, sunSpecMarkerBytes)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	res, err := client.DetectSunSpec(context.Background(), &SunSpecOptions{UnitID: 1})
	if err != nil {
		t.Fatalf("DetectSunSpec: %v", err)
	}
	if !res.Detected {
		t.Fatal("expected Detected true")
	}
	if res.BaseAddress != 0 {
		t.Errorf("BaseAddress = %d, want 0", res.BaseAddress)
	}
	if res.Marker[0] != 0x5375 || res.Marker[1] != 0x6E53 {
		t.Errorf("Marker = [0x%04X, 0x%04X], want [0x5375, 0x6E53]", res.Marker[0], res.Marker[1])
	}
}

// TestDetectSunSpec_AllProbesFail_ReturnsError verifies that when every probe fails (e.g. transport/exception),
// DetectSunSpec returns an error so "device not SunSpec" is not confused with "could not read at all".
func TestDetectSunSpec_AllProbesFail_ReturnsError(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			// Always return exception so no probe gets a successful read
			_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	res, err := client.DetectSunSpec(context.Background(), &SunSpecOptions{UnitID: 1, BaseAddresses: []uint16{0}})
	if err == nil {
		t.Fatal("DetectSunSpec expected error when all probes fail")
	}
	if res == nil || len(res.Attempts) == 0 {
		t.Error("expected partial result with attempts when all probes fail")
	}
	if res != nil && res.Detected {
		t.Error("expected Detected false when all probes fail")
	}
}

// TestDetectSunSpec_NotFound verifies that when no candidate has the marker, result is Detected false and error is nil.
func TestDetectSunSpec_NotFound(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			// Return non-SunSpec data for any read
			_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{0x0000, 0x0000})
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	res, err := client.DetectSunSpec(context.Background(), &SunSpecOptions{UnitID: 1})
	if err != nil {
		t.Fatalf("DetectSunSpec: %v", err)
	}
	if res.Detected {
		t.Fatal("expected Detected false when no marker present")
	}
	if len(res.Attempts) == 0 {
		t.Error("expected at least one attempt")
	}
}

// TestDetectSunSpec_NilOpts verifies that nil opts uses default base addresses and finds marker at 50000.
func TestDetectSunSpec_NilOpts(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if addr == 50000 && qty == 2 {
				_ = writeMBAPNormal(sock, txid, unitID, fc, sunSpecMarkerBytes)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	res, err := client.DetectSunSpec(context.Background(), nil)
	if err != nil {
		t.Fatalf("DetectSunSpec: %v", err)
	}
	if !res.Detected {
		t.Fatal("expected Detected true (marker at 50000)")
	}
	if res.BaseAddress != 50000 {
		t.Errorf("BaseAddress = %d, want 50000", res.BaseAddress)
	}
	// Default bases are 0, 40000, 50000, 1, 39999, 40001, 49999, 50001; marker at 50000 = 3rd probe.
	if len(res.Attempts) != 3 {
		t.Errorf("expected 3 attempts up to 50000, got %d", len(res.Attempts))
	}
}

// TestDetectSunSpec_ContextCanceled verifies error when context is canceled before/during probe.
// Uses a real listener so Open() succeeds; context is canceled before the first read returns.
func TestDetectSunSpec_ContextCanceled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before first probe

	_, err = client.DetectSunSpec(ctx, &SunSpecOptions{UnitID: 1})
	if err == nil {
		t.Fatal("expected error when context canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestReadSunSpecModelHeaders_SimpleChain verifies reading a short model chain ending with end model.
func TestReadSunSpecModelHeaders_SimpleChain(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if qty != 2 {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataValue))
				continue
			}
			switch addr {
			case 2:
				// First model: ID 1, length 2
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{1, 2})
			case 6:
				// End model: 0xFFFF, 0
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{0xFFFF, 0})
			default:
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	models, err := client.ReadSunSpecModelHeaders(context.Background(), &SunSpecOptions{UnitID: 1}, 0)
	if err != nil {
		t.Fatalf("ReadSunSpecModelHeaders: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != 1 || models[0].Length != 2 || models[0].StartAddress != 2 || models[0].NextAddress != 6 {
		t.Errorf("first model: ID=%d Length=%d Start=%d Next=%d", models[0].ID, models[0].Length, models[0].StartAddress, models[0].NextAddress)
	}
	if !models[1].IsEndModel || models[1].ID != 0xFFFF {
		t.Errorf("second model: expected end model, got ID=%d IsEndModel=%t", models[1].ID, models[1].IsEndModel)
	}
}

// TestDiscoverSunSpec_Full verifies Detect + ReadSunSpecModelHeaders in one call.
func TestDiscoverSunSpec_Full(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if qty != 2 {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataValue))
				continue
			}
			switch addr {
			case 0:
				_ = writeMBAPNormal(sock, txid, unitID, fc, sunSpecMarkerBytes)
			case 2:
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{1, 2})
			case 6:
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{0xFFFF, 0})
			default:
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	disc, err := client.DiscoverSunSpec(context.Background(), &SunSpecOptions{UnitID: 1})
	if err != nil {
		t.Fatalf("DiscoverSunSpec: %v", err)
	}
	if !disc.Detection.Detected {
		t.Fatal("expected Detection.Detected true")
	}
	if disc.Detection.BaseAddress != 0 {
		t.Errorf("BaseAddress = %d, want 0", disc.Detection.BaseAddress)
	}
	if len(disc.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(disc.Models))
	}
	if disc.Models[1].ID != 0xFFFF || !disc.Models[1].IsEndModel {
		t.Error("expected second model to be end model")
	}
}

// TestDiscoverSunSpec_PartialResults verifies that when model chain read fails partway,
// DiscoverSunSpec still returns partial model results (requirement: include partial where possible).
func TestDiscoverSunSpec_PartialResults(t *testing.T) {
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
		readCount := 0
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if qty != 2 {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataValue))
				continue
			}
			switch addr {
			case 0:
				_ = writeMBAPNormal(sock, txid, unitID, fc, sunSpecMarkerBytes)
			case 2:
				// First model: ID 1, length 2
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{1, 2})
			case 6:
				// Fail the second read (where end model would be) so we get partial + error
				readCount++
				if readCount == 1 {
					_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
				} else {
					_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{0xFFFF, 0})
				}
			default:
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	disc, err := client.DiscoverSunSpec(context.Background(), &SunSpecOptions{UnitID: 1})
	// Should have partial models (one model read before the failure at addr 6).
	if len(disc.Models) != 1 {
		t.Errorf("expected 1 partial model, got %d", len(disc.Models))
	}
	if disc.Models[0].ID != 1 || disc.Models[0].Length != 2 {
		t.Errorf("partial model: ID=%d Length=%d", disc.Models[0].ID, disc.Models[0].Length)
	}
	if err == nil {
		t.Error("expected error when second header read fails")
	}
	if !disc.Detection.Detected {
		t.Error("expected Detection.Detected true")
	}
}

// TestReadSunSpecModelHeaders_BaseAddressOverflow verifies ErrSunSpecModelChainInvalid when baseAddress+2 would wrap (e.g. base 65535).
func TestReadSunSpecModelHeaders_BaseAddressOverflow(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	client, err := NewClient(&ClientConfiguration{URL: "tcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	models, err := client.ReadSunSpecModelHeaders(context.Background(), &SunSpecOptions{UnitID: 1}, 65535)
	if err == nil {
		t.Fatal("expected ErrSunSpecModelChainInvalid for baseAddress+2 overflow")
	}
	if !errors.Is(err, ErrSunSpecModelChainInvalid) {
		t.Errorf("expected ErrSunSpecModelChainInvalid, got %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected 0 models, got %d", len(models))
	}
}

// TestReadSunSpecModelHeaders_LengthZeroNonEnd verifies ErrSunSpecModelChainInvalid when length is 0 but ID is not end model.
func TestReadSunSpecModelHeaders_LengthZeroNonEnd(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if qty != 2 {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataValue))
				continue
			}
			switch addr {
			case 2:
				// Malformed: ID 1, length 0 (not end model)
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{1, 0})
			default:
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	models, err := client.ReadSunSpecModelHeaders(context.Background(), &SunSpecOptions{UnitID: 1}, 0)
	if err == nil {
		t.Fatal("expected ErrSunSpecModelChainInvalid")
	}
	if !errors.Is(err, ErrSunSpecModelChainInvalid) {
		t.Errorf("expected ErrSunSpecModelChainInvalid, got %v", err)
	}
	if len(models) != 1 {
		t.Errorf("expected 1 partial model, got %d", len(models))
	}
}

// TestReadSunSpecModelHeaders_AddressOverflow verifies ErrProtocolError when model length would overflow address space.
func TestReadSunSpecModelHeaders_AddressOverflow(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Base 65532 makes the first header address 65534. A model length of 3 then causes end-exclusive address 65539, which must be rejected as overflow.
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if qty != 2 {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataValue))
				continue
			}
			// Base 65532: first header at 65534; model length 3 gives end-exclusive 65539, which overflows
			if addr == 65534 {
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{1, 3})
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	models, err := client.ReadSunSpecModelHeaders(context.Background(), &SunSpecOptions{UnitID: 1}, 65532)
	if err == nil {
		t.Fatal("expected overflow error")
	}
	if !errors.Is(err, ErrProtocolError) {
		t.Errorf("expected ErrProtocolError, got %v", err)
	}
	// Overflow is detected before appending the invalid model, so no partial model
	if len(models) != 0 {
		t.Errorf("expected 0 models on overflow, got %d", len(models))
	}
}

// TestReadSunSpecModelHeaders_MaxModelsLimit verifies walk stops at MaxModels and returns no error (normal truncation).
func TestReadSunSpecModelHeaders_MaxModelsLimit(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if qty != 2 {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataValue))
				continue
			}
			// Model 1 len 2 at 2, then model 2 len 2 at 6, then end at 10
			switch addr {
			case 2:
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{1, 2})
			case 6:
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{2, 2})
			case 10:
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{0xFFFF, 0})
			default:
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	opts := &SunSpecOptions{UnitID: 1, MaxModels: 2}
	models, err := client.ReadSunSpecModelHeaders(context.Background(), opts, 0)
	if err != nil {
		t.Fatalf("ReadSunSpecModelHeaders: %v", err)
	}
	if len(models) != 2 {
		t.Errorf("expected 2 models (MaxModels=2), got %d", len(models))
	}
	// Should have stopped at guard, not at end model
	if len(models) > 0 && models[len(models)-1].IsEndModel {
		t.Error("expected truncation at MaxModels, not end model")
	}
}

// TestReadSunSpecModelHeaders_MaxAddressSpanExceeded verifies ErrSunSpecModelChainLimitExceeded when span exceeds limit.
func TestReadSunSpecModelHeaders_MaxAddressSpanExceeded(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if qty != 2 {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataValue))
				continue
			}
			switch addr {
			case 2:
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{1, 2})
			case 6:
				_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{2, 100})
			default:
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Base 0, MaxAddressSpan 5: first model (addr 2, len 2) has nextAddr 6; span 6 > 5 so limit triggers after first model
	opts := &SunSpecOptions{UnitID: 1, MaxAddressSpan: 5}
	models, err := client.ReadSunSpecModelHeaders(context.Background(), opts, 0)
	if err == nil {
		t.Fatal("expected ErrSunSpecModelChainLimitExceeded")
	}
	if !errors.Is(err, ErrSunSpecModelChainLimitExceeded) {
		t.Errorf("expected ErrSunSpecModelChainLimitExceeded, got %v", err)
	}
	if len(models) != 1 {
		t.Errorf("expected 1 partial model, got %d", len(models))
	}
}

// TestDetectSunSpec_CustomBaseAddresses verifies custom BaseAddresses override.
func TestDetectSunSpec_CustomBaseAddresses(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if addr == 99 && qty == 2 {
				_ = writeMBAPNormal(sock, txid, unitID, fc, sunSpecMarkerBytes)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	res, err := client.DetectSunSpec(context.Background(), &SunSpecOptions{
		UnitID:        1,
		BaseAddresses: []uint16{99},
	})
	if err != nil {
		t.Fatalf("DetectSunSpec: %v", err)
	}
	if !res.Detected || res.BaseAddress != 99 {
		t.Errorf("expected Detected at base 99, got Detected=%v BaseAddress=%d", res.Detected, res.BaseAddress)
	}
}

// TestDetectSunSpec_FirstProbeExceptionThenMatch verifies that when first candidate returns exception, second can still match.
func TestDetectSunSpec_FirstProbeExceptionThenMatch(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if qty != 2 {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataValue))
				continue
			}
			switch addr {
			case 40000:
				_ = writeMBAPNormal(sock, txid, unitID, fc, sunSpecMarkerBytes)
			default:
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	res, err := client.DetectSunSpec(context.Background(), &SunSpecOptions{UnitID: 1})
	if err != nil {
		t.Fatalf("DetectSunSpec: %v", err)
	}
	if !res.Detected || res.BaseAddress != 40000 {
		t.Errorf("expected Detected at 40000 after exception at 0, got Detected=%v BaseAddress=%d", res.Detected, res.BaseAddress)
	}
	if len(res.Attempts) < 2 {
		t.Error("expected at least 2 attempts")
	}
	if res.Attempts[0].Error == nil {
		t.Error("expected first attempt to have error")
	}
}

// TestDetectSunSpec_InvalidOptions verifies ErrUnexpectedParameters for invalid options.
func TestDetectSunSpec_InvalidOptions(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	t.Run("EmptyBaseAddresses", func(t *testing.T) {
		_, err := client.DetectSunSpec(context.Background(), &SunSpecOptions{UnitID: 1, BaseAddresses: []uint16{}})
		if err == nil {
			t.Fatal("expected error for empty BaseAddresses")
		}
		if !errors.Is(err, ErrUnexpectedParameters) {
			t.Errorf("expected ErrUnexpectedParameters, got %v", err)
		}
	})
}

// TestReadSunSpecModelHeaders_InvalidOptions verifies ErrUnexpectedParameters for invalid options.
func TestReadSunSpecModelHeaders_InvalidOptions(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.ReadSunSpecModelHeaders(context.Background(), &SunSpecOptions{UnitID: 1, BaseAddresses: []uint16{}}, 0)
	if err == nil {
		t.Fatal("expected error for empty BaseAddresses")
	}
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("expected ErrUnexpectedParameters, got %v", err)
	}
}

// TestDiscoverSunSpec_InvalidOpts verifies that DiscoverSunSpec returns error when DetectSunSpec fails due to invalid options.
func TestDiscoverSunSpec_InvalidOpts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	disc, err := client.DiscoverSunSpec(context.Background(), &SunSpecOptions{UnitID: 1, BaseAddresses: []uint16{}})
	if err == nil {
		t.Fatal("expected error for empty BaseAddresses")
	}
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("expected ErrUnexpectedParameters, got %v", err)
	}
	if disc != nil {
		t.Error("expected nil result on error")
	}
}

// TestDiscoverSunSpec_NotDetected verifies that DiscoverSunSpec returns Detected=false with nil error when device is not SunSpec.
func TestDiscoverSunSpec_NotDetected(t *testing.T) {
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
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			// Return non-SunSpec data for all reads
			_ = writeMBAPRegisters(sock, txid, unitID, fc, []uint16{0x0000, 0x0000})
		}
	}()

	client, err := NewClient(&ClientConfiguration{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	disc, err := client.DiscoverSunSpec(context.Background(), &SunSpecOptions{UnitID: 1})
	if err != nil {
		t.Fatalf("DiscoverSunSpec: %v", err)
	}
	if disc.Detection.Detected {
		t.Error("expected Detection.Detected false")
	}
	if len(disc.Models) != 0 {
		t.Errorf("expected 0 models, got %d", len(disc.Models))
	}
}
