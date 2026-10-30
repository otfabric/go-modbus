// SPDX-License-Identifier: MIT

package batch_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-modbus"
	"github.com/otfabric/go-modbus/batch"
	"github.com/otfabric/go-modbus/codec"
	"github.com/otfabric/go-modbus/internal/benchserver"
)

// fault is what faultHandler does with a request.
type fault struct {
	err  error         // returned as a Modbus exception
	hang bool          // no response until the request's context ends or a minute passes
	seen chan struct{} // closed when the request arrives, if set
}

// request is one request a faultHandler received.
type request struct {
	unit uint8
	fc   modbus.FunctionCode
	addr uint16
}

// faultHandler serves benchserver data, and injects faults by unit, function
// code and start address. It logs every request it receives.
type faultHandler struct {
	benchserver.Handler
	faults map[request]fault

	mu  sync.Mutex
	log []request
}

func (h *faultHandler) inject(ctx context.Context, r request) error {
	h.mu.Lock()
	h.log = append(h.log, r)
	h.mu.Unlock()
	f, ok := h.faults[r]
	if !ok {
		return nil
	}
	if f.seen != nil {
		close(f.seen)
	}
	if f.hang {
		select {
		case <-ctx.Done():
		case <-time.After(time.Minute):
		}
		return modbus.ErrServerDeviceFailure
	}
	return f.err
}

func (h *faultHandler) requests() []request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]request(nil), h.log...)
}

func (h *faultHandler) HandleCoils(ctx context.Context, req *modbus.CoilsRequest) ([]bool, error) {
	if err := h.inject(ctx, request{req.UnitID, fc01, req.Addr}); err != nil {
		return nil, err
	}
	return h.Handler.HandleCoils(ctx, req)
}

func (h *faultHandler) HandleDiscreteInputs(ctx context.Context, req *modbus.DiscreteInputsRequest) ([]bool, error) {
	if err := h.inject(ctx, request{req.UnitID, fc02, req.Addr}); err != nil {
		return nil, err
	}
	return h.Handler.HandleDiscreteInputs(ctx, req)
}

func (h *faultHandler) HandleHoldingRegisters(ctx context.Context, req *modbus.HoldingRegistersRequest) ([]uint16, error) {
	if err := h.inject(ctx, request{req.UnitID, fc03, req.Addr}); err != nil {
		return nil, err
	}
	return h.Handler.HandleHoldingRegisters(ctx, req)
}

func (h *faultHandler) HandleInputRegisters(ctx context.Context, req *modbus.InputRegistersRequest) ([]uint16, error) {
	if err := h.inject(ctx, request{req.UnitID, fc04, req.Addr}); err != nil {
		return nil, err
	}
	return h.Handler.HandleInputRegisters(ctx, req)
}

// shortTimeout gives the client a timeout that a hanging request reaches
// quickly.
func shortTimeout(c *modbus.Config) { c.Timeout = 200 * time.Millisecond }

func served(addr, count uint16) []uint16 {
	out := make([]uint16, count)
	for i := range out {
		out[i] = benchserver.RegisterValue(addr + uint16(i))
	}
	return out
}

func servedBits(addr, count uint16) []bool {
	out := make([]bool, count)
	for i := range out {
		out[i] = benchserver.BitValue(addr + uint16(i))
	}
	return out
}

// checkServed checks that every OK item holds the data the server serves.
func checkServed(t *testing.T, p *batch.Plan, res *batch.Result) {
	t.Helper()
	for i, it := range p.Items() {
		ir := res.Items[i]
		if ir.State != batch.OK {
			continue
		}
		if it.FC == fc01 || it.FC == fc02 {
			if !reflect.DeepEqual(ir.Bits, servedBits(it.Addr, it.Count)) {
				t.Errorf("item %q: bits %v, served %v", it.Name, ir.Bits, servedBits(it.Addr, it.Count))
			}
		} else if !reflect.DeepEqual(ir.Registers, served(it.Addr, it.Count)) {
			t.Errorf("item %q: registers %v, served %v", it.Name, ir.Registers, served(it.Addr, it.Count))
		}
	}
}

func windowStates(res *batch.Result) []batch.State {
	out := make([]batch.State, len(res.Windows))
	for i, w := range res.Windows {
		out[i] = w.State
	}
	return out
}

func itemStates(res *batch.Result) map[string]batch.State {
	out := map[string]batch.State{}
	for _, ir := range res.Items {
		out[ir.Name] = ir.State
	}
	return out
}

func execError(t *testing.T, err error) *batch.ExecError {
	t.Helper()
	var ee *batch.ExecError
	if !errors.As(err, &ee) || !errors.Is(err, batch.ErrIncomplete) {
		t.Fatalf("err = %v, want an *ExecError matching ErrIncomplete", err)
	}
	return ee
}

// TestExecute_ReducesRequests reads every benchmark read set through a plan
// and checks that the server saw one request per window, fewer than one per
// read (except for the gapped reads planned without gaps, one window per
// read), and that every item holds what the server serves.
func TestExecute_ReducesRequests(t *testing.T) {
	h := &faultHandler{}
	c := benchserver.Start(t, h)
	for _, set := range benchserver.ReadSets() {
		for _, st := range strategies {
			t.Run(set.Name+"/"+st.name, func(t *testing.T) {
				p, err := batch.Compile(setItems(set), st.opts)
				if err != nil {
					t.Fatal(err)
				}
				h.Reset()
				res, err := p.Execute(context.Background(), c)
				if err != nil {
					t.Fatal(err)
				}
				reduced := set.Name != "gapped" || st.opts.MaxGap > 0
				if got := h.Requests(); got != int64(len(p.Windows())) || (reduced && got >= int64(len(set.Reads))) {
					t.Errorf("%d requests for %d windows and %d reads", got, len(p.Windows()), len(set.Reads))
				}
				for i, w := range res.Windows {
					if w.State != batch.OK || w.Err != nil || w.Started.IsZero() || w.Finished.Before(w.Started) {
						t.Errorf("window %d = %+v", i, w)
					}
				}
				if res.Started.IsZero() || res.Finished.Before(res.Started) {
					t.Errorf("result timing %v..%v", res.Started, res.Finished)
				}
				checkServed(t, p, res)
			})
		}
	}
}

// TestExecute_ThreeWindowsThenTimeout reads five windows; the fourth gets
// no response. The first three keep their data, the item split across the
// third and fourth keeps its prefix and is Failed, and the fifth window is
// not attempted unless ContinueOnTransportError is set.
func TestExecute_ThreeWindowsThenTimeout(t *testing.T) {
	f32 := codec.MustRuntimeCodecByID("float32/layout:4321")
	items := []batch.Item{
		{Name: "f0", UnitID: 1, FC: fc03, Addr: 0, Codec: f32},
		{Name: "f1", UnitID: 1, FC: fc03, Addr: 10, Codec: f32},
		item("split", 1, fc03, 20, 4),
		{Name: "f3", UnitID: 1, FC: fc03, Addr: 30, Codec: f32},
	}
	h := &faultHandler{faults: map[request]fault{{1, fc03, 22}: {hang: true}}}
	c := benchserver.Start(t, h, shortTimeout)

	for _, cont := range []bool{false, true} {
		t.Run(fmt.Sprintf("ContinueOnTransportError=%v", cont), func(t *testing.T) {
			p, err := batch.Compile(items, batch.Options{MaxRegisters: 2, ContinueOnTransportError: cont})
			if err != nil {
				t.Fatal(err)
			}
			if n := len(p.Windows()); n != 5 {
				t.Fatalf("%d windows, want 5", n)
			}
			h.mu.Lock()
			h.log = nil
			h.mu.Unlock()

			res, err := p.Execute(context.Background(), c)
			ee := execError(t, err)
			if !errors.Is(err, modbus.ErrRequestTimedOut) {
				t.Errorf("err = %v, want it to match ErrRequestTimedOut", err)
			}

			last, attempted, notAttempted := batch.NotAttempted, 4, 1
			if cont {
				last, attempted, notAttempted = batch.OK, 5, 0
			}
			want := []batch.State{batch.OK, batch.OK, batch.OK, batch.Failed, last}
			if got := windowStates(res); !reflect.DeepEqual(got, want) {
				t.Fatalf("window states %v, want %v", got, want)
			}
			if n := len(h.requests()); n != attempted {
				t.Errorf("server saw %d requests, want %d", n, attempted)
			}
			if ee.Failed != 1 || ee.NotAttempted != notAttempted || ee.Total != 5 || !errors.Is(ee.First, modbus.ErrRequestTimedOut) {
				t.Errorf("ExecError = %+v", ee)
			}
			if w := res.Windows[3]; w.Registers != nil || w.Started.IsZero() {
				t.Errorf("failed window = %+v", w)
			}
			if !cont {
				w := res.Windows[4]
				if !errors.Is(w.Err, batch.ErrNotAttempted) || !errors.Is(w.Err, modbus.ErrRequestTimedOut) || !w.Started.IsZero() {
					t.Errorf("window 4 = %+v, want NotAttempted wrapping the timeout", w)
				}
				if !errors.Is(ee.Stop, modbus.ErrRequestTimedOut) {
					t.Errorf("Stop = %v", ee.Stop)
				}
			} else if ee.Stop != nil {
				t.Errorf("Stop = %v, want nil when every window was attempted", ee.Stop)
			}

			byName := map[string]batch.ItemResult{}
			for _, ir := range res.Items {
				byName[ir.Name] = ir
			}
			for _, name := range []string{"f0", "f1"} {
				ir := byName[name]
				want, _ := f32.DecodeRegistersAny(ir.Registers)
				if ir.State != batch.OK || ir.Value == nil || ir.Value != want {
					t.Errorf("%s = %+v, want OK with its value", name, ir)
				}
			}
			split := byName["split"]
			var we *batch.WindowError
			if split.State != batch.Failed || !reflect.DeepEqual(split.Registers, served(20, 2)) || !errors.As(split.Err, &we) || we.Window != 3 {
				t.Errorf("split = %+v, want Failed with the prefix of window 2", split)
			}
			if f3 := byName["f3"]; f3.State != last {
				t.Errorf("f3 = %+v, want %v", f3, last)
			}
			checkServed(t, p, res)
		})
	}
}

// TestExecute_SplitCodecItem checks that an item with a codec, split across
// windows, is decoded only when every window is read.
func TestExecute_SplitCodecItem(t *testing.T) {
	f32 := codec.MustRuntimeCodecByID("float32/layout:4321")
	p, err := batch.Compile([]batch.Item{{Name: "f", UnitID: 1, FC: fc03, Addr: 100, Codec: f32}}, batch.Options{MaxRegisters: 1})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Windows()); n != 2 {
		t.Fatalf("%d windows, want 2", n)
	}

	c := benchserver.Start(t, &faultHandler{})
	res, err := p.Execute(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := f32.DecodeRegistersAny(served(100, 2))
	if ir := res.Items[0]; ir.State != batch.OK || ir.Value != want {
		t.Errorf("whole item = %+v, want %v", ir, want)
	}

	h := &faultHandler{faults: map[request]fault{{1, fc03, 101}: {err: modbus.ErrIllegalDataAddress}}}
	c = benchserver.Start(t, h)
	res, err = p.Execute(context.Background(), c)
	_ = execError(t, err)
	ir := res.Items[0]
	if ir.State != batch.Failed || ir.Value != nil || !reflect.DeepEqual(ir.Registers, served(100, 1)) || !errors.Is(ir.Err, modbus.ErrIllegalDataAddress) {
		t.Errorf("item = %+v, want Failed with one register and no value", ir)
	}
}

// TestExecute_ExceptionsFailOnlyTheirWindow checks that Modbus exceptions,
// from the device or a gateway, fail their window and the batch goes on.
func TestExecute_ExceptionsFailOnlyTheirWindow(t *testing.T) {
	h := &faultHandler{faults: map[request]fault{
		{1, fc03, 10}: {err: modbus.ErrIllegalDataAddress},
		{2, fc04, 0}:  {err: modbus.ErrGWTargetFailedToRespond},
	}}
	c := benchserver.Start(t, h)
	p, err := batch.Compile([]batch.Item{
		item("u1a", 1, fc03, 0, 2),
		item("u1b", 1, fc03, 10, 2),
		item("u1c", 1, fc01, 0, 5),
		item("u2", 2, fc04, 0, 4),
		item("u3", 3, fc02, 7, 3),
	}, batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Execute(context.Background(), c)
	ee := execError(t, err)
	if ee.Failed != 2 || ee.NotAttempted != 0 || ee.Stop != nil || !errors.Is(err, modbus.ErrIllegalDataAddress) {
		t.Errorf("ExecError = %+v", ee)
	}
	want := map[string]batch.State{"u1a": batch.OK, "u1b": batch.Failed, "u1c": batch.OK, "u2": batch.Failed, "u3": batch.OK}
	if got := itemStates(res); !reflect.DeepEqual(got, want) {
		t.Errorf("item states %v, want %v", got, want)
	}
	for _, ir := range res.Items {
		var exc *modbus.ExceptionError
		if ir.Name == "u2" && (!errors.As(ir.Err, &exc) || !errors.Is(ir.Err, modbus.ErrGWTargetFailedToRespond)) {
			t.Errorf("u2 err = %v, want the gateway exception", ir.Err)
		}
	}
	if n := len(h.requests()); n != len(p.Windows()) {
		t.Errorf("server saw %d requests for %d windows", n, len(p.Windows()))
	}
	checkServed(t, p, res)
}

// TestExecute_FunctionCodeNotAllowed checks that a window the client refuses
// to send fails alone, without a request.
func TestExecute_FunctionCodeNotAllowed(t *testing.T) {
	h := &faultHandler{}
	c := benchserver.Start(t, h, func(cfg *modbus.Config) {
		cfg.AllowedFunctionCodes = []modbus.FunctionCode{fc03}
	})
	p, err := batch.Compile([]batch.Item{item("coils", 1, fc01, 0, 8), item("regs", 1, fc03, 0, 2)}, batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Execute(context.Background(), c)
	if !errors.Is(err, modbus.ErrFunctionCodeNotAllowed) {
		t.Fatalf("err = %v", err)
	}
	if got := windowStates(res); !reflect.DeepEqual(got, []batch.State{batch.Failed, batch.OK}) {
		t.Errorf("window states %v", got)
	}
	if got := h.requests(); len(got) != 1 || got[0].fc != fc03 {
		t.Errorf("server saw %v, want only the FC03 request", got)
	}
}

// TestExecute_Cancellation covers a context cancelled before the batch, one
// cancelled during a request, and a deadline reached during a request.
func TestExecute_Cancellation(t *testing.T) {
	items := []batch.Item{item("a", 1, fc03, 0, 2), item("b", 1, fc03, 10, 2), item("c", 1, fc03, 20, 2)}
	p, err := batch.Compile(items, batch.Options{ContinueOnTransportError: true})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("before", func(t *testing.T) {
		h := &faultHandler{}
		c := benchserver.Start(t, h)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res, err := p.Execute(ctx, c)
		ee := execError(t, err)
		if ee.NotAttempted != 3 || ee.Failed != 0 || ee.First != nil || !errors.Is(err, context.Canceled) {
			t.Errorf("ExecError = %+v", ee)
		}
		for _, ir := range res.Items {
			if ir.State != batch.NotAttempted || !errors.Is(ir.Err, context.Canceled) {
				t.Errorf("item %+v", ir)
			}
		}
		if n := len(h.requests()); n != 0 {
			t.Errorf("server saw %d requests", n)
		}
	})

	t.Run("during", func(t *testing.T) {
		seen := make(chan struct{})
		h := &faultHandler{faults: map[request]fault{{1, fc03, 10}: {hang: true, seen: seen}}}
		c := benchserver.Start(t, h)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-seen
			cancel()
		}()
		start := time.Now()
		res, err := p.Execute(ctx, c)
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("Execute took %v after cancellation", d)
		}
		ee := execError(t, err)
		if !errors.Is(err, context.Canceled) || !errors.Is(ee.Stop, context.Canceled) {
			t.Errorf("err = %v, Stop = %v; want both to match context.Canceled", err, ee.Stop)
		}
		want := []batch.State{batch.OK, batch.Failed, batch.NotAttempted}
		if got := windowStates(res); !reflect.DeepEqual(got, want) {
			t.Errorf("window states %v, want %v (cancellation ends the batch despite ContinueOnTransportError)", got, want)
		}
		if n := len(h.requests()); n != 2 {
			t.Errorf("server saw %d requests, want 2", n)
		}
		checkServed(t, p, res)
	})

	t.Run("deadline", func(t *testing.T) {
		h := &faultHandler{faults: map[request]fault{{1, fc03, 10}: {hang: true}}}
		c := benchserver.Start(t, h)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		res, err := p.Execute(ctx, c)
		ee := execError(t, err)
		if !errors.Is(ee.Stop, context.DeadlineExceeded) {
			t.Errorf("Stop = %v, want context.DeadlineExceeded", ee.Stop)
		}
		want := []batch.State{batch.OK, batch.Failed, batch.NotAttempted}
		if got := windowStates(res); !reflect.DeepEqual(got, want) {
			t.Errorf("window states %v, want %v", got, want)
		}
	})
}

// TestExecute_Concurrent executes one plan from many goroutines on one
// client, with writes interleaved, and checks that results are correct and
// the plan is unchanged.
func TestExecute_Concurrent(t *testing.T) {
	h := &faultHandler{}
	c := benchserver.Start(t, h)
	set := benchserver.ReadSets()[2] // mixed: two units, three function codes
	p, err := batch.Compile(setItems(set), batch.Options{MaxGap: 2})
	if err != nil {
		t.Fatal(err)
	}
	windows, items := p.Windows(), p.Items()

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := 0; g < 32; g++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				res, err := p.Execute(context.Background(), c)
				if err != nil {
					errs <- err
					return
				}
				// Scribble on the result: the plan must not see it.
				res.Windows[0].Items[0] = 99
				res.Windows[0].Addr = 9999
				checkServed(t, p, res)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				// benchserver refuses writes: an exception, between batch requests.
				if err := c.WriteRegister(context.Background(), 1, 0, 1); !errors.Is(err, modbus.ErrIllegalFunction) {
					errs <- fmt.Errorf("WriteRegister: %w", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if !reflect.DeepEqual(p.Windows(), windows) || !reflect.DeepEqual(p.Items(), items) {
		t.Error("executing changed the plan")
	}
}

// TestExecute_NoGoroutines checks that Execute leaves no goroutines behind.
func TestExecute_NoGoroutines(t *testing.T) {
	c := benchserver.Start(t, &faultHandler{})
	p, err := batch.Compile(setItems(benchserver.ReadSets()[2]), batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	for i := 0; i < 1000; i++ {
		if _, err := p.Execute(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("%d goroutines before 1000 executions, %d after", before, after)
	}
}

// step is what fakeReader does for one window.
type step struct {
	err    error
	short  bool // return one value too few
	cancel bool // cancel the context, then return its error
}

// fakeReader plays one step per request, in order.
type fakeReader struct {
	steps  []step
	cancel context.CancelFunc
	n      int
}

func (f *fakeReader) next(ctx context.Context, quantity uint16) (int, error) {
	s := step{}
	if f.n < len(f.steps) {
		s = f.steps[f.n]
	}
	f.n++
	if s.cancel {
		f.cancel()
		return 0, ctx.Err()
	}
	if s.err != nil {
		return 0, s.err
	}
	if s.short {
		return int(quantity) - 1, nil
	}
	return int(quantity), nil
}

func (f *fakeReader) ReadRegisters(ctx context.Context, _ uint8, _, quantity uint16, _ modbus.RegType) ([]uint16, error) {
	n, err := f.next(ctx, quantity)
	if err != nil {
		return nil, err
	}
	return make([]uint16, n), nil
}

func (f *fakeReader) ReadCoils(ctx context.Context, _ uint8, _, quantity uint16) ([]bool, error) {
	n, err := f.next(ctx, quantity)
	if err != nil {
		return nil, err
	}
	return make([]bool, n), nil
}

func (f *fakeReader) ReadDiscreteInputs(ctx context.Context, unit uint8, addr, quantity uint16) ([]bool, error) {
	return f.ReadCoils(ctx, unit, addr, quantity)
}

// TestExecute_StateMachine checks window states and the error for scripted
// reader outcomes on four windows.
func TestExecute_StateMachine(t *testing.T) {
	exc := &modbus.ExceptionError{FunctionCode: fc03, ExceptionCode: 0x02, Sentinel: modbus.ErrIllegalDataAddress}
	transport := fmt.Errorf("read: %w", modbus.ErrRequestTimedOut)
	const (
		O = batch.OK
		F = batch.Failed
		N = batch.NotAttempted
	)
	tests := []struct {
		name  string
		cont  bool
		steps []step
		want  []batch.State
		calls int
		stop  error // what Stop must match; nil for none
	}{
		{"all ok", false, nil, []batch.State{O, O, O, O}, 4, nil},
		{"exception", false, []step{{}, {err: exc}}, []batch.State{O, F, O, O}, 4, nil},
		{"not allowed", false, []step{{err: modbus.ErrFunctionCodeNotAllowed}}, []batch.State{F, O, O, O}, 4, nil},
		{"transport stops", false, []step{{}, {}, {}, {err: transport}}, []batch.State{O, O, O, F}, 4, nil},
		{"transport stops early", false, []step{{}, {err: transport}}, []batch.State{O, F, N, N}, 2, modbus.ErrRequestTimedOut},
		{"transport continues", true, []step{{}, {err: transport}, {err: transport}}, []batch.State{O, F, F, O}, 4, nil},
		{"short response stops", false, []step{{short: true}}, []batch.State{F, N, N, N}, 1, modbus.ErrProtocolError},
		{"cancel stops despite continue", true, []step{{}, {cancel: true}}, []batch.State{O, F, N, N}, 2, context.Canceled},
	}
	p, err := batch.Compile([]batch.Item{
		item("a", 1, fc03, 0, 2), item("b", 1, fc04, 0, 2), item("c", 1, fc01, 0, 2), item("d", 1, fc02, 0, 2),
	}, batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, err := batch.Compile(p.Items(), batch.Options{ContinueOnTransportError: tc.cont})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := &fakeReader{steps: tc.steps, cancel: cancel}
			res, err := q.Execute(ctx, r)
			if got := windowStates(res); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("window states %v, want %v", got, tc.want)
			}
			if r.n != tc.calls {
				t.Errorf("%d reads, want %d", r.n, tc.calls)
			}
			var failed, notAttempted int
			for _, s := range tc.want {
				switch s {
				case F:
					failed++
				case N:
					notAttempted++
				case O, batch.DecodeFailed:
				}
			}
			if failed+notAttempted == 0 {
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}
				return
			}
			ee := execError(t, err)
			if ee.Failed != failed || ee.NotAttempted != notAttempted || ee.Total != 4 {
				t.Errorf("ExecError = %+v", ee)
			}
			if (tc.stop == nil) != (ee.Stop == nil) || (tc.stop != nil && !errors.Is(ee.Stop, tc.stop)) {
				t.Errorf("Stop = %v, want %v", ee.Stop, tc.stop)
			}
			for i, w := range res.Windows {
				if w.State == N && (!errors.Is(w.Err, batch.ErrNotAttempted) || !errors.Is(w.Err, tc.stop)) {
					t.Errorf("window %d err = %v, want ErrNotAttempted and %v", i, w.Err, tc.stop)
				}
			}
		})
	}
}

func TestExecute_NilReader(t *testing.T) {
	p, err := batch.Compile([]batch.Item{item("a", 1, fc03, 0, 1)}, batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Execute(context.Background(), nil)
	var ve *batch.ValidationError
	if res != nil || !errors.As(err, &ve) {
		t.Errorf("Execute(nil) = (%v, %v), want a *ValidationError", res, err)
	}
}

func TestExecError(t *testing.T) {
	first := &batch.WindowError{Window: 1, Err: modbus.ErrIllegalDataAddress}
	e := &batch.ExecError{Failed: 2, NotAttempted: 1, Total: 4, First: first, Stop: context.Canceled}
	if got := e.Error(); !strings.HasPrefix(got, "batch: 2 of 4 windows failed, 1 not attempted: ") || !strings.Contains(got, "illegal data address") {
		t.Errorf("Error() = %q", got)
	}
	for _, target := range []error{batch.ErrIncomplete, modbus.ErrIllegalDataAddress, context.Canceled} {
		if !errors.Is(e, target) {
			t.Errorf("ExecError does not match %v", target)
		}
	}
	if errors.Is(e, batch.ErrNotAttempted) {
		t.Error("ExecError matches ErrNotAttempted")
	}
	onlyStop := &batch.ExecError{NotAttempted: 3, Total: 3, Stop: context.Canceled}
	if got := onlyStop.Error(); !strings.HasSuffix(got, context.Canceled.Error()) {
		t.Errorf("Error() = %q, want the stop reason", got)
	}
}
