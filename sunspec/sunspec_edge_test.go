// SPDX-License-Identifier: MIT

package sunspec

import (
	"context"
	"errors"
	"testing"

	"github.com/otfabric/go-modbus/internal/protocol"
)

// funcReader adapts a function to the Reader interface.
type funcReader func(ctx context.Context, unitID uint8, addr, byteCount uint16, regType RegType) ([]byte, error)

func (f funcReader) ReadRawBytes(ctx context.Context, unitID uint8, addr, byteCount uint16, regType RegType) ([]byte, error) {
	return f(ctx, unitID, addr, byteCount, regType)
}

func invalidOptions() *Options {
	return &Options{RegType: protocol.RegType(99)}
}

func wantParameterError(t *testing.T, err error) {
	t.Helper()
	var pe *protocol.ParameterError
	if !errors.As(err, &pe) {
		t.Fatalf("want *protocol.ParameterError, got %T: %v", err, err)
	}
}

func TestInvalidOptionsRejectedBeforeAnyRead(t *testing.T) {
	reads := 0
	r := funcReader(func(context.Context, uint8, uint16, uint16, RegType) ([]byte, error) {
		reads++
		return sunSpecMarkerBytes(), nil
	})
	ctx := context.Background()

	det, err := Detect(ctx, r, invalidOptions())
	wantParameterError(t, err)
	if det != nil {
		t.Errorf("Detect returned a result with invalid options: %+v", det)
	}

	models, err := ReadModelHeaders(ctx, r, invalidOptions(), 40000)
	wantParameterError(t, err)
	if models != nil {
		t.Errorf("ReadModelHeaders returned models with invalid options: %+v", models)
	}

	disc, err := Discover(ctx, r, invalidOptions())
	wantParameterError(t, err)
	if disc != nil {
		t.Errorf("Discover returned a result with invalid options: %+v", disc)
	}

	if reads != 0 {
		t.Errorf("%d reads were issued despite invalid options", reads)
	}
}

func TestDetect_PassesOptionsToReader(t *testing.T) {
	type call struct {
		unitID    uint8
		addr      uint16
		byteCount uint16
		regType   RegType
	}
	var calls []call
	r := funcReader(func(_ context.Context, unitID uint8, addr, byteCount uint16, regType RegType) ([]byte, error) {
		calls = append(calls, call{unitID, addr, byteCount, regType})
		if addr == 200 {
			return sunSpecMarkerBytes(), nil
		}
		return []byte{0, 0, 0, 0}, nil
	})
	res, err := Detect(context.Background(), r, &Options{UnitID: 17, RegType: InputRegister, BaseAddresses: []uint16{100, 200, 300}})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !res.Detected || res.BaseAddress != 200 || res.UnitID != 17 || res.RegType != InputRegister {
		t.Fatalf("unexpected result %+v", res)
	}
	want := []call{{17, 100, 4, InputRegister}, {17, 200, 4, InputRegister}}
	if len(calls) != len(want) {
		t.Fatalf("reader calls = %+v, want %+v (probing must stop at the first match)", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
}

// A truncated probe response is recorded as an unmatched attempt and neither
// counts as a successful probe nor as an error.
func TestDetect_ShortProbeResponse(t *testing.T) {
	r := &mockReader{responses: map[uint16][]byte{
		10: {0x53, 0x75},
		20: sunSpecMarkerBytes(),
	}}
	res, err := Detect(context.Background(), r, &Options{BaseAddresses: []uint16{10, 20}})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !res.Detected || res.BaseAddress != 20 {
		t.Fatalf("marker at second address not detected: %+v", res)
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(res.Attempts))
	}
	short := res.Attempts[0]
	if short.BaseAddress != 10 || short.Matched || short.Error != nil || short.Registers != nil {
		t.Errorf("short attempt recorded as %+v", short)
	}

	// Only short responses: not detected, and no error to report.
	res, err = Detect(context.Background(), r, &Options{BaseAddresses: []uint16{10}})
	if err != nil {
		t.Fatalf("Detect (short only): %v", err)
	}
	if res.Detected || len(res.Attempts) != 1 {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestReadModelHeaders_BaseAddressOverflow(t *testing.T) {
	r := &mockReader{responses: map[uint16][]byte{}}
	for _, base := range []uint16{0xFFFE, 0xFFFF} {
		models, err := ReadModelHeaders(context.Background(), r, nil, base)
		if !errors.Is(err, protocol.ErrSunSpecModelChainInvalid) {
			t.Errorf("base %#x: want ErrSunSpecModelChainInvalid, got %v", base, err)
		}
		if models != nil {
			t.Errorf("base %#x: got models %+v", base, models)
		}
	}
}

func TestReadModelHeaders_ContextCancelledMidChain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := funcReader(func(_ context.Context, _ uint8, addr, _ uint16, _ RegType) ([]byte, error) {
		if addr != 102 {
			t.Errorf("unexpected read at %d after cancellation", addr)
		}
		cancel()
		return modelHeaderBytes(1, 66), nil
	})
	models, err := ReadModelHeaders(ctx, r, nil, 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(models) != 1 || models[0].ID != 1 || models[0].Length != 66 || models[0].NextAddress != 170 {
		t.Fatalf("models read before cancellation not returned: %+v", models)
	}
}

func TestReadModelHeaders_ShortHeader(t *testing.T) {
	r := &mockReader{responses: map[uint16][]byte{
		102: modelHeaderBytes(1, 4),
		108: {0x00, 0x67},
	}}
	models, err := ReadModelHeaders(context.Background(), r, nil, 100)
	if !errors.Is(err, protocol.ErrProtocolError) {
		t.Fatalf("want ErrProtocolError, got %v", err)
	}
	if len(models) != 1 || models[0].ID != 1 {
		t.Fatalf("want the one complete model, got %+v", models)
	}
}

func TestReadModelHeaders_ModelBeyondAddressSpace(t *testing.T) {
	// Model body would end past register 0xFFFF.
	r := &mockReader{responses: map[uint16][]byte{0xFF00: modelHeaderBytes(101, 0x0200)}}
	models, err := ReadModelHeaders(context.Background(), r, nil, 0xFF00-2)
	if !errors.Is(err, protocol.ErrProtocolError) {
		t.Fatalf("want ErrProtocolError, got %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("out-of-range model was returned: %+v", models)
	}
}

func TestReadModelHeaders_ModelEndsExactlyAtAddressSpaceEnd(t *testing.T) {
	// The model occupies registers up to and including 0xFFFF, so there is
	// no room for a following header: the chain has no end model.
	const start = 0xFF00
	length := uint16(0x10000 - start - 2)
	r := &mockReader{responses: map[uint16][]byte{start: modelHeaderBytes(101, length)}}
	models, err := ReadModelHeaders(context.Background(), r, nil, start-2)
	if !errors.Is(err, protocol.ErrSunSpecModelChainInvalid) {
		t.Fatalf("want ErrSunSpecModelChainInvalid, got %v", err)
	}
	if len(models) != 1 || models[0].StartAddress != start || models[0].EndAddress != 0xFFFF {
		t.Fatalf("unexpected models %+v", models)
	}
}

func TestReadModelHeaders_MaxModelsLimit(t *testing.T) {
	r := funcReader(func(_ context.Context, _ uint8, addr, _ uint16, _ RegType) ([]byte, error) {
		return modelHeaderBytes(addr, 2), nil
	})
	models, err := ReadModelHeaders(context.Background(), r, &Options{BaseAddresses: []uint16{0}, MaxModels: 3}, 0)
	if err != nil {
		t.Fatalf("ReadModelHeaders: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3 (MaxModels)", len(models))
	}
	for i, m := range models {
		if want := uint16(2 + 4*i); m.StartAddress != want || m.IsEndModel {
			t.Errorf("model %d: %+v, want start %d", i, m, want)
		}
	}
}

func TestDiscover_DetectionReadErrors(t *testing.T) {
	boom := errors.New("link down")
	disc, err := Discover(context.Background(), &mockReader{err: boom}, &Options{BaseAddresses: []uint16{0, 40000}})
	if !errors.Is(err, boom) {
		t.Fatalf("want read error, got %v", err)
	}
	if disc != nil {
		t.Fatalf("got a discovery result alongside the error: %+v", disc)
	}
}

func TestDiscover_ChainErrorKeepsPartialModels(t *testing.T) {
	r := &mockReader{responses: map[uint16][]byte{
		40000: sunSpecMarkerBytes(),
		40002: modelHeaderBytes(1, 66),
		// 40070 missing: the read of the next header fails.
	}}
	disc, err := Discover(context.Background(), r, nil)
	if !errors.Is(err, protocol.ErrIllegalDataAddress) {
		t.Fatalf("want ErrIllegalDataAddress, got %v", err)
	}
	if disc == nil || !disc.Detection.Detected || disc.Detection.BaseAddress != 40000 {
		t.Fatalf("detection result lost: %+v", disc)
	}
	if len(disc.Models) != 1 || disc.Models[0].ID != 1 || disc.Models[0].NextAddress != 40070 {
		t.Fatalf("partial models not returned: %+v", disc.Models)
	}
}
