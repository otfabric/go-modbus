// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

// The worked examples of the MODBUS Application Protocol Specification
// V1.1b3, section 6, replayed byte for byte as known-good wire fixtures: the
// client must send exactly the example request and decode the example
// response, and the server, given a device that holds the example data, must
// answer the example request with exactly the example response. The bytes
// are those of spec/MODBUS-SPEC.md; addresses are the PDU's (0-based).
//
// FC08, FC17, FC20, FC12 and FC43 have their fixtures next to their own tests.

// specDevice holds the data of the worked examples.
type specDevice struct {
	coils    map[uint16]bool
	discrete map[uint16]bool
	holding  map[uint16]uint16
	input    map[uint16]uint16
	fifo     map[uint16][]uint16
}

func newSpecDevice() *specDevice {
	d := &specDevice{
		coils: map[uint16]bool{}, discrete: map[uint16]bool{},
		holding: map[uint16]uint16{}, input: map[uint16]uint16{}, fifo: map[uint16][]uint16{},
	}
	// §6.1: outputs 20-38 (addresses 19-37) read as CD 6B 05.
	for i, b := range []bool{true, false, true, true, false, false, true, true, true, true, false, true, false, true, true, false, true, false, true} {
		d.coils[19+uint16(i)] = b
	}
	// §6.2: inputs 197-218 (addresses 196-217) read as AC DB 35.
	for i, v := range []byte{0xAC, 0xDB, 0x35} {
		for bit := range 8 {
			if n := 8*i + bit; n < 22 {
				d.discrete[196+uint16(n)] = v>>bit&1 == 1
			}
		}
	}
	// §6.3: registers 108-110 (addresses 107-109) hold 0x022B, 0, 0x0064.
	d.holding[107], d.holding[108], d.holding[109] = 0x022B, 0x0000, 0x0064
	// §6.4: input register 9 (address 8) holds 0x000A.
	d.input[8] = 0x000A
	// §6.17: registers 4-9 (addresses 3-8) hold 00FE 0ACD 0001 0003 000D 00FF.
	for i, v := range []uint16{0x00FE, 0x0ACD, 0x0001, 0x0003, 0x000D, 0x00FF} {
		d.holding[3+uint16(i)] = v
	}
	// §6.18: the FIFO at 1246 holds 440 and 4740.
	d.fifo[0x04DE] = []uint16{0x01B8, 0x1284}
	return d
}

func (d *specDevice) HandleCoils(_ context.Context, req *CoilsRequest) ([]bool, error) {
	if req.IsWrite {
		for i, v := range req.Args {
			d.coils[req.Addr+uint16(i)] = v
		}
		return nil, nil
	}
	out := make([]bool, req.Quantity)
	for i := range out {
		out[i] = d.coils[req.Addr+uint16(i)]
	}
	return out, nil
}

func (d *specDevice) HandleDiscreteInputs(_ context.Context, req *DiscreteInputsRequest) ([]bool, error) {
	out := make([]bool, req.Quantity)
	for i := range out {
		out[i] = d.discrete[req.Addr+uint16(i)]
	}
	return out, nil
}

func (d *specDevice) HandleHoldingRegisters(_ context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	if req.IsWrite {
		for i, v := range req.Args {
			d.holding[req.Addr+uint16(i)] = v
		}
		return nil, nil
	}
	out := make([]uint16, req.Quantity)
	for i := range out {
		out[i] = d.holding[req.Addr+uint16(i)]
	}
	return out, nil
}

func (d *specDevice) HandleInputRegisters(_ context.Context, req *InputRegistersRequest) ([]uint16, error) {
	out := make([]uint16, req.Quantity)
	for i := range out {
		out[i] = d.input[req.Addr+uint16(i)]
	}
	return out, nil
}

func (d *specDevice) HandleMaskWrite(_ context.Context, req *MaskWriteRequest) error {
	cur := d.holding[req.Addr]
	d.holding[req.Addr] = cur&req.AndMask | req.OrMask&^req.AndMask
	return nil
}

func (d *specDevice) HandleReadWriteRegisters(_ context.Context, req *ReadWriteRegistersRequest) ([]uint16, error) {
	for i, v := range req.WriteValues {
		d.holding[req.WriteAddr+uint16(i)] = v
	}
	out := make([]uint16, req.ReadQty)
	for i := range out {
		out[i] = d.holding[req.ReadAddr+uint16(i)]
	}
	return out, nil
}

func (d *specDevice) HandleReadFIFOQueue(_ context.Context, req *FIFOQueueRequest) ([]uint16, error) {
	v, ok := d.fifo[req.Addr]
	if !ok {
		return nil, ErrIllegalDataAddress
	}
	return v, nil
}

// specExample is one worked example: the request and response PDUs, and the
// client call that must send the request and accept the response.
type specExample struct {
	section  string
	request  []byte
	response []byte
	call     func(ctx context.Context, c *Client) error
}

func specExamples(t *testing.T) []specExample {
	return []specExample{
		{"6.1 Read Coils", []byte{0x01, 0x00, 0x13, 0x00, 0x13}, []byte{0x01, 0x03, 0xCD, 0x6B, 0x05},
			func(ctx context.Context, c *Client) error {
				got, err := c.ReadCoils(ctx, 1, 0x13, 0x13)
				want := []bool{true, false, true, true, false, false, true, true, true, true, false, true, false, true, true, false, true, false, true}
				if err == nil && !reflect.DeepEqual(got, want) {
					t.Errorf("6.1: %v", got)
				}
				return err
			}},
		{"6.2 Read Discrete Inputs", []byte{0x02, 0x00, 0xC4, 0x00, 0x16}, []byte{0x02, 0x03, 0xAC, 0xDB, 0x35},
			func(ctx context.Context, c *Client) error {
				got, err := c.ReadDiscreteInputs(ctx, 1, 0xC4, 0x16)
				if err == nil && (len(got) != 22 || got[0] || got[1] || !got[2] || !got[21] || got[19]) {
					t.Errorf("6.2: %v", got)
				}
				return err
			}},
		{"6.3 Read Holding Registers", []byte{0x03, 0x00, 0x6B, 0x00, 0x03}, []byte{0x03, 0x06, 0x02, 0x2B, 0x00, 0x00, 0x00, 0x64},
			func(ctx context.Context, c *Client) error {
				got, err := c.ReadHoldingRegisters(ctx, 1, 0x6B, 3)
				if err == nil && !reflect.DeepEqual(got, []uint16{555, 0, 100}) {
					t.Errorf("6.3: %v", got)
				}
				return err
			}},
		{"6.4 Read Input Registers", []byte{0x04, 0x00, 0x08, 0x00, 0x01}, []byte{0x04, 0x02, 0x00, 0x0A},
			func(ctx context.Context, c *Client) error {
				got, err := c.ReadInputRegisters(ctx, 1, 0x08, 1)
				if err == nil && !reflect.DeepEqual(got, []uint16{10}) {
					t.Errorf("6.4: %v", got)
				}
				return err
			}},
		{"6.5 Write Single Coil", []byte{0x05, 0x00, 0xAC, 0xFF, 0x00}, []byte{0x05, 0x00, 0xAC, 0xFF, 0x00},
			func(ctx context.Context, c *Client) error { return c.WriteCoil(ctx, 1, 0xAC, true) }},
		{"6.6 Write Single Register", []byte{0x06, 0x00, 0x01, 0x00, 0x03}, []byte{0x06, 0x00, 0x01, 0x00, 0x03},
			func(ctx context.Context, c *Client) error { return c.WriteRegister(ctx, 1, 0x01, 0x0003) }},
		{"6.11 Write Multiple Coils", []byte{0x0F, 0x00, 0x13, 0x00, 0x0A, 0x02, 0xCD, 0x01}, []byte{0x0F, 0x00, 0x13, 0x00, 0x0A},
			func(ctx context.Context, c *Client) error {
				return c.WriteCoils(ctx, 1, 0x13, []bool{true, false, true, true, false, false, true, true, true, false})
			}},
		{"6.12 Write Multiple Registers", []byte{0x10, 0x00, 0x01, 0x00, 0x02, 0x04, 0x00, 0x0A, 0x01, 0x02}, []byte{0x10, 0x00, 0x01, 0x00, 0x02},
			func(ctx context.Context, c *Client) error {
				return c.WriteRegisters(ctx, 1, 0x01, []uint16{0x000A, 0x0102})
			}},
		{"6.16 Mask Write Register", []byte{0x16, 0x00, 0x04, 0x00, 0xF2, 0x00, 0x25}, []byte{0x16, 0x00, 0x04, 0x00, 0xF2, 0x00, 0x25},
			func(ctx context.Context, c *Client) error { return c.MaskWriteRegister(ctx, 1, 0x04, 0x00F2, 0x0025) }},
		{"6.17 Read/Write Multiple Registers",
			[]byte{0x17, 0x00, 0x03, 0x00, 0x06, 0x00, 0x0E, 0x00, 0x03, 0x06, 0x00, 0xFF, 0x00, 0xFF, 0x00, 0xFF},
			[]byte{0x17, 0x0C, 0x00, 0xFE, 0x0A, 0xCD, 0x00, 0x01, 0x00, 0x03, 0x00, 0x0D, 0x00, 0xFF},
			func(ctx context.Context, c *Client) error {
				got, err := c.ReadWriteMultipleRegisters(ctx, 1, 0x03, 6, 0x0E, []uint16{0x00FF, 0x00FF, 0x00FF})
				if err == nil && !reflect.DeepEqual(got, []uint16{0x00FE, 0x0ACD, 0x0001, 0x0003, 0x000D, 0x00FF}) {
					t.Errorf("6.17: %04X", got)
				}
				return err
			}},
		{"6.18 Read FIFO Queue", []byte{0x18, 0x04, 0xDE}, []byte{0x18, 0x00, 0x06, 0x00, 0x02, 0x01, 0xB8, 0x12, 0x84},
			func(ctx context.Context, c *Client) error {
				got, err := c.ReadFIFOQueue(ctx, 1, 0x04DE)
				if err == nil && !reflect.DeepEqual(got, []uint16{440, 4740}) {
					t.Errorf("6.18: %v", got)
				}
				return err
			}},
	}
}

// TestSpecExamples_Client: the client sends each example request byte for
// byte and accepts the example response.
func TestSpecExamples_Client(t *testing.T) {
	for _, ex := range specExamples(t) {
		t.Run(ex.section, func(t *testing.T) {
			var got []byte
			client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, payload []byte) []byte {
				got = append([]byte{fc}, payload...)
				return normalFrame(txid, unitID, ex.response[0], ex.response[1:])
			})
			defer cleanup()
			if err := ex.call(context.Background(), client); err != nil {
				t.Fatalf("call: %v", err)
			}
			if !bytes.Equal(got, ex.request) {
				t.Errorf("request % X, want % X", got, ex.request)
			}
		})
	}
}

// TestSpecExamples_Server: the server answers each example request with the
// example response, from a device holding the example data.
func TestSpecExamples_Server(t *testing.T) {
	for _, ex := range specExamples(t) {
		t.Run(ex.section, func(t *testing.T) {
			dev := newSpecDevice()
			tr, _ := serveOne(t, dev, FunctionCode(ex.request[0]), ex.request[1:]...)
			if tr.closed || len(tr.responses) != 1 {
				t.Fatalf("responses %+v, closed %v", tr.responses, tr.closed)
			}
			res := tr.responses[0]
			got := append([]byte{res.FunctionCode}, res.Payload...)
			if !bytes.Equal(got, ex.response) {
				t.Errorf("response % X, want % X", got, ex.response)
			}
		})
	}
	// §6.16: the mask write turns register 5 (address 4) from 0x12 into 0x17.
	// (§6.17 uses the same address with other data: the examples are
	// independent, so the register is set here.)
	dev := newSpecDevice()
	dev.holding[4] = 0x0012
	serveOne(t, dev, FCMaskWriteRegister, 0x00, 0x04, 0x00, 0xF2, 0x00, 0x25)
	if dev.holding[4] != 0x0017 {
		t.Errorf("6.16: register 5 = 0x%04X, want 0x0017", dev.holding[4])
	}
}
