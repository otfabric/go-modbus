// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	intsession "github.com/otfabric/go-modbus/internal/session"
)

// tapRecorder records PDUTap callbacks.
type tapRecorder struct {
	mu  sync.Mutex
	exs []PDUExchange
	ctx []context.Context
}

func (r *tapRecorder) OnPDU(ctx context.Context, ex PDUExchange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exs = append(r.exs, ex)
	r.ctx = append(r.ctx, ctx)
}

// take returns the exchanges recorded since the last call and forgets them.
func (r *tapRecorder) take() []PDUExchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.exs
	r.exs, r.ctx = nil, nil
	return out
}

// newTappedClient is newScriptedClient with a PDUTap attached.
func newTappedClient(t *testing.T, fn scriptFn) (*Client, *scriptedTransport, *tapRecorder) {
	t.Helper()
	c, tr, _ := newScriptedClient(t, fn)
	rec := &tapRecorder{}
	c.tap = rec
	return c, tr, rec
}

func reqPDU(r adu.Request) []byte { return append([]byte{r.FunctionCode}, r.Payload...) }

// Every client method reports each request it puts on the wire exactly once,
// with the request PDU as sent and the response PDU as received.
func TestPDUTap_AllClientCalls(t *testing.T) {
	const unit = 7
	for _, cc := range allClientCalls() {
		t.Run(cc.name, func(t *testing.T) {
			var mu sync.Mutex
			var answers [][]byte
			c, tr, rec := newTappedClient(t, func(req *adu.Request) (*adu.Response, error) {
				res, err := goodDevice(req)
				if err == nil {
					mu.Lock()
					answers = append(answers, append([]byte{res.FunctionCode}, res.Payload...))
					mu.Unlock()
				}
				return res, err
			})
			before := time.Now()
			if err := cc.call(context.Background(), c, unit); err != nil {
				t.Fatalf("call failed: %v", err)
			}
			after := time.Now()

			sent := tr.requests()
			got := rec.take()
			if len(got) != len(sent) || len(got) != len(cc.fcs) {
				t.Fatalf("%d exchanges reported, %d requests sent, want %d", len(got), len(sent), len(cc.fcs))
			}
			for i, ex := range got {
				if ex.UnitID != unit {
					t.Errorf("exchange %d: unit %d, want %d", i, ex.UnitID, unit)
				}
				if !bytes.Equal(ex.Request, reqPDU(sent[i])) {
					t.Errorf("exchange %d: request % X, sent % X", i, ex.Request, reqPDU(sent[i]))
				}
				if !bytes.Equal(ex.Response, answers[i]) {
					t.Errorf("exchange %d: response % X, answered % X", i, ex.Response, answers[i])
				}
				if ex.Request[0] != byte(cc.fcs[i]) {
					t.Errorf("exchange %d: function 0x%02X, want 0x%02X", i, ex.Request[0], byte(cc.fcs[i]))
				}
				if ex.Received.Before(before) || ex.Received.After(after) {
					t.Errorf("exchange %d: received %v, outside the call [%v, %v]", i, ex.Received, before, after)
				}
				if i > 0 && ex.Received.Before(got[i-1].Received) {
					t.Errorf("exchange %d: received before exchange %d", i, i-1)
				}
			}
		})
	}
}

// Responses that answer the request are reported, usable or not; responses
// that do not, and requests that get no response, are not.
func TestPDUTap_WhatIsReported(t *testing.T) {
	read := func(ctx context.Context, c *Client) error {
		_, err := c.ReadHoldingRegisters(ctx, 1, 0x0010, 2)
		return err
	}
	cases := []struct {
		name    string
		fn      scriptFn
		wantErr bool
		want    []byte // response PDU reported; nil means nothing reported
	}{
		{
			name: "normal response",
			fn:   goodDevice,
			want: []byte{0x03, 0x04, 0x12, 0x34, 0x12, 0x34},
		},
		{
			name:    "exception response",
			fn:      func(req *adu.Request) (*adu.Response, error) { return excRes(req, 0x02), nil },
			wantErr: true,
			want:    []byte{0x83, 0x02},
		},
		{
			name: "response the method rejects",
			fn: func(req *adu.Request) (*adu.Response, error) {
				return okRes(req, 0x02, 0x12, 0x34), nil // one register for two
			},
			wantErr: true,
			want:    []byte{0x03, 0x02, 0x12, 0x34},
		},
		{
			name: "unrelated function code",
			fn: func(req *adu.Request) (*adu.Response, error) {
				return &adu.Response{UnitID: req.UnitID, FunctionCode: 0x04, Payload: []byte{0x04, 0, 0, 0, 0}}, nil
			},
			wantErr: true,
		},
		{
			name: "other unit id",
			fn: func(req *adu.Request) (*adu.Response, error) {
				res, _ := goodDevice(req)
				res.UnitID++
				return res, nil
			},
			wantErr: true,
		},
		{
			name:    "transport error",
			fn:      func(*adu.Request) (*adu.Response, error) { return nil, ErrRequestTimedOut },
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, rec := newTappedClient(t, tc.fn)
			err := read(context.Background(), c)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			got := rec.take()
			if tc.want == nil {
				if len(got) != 0 {
					t.Fatalf("reported %+v, want nothing", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("%d exchanges reported, want 1", len(got))
			}
			if !bytes.Equal(got[0].Response, tc.want) {
				t.Errorf("response % X, want % X", got[0].Response, tc.want)
			}
			if want := []byte{0x03, 0x00, 0x10, 0x00, 0x02}; !bytes.Equal(got[0].Request, want) {
				t.Errorf("request % X, want % X", got[0].Request, want)
			}
		})
	}
}

// A request refused by AllowedFunctionCodes is not sent and not reported.
func TestPDUTap_RefusedRequest(t *testing.T) {
	rec := &tapRecorder{}
	c, err := New(Config{
		URL:                  "tcp://scripted.invalid:502",
		PDUTap:               rec,
		AllowedFunctionCodes: []FunctionCode{FCReadInputRegisters},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.ReadHoldingRegisters(context.Background(), 1, 0, 1); !errors.Is(err, ErrFunctionCodeNotAllowed) {
		t.Fatalf("err = %v, want ErrFunctionCodeNotAllowed", err)
	}
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("reported %+v, want nothing", got)
	}
}

// retryOnce retries the first failed attempt of a request.
type retryOnce struct{}

func (retryOnce) ShouldRetry(attempt int, _ error) (bool, time.Duration) { return attempt == 0, 0 }

// A request that succeeds after a retry is reported once.
func TestPDUTap_RetriedRequestReportedOnce(t *testing.T) {
	rec := &tapRecorder{}
	c, err := New(Config{URL: "tcp://scripted.invalid:502", PDUTap: rec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var calls int
	tr := &scriptedTransport{fn: func(req *adu.Request) (*adu.Response, error) {
		calls++
		if calls == 1 {
			return nil, ErrRequestTimedOut
		}
		return goodDevice(req)
	}}
	eng := intsession.NewEngine(intsession.Config{
		Dial:  func() (intsession.Transport[*adu.Request, *adu.Response], error) { return tr, nil },
		Retry: retryOnce{},
	})
	if err := eng.Open(); err != nil {
		t.Fatalf("engine Open: %v", err)
	}
	c.lock.Lock()
	c.state.engine = eng
	c.lock.Unlock()
	t.Cleanup(func() { _ = c.Close() })

	if _, err := c.ReadHoldingRegisters(context.Background(), 1, 0, 1); err != nil {
		t.Fatalf("read: %v", err)
	}
	if n := len(tr.requests()); n != 2 {
		t.Fatalf("%d attempts on the wire, want 2", n)
	}
	if got := rec.take(); len(got) != 1 {
		t.Fatalf("%d exchanges reported, want 1", len(got))
	}
}

// The tap receives the caller's context and owns the PDUs it is given.
func TestPDUTap_ContextAndOwnership(t *testing.T) {
	payload := []byte{0x02, 0xAB, 0xCD}
	c, _, rec := newTappedClient(t, func(req *adu.Request) (*adu.Response, error) {
		return &adu.Response{UnitID: req.UnitID, FunctionCode: req.FunctionCode, Payload: payload}, nil
	})
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "call-1")
	if _, err := c.ReadInputRegisters(ctx, 1, 0, 1); err != nil {
		t.Fatalf("read: %v", err)
	}
	rec.mu.Lock()
	gotCtx := rec.ctx
	rec.mu.Unlock()
	got := rec.take()
	if len(got) != 1 || len(gotCtx) != 1 {
		t.Fatalf("%d exchanges reported, want 1", len(got))
	}
	if v := gotCtx[0].Value(key{}); v != "call-1" {
		t.Errorf("context value %v, want call-1", v)
	}
	got[0].Response[1] = 0x00
	if payload[0] != 0x02 || payload[1] != 0xAB {
		t.Errorf("changing the reported response changed the transport's buffer: % X", payload)
	}
}

func TestPDUTap_Config(t *testing.T) {
	var n int
	tap := PDUTapFunc(func(context.Context, PDUExchange) { n++ })
	tap.OnPDU(context.Background(), PDUExchange{})
	if n != 1 {
		t.Fatalf("PDUTapFunc called %d times, want 1", n)
	}
	conf := NewConfig(TransportConfig{URL: "tcp://localhost:502"}, ExecutionConfig{}, ObservabilityConfig{PDUTap: tap})
	if conf.PDUTap == nil {
		t.Fatal("NewConfig dropped ObservabilityConfig.PDUTap")
	}
	c, err := New(conf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.tap == nil {
		t.Fatal("New dropped Config.PDUTap")
	}
}

// Over a real connection the tap sees the PDUs the server sent, page by page
// for a paged device identification read.
func TestE2E_PDUTap(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		rec := &tapRecorder{}
		p := e2eStart(t, kind, e2eNewDevice(), e2eOpts{client: func(c *Config) { c.PDUTap = rec }})
		ctx := context.Background()

		for _, op := range e2eAllOps() {
			err := op.call(ctx, p.client)
			if op.served && err != nil {
				t.Fatalf("%s: %v", op.name, err)
			}
			got := rec.take()
			if op.served && len(got) != op.requests {
				t.Errorf("%s: %d exchanges reported, want %d", op.name, len(got), op.requests)
				continue
			}
			for i, ex := range got {
				if ex.UnitID != e2eUnit || len(ex.Request) == 0 || len(ex.Response) == 0 {
					t.Errorf("%s: exchange %d: %+v", op.name, i, ex)
					continue
				}
				if fc := ex.Response[0] &^ 0x80; fc != ex.Request[0] {
					t.Errorf("%s: exchange %d answers 0x%02X with 0x%02X", op.name, i, ex.Request[0], ex.Response[0])
				}
				if !op.served && !reflect.DeepEqual(ex.Response, []byte{ex.Request[0] | 0x80, 0x01}) {
					t.Errorf("%s: exchange %d: response % X, want Illegal Function", op.name, i, ex.Response)
				}
			}
		}
	})
}

// A paged device identification read is reported page by page, each page as
// the device sent it.
func TestPDUTap_PagedDeviceIdentification(t *testing.T) {
	pages := map[byte][]byte{
		0x00: {0x0E, 0x03, 0x81, 0xFF, 0x01, 0x01, 0x00, 0x03, 'a', 'b', 'c'},
		0x01: {0x0E, 0x03, 0x81, 0x00, 0x00, 0x01, 0x01, 0x02, 'X', '1'},
	}
	c, _, rec := newTappedClient(t, func(req *adu.Request) (*adu.Response, error) {
		page, ok := pages[req.Payload[2]]
		if !ok {
			return excRes(req, 0x02), nil
		}
		return okRes(req, page...), nil
	})
	di, err := c.ReadAllDeviceIdentification(context.Background(), 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(di.Objects) != 2 {
		t.Fatalf("%d objects decoded, want 2", len(di.Objects))
	}
	got := rec.take()
	if len(got) != 2 {
		t.Fatalf("%d exchanges reported, want 2", len(got))
	}
	for i, start := range []byte{0x00, 0x01} {
		if want := []byte{0x2B, 0x0E, 0x03, start}; !bytes.Equal(got[i].Request, want) {
			t.Errorf("page %d: request % X, want % X", i, got[i].Request, want)
		}
		if want := append([]byte{0x2B}, pages[start]...); !bytes.Equal(got[i].Response, want) {
			t.Errorf("page %d: response % X, want % X", i, got[i].Response, want)
		}
	}
}
