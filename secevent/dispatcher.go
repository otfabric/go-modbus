// SPDX-License-Identifier: MIT

package secevent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-modbus/internal/callback"
	"github.com/otfabric/go-modbus/internal/goid"
)

// Defaults of Config.
const (
	DefaultQueueSize        = 1024
	DefaultAggregateWindow  = time.Second
	DefaultMaxAggregateKeys = 4096
	DefaultMaxRaw           = 64
)

// ErrNoSink is returned by NewDispatcher when Config.Sink is nil.
var ErrNoSink = errors.New("secevent: no sink")

// Config configures a Dispatcher. Zero values take the defaults.
type Config struct {
	// Sink receives the events. Required.
	Sink Sink
	// QueueSize is the number of events that may wait for the sink; further
	// ones are dropped and counted. Default DefaultQueueSize.
	QueueSize int
	// AggregateWindow is how long repeats of an event are merged into one:
	// the first occurrence is delivered at once, the ones that follow inside
	// the window are counted and delivered together as one event (Count,
	// FirstSeen) when it ends. Zero is DefaultAggregateWindow; a negative
	// value turns merging off.
	AggregateWindow time.Duration
	// MaxAggregateKeys bounds the memory of the merging: when that many
	// distinct events are being merged, the pending count of one is
	// delivered early to make room. Default DefaultMaxAggregateKeys.
	MaxAggregateKeys int
	// MaxRaw is the most bytes of Event.Raw kept. Default DefaultMaxRaw.
	MaxRaw int
	// Logf, when set, is told of a sink that did not return normally
	// (with the stack).
	Logf func(format string, args ...any)
}

type aggKey struct {
	conn   uint64
	kind   Kind
	fields FieldMask
	unit   uint8
	fc     FunctionCode
	sub    uint16
	addr   uint16
}

type aggEntry struct {
	first           time.Time
	suppressed      int
	firstSuppressed time.Time
	last            Event
}

// Dispatcher queues events and delivers them to a Sink from a goroutine of
// its own. Emit never blocks. See the package documentation.
type Dispatcher struct {
	sink   Sink
	window time.Duration
	maxKey int
	maxRaw int
	logf   func(format string, args ...any)
	now    func() time.Time

	queue chan Event
	stop  chan struct{}
	done  chan struct{}
	gid   atomic.Uint64
	once  sync.Once

	mu     sync.Mutex
	closed bool
	agg    map[aggKey]*aggEntry
	events map[Kind]uint64
	cats   map[Category]uint64

	merged         atomic.Uint64
	delivered      atomic.Uint64
	droppedTotal   atomic.Uint64
	droppedPending atomic.Uint64
	sinkFailures   atomic.Uint64
}

// NewDispatcher starts a dispatcher that delivers to cfg.Sink. Close it when
// it is no longer needed.
func NewDispatcher(cfg Config) (*Dispatcher, error) {
	if cfg.Sink == nil {
		return nil, ErrNoSink
	}
	d := &Dispatcher{
		sink:   cfg.Sink,
		window: cfg.AggregateWindow,
		maxKey: cfg.MaxAggregateKeys,
		maxRaw: cfg.MaxRaw,
		logf:   cfg.Logf,
		now:    time.Now,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		agg:    make(map[aggKey]*aggEntry),
		events: make(map[Kind]uint64),
		cats:   make(map[Category]uint64),
	}
	if d.window == 0 {
		d.window = DefaultAggregateWindow
	}
	if d.maxKey <= 0 {
		d.maxKey = DefaultMaxAggregateKeys
	}
	if d.maxRaw <= 0 {
		d.maxRaw = DefaultMaxRaw
	}
	size := cfg.QueueSize
	if size <= 0 {
		size = DefaultQueueSize
	}
	d.queue = make(chan Event, size)
	go d.run()
	return d, nil
}

// Emit hands e to the dispatcher. It never blocks: when the queue is full the
// event is dropped and counted. Unset Time, Category, Severity, Action,
// Protocol and Technique are filled in (from the catalogue, for a known
// kind). It does nothing on a nil or closed dispatcher.
func (d *Dispatcher) Emit(e Event) {
	if d == nil {
		return
	}
	now := d.now()
	if e.Time.IsZero() {
		e.Time = now
	}
	applyDefaults(&e)
	if n := len(e.Raw); n > 0 {
		if n > d.maxRaw {
			n = d.maxRaw
		}
		e.Raw = append([]byte(nil), e.Raw[:n]...)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.countLocked(&e)
	if d.window > 0 && !catalogue[e.Kind].AlwaysDeliver {
		key := aggKey{conn: e.ConnID, kind: e.Kind, fields: e.Fields,
			unit: e.UnitID, fc: e.FunctionCode, sub: e.SubFunction, addr: e.Address}
		if ent, ok := d.agg[key]; ok {
			if now.Sub(ent.first) < d.window {
				if ent.suppressed == 0 {
					ent.firstSuppressed = e.Time
				}
				ent.suppressed++
				ent.last = e
				d.merged.Add(1)
				return
			}
			d.flushLocked(ent)
			delete(d.agg, key)
		}
		if len(d.agg) >= d.maxKey {
			for k, ent := range d.agg {
				d.flushLocked(ent)
				delete(d.agg, k)
				break
			}
		}
		d.agg[key] = &aggEntry{first: now}
	}
	d.enqueueLocked(e)
}

func (d *Dispatcher) countLocked(e *Event) {
	d.events[e.Kind]++
	d.cats[e.Category]++
}

// flushLocked queues the merged event of ent, if it has repeats.
func (d *Dispatcher) flushLocked(ent *aggEntry) {
	if ent.suppressed == 0 {
		return
	}
	ev := ent.last
	ev.Count = ent.suppressed
	ev.FirstSeen = ent.firstSuppressed
	d.enqueueLocked(ev)
}

func (d *Dispatcher) enqueueLocked(e Event) {
	select {
	case d.queue <- e:
	default:
		d.droppedTotal.Add(1)
		d.droppedPending.Add(1)
	}
}

func (d *Dispatcher) run() {
	defer close(d.done)
	d.gid.Store(goid.ID())
	var tick <-chan time.Time
	if d.window > 0 {
		iv := min(max(d.window/2, 5*time.Millisecond), time.Second)
		t := time.NewTicker(iv)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case e := <-d.queue:
			d.deliver(e)
		case <-tick:
			d.sweep(d.now(), false)
		case <-d.stop:
			// closed is set before stop is closed, and Emit checks it under
			// the lock that queues, so nothing is queued after this drain.
			d.drain()
			d.sweep(d.now(), true)
			d.drain()
			return
		}
	}
}

func (d *Dispatcher) drain() {
	for {
		select {
		case e := <-d.queue:
			d.deliver(e)
		default:
			return
		}
	}
}

// sweep ends the windows that are over (all of them when all is set) and
// delivers the merged events.
func (d *Dispatcher) sweep(now time.Time, all bool) {
	var out []Event
	d.mu.Lock()
	for k, ent := range d.agg {
		if !all && now.Sub(ent.first) < d.window {
			continue
		}
		if ent.suppressed > 0 {
			ev := ent.last
			ev.Count = ent.suppressed
			ev.FirstSeen = ent.firstSuppressed
			out = append(out, ev)
		}
		delete(d.agg, k)
	}
	d.mu.Unlock()
	for _, e := range out {
		d.deliver(e)
	}
}

// deliver passes e to the sink, after the notice of the events that were
// dropped since the last delivery.
func (d *Dispatcher) deliver(e Event) {
	if n := d.droppedPending.Swap(0); n > 0 {
		d.deliverOne(d.synthetic(KindMetaEventsDropped, int(n),
			fmt.Sprintf("%d events were dropped because the queue to the sink was full", n)))
	}
	d.deliverOne(e)
}

func (d *Dispatcher) synthetic(kind Kind, count int, detail string) Event {
	e := Event{Time: d.now(), Kind: kind, Count: count, Detail: detail}
	applyDefaults(&e)
	d.mu.Lock()
	d.countLocked(&e)
	d.mu.Unlock()
	return e
}

func (d *Dispatcher) deliverOne(e Event) {
	fail, _ := callback.Run("secevent.Sink", func() error {
		d.sink.OnEvent(e)
		return nil
	})
	d.delivered.Add(1)
	if fail == nil {
		return
	}
	if d.sinkFailures.Add(1) > 1 {
		return
	}
	if d.logf != nil {
		d.logf("secevent: the sink did not return normally: %v\n%s", fail.Value, fail.Stack)
	}
	// Reported once; a sink that fails on this one too is not asked again.
	n := d.synthetic(KindMetaSinkPanic, 0, fmt.Sprintf("the sink did not return normally: %v", fail.Value))
	_, _ = callback.Run("secevent.Sink", func() error {
		d.sink.OnEvent(n)
		return nil
	})
}

// Stats returns a snapshot of the counters.
func (d *Dispatcher) Stats() Stats {
	if d == nil {
		return Stats{}
	}
	s := Stats{
		Delivered:    d.delivered.Load(),
		Merged:       d.merged.Load(),
		Dropped:      d.droppedTotal.Load(),
		SinkFailures: d.sinkFailures.Load(),
	}
	d.mu.Lock()
	s.Events = make(map[Kind]uint64, len(d.events))
	for k, v := range d.events {
		s.Events[k] = v
	}
	s.Categories = make(map[Category]uint64, len(d.cats))
	for k, v := range d.cats {
		s.Categories[k] = v
	}
	d.mu.Unlock()
	return s
}

// OnGoroutine reports whether the caller runs on the dispatcher's goroutine,
// that is, inside the sink.
func (d *Dispatcher) OnGoroutine() bool {
	return d != nil && d.gid.Load() == goid.ID()
}

// CloseNoWait stops the dispatcher accepting events. The events already
// queued are still delivered, then the goroutine ends. It does not wait.
func (d *Dispatcher) CloseNoWait() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		close(d.stop)
	})
}

// Close stops the dispatcher accepting events and waits until the ones queued
// have been delivered, or ctx ends. Called from the sink itself it does not
// wait (it would wait for itself): the queued events are delivered after the
// sink returns.
func (d *Dispatcher) Close(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.CloseNoWait()
	if d.OnGoroutine() {
		return nil
	}
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
