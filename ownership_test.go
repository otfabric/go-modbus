// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"reflect"
	"slices"
	"sync"
	"testing"
)

// Tests of the ownership table in API.md ("Ownership of slices"): what the
// client returns is the caller's, what a handler is given is the handler's, and
// what a handler returns is read once, while the response is encoded, and not
// kept.

// A slice the client returns is the caller's: changing it changes nothing the
// client or the server holds, and the same read returns the same values again.
func TestOwnership_ClientResultsAreTheCallers(t *testing.T) {
	dev := e2eNewDevice()
	dev.setHolding(10, 0x1111, 0x2222, 0x3333)
	dev.setInput(10, 0x4444, 0x5555)
	dev.setCoils(10, true, false, true, true)
	dev.setDiscrete(10, false, true, true)
	p := e2eStart(t, "tcp", dev, e2eOpts{})
	c := p.client
	ctx := context.Background()

	scribbleWords := func(v []uint16) {
		for i := range v {
			v[i] = ^v[i]
		}
	}
	scribbleBytes := func(v []byte) {
		for i := range v {
			v[i] = ^v[i]
		}
	}
	scribbleBools := func(v []bool) {
		for i := range v {
			v[i] = !v[i]
		}
	}
	// Each read returns a copy of its result and a function that overwrites
	// the result itself.
	reads := []struct {
		name string
		read func() (snapshot any, scribble func(), err error)
	}{
		{"ReadCoils", func() (any, func(), error) {
			v, err := c.ReadCoils(ctx, e2eUnit, 10, 4)
			return slices.Clone(v), func() { scribbleBools(v) }, err
		}},
		{"ReadDiscreteInputs", func() (any, func(), error) {
			v, err := c.ReadDiscreteInputs(ctx, e2eUnit, 10, 3)
			return slices.Clone(v), func() { scribbleBools(v) }, err
		}},
		{"ReadHoldingRegisters", func() (any, func(), error) {
			v, err := c.ReadHoldingRegisters(ctx, e2eUnit, 10, 3)
			return slices.Clone(v), func() { scribbleWords(v) }, err
		}},
		{"ReadInputRegisters", func() (any, func(), error) {
			v, err := c.ReadInputRegisters(ctx, e2eUnit, 10, 2)
			return slices.Clone(v), func() { scribbleWords(v) }, err
		}},
		{"ReadRegisterBytes", func() (any, func(), error) {
			v, err := c.ReadRegisterBytes(ctx, e2eUnit, 10, 6, HoldingRegister)
			return slices.Clone(v), func() { scribbleBytes(v) }, err
		}},
		{"ReadFIFOQueue", func() (any, func(), error) {
			v, err := c.ReadFIFOQueue(ctx, e2eUnit, 0x04DE)
			return slices.Clone(v), func() { scribbleWords(v) }, err
		}},
		{"ReadFileRecords", func() (any, func(), error) {
			v, err := c.ReadFileRecords(ctx, e2eUnit, []FileRecordRequest{{FileNumber: 4, RecordNumber: 1, RecordLength: 2}, {FileNumber: 9, RecordNumber: 0, RecordLength: 4}})
			snap := make([][]uint16, len(v))
			for i := range v {
				snap[i] = slices.Clone(v[i])
			}
			return snap, func() {
				for i := range v {
					scribbleWords(v[i])
				}
			}, err
		}},
		{"ReadAllDeviceIdentification", func() (any, func(), error) {
			v, err := c.ReadAllDeviceIdentification(ctx, e2eUnit)
			if err != nil {
				return nil, nil, err
			}
			return slices.Clone(v.Objects), func() {
				for i := range v.Objects {
					v.Objects[i] = DeviceIdentificationObject{ID: 0xEE, Value: "scribbled"}
				}
			}, nil
		}},
		{"GetCommEventLog", func() (any, func(), error) {
			v, err := c.GetCommEventLog(ctx, e2eUnit)
			if err != nil {
				return nil, nil, err
			}
			return slices.Clone(v.Events), func() { scribbleBytes(v.Events) }, nil
		}},
		{"Diagnostics", func() (any, func(), error) {
			v, err := c.Diagnostics(ctx, e2eUnit, DiagReturnQueryData, []byte{0xA5, 0x37, 0x01})
			if err != nil {
				return nil, nil, err
			}
			return slices.Clone(v.Data), func() { scribbleBytes(v.Data) }, nil
		}},
		{"ReportServerID", func() (any, func(), error) {
			v, err := c.ReportServerID(ctx, e2eUnit)
			if err != nil {
				return nil, nil, err
			}
			return slices.Clone(v.Data), func() { scribbleBytes(v.Data) }, nil
		}},
	}
	for _, r := range reads {
		first, scribble, err := r.read()
		if err != nil {
			t.Errorf("%s: %v", r.name, err)
			continue
		}
		if reflect.ValueOf(first).Len() == 0 {
			t.Errorf("%s: empty result, nothing to check", r.name)
			continue
		}
		scribble()
		second, _, err := r.read()
		if err != nil || !reflect.DeepEqual(first, second) {
			t.Errorf("%s after its first result was overwritten: %v, %v; want %v again", r.name, second, err, first)
		}
	}
	// The device's own tables did not change either.
	if got, want := dev.holdingAt(10, 3), []uint16{0x1111, 0x2222, 0x3333}; !slices.Equal(got, want) {
		t.Errorf("device holding registers %#04x, want %#04x", got, want)
	}
}

// keepingHandler keeps the slices of the requests it is given, without copying
// them, and then overwrites the ones named by scribble.
type keepingHandler struct {
	noSerialFCHandler
	mu       sync.Mutex
	coils    [][]bool
	words    [][]uint16
	rwWrites [][]uint16
	diag     [][]byte
	records  [][]FileRecord
	// scribble makes the handler overwrite the slices of a request after it
	// has looked at them, to show that the server does not read them again.
	scribble bool
}

func (h *keepingHandler) HandleCoils(_ context.Context, req *CoilsRequest) ([]bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !req.IsWrite {
		return make([]bool, req.Quantity), nil
	}
	h.coils = append(h.coils, req.Args)
	return nil, nil
}

func (h *keepingHandler) HandleHoldingRegisters(_ context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !req.IsWrite {
		return make([]uint16, req.Quantity), nil
	}
	h.words = append(h.words, req.Args)
	return nil, nil
}

func (h *keepingHandler) HandleReadWriteRegisters(_ context.Context, req *ReadWriteRegistersRequest) ([]uint16, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rwWrites = append(h.rwWrites, req.WriteValues)
	return make([]uint16, req.ReadQty), nil
}

func (h *keepingHandler) HandleDiagnostics(_ context.Context, req *DiagnosticsRequest) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.diag = append(h.diag, req.Data)
	echo := bytes.Clone(req.Data)
	if h.scribble {
		for i := range req.Data {
			req.Data[i] = 0
		}
	}
	return echo, nil
}

func (h *keepingHandler) HandleWriteFileRecords(_ context.Context, req *WriteFileRecordsRequest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, req.Records)
	if h.scribble {
		for i := range req.Records {
			for j := range req.Records[i].Data {
				req.Records[i].Data[j] = 0
			}
		}
	}
	return nil
}

// The slices in a request are the handler's. It may keep them: a later request
// does not change them. It may overwrite them: the server does not read them
// again, so the response is what the request called for.
func TestOwnership_RequestSlicesAreTheHandlers(t *testing.T) {
	ctx := context.Background()
	t.Run("kept", func(t *testing.T) {
		h := &keepingHandler{}
		p := e2eStart(t, "tcp", h, e2eOpts{})
		c := p.client
		// Two requests of each kind, same shape, different values, on one connection.
		for round, v := range []uint16{0x1111, 0xEEEE} {
			on := round == 0
			if err := c.WriteCoils(ctx, e2eUnit, 0, []bool{on, !on, on, on, !on, on, !on, !on, on}); err != nil {
				t.Fatalf("WriteCoils: %v", err)
			}
			if err := c.WriteRegisters(ctx, e2eUnit, 0, []uint16{v, v + 1, v + 2}); err != nil {
				t.Fatalf("WriteRegisters: %v", err)
			}
			if _, err := c.ReadWriteMultipleRegisters(ctx, e2eUnit, 0, 1, 8, []uint16{v + 3, v + 4}); err != nil {
				t.Fatalf("ReadWriteMultipleRegisters: %v", err)
			}
			if _, err := c.Diagnostics(ctx, e2eUnit, DiagReturnQueryData, []byte{byte(v), byte(v >> 8), 0x55}); err != nil {
				t.Fatalf("Diagnostics: %v", err)
			}
			if err := c.WriteFileRecords(ctx, e2eUnit, []FileRecord{{FileNumber: 4, RecordNumber: 7, Data: []uint16{v + 5, v + 6}}}); err != nil {
				t.Fatalf("WriteFileRecords: %v", err)
			}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		wantCoils := [][]bool{
			{true, false, true, true, false, true, false, false, true},
			{false, true, false, false, true, false, true, true, false},
		}
		if !reflect.DeepEqual(h.coils, wantCoils) {
			t.Errorf("kept coil arguments %v, want %v", h.coils, wantCoils)
		}
		if want := [][]uint16{{0x1111, 0x1112, 0x1113}, {0xEEEE, 0xEEEF, 0xEEF0}}; !reflect.DeepEqual(h.words, want) {
			t.Errorf("kept register arguments %#04x, want %#04x", h.words, want)
		}
		if want := [][]uint16{{0x1114, 0x1115}, {0xEEF1, 0xEEF2}}; !reflect.DeepEqual(h.rwWrites, want) {
			t.Errorf("kept FC23 write values %#04x, want %#04x", h.rwWrites, want)
		}
		if want := [][]byte{{0x11, 0x11, 0x55}, {0xEE, 0xEE, 0x55}}; !reflect.DeepEqual(h.diag, want) {
			t.Errorf("kept diagnostics data % X, want % X", h.diag, want)
		}
		wantRecs := [][]FileRecord{
			{{FileNumber: 4, RecordNumber: 7, Data: []uint16{0x1116, 0x1117}}},
			{{FileNumber: 4, RecordNumber: 7, Data: []uint16{0xEEF3, 0xEEF4}}},
		}
		if !reflect.DeepEqual(h.records, wantRecs) {
			t.Errorf("kept file records %v, want %v", h.records, wantRecs)
		}
	})
	t.Run("overwritten", func(t *testing.T) {
		h := &keepingHandler{scribble: true}
		p := e2eStart(t, "tcp", h, e2eOpts{})
		c := p.client
		data := []byte{0xDE, 0xAD, 0xBE, 0xEF}
		dr, err := c.Diagnostics(ctx, e2eUnit, DiagReturnQueryData, data)
		if err != nil || !bytes.Equal(dr.Data, data) {
			t.Errorf("Diagnostics echo after the handler overwrote its request data: %v, %v", dr, err)
		}
		// FC21 answers with the echo of the request: it is taken from the frame,
		// not from the records the handler was given.
		if err := c.WriteFileRecords(ctx, e2eUnit, []FileRecord{{FileNumber: 4, RecordNumber: 7, Data: []uint16{0x0102, 0x0304}}}); err != nil {
			t.Errorf("WriteFileRecords after the handler overwrote its records: %v", err)
		}
	})
}

// reusingHandler serves holding registers from one buffer that it fills anew
// for every read and returns as it is.
type reusingHandler struct {
	noSerialFCHandler
	buf []uint16
}

func (h *reusingHandler) HandleHoldingRegisters(_ context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	// The buffer of the previous call: the server is done with it.
	for i := range h.buf {
		h.buf[i] = 0xDEAD
	}
	h.buf = h.buf[:0]
	for i := range req.Quantity {
		h.buf = append(h.buf, req.Addr+i)
	}
	return h.buf, nil
}

// A slice a handler returns is read while the response is encoded and not kept:
// a handler may fill the same buffer again for the next request on that
// connection.
func TestOwnership_HandlerResultIsNotKept(t *testing.T) {
	h := &reusingHandler{buf: make([]uint16, 0, 125)}
	p := e2eStart(t, "tcp", h, e2eOpts{})
	ctx := context.Background()
	for _, r := range []struct{ addr, qty uint16 }{{100, 125}, {7, 3}, {5000, 60}, {0xFFFF, 1}} {
		got, err := p.client.ReadHoldingRegisters(ctx, e2eUnit, r.addr, r.qty)
		if err != nil {
			t.Fatalf("ReadHoldingRegisters(%d, %d): %v", r.addr, r.qty, err)
		}
		for i, v := range got {
			if v != r.addr+uint16(i) {
				t.Fatalf("ReadHoldingRegisters(%d, %d): register %d = %#04x, want %#04x", r.addr, r.qty, i, v, r.addr+uint16(i))
			}
		}
	}
}

// snapshotHandler keeps one register table for all connections. A write sets
// every register of its range to the same value; a read returns a copy made
// under the lock, as RequestHandler asks for shared state.
type snapshotHandler struct {
	noSerialFCHandler
	mu   sync.Mutex
	regs [64]uint16
}

func (h *snapshotHandler) HandleHoldingRegisters(_ context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if int(req.Addr)+int(req.Quantity) > len(h.regs) {
		return nil, ErrIllegalDataAddress
	}
	if req.IsWrite {
		copy(h.regs[req.Addr:], req.Args)
		return nil, nil
	}
	return slices.Clone(h.regs[req.Addr : req.Addr+req.Quantity]), nil
}

// The server encodes what a handler returned after the handler has returned,
// so outside the handler's lock. With the copy the interface asks for, readers
// on one connection see whole writes of another: every register of a response
// has the value of one write. Run with -race, this is also the proof that the
// server reads nothing of the handler's but that copy.
func TestOwnership_HandlerReturnsACopyOfSharedState(t *testing.T) {
	h := &snapshotHandler{}
	p := e2eStart(t, "tcp", h, e2eOpts{})
	writer := p.newClient(nil)
	if err := writer.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	const n, rounds = 60, 200

	var wg sync.WaitGroup
	wg.Go(func() {
		vals := make([]uint16, n)
		for round := range rounds {
			for i := range vals {
				vals[i] = uint16(round + 1)
			}
			if err := writer.WriteRegisters(ctx, e2eUnit, 0, vals); err != nil {
				t.Errorf("WriteRegisters: %v", err)
				return
			}
		}
	})
	for range rounds {
		got, err := p.client.ReadHoldingRegisters(ctx, e2eUnit, 0, n)
		if err != nil {
			t.Fatalf("ReadHoldingRegisters: %v", err)
		}
		for i, v := range got {
			if v != got[0] {
				t.Fatalf("a response mixes two writes: register 0 = %d, register %d = %d", got[0], i, v)
			}
		}
	}
	wg.Wait()
}
