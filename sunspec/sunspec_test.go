package sunspec

import (
	"context"
	"errors"
	"testing"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/protocol"
)

type mockReader struct {
	responses map[uint16][]byte
	err       error
}

func (m *mockReader) ReadRawBytes(_ context.Context, _ uint8, addr uint16, byteCount uint16, _ RegType) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	data, ok := m.responses[addr]
	if !ok {
		return nil, protocol.ErrIllegalDataAddress
	}
	if len(data) < int(byteCount) {
		return data, nil
	}
	return data[:byteCount], nil
}

func sunSpecMarkerBytes() []byte {
	r0 := adu.Uint16ToBytes(adu.BigEndian, MarkerReg0)
	r1 := adu.Uint16ToBytes(adu.BigEndian, MarkerReg1)
	return append(r0, r1...)
}

func modelHeaderBytes(id, length uint16) []byte {
	h := adu.Uint16ToBytes(adu.BigEndian, id)
	l := adu.Uint16ToBytes(adu.BigEndian, length)
	return append(h, l...)
}

func TestApplyDefaults_Nil(t *testing.T) {
	o := ApplyDefaults(nil)
	if o.UnitID != 1 {
		t.Errorf("UnitID = %d, want 1", o.UnitID)
	}
	if o.MaxModels != 256 {
		t.Errorf("MaxModels = %d, want 256", o.MaxModels)
	}
	if len(o.BaseAddresses) != len(DefaultBaseAddresses) {
		t.Errorf("BaseAddresses length = %d, want %d", len(o.BaseAddresses), len(DefaultBaseAddresses))
	}
}

func TestApplyDefaults_ZeroUnitID(t *testing.T) {
	o := ApplyDefaults(&Options{UnitID: 0, MaxModels: 10})
	if o.UnitID != 1 {
		t.Errorf("UnitID = %d, want 1 (default when 0)", o.UnitID)
	}
}

func TestApplyDefaults_CustomAddresses(t *testing.T) {
	custom := []uint16{100, 200}
	o := ApplyDefaults(&Options{UnitID: 5, BaseAddresses: custom, MaxModels: 50})
	if len(o.BaseAddresses) != 2 || o.BaseAddresses[0] != 100 {
		t.Errorf("BaseAddresses = %v, want %v", o.BaseAddresses, custom)
	}
}

func TestValidateOptions(t *testing.T) {
	valid := &Options{UnitID: 1, RegType: HoldingRegister, BaseAddresses: []uint16{40000}}
	if err := ValidateOptions(valid); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	noAddrs := &Options{UnitID: 1, RegType: HoldingRegister, BaseAddresses: nil}
	if err := ValidateOptions(noAddrs); err == nil {
		t.Error("expected error for empty base addresses")
	}
	badRegType := &Options{UnitID: 1, RegType: 99, BaseAddresses: []uint16{0}}
	if err := ValidateOptions(badRegType); err == nil {
		t.Error("expected error for invalid RegType")
	}
}

func TestDetect_Found(t *testing.T) {
	r := &mockReader{
		responses: map[uint16][]byte{
			40000: sunSpecMarkerBytes(),
		},
	}
	opts := &Options{UnitID: 1, RegType: HoldingRegister, BaseAddresses: []uint16{40000}}
	res, err := Detect(context.Background(), r, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Detected {
		t.Error("expected Detected = true")
	}
	if res.BaseAddress != 40000 {
		t.Errorf("BaseAddress = %d, want 40000", res.BaseAddress)
	}
}

func TestDetect_NotFound(t *testing.T) {
	r := &mockReader{
		responses: map[uint16][]byte{
			40000: {0x00, 0x01, 0x00, 0x02},
		},
	}
	opts := &Options{UnitID: 1, RegType: HoldingRegister, BaseAddresses: []uint16{40000}}
	res, err := Detect(context.Background(), r, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Detected {
		t.Error("expected Detected = false")
	}
}

func TestDetect_AllProbesFail(t *testing.T) {
	r := &mockReader{err: errors.New("transport error")}
	opts := &Options{UnitID: 1, RegType: HoldingRegister, BaseAddresses: []uint16{0, 40000}}
	res, err := Detect(context.Background(), r, opts)
	if err == nil {
		t.Fatal("expected error when all probes fail")
	}
	if res == nil {
		t.Fatal("result should still be non-nil")
	}
	if res.Detected {
		t.Error("Detected should be false")
	}
}

func TestDetect_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &mockReader{responses: map[uint16][]byte{0: sunSpecMarkerBytes()}}
	res, err := Detect(ctx, r, &Options{UnitID: 1, BaseAddresses: []uint16{0}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if res == nil {
		t.Fatal("result should be non-nil even on context cancel")
	}
}

func TestDetect_SecondAddressMatch(t *testing.T) {
	r := &mockReader{
		responses: map[uint16][]byte{
			50000: sunSpecMarkerBytes(),
		},
	}
	opts := &Options{UnitID: 1, BaseAddresses: []uint16{40000, 50000}}
	res, err := Detect(context.Background(), r, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Detected || res.BaseAddress != 50000 {
		t.Errorf("expected Detected at 50000, got Detected=%v BaseAddr=%d", res.Detected, res.BaseAddress)
	}
	if len(res.Attempts) != 2 {
		t.Errorf("expected 2 attempts, got %d", len(res.Attempts))
	}
}

func TestReadModelHeaders_Basic(t *testing.T) {
	r := &mockReader{
		responses: map[uint16][]byte{
			40002: modelHeaderBytes(1, 50),
			40054: modelHeaderBytes(EndModelID, EndModelLength),
		},
	}
	opts := &Options{UnitID: 1, RegType: HoldingRegister, BaseAddresses: []uint16{40000}, MaxModels: 100}
	models, err := ReadModelHeaders(context.Background(), r, opts, 40000)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != 1 || models[0].Length != 50 {
		t.Errorf("model 0: ID=%d Length=%d, want 1/50", models[0].ID, models[0].Length)
	}
	if !models[1].IsEndModel {
		t.Error("second model should be end model")
	}
}

func TestReadModelHeaders_ZeroLengthNonEnd(t *testing.T) {
	r := &mockReader{
		responses: map[uint16][]byte{
			2: modelHeaderBytes(999, 0),
		},
	}
	opts := &Options{UnitID: 1, RegType: HoldingRegister, BaseAddresses: []uint16{0}, MaxModels: 100}
	_, err := ReadModelHeaders(context.Background(), r, opts, 0)
	if !errors.Is(err, protocol.ErrSunSpecModelChainInvalid) {
		t.Errorf("expected ErrSunSpecModelChainInvalid, got %v", err)
	}
}

func TestReadModelHeaders_MaxAddressSpan(t *testing.T) {
	r := &mockReader{
		responses: map[uint16][]byte{
			2: modelHeaderBytes(1, 100),
		},
	}
	opts := &Options{
		UnitID:         1,
		RegType:        HoldingRegister,
		BaseAddresses:  []uint16{0},
		MaxModels:      100,
		MaxAddressSpan: 50,
	}
	_, err := ReadModelHeaders(context.Background(), r, opts, 0)
	if !errors.Is(err, protocol.ErrSunSpecModelChainLimitExceeded) {
		t.Errorf("expected ErrSunSpecModelChainLimitExceeded, got %v", err)
	}
}

func TestReadModelHeaders_ReadError(t *testing.T) {
	r := &mockReader{err: errors.New("read failure")}
	opts := &Options{UnitID: 1, RegType: HoldingRegister, BaseAddresses: []uint16{0}, MaxModels: 100}
	_, err := ReadModelHeaders(context.Background(), r, opts, 0)
	if err == nil {
		t.Fatal("expected error on read failure")
	}
}

func TestDiscover_Detected(t *testing.T) {
	r := &mockReader{
		responses: map[uint16][]byte{
			40000: sunSpecMarkerBytes(),
			40002: modelHeaderBytes(EndModelID, EndModelLength),
		},
	}
	opts := &Options{UnitID: 1, RegType: HoldingRegister, BaseAddresses: []uint16{40000}, MaxModels: 100}
	disc, err := Discover(context.Background(), r, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !disc.Detection.Detected {
		t.Error("expected detection")
	}
	if len(disc.Models) != 1 || !disc.Models[0].IsEndModel {
		t.Errorf("expected end model, got %d models", len(disc.Models))
	}
}

func TestDiscover_NotDetected(t *testing.T) {
	r := &mockReader{responses: map[uint16][]byte{40000: {0, 0, 0, 0}}}
	opts := &Options{UnitID: 1, BaseAddresses: []uint16{40000}}
	disc, err := Discover(context.Background(), r, opts)
	if err != nil {
		t.Fatal(err)
	}
	if disc.Detection.Detected {
		t.Error("expected no detection")
	}
	if len(disc.Models) != 0 {
		t.Errorf("expected no models, got %d", len(disc.Models))
	}
}
