// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/connid"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// connRecorder records the connection events of an engine or a pool.
type connRecorder struct {
	mu     sync.Mutex
	events []ConnEvent
}

func (r *connRecorder) observe(ev ConnEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *connRecorder) snapshot() []ConnEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ConnEvent(nil), r.events...)
}

// checkLifecycles checks the invariants of a finished event stream: every
// identifier is dialled once and first; every successful dial ends with
// exactly one Discarded or Closed (all, when the engine or pool was closed),
// and a failed dial with none. It returns the number of successful dials.
func checkLifecycles(t *testing.T, events []ConnEvent, closed bool) int {
	t.Helper()
	type life struct {
		dialed, ok bool
		ends       int
	}
	lives := map[uint64]*life{}
	for _, ev := range events {
		if ev.ID == 0 {
			t.Fatalf("event without an id: %+v", ev)
		}
		l := lives[ev.ID]
		switch ev.Kind {
		case ConnDialed:
			if l != nil {
				t.Fatalf("id %d dialled twice", ev.ID)
			}
			lives[ev.ID] = &life{dialed: true, ok: ev.Err == nil}
		case ConnDiscarded, ConnClosed:
			if l == nil {
				t.Fatalf("id %d ended before it was dialled: %+v", ev.ID, ev)
			}
			if !l.ok {
				t.Fatalf("failed dial %d reported ended: %+v", ev.ID, ev)
			}
			l.ends++
		default:
			t.Fatalf("unknown kind %+v", ev)
		}
	}
	n := 0
	for id, l := range lives {
		if l.ok {
			n++
		}
		if l.ends > 1 || (closed && l.ok && l.ends != 1) {
			t.Errorf("id %d: %d terminal events, want exactly one", id, l.ends)
		}
	}
	return n
}

// countKinds counts the events of each kind.
func countKinds(events []ConnEvent) (dialed, discarded, closed int) {
	for _, ev := range events {
		switch ev.Kind {
		case ConnDialed:
			dialed++
		case ConnDiscarded:
			discarded++
		case ConnClosed:
			closed++
		}
	}
	return
}

// funcTransport answers with fn.
type funcTransport struct {
	fn     func(ctx context.Context, req *adu.Request) (*adu.Response, error)
	closed atomic.Bool
}

func (f *funcTransport) ExecuteRequest(ctx context.Context, req *adu.Request) (*adu.Response, error) {
	if f.closed.Load() {
		return nil, io.ErrClosedPipe
	}
	return f.fn(ctx, req)
}

func (f *funcTransport) Close() error { f.closed.Store(true); return nil }

// The single connection: Open's dial, the ID it gets in ctx, the response's
// ConnectionID, a discard after a transport error, the redial, and the close.
func TestConnEvents_Single(t *testing.T) {
	rec := &connRecorder{}
	var dialIDs []uint64
	fail := atomic.Bool{}
	boom := errors.New("boom")
	e := NewEngine(Config{
		Connections: rec.observe,
		Dial: func(ctx context.Context) (aduTransport, error) {
			dialIDs = append(dialIDs, connid.From(ctx))
			return &funcTransport{fn: func(_ context.Context, req *adu.Request) (*adu.Response, error) {
				if fail.Load() {
					return nil, boom
				}
				return &adu.Response{UnitID: req.UnitID}, nil
			}}, nil
		},
	})
	if err := e.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := e.Execute(context.Background(), &adu.Request{UnitID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.ConnectionID == 0 || res.ConnectionID != dialIDs[0] {
		t.Fatalf("response ConnectionID %d, want the dial's %v", res.ConnectionID, dialIDs)
	}
	fail.Store(true)
	if _, err := e.Execute(context.Background(), &adu.Request{UnitID: 1}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	fail.Store(false)
	res, err = e.Execute(context.Background(), &adu.Request{UnitID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(dialIDs) != 2 || res.ConnectionID != dialIDs[1] || dialIDs[0] == dialIDs[1] {
		t.Fatalf("dial ids %v, response %d: want a second, different connection", dialIDs, res.ConnectionID)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	evs := rec.snapshot()
	checkLifecycles(t, evs, true)
	want := []struct {
		kind   ConnEventKind
		id     uint64
		origin ConnOrigin
		reason DiscardReason
	}{
		{ConnDialed, dialIDs[0], OriginOpen, 0},
		{ConnDiscarded, dialIDs[0], 0, DiscardTransportError},
		{ConnDialed, dialIDs[1], OriginRedial, 0},
		{ConnClosed, dialIDs[1], 0, 0},
	}
	if len(evs) != len(want) {
		t.Fatalf("events %+v, want %d", evs, len(want))
	}
	for i, w := range want {
		ev := evs[i]
		if ev.Kind != w.kind || ev.ID != w.id || ev.Origin != w.origin || ev.Reason != w.reason || ev.Pooled {
			t.Errorf("event %d = %+v, want %+v", i, ev, w)
		}
	}
	if !errors.Is(evs[1].Err, boom) {
		t.Errorf("discard err = %v, want boom", evs[1].Err)
	}
}

// A retry drops the single connection even after an error that does not
// break it (DiscardRetry); a suspect response drops it too.
func TestConnEvents_SingleRetryAndSuspect(t *testing.T) {
	rec := &connRecorder{}
	soft := errors.New("soft")
	var calls atomic.Int32
	e := NewEngine(Config{
		Connections: rec.observe,
		Retry:       fixedRetry{max: 1},
		Broken:      func(err error) bool { return !errors.Is(err, soft) },
		Suspect:     func(_ *adu.Request, res *adu.Response) bool { return res.UnitID == 0xEE },
		Dial: func(context.Context) (aduTransport, error) {
			return &funcTransport{fn: func(_ context.Context, req *adu.Request) (*adu.Response, error) {
				switch calls.Add(1) {
				case 1:
					return nil, soft
				case 3:
					return &adu.Response{UnitID: 0xEE}, nil
				}
				return &adu.Response{UnitID: req.UnitID}, nil
			}}, nil
		},
	})
	if err := e.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), &adu.Request{UnitID: 1}); err != nil {
		t.Fatalf("retried request: %v", err)
	}
	if _, err := e.Execute(context.Background(), &adu.Request{UnitID: 1}); err != nil {
		t.Fatalf("suspect request: %v", err)
	}
	_ = e.Close()

	evs := rec.snapshot()
	checkLifecycles(t, evs, true)
	var reasons []DiscardReason
	for _, ev := range evs {
		if ev.Kind == ConnDiscarded {
			reasons = append(reasons, ev.Reason)
			if ev.Reason == DiscardRetry && !errors.Is(ev.Err, soft) {
				t.Errorf("retry discard err = %v, want soft", ev.Err)
			}
		}
	}
	if len(reasons) != 2 || reasons[0] != DiscardRetry || reasons[1] != DiscardSuspectResponse {
		t.Fatalf("discard reasons %v, want [retry suspect]", reasons)
	}
}

// A failed redial is reported once, with its error and an id of its own, and
// ends nothing.
func TestConnEvents_FailedRedial(t *testing.T) {
	rec := &connRecorder{}
	dialErr := errors.New("refused")
	var n atomic.Int32
	e := NewEngine(Config{
		Connections: rec.observe,
		Dial: func(context.Context) (aduTransport, error) {
			if n.Add(1) > 1 {
				return nil, dialErr
			}
			return &funcTransport{fn: func(context.Context, *adu.Request) (*adu.Response, error) {
				return nil, io.EOF
			}}, nil
		},
	})
	if err := e.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _ = e.Execute(context.Background(), &adu.Request{})
	if _, err := e.Execute(context.Background(), &adu.Request{}); !errors.Is(err, dialErr) {
		t.Fatalf("err = %v, want the dial error", err)
	}
	_ = e.Close()
	evs := rec.snapshot()
	if ok := checkLifecycles(t, evs, true); ok != 1 {
		t.Fatalf("%d successful dials, want 1", ok)
	}
	last := evs[len(evs)-1]
	if last.Kind != ConnDialed || last.Origin != OriginRedial || !errors.Is(last.Err, dialErr) {
		t.Fatalf("last event %+v, want the failed redial", last)
	}
}

// Pre-warming: MinConns dials with OriginOpen; a failing pre-warm dial
// discards the ones already made with DiscardOpenFailed.
func TestConnEvents_PoolPrewarm(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		rec := &connRecorder{}
		e := NewEngine(Config{UsePool: true, MinConns: 2, MaxConns: 3, Connections: rec.observe, Dial: newFakeDial()})
		if err := e.Open(context.Background()); err != nil {
			t.Fatal(err)
		}
		evs := rec.snapshot()
		if len(evs) != 2 {
			t.Fatalf("events %+v, want 2 dials", evs)
		}
		for _, ev := range evs {
			if ev.Kind != ConnDialed || ev.Origin != OriginOpen || !ev.Pooled || ev.Err != nil {
				t.Errorf("event %+v, want a pooled Open dial", ev)
			}
		}
		_ = e.Close()
		evs = rec.snapshot()
		checkLifecycles(t, evs, true)
		if _, _, closed := countKinds(evs); closed != 2 {
			t.Fatalf("%d closed, want 2", closed)
		}
	})
	t.Run("failure", func(t *testing.T) {
		rec := &connRecorder{}
		dialErr := errors.New("refused")
		var n atomic.Int32
		e := NewEngine(Config{UsePool: true, MinConns: 3, MaxConns: 3, Connections: rec.observe,
			Dial: func(context.Context) (aduTransport, error) {
				if n.Add(1) == 3 {
					return nil, dialErr
				}
				return &fakeTransport{}, nil
			}})
		if err := e.Open(context.Background()); !errors.Is(err, dialErr) {
			t.Fatalf("Open = %v, want the dial error", err)
		}
		evs := rec.snapshot()
		checkLifecycles(t, evs, true)
		dialed, discarded, closed := countKinds(evs)
		if dialed != 3 || discarded != 2 || closed != 0 {
			t.Fatalf("events %+v: want 3 dials, 2 discards", evs)
		}
		for _, ev := range evs {
			if ev.Kind == ConnDiscarded && (ev.Reason != DiscardOpenFailed || !errors.Is(ev.Err, dialErr)) {
				t.Errorf("discard %+v, want open failed with the dial error", ev)
			}
		}
	})
}

// Pool exhaustion: with every connection in use, a waiting request dials
// nothing and takes the connection released first; a discarded one is
// replaced by an on-demand dial.
func TestConnEvents_PoolExhaustion(t *testing.T) {
	rec := &connRecorder{}
	var made []*blockingTransport
	var mu sync.Mutex
	e := NewEngine(Config{UsePool: true, MaxConns: 2, Connections: rec.observe,
		Dial: func(context.Context) (aduTransport, error) {
			b := newBlockingTransport()
			mu.Lock()
			made = append(made, b)
			mu.Unlock()
			return b, nil
		}})
	if err := e.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan *adu.Response, 3)
	for range 2 {
		wg.Go(func() {
			res, _ := e.Execute(context.Background(), &adu.Request{UnitID: 1})
			results <- res
		})
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(made) == 2 })
	for _, b := range made {
		<-b.entered
	}
	wg.Go(func() {
		res, _ := e.Execute(context.Background(), &adu.Request{UnitID: 1})
		results <- res
	})
	time.Sleep(20 * time.Millisecond)
	if d, _, _ := countKinds(rec.snapshot()); d != 2 {
		t.Fatalf("%d dials while exhausted, want 2", d)
	}
	close(made[0].release)
	res := <-results
	<-made[0].entered
	res2 := <-results
	if res == nil || res2 == nil || res.ConnectionID != res2.ConnectionID {
		t.Fatalf("responses %+v, %+v: the waiter should reuse the released connection", res, res2)
	}
	// Fail the second connection: it is discarded, and the next request dials.
	_ = made[1].Close()
	if r := <-results; r != nil {
		t.Fatalf("closed connection answered %+v", r)
	}
	wg.Wait()
	if _, disc, _ := countKinds(rec.snapshot()); disc != 1 {
		t.Fatalf("%d discards, want 1", disc)
	}
	// Both slots are free: the idle first connection serves this one.
	close(made[1].release)
	go func() { _, _ = e.Execute(context.Background(), &adu.Request{UnitID: 1}) }()
	<-made[0].entered
	_ = e.Close()

	evs := rec.snapshot()
	checkLifecycles(t, evs, true)
	for _, ev := range evs {
		if ev.Kind == ConnDialed && (ev.Origin != OriginOnDemand || !ev.Pooled) {
			t.Errorf("dial %+v, want pooled on demand", ev)
		}
		if ev.Kind == ConnDiscarded && ev.Reason != DiscardTransportError {
			t.Errorf("discard %+v, want transport error", ev)
		}
	}
}

// A dial cancelled by its request's context is reported with the context's
// error; the slot is free again.
func TestConnEvents_CancelledDial(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		rec := &connRecorder{}
		var block atomic.Bool
		cfg := Config{Connections: rec.observe, Dial: func(ctx context.Context) (aduTransport, error) {
			if block.Load() {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return &funcTransport{fn: func(context.Context, *adu.Request) (*adu.Response, error) { return nil, io.EOF }}, nil
		}}
		if pooled {
			cfg.UsePool, cfg.MaxConns = true, 2
		}
		e := NewEngine(cfg)
		if err := e.Open(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !pooled {
			_, _ = e.Execute(context.Background(), &adu.Request{}) // drops the connection
		}
		block.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := e.Execute(ctx, &adu.Request{})
		cancel()
		if err == nil {
			t.Fatal("cancelled dial succeeded")
		}
		_ = e.Close()
		evs := rec.snapshot()
		checkLifecycles(t, evs, true)
		last := evs[len(evs)-1]
		if last.Kind != ConnDialed || !errors.Is(last.Err, context.DeadlineExceeded) || last.Pooled != pooled {
			t.Errorf("pooled=%v: last event %+v, want the cancelled dial", pooled, last)
		}
	}
}

// Concurrent failures on a pool, then Close while requests are in flight:
// every connection that was dialled ends exactly once, Discarded or Closed,
// and identifiers are never shared.
func TestConnEvents_ConcurrentFailuresAndCloseRace(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		rec := &connRecorder{}
		var n atomic.Int64
		cfg := Config{Connections: rec.observe, Retry: fixedRetry{max: 2},
			Dial: func(context.Context) (aduTransport, error) {
				return &funcTransport{fn: func(ctx context.Context, req *adu.Request) (*adu.Response, error) {
					if n.Add(1)%3 == 0 {
						return nil, io.ErrUnexpectedEOF
					}
					time.Sleep(time.Duration(n.Load()%3) * time.Millisecond)
					return &adu.Response{UnitID: req.UnitID}, nil
				}}, nil
			}}
		if pooled {
			cfg.UsePool, cfg.MinConns, cfg.MaxConns = true, 1, 4
		}
		e := NewEngine(cfg)
		if err := e.Open(context.Background()); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				for range 20 {
					if _, err := e.Execute(context.Background(), &adu.Request{UnitID: 1}); errors.Is(err, protocol.ErrClientNotOpen) {
						return
					}
				}
			})
		}
		time.Sleep(5 * time.Millisecond)
		_ = e.Close()
		wg.Wait()
		evs := rec.snapshot()
		if ok := checkLifecycles(t, evs, true); ok == 0 {
			t.Fatalf("pooled=%v: no dials", pooled)
		}
		// Nothing is reported after Close returned and the requests ended.
		if got := len(rec.snapshot()); got != len(evs) {
			t.Errorf("pooled=%v: events after the end", pooled)
		}
	}
}

// A dial that completes after Close is reported closed; so is a connection
// Open dialled when Close ran meanwhile.
func TestConnEvents_DialAfterClose(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		rec := &connRecorder{}
		gate := make(chan struct{})
		entered := make(chan struct{}, 1)
		var n atomic.Int32
		cfg := Config{Connections: rec.observe, Dial: func(context.Context) (aduTransport, error) {
			if n.Add(1) == 2 {
				entered <- struct{}{}
				<-gate
			}
			return &funcTransport{fn: func(context.Context, *adu.Request) (*adu.Response, error) { return nil, io.EOF }}, nil
		}}
		if pooled {
			cfg.UsePool, cfg.MaxConns = true, 2
		}
		e := NewEngine(cfg)
		if err := e.Open(context.Background()); err != nil {
			t.Fatal(err)
		}
		// The first request fails and drops its connection (the single one
		// Open dialled, or the pool's first, dialled on demand); the second
		// dials again, and that dial is held until Close has run.
		_, _ = e.Execute(context.Background(), &adu.Request{})
		done := make(chan error, 1)
		go func() { _, err := e.Execute(context.Background(), &adu.Request{}); done <- err }()
		<-entered
		_ = e.Close()
		close(gate)
		if err := <-done; !errors.Is(err, protocol.ErrClientNotOpen) {
			t.Errorf("pooled=%v: request = %v, want ErrClientNotOpen", pooled, err)
		}
		evs := rec.snapshot()
		checkLifecycles(t, evs, true)
		last := evs[len(evs)-1]
		if last.Kind != ConnClosed || last.Pooled != pooled {
			t.Errorf("pooled=%v: last event %+v, want the late dial closed", pooled, last)
		}
	}

	// Open loses the race with Close.
	rec := &connRecorder{}
	g := newGatedDial()
	e := NewEngine(Config{Connections: rec.observe, Dial: g.dial})
	errc := make(chan error, 1)
	go func() { errc <- e.Open(context.Background()) }()
	<-g.dialling
	_ = e.Close()
	close(g.proceed)
	if err := <-errc; !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Fatalf("Open = %v, want ErrClientNotOpen", err)
	}
	evs := rec.snapshot()
	checkLifecycles(t, evs, true)
	if len(evs) != 2 || evs[1].Kind != ConnClosed {
		t.Fatalf("events %+v, want a dial and a close", evs)
	}
}

// Pool.Execute discards after an
// error with DiscardTransportError.
func TestConnEvents_PoolExecuteDiscard(t *testing.T) {
	rec := &connRecorder{}
	boom := errors.New("boom")
	p, err := newPool(context.Background(), 1, 1, func(context.Context) (aduTransport, error) {
		return &fakeTransport{execErr: boom}, nil
	}, rec.observe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Execute(context.Background(), &adu.Request{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	_ = p.CloseAll()
	_ = p.CloseAll()
	evs := rec.snapshot()
	checkLifecycles(t, evs, true)
	if len(evs) != 2 || evs[1].Kind != ConnDiscarded || evs[1].Reason != DiscardTransportError || !errors.Is(evs[1].Err, boom) {
		t.Fatalf("events %+v", evs)
	}
}

// A dial that reports success without a transport is reported failed, with
// ErrClientNotOpen, and the connection has no end.
func TestConnEvents_NilTransport(t *testing.T) {
	rec := &connRecorder{}
	p, err := newPool(context.Background(), 0, 1, func(context.Context) (aduTransport, error) { return nil, nil }, rec.observe)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.acquireConn(context.Background()); !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Fatalf("acquire = %v, want ErrClientNotOpen", err)
	}
	evs := rec.snapshot()
	checkLifecycles(t, evs, true)
	if len(evs) != 1 || evs[0].Kind != ConnDialed || !errors.Is(evs[0].Err, protocol.ErrClientNotOpen) || !evs[0].Pooled {
		t.Fatalf("events %+v, want one failed pooled dial", evs)
	}
}

// A second CloseAll, and the release of a connection CloseAll shut, report
// nothing more: the connection in use is closed once.
func TestConnEvents_PoolCloseAllTwice(t *testing.T) {
	rec := &connRecorder{}
	ft := &funcTransport{fn: func(context.Context, *adu.Request) (*adu.Response, error) { return &adu.Response{}, nil }}
	p, err := newPool(context.Background(), 0, 1, func(context.Context) (aduTransport, error) { return ft, nil }, rec.observe)
	if err != nil {
		t.Fatal(err)
	}
	tr, _, err := p.acquireConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CloseAll(); err != nil {
		t.Fatal(err)
	}
	if err := p.CloseAll(); err != nil {
		t.Fatal(err)
	}
	p.release(tr)
	if !ft.closed.Load() {
		t.Error("the connection in use was not closed")
	}
	evs := rec.snapshot()
	checkLifecycles(t, evs, true)
	if d, x, c := countKinds(evs); d != 1 || x != 0 || c != 1 {
		t.Fatalf("events %+v, want a dial and one close", evs)
	}
}

// CloseDeferred closes the connections and reports them only when report
// runs, so that the caller can finish closing before the application's code
// runs; Close reports them itself.
func TestConnEvents_CloseDeferred(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		rec := &connRecorder{}
		var trs []*funcTransport
		var mu sync.Mutex
		cfg := Config{Connections: rec.observe, Dial: func(context.Context) (aduTransport, error) {
			ft := &funcTransport{fn: func(context.Context, *adu.Request) (*adu.Response, error) { return &adu.Response{}, nil }}
			mu.Lock()
			trs = append(trs, ft)
			mu.Unlock()
			return ft, nil
		}}
		if pooled {
			cfg.UsePool, cfg.MinConns, cfg.MaxConns = true, 2, 2
		}
		e := NewEngine(cfg)
		if err := e.Open(context.Background()); err != nil {
			t.Fatal(err)
		}
		report, err := e.CloseDeferred()
		if err != nil {
			t.Fatal(err)
		}
		for _, ft := range trs {
			if !ft.closed.Load() {
				t.Errorf("pooled=%v: a connection is still open after CloseDeferred", pooled)
			}
		}
		if _, _, c := countKinds(rec.snapshot()); c != 0 {
			t.Fatalf("pooled=%v: %d closes reported before report ran", pooled, c)
		}
		report()
		evs := rec.snapshot()
		checkLifecycles(t, evs, true)
		for _, ev := range evs {
			if ev.Pooled != pooled {
				t.Errorf("pooled=%v: event %+v", pooled, ev)
			}
		}
		if _, _, c := countKinds(evs); c != len(trs) {
			t.Errorf("pooled=%v: %d closes for %d connections", pooled, c, len(trs))
		}
		// A second close has nothing left to report.
		report, err = e.CloseDeferred()
		if err != nil {
			t.Fatal(err)
		}
		report()
		if n := len(rec.snapshot()); n != len(evs) {
			t.Errorf("pooled=%v: the second close reported %d events", pooled, n-len(evs))
		}
	}
}

// A pooled connection whose response Config.Suspect flags is discarded with
// DiscardSuspectResponse, and the response still carries its ConnectionID.
func TestConnEvents_PoolSuspect(t *testing.T) {
	rec := &connRecorder{}
	e := NewEngine(Config{
		Connections: rec.observe, UsePool: true, MaxConns: 2,
		Suspect: func(*adu.Request, *adu.Response) bool { return true },
		Dial: func(context.Context) (aduTransport, error) {
			return &funcTransport{fn: func(context.Context, *adu.Request) (*adu.Response, error) { return &adu.Response{}, nil }}, nil
		},
	})
	if err := e.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := e.Execute(context.Background(), &adu.Request{})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.Close()
	evs := rec.snapshot()
	checkLifecycles(t, evs, true)
	if len(evs) != 2 || evs[1].Kind != ConnDiscarded || evs[1].Reason != DiscardSuspectResponse || !evs[1].Pooled {
		t.Fatalf("events %+v, want a dial and a suspect discard", evs)
	}
	if res.ConnectionID != evs[0].ID {
		t.Errorf("response ConnectionID %d, want %d", res.ConnectionID, evs[0].ID)
	}
}

// transport on a closed engine dials nothing.
func TestConnEvents_TransportAfterClose(t *testing.T) {
	rec := &connRecorder{}
	e := NewEngine(Config{Connections: rec.observe, Dial: func(context.Context) (aduTransport, error) {
		return &funcTransport{fn: func(context.Context, *adu.Request) (*adu.Response, error) { return &adu.Response{}, nil }}, nil
	}})
	if err := e.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = e.Close()
	if tr, id, err := e.transport(context.Background(), 1); !errors.Is(err, protocol.ErrClientNotOpen) || tr != nil || id != 0 {
		t.Fatalf("transport = %v, %d, %v; want ErrClientNotOpen", tr, id, err)
	}
	if _, _, c := countKinds(rec.snapshot()); c != 1 {
		t.Errorf("events %+v", rec.snapshot())
	}
}

// A caller that waited for a slot and gets one after the pool closed hands
// the slot back and dials nothing.
func TestConnEvents_PoolClosedWhileWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &connRecorder{}
		p, err := newPool(context.Background(), 0, 1, func(context.Context) (aduTransport, error) {
			return &funcTransport{fn: func(context.Context, *adu.Request) (*adu.Response, error) { return &adu.Response{}, nil }}, nil
		}, rec.observe)
		if err != nil {
			t.Fatal(err)
		}
		<-p.slots // the only slot is taken
		errc := make(chan error, 1)
		go func() { _, _, err := p.acquireConn(context.Background()); errc <- err }()
		synctest.Wait() // blocked waiting for a slot
		// The pool closes as the slot comes back (CloseAll has not woken the
		// waiters yet).
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		p.slots <- struct{}{}
		if err := <-errc; !errors.Is(err, errPoolClosed) {
			t.Fatalf("acquire = %v, want errPoolClosed", err)
		}
		if len(p.slots) != 1 {
			t.Error("the slot was not handed back")
		}
		if evs := rec.snapshot(); len(evs) != 0 {
			t.Errorf("events %+v, want none", evs)
		}
	})
}
