package modbus

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/otfabric/modbus/codec"
)

func TestValidateRuntimeDecodePlan_Valid(t *testing.T) {
	rc := codec.MustRuntimeCodecByID("uint32/layout:4321")
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0, Quantity: 4, RegType: HoldingRegister},
		Items: []RuntimeDecodeItem{
			{Name: "a", Offset: 0, Codec: rc},
			{Name: "b", Offset: 2, Codec: rc},
		},
	}
	if err := ValidateRuntimeDecodePlan(plan); err != nil {
		t.Fatalf("expected valid plan: %v", err)
	}
}

func TestValidateRuntimeDecodePlan_InvalidWindowQuantity(t *testing.T) {
	rc := codec.MustRuntimeCodecByID("uint32/layout:4321")
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0, Quantity: 0, RegType: HoldingRegister},
		Items:  []RuntimeDecodeItem{{Name: "x", Offset: 0, Codec: rc}},
	}
	err := ValidateRuntimeDecodePlan(plan)
	if err == nil {
		t.Fatal("expected validation error for quantity 0")
	}
	var e *RuntimePlanValidationError
	if !errors.As(err, &e) {
		t.Errorf("expected *RuntimePlanValidationError, got %T", err)
	}
	if e.Reason == "" {
		t.Error("Reason should be set")
	}
	if e.Error() == "" {
		t.Error("Error() should return non-empty string")
	}
}

func TestValidateRuntimeDecodePlan_ItemSpillsPastWindow(t *testing.T) {
	rc := codec.MustRuntimeCodecByID("uint32/layout:4321")
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0, Quantity: 2, RegType: HoldingRegister},
		Items:  []RuntimeDecodeItem{{Name: "x", Offset: 1, Codec: rc}},
	}
	err := ValidateRuntimeDecodePlan(plan)
	if err == nil {
		t.Fatal("expected validation error (offset 1 + count 2 > quantity 2)")
	}
	var e *RuntimePlanValidationError
	if !errors.As(err, &e) {
		t.Errorf("expected *RuntimePlanValidationError, got %T", err)
	}
	if e.ItemName != "x" {
		t.Errorf("ItemName = %q, want \"x\"", e.ItemName)
	}
	_ = e.Error()
}

func TestValidateRuntimeDecodePlan_DuplicateNames(t *testing.T) {
	rc := codec.MustRuntimeCodecByID("uint16/layout:21")
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0, Quantity: 2, RegType: HoldingRegister},
		Items: []RuntimeDecodeItem{
			{Name: "dup", Offset: 0, Codec: rc},
			{Name: "dup", Offset: 1, Codec: rc},
		},
	}
	err := ValidateRuntimeDecodePlan(plan)
	if err == nil {
		t.Fatal("expected validation error for duplicate names")
	}
	var e *RuntimePlanValidationError
	if !errors.As(err, &e) {
		t.Errorf("expected *RuntimePlanValidationError, got %T", err)
	}
	if e.ItemName != "dup" {
		t.Errorf("ItemName = %q, want \"dup\"", e.ItemName)
	}
	if e.Error() == "" {
		t.Error("Error() with ItemName should return non-empty string")
	}
}

func TestValidateRuntimeDecodePlan_NilCodec(t *testing.T) {
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0, Quantity: 1, RegType: HoldingRegister},
		Items:  []RuntimeDecodeItem{{Name: "x", Offset: 0, Codec: nil}},
	}
	err := ValidateRuntimeDecodePlan(plan)
	if err == nil {
		t.Fatal("expected validation error for nil codec")
	}
	var e *RuntimePlanValidationError
	if !errors.As(err, &e) {
		t.Errorf("expected *RuntimePlanValidationError, got %T", err)
	}
	if e.ItemName != "x" {
		t.Errorf("ItemName = %q, want \"x\"", e.ItemName)
	}
	_ = e.Error()
}

func TestValidateRuntimeDecodePlan_NoItems(t *testing.T) {
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0, Quantity: 4, RegType: HoldingRegister},
		Items:  nil,
	}
	err := ValidateRuntimeDecodePlan(plan)
	if err == nil {
		t.Fatal("expected validation error for no items")
	}
}

func TestExecuteRuntimeDecodePlanOffline_ValidatesPlan(t *testing.T) {
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0, Quantity: 2, RegType: HoldingRegister},
		Items:  nil,
	}
	_, err := ExecuteRuntimeDecodePlanOffline([]uint16{0, 0}, plan)
	if err == nil {
		t.Fatal("expected validation error for plan with no items")
	}
	var e *RuntimePlanValidationError
	if !errors.As(err, &e) {
		t.Errorf("expected *RuntimePlanValidationError, got %T", err)
	}
}

func TestExecuteRuntimeDecodePlanOffline_RegsWrongLength(t *testing.T) {
	rc := codec.MustRuntimeCodecByID("uint32/layout:4321")
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0, Quantity: 2, RegType: HoldingRegister},
		Items:  []RuntimeDecodeItem{{Name: "x", Offset: 0, Codec: rc}},
	}
	// Too short
	_, err := ExecuteRuntimeDecodePlanOffline([]uint16{0}, plan)
	if err == nil {
		t.Fatal("expected error when regs length != window quantity (too short)")
	}
	var e *RuntimePlanValidationError
	if !errors.As(err, &e) {
		t.Errorf("expected *RuntimePlanValidationError, got %T", err)
	}
	// Too long
	_, err = ExecuteRuntimeDecodePlanOffline([]uint16{0, 0, 0}, plan)
	if err == nil {
		t.Fatal("expected error when regs length != window quantity (too long)")
	}
	if !errors.As(err, &e) {
		t.Errorf("expected *RuntimePlanValidationError, got %T", err)
	}
}

func TestExecuteRuntimeDecodePlanOffline_Success(t *testing.T) {
	rc := codec.MustRuntimeCodecByID("uint32/layout:4321")
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 100, Quantity: 4, RegType: HoldingRegister},
		Items: []RuntimeDecodeItem{
			{Name: "lo", Offset: 0, Codec: rc},
			{Name: "hi", Offset: 2, Codec: rc},
		},
	}
	regs := []uint16{0x1234, 0x5678, 0xDEAD, 0xBEEF}
	result, err := ExecuteRuntimeDecodePlanOffline(regs, plan)
	if err != nil {
		t.Fatalf("ExecuteRuntimeDecodePlanOffline: %v", err)
	}
	if result.Addr != 100 || result.Quantity != 4 {
		t.Errorf("result Addr=%d Quantity=%d", result.Addr, result.Quantity)
	}
	if len(result.Values) != 2 {
		t.Fatalf("len(Values) = %d, want 2", len(result.Values))
	}
	if result.Values[0].Error != nil {
		t.Errorf("item 0 error: %v", result.Values[0].Error)
	}
	if v, ok := result.Values[0].Value.(uint32); !ok || v != 0x12345678 {
		t.Errorf("item 0 Value = %v (%T), want uint32(0x12345678)", result.Values[0].Value, result.Values[0].Value)
	}
	if result.Values[1].Error != nil {
		t.Errorf("item 1 error: %v", result.Values[1].Error)
	}
	if v, ok := result.Values[1].Value.(uint32); !ok || v != 0xDEADBEEF {
		t.Errorf("item 1 Value = %v (%T), want uint32(0xDEADBEEF)", result.Values[1].Value, result.Values[1].Value)
	}
}

// failingRuntimeDecoder is a test decoder that always returns an error from DecodeRegistersAny.
type failingRuntimeDecoder struct {
	spec codec.RegisterSpec
}

func (failingRuntimeDecoder) ID() string                         { return "test/fail" }
func (failingRuntimeDecoder) Name() string                       { return "fail" }
func (f failingRuntimeDecoder) RegisterSpec() codec.RegisterSpec { return f.spec }
func (failingRuntimeDecoder) ByteSpec() codec.ByteSpec           { return codec.ByteSpec{Count: 2} }
func (failingRuntimeDecoder) ValueKind() codec.CodecValueKind    { return codec.CodecValueUint16 }
func (failingRuntimeDecoder) DecodeRegistersAny([]uint16) (any, error) {
	return nil, errors.New("decode failed")
}

func TestExecuteRuntimeDecodePlanOffline_OneItemFailsDecode(t *testing.T) {
	rc := codec.MustRuntimeCodecByID("uint32/layout:4321")
	failDecoder := &failingRuntimeDecoder{spec: codec.RegisterSpec{Count: 2}}
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0, Quantity: 4, RegType: HoldingRegister},
		Items: []RuntimeDecodeItem{
			{Name: "ok", Offset: 0, Codec: rc},
			{Name: "fail", Offset: 2, Codec: failDecoder},
		},
	}
	regs := []uint16{0x1234, 0x5678, 0x0000, 0x0000}
	result, err := ExecuteRuntimeDecodePlanOffline(regs, plan)
	if err != nil {
		t.Fatalf("ExecuteRuntimeDecodePlanOffline: %v", err)
	}
	if result.Values[0].Error != nil {
		t.Errorf("item 0 should succeed: %v", result.Values[0].Error)
	}
	if result.Values[0].Value.(uint32) != 0x12345678 {
		t.Errorf("item 0 Value = %v", result.Values[0].Value)
	}
	if result.Values[1].Error == nil {
		t.Error("item 1 should fail decode")
	}
}

func TestExecuteRuntimeDecodePlan_TransportFailure(t *testing.T) {
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
	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	rc := codec.MustRuntimeCodecByID("uint32/layout:4321")
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0, Quantity: 2, RegType: HoldingRegister},
		Items:  []RuntimeDecodeItem{{Name: "x", Offset: 0, Codec: rc}},
	}
	result, err := ExecuteRuntimeDecodePlan(client, context.Background(), 1, plan)
	if err == nil {
		t.Fatal("expected transport error")
	}
	if result != nil {
		t.Error("expected nil result on transport failure")
	}
}

func TestExecuteRuntimeDecodePlan_Integration(t *testing.T) {
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
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			qty := int(frame[10])<<8 | int(frame[11])
			if qty == 4 {
				// Return 4 registers: 0x1234,0x5678, 0xDEAD,0xBEEF
				payload := []byte{0x08, 0x12, 0x34, 0x56, 0x78, 0xDE, 0xAD, 0xBE, 0xEF}
				_ = writeMBAPRegs(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
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

	rc := codec.MustRuntimeCodecByID("uint32/layout:4321")
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0, Quantity: 4, RegType: HoldingRegister},
		Items: []RuntimeDecodeItem{
			{Name: "first", Offset: 0, Codec: rc},
			{Name: "second", Offset: 2, Codec: rc},
		},
	}
	result, err := ExecuteRuntimeDecodePlan(client, context.Background(), 1, plan)
	if err != nil {
		t.Fatalf("ExecuteRuntimeDecodePlan: %v", err)
	}
	if len(result.Registers) != 4 {
		t.Errorf("len(Registers) = %d, want 4", len(result.Registers))
	}
	if result.Values[0].Value.(uint32) != 0x12345678 || result.Values[1].Value.(uint32) != 0xDEADBEEF {
		t.Errorf("Values = %v", result.Values)
	}
}
