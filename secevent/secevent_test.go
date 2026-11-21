// SPDX-License-Identifier: MIT

package secevent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// collector is a Sink that keeps what it receives.
type collector struct {
	mu sync.Mutex
	ev []Event
}

func (c *collector) OnEvent(e Event) {
	c.mu.Lock()
	c.ev = append(c.ev, e)
	c.mu.Unlock()
}

func (c *collector) all() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.ev...)
}

// waitFor polls until cond holds, or fails the test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func newTestDispatcher(t *testing.T, cfg Config) (*Dispatcher, *collector) {
	t.Helper()
	c := &collector{}
	if cfg.Sink == nil {
		cfg.Sink = c
	}
	d, err := NewDispatcher(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close(context.Background()) })
	return d, c
}

func TestNewDispatcherNeedsSink(t *testing.T) {
	if _, err := NewDispatcher(Config{}); !errors.Is(err, ErrNoSink) {
		t.Fatalf("err = %v, want ErrNoSink", err)
	}
}

func TestDispatcherDeliversInOrderWithDefaults(t *testing.T) {
	d, c := newTestDispatcher(t, Config{AggregateWindow: -1})
	d.Emit(Event{Kind: KindConnAccepted, ConnID: 1})
	d.Emit(Event{Kind: KindConnUnexpected, ConnID: 2, Action: ActionRejected})
	d.Emit(Event{Kind: "app.custom"})
	waitFor(t, "3 events", func() bool { return len(c.all()) == 3 })
	ev := c.all()
	if ev[0].Kind != KindConnAccepted || ev[1].Kind != KindConnUnexpected || ev[2].Kind != "app.custom" {
		t.Fatalf("order: %v %v %v", ev[0].Kind, ev[1].Kind, ev[2].Kind)
	}
	if ev[0].Category != CategoryConnection || ev[0].Severity != SeverityInfo || ev[0].Action != ActionObserved ||
		ev[0].Protocol != "modbus" || ev[0].Time.IsZero() {
		t.Errorf("defaults not filled: %+v", ev[0])
	}
	if ev[1].Severity != SeverityWarning || ev[1].Technique != "T0846" || ev[1].Action != ActionRejected {
		t.Errorf("catalogue defaults: %+v", ev[1])
	}
	if ev[2].Category != 0 || ev[2].Severity != SeverityInfo {
		t.Errorf("unknown kind: %+v", ev[2])
	}
	st := d.Stats()
	if st.Events[KindConnAccepted] != 1 || st.Categories[CategoryConnection] != 2 {
		t.Errorf("stats: %+v", st)
	}
}

func TestDispatcherNeverBlocksAndReportsDrops(t *testing.T) {
	block := make(chan struct{})
	var mu sync.Mutex
	var got []Event
	sink := SinkFunc(func(e Event) {
		<-block
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})
	d, _ := newTestDispatcher(t, Config{Sink: sink, QueueSize: 4, AggregateWindow: -1})

	start := time.Now()
	for i := range 200 {
		d.Emit(Event{Kind: KindConnAccepted, ConnID: uint64(i + 1)})
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Emit blocked on a slow sink: %v", time.Since(start))
	}
	st := d.Stats()
	if st.Dropped < 190 {
		t.Fatalf("Dropped = %d, want at least 190", st.Dropped)
	}
	close(block)
	waitFor(t, "delivery", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) >= 2 })
	d.Emit(Event{Kind: KindConnClosed}) // the notice of the drops that are still unreported comes before it
	sum := func() (n uint64, other bool) {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range got {
			if e.Kind == KindMetaEventsDropped {
				n += uint64(e.Count)
				if e.Category != CategoryMeta {
					other = true
				}
			}
		}
		return n, other
	}
	waitFor(t, "the drop notices", func() bool { n, _ := sum(); return n == d.Stats().Dropped })
	if n, bad := sum(); n < 190 || bad {
		t.Errorf("notices count %d drops (bad category: %v), want at least 190", n, bad)
	}
}

func TestDispatcherMergesRepeats(t *testing.T) {
	d, c := newTestDispatcher(t, Config{AggregateWindow: 60 * time.Millisecond})
	e := Event{Kind: KindConnNoRequest, ConnID: 7, Detail: "x"}
	for range 5 {
		d.Emit(e)
	}
	d.Emit(Event{Kind: KindConnNoRequest, ConnID: 8}) // another key: not merged
	waitFor(t, "the merged event", func() bool { return len(c.all()) >= 3 })
	ev := c.all()
	if ev[0].Count != 0 || ev[0].ConnID != 7 {
		t.Errorf("first occurrence is delivered at once, alone: %+v", ev[0])
	}
	var merged Event
	for _, x := range ev {
		if x.Count > 1 {
			merged = x
		}
	}
	if merged.Count != 4 || merged.FirstSeen.IsZero() || merged.ConnID != 7 {
		t.Errorf("merged event: %+v", merged)
	}
	if d.Stats().Merged != 4 {
		t.Errorf("Merged = %d, want 4", d.Stats().Merged)
	}
	// after the window a repeat is a new first occurrence
	time.Sleep(150 * time.Millisecond)
	d.Emit(e)
	waitFor(t, "a new first occurrence", func() bool {
		n := 0
		for _, x := range c.all() {
			if x.ConnID == 7 && x.Count == 0 {
				n++
			}
		}
		return n == 2
	})
}

func TestDispatcherMergesConnectionKindsByHost(t *testing.T) {
	d, c := newTestDispatcher(t, Config{AggregateWindow: 60 * time.Millisecond})
	peer := func(ip string, port uint16) PeerInfo {
		return PeerInfo{RemoteAddr: netip.AddrPortFrom(netip.MustParseAddr(ip), port)}
	}
	for i := range 5 { // a connection flood from one host, a new port each time
		d.Emit(Event{Kind: KindConnAccepted, ConnID: uint64(10 + i), Peer: peer("192.0.2.1", uint16(40000+i))})
	}
	d.Emit(Event{Kind: KindConnAccepted, ConnID: 20, Peer: peer("192.0.2.2", 40000)}) // another host
	d.Emit(Event{Kind: KindProtoBadMBAP, ConnID: 30, Peer: peer("192.0.2.1", 1)})     // keyed per connection
	d.Emit(Event{Kind: KindProtoBadMBAP, ConnID: 31, Peer: peer("192.0.2.1", 2)})
	waitFor(t, "the merged event", func() bool { return len(c.all()) == 5 })
	var merged Event
	for _, e := range c.all() {
		if e.Count > 0 {
			merged = e
		}
	}
	if merged.Count != 4 || merged.ConnID != 14 || merged.Peer.RemoteAddr.Addr().String() != "192.0.2.1" {
		t.Errorf("merged event: %+v", merged)
	}
	if d.Stats().Merged != 4 {
		t.Errorf("Merged = %d, want 4", d.Stats().Merged)
	}
}

func TestDispatcherTimerRunsOnlyWhileMerging(t *testing.T) {
	d, c := newTestDispatcher(t, Config{AggregateWindow: 20 * time.Millisecond})
	for round := range 3 { // the timer is armed, runs out, and is armed again
		d.Emit(Event{Kind: "app.k", ConnID: 1})
		d.Emit(Event{Kind: "app.k", ConnID: 1})
		want := 2 * (round + 1)
		waitFor(t, "the merged event", func() bool { return len(c.all()) == want })
		waitFor(t, "the window to end", func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return len(d.agg) == 0
		})
	}
}

func TestDispatcherDoesNotMergeAlwaysDeliverOrWhenOff(t *testing.T) {
	d, c := newTestDispatcher(t, Config{AggregateWindow: time.Hour})
	for range 3 {
		d.Emit(Event{Kind: KindMetaSinkPanic})
	}
	waitFor(t, "3 meta events", func() bool { return len(c.all()) == 3 })

	d2, c2 := newTestDispatcher(t, Config{AggregateWindow: -1})
	for range 3 {
		d2.Emit(Event{Kind: KindConnNoRequest})
	}
	waitFor(t, "3 events with merging off", func() bool { return len(c2.all()) == 3 })
}

func TestDispatcherMergedFieldsAreKeyed(t *testing.T) {
	d, c := newTestDispatcher(t, Config{AggregateWindow: time.Hour})
	d.Emit(Event{Kind: "app.k", Fields: FieldUnitID, UnitID: 1})
	d.Emit(Event{Kind: "app.k", Fields: FieldUnitID, UnitID: 2})
	d.Emit(Event{Kind: "app.k", Fields: FieldUnitID, UnitID: 1})
	waitFor(t, "2 events", func() bool { return len(c.all()) == 2 })
	if d.Stats().Merged != 1 {
		t.Errorf("Merged = %d, want 1", d.Stats().Merged)
	}
}

func TestDispatcherBoundsAggregationKeys(t *testing.T) {
	d, c := newTestDispatcher(t, Config{AggregateWindow: time.Hour, MaxAggregateKeys: 2})
	for id := uint64(1); id <= 2; id++ {
		d.Emit(Event{Kind: "app.k", ConnID: id})
		d.Emit(Event{Kind: "app.k", ConnID: id}) // one pending repeat each
	}
	d.Emit(Event{Kind: "app.k", ConnID: 3}) // full: one pending count is delivered early
	waitFor(t, "an early merged event", func() bool {
		for _, e := range c.all() {
			if e.Count == 1 {
				return true
			}
		}
		return false
	})
	d.mu.Lock()
	n := len(d.agg)
	d.mu.Unlock()
	if n > 2 {
		t.Errorf("%d keys, want at most 2", n)
	}
}

func TestDispatcherFlushesPendingOnClose(t *testing.T) {
	c := &collector{}
	d, err := NewDispatcher(Config{Sink: c, AggregateWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	d.Emit(Event{Kind: "app.k"})
	d.Emit(Event{Kind: "app.k"})
	d.Emit(Event{Kind: "app.k"})
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	ev := c.all()
	if len(ev) != 2 || ev[1].Count != 2 {
		t.Fatalf("after Close: %+v", ev)
	}
	d.Emit(Event{Kind: "app.k"}) // after Close: ignored
	if len(c.all()) != 2 {
		t.Error("an event after Close was delivered")
	}
}

func TestDispatcherSinkPanicIsReportedOnce(t *testing.T) {
	var mu sync.Mutex
	var got []Event
	var logged int
	calls := 0
	sink := SinkFunc(func(e Event) {
		mu.Lock()
		calls++
		n := calls
		got = append(got, e)
		mu.Unlock()
		if n == 1 || n == 2 {
			panic("boom")
		}
	})
	d, _ := newTestDispatcher(t, Config{Sink: sink, AggregateWindow: -1, Logf: func(string, ...any) {
		mu.Lock()
		logged++
		mu.Unlock()
	}})
	for range 4 {
		d.Emit(Event{Kind: KindConnAccepted})
	}
	waitFor(t, "delivery", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) >= 5 })
	st := d.Stats()
	if st.SinkFailures != 2 { // the event, then the meta.sink_panic event itself
		t.Errorf("SinkFailures = %d, want 2", st.SinkFailures)
	}
	mu.Lock()
	defer mu.Unlock()
	panics := 0
	for _, e := range got {
		if e.Kind == KindMetaSinkPanic {
			panics++
		}
	}
	if panics != 1 || logged != 1 {
		t.Errorf("meta.sink_panic events = %d, log lines = %d, want 1 each", panics, logged)
	}
}

func TestCloseFromSinkDoesNotWait(t *testing.T) {
	var d *Dispatcher
	closed := make(chan error, 1)
	sink := SinkFunc(func(e Event) {
		if !d.OnGoroutine() {
			t.Error("OnGoroutine is false inside the sink")
		}
		closed <- d.Close(context.Background())
	})
	var err error
	d, err = NewDispatcher(Config{Sink: sink, AggregateWindow: -1})
	if err != nil {
		t.Fatal(err)
	}
	d.Emit(Event{Kind: KindConnAccepted})
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close from the sink: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close called from the sink waited for itself")
	}
	if d.OnGoroutine() {
		t.Error("OnGoroutine is true outside the sink")
	}
}

func TestCloseHonoursContext(t *testing.T) {
	block := make(chan struct{})
	d, err := NewDispatcher(Config{Sink: SinkFunc(func(Event) { <-block }), AggregateWindow: -1})
	if err != nil {
		t.Fatal(err)
	}
	d.Emit(Event{Kind: KindConnAccepted})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := d.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close = %v, want the context's error", err)
	}
	close(block)
	d.CloseNoWait() // idempotent
	if err := d.Close(context.Background()); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestNilDispatcherIsSafe(t *testing.T) {
	var d *Dispatcher
	d.Emit(Event{Kind: KindConnAccepted})
	d.CloseNoWait()
	if err := d.Close(context.Background()); err != nil {
		t.Error(err)
	}
	if d.OnGoroutine() {
		t.Error("nil dispatcher on goroutine")
	}
	if st := d.Stats(); st.Delivered != 0 || st.Events != nil {
		t.Errorf("stats: %+v", st)
	}
}

func TestRawIsCopiedAndCut(t *testing.T) {
	d, c := newTestDispatcher(t, Config{AggregateWindow: -1, MaxRaw: 4})
	raw := []byte{1, 2, 3, 4, 5, 6}
	d.Emit(Event{Kind: KindProtoBadPDU, Raw: raw})
	waitFor(t, "event", func() bool { return len(c.all()) == 1 })
	raw[0] = 99
	got := c.all()[0].Raw
	if !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Errorf("Raw = %v", got)
	}
}

func TestKindCatalogue(t *testing.T) {
	kinds := Kinds()
	if len(kinds) == 0 {
		t.Fatal("empty catalogue")
	}
	for i, k := range kinds {
		info, ok := Describe(k)
		if !ok || info.Category == 0 || info.Severity == 0 {
			t.Errorf("%s: incomplete entry %+v", k, info)
		}
		if i > 0 && kinds[i-1] >= k {
			t.Errorf("Kinds not sorted at %s", k)
		}
		if !strings.Contains(string(k), ".") || strings.ToLower(string(k)) != string(k) {
			t.Errorf("%s: kinds are lower case dotted codes", k)
		}
	}
	if _, ok := Describe("nope"); ok {
		t.Error("unknown kind described")
	}
}

func TestEnumStrings(t *testing.T) {
	for c, want := range map[Category]string{CategoryConnection: "connection", CategoryMeta: "meta", 0: "unspecified", 99: "Category(99)"} {
		if c.String() != want {
			t.Errorf("%d: %q", c, c.String())
		}
	}
	for s, want := range map[Severity]string{SeverityInfo: "info", SeverityNotice: "notice", SeverityWarning: "warning",
		SeverityAlert: "alert", SeverityCritical: "critical", 0: "unspecified", 99: "Severity(99)"} {
		if s.String() != want {
			t.Errorf("%d: %q", s, s.String())
		}
	}
	for a, want := range map[Action]string{ActionObserved: "observed", 0: "observed", ActionRejected: "rejected",
		ActionThrottled: "throttled", ActionDisconnected: "disconnected", ActionBlocked: "blocked", 99: "Action(99)"} {
		if a.String() != want {
			t.Errorf("%d: %q", a, a.String())
		}
	}
}

func TestEventString(t *testing.T) {
	e := Event{
		Kind: KindConnRejectedLimit, Severity: SeverityWarning, Action: ActionRejected, ConnID: 3,
		Peer:   PeerInfo{RemoteAddr: netip.MustParseAddrPort("10.0.0.7:5020"), Role: "operator"},
		Fields: FieldUnitID | FieldFunctionCode, UnitID: 4, FunctionCode: 0x2b, Count: 3,
		Detail: "all slots taken", Err: errors.New("e"),
	}
	want := "warning conn.rejected_limit action=rejected conn=3 peer=10.0.0.7:5020 role=operator unit=4 fc=0x2b count=3: all slots taken (e)"
	if got := e.String(); got != want {
		t.Errorf("String:\n got %q\nwant %q", got, want)
	}
	if got := (Event{Kind: "a.b", Severity: SeverityInfo}).String(); got != "info a.b" {
		t.Errorf("minimal: %q", got)
	}
}

func TestEventJSONIsStable(t *testing.T) {
	ts := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	full := Event{
		Time: ts, Kind: KindConnRejectedLimit, Category: CategoryConnection, Severity: SeverityWarning,
		Action: ActionRejected, Protocol: "modbus", Transport: "tcp+tls", Role: "server", ConnID: 9,
		Peer: PeerInfo{Network: "tcp", RemoteAddr: netip.MustParseAddrPort("10.0.0.7:5020"),
			LocalAddr: netip.MustParseAddrPort("10.0.0.1:802"), Role: "operator", ConnectedAt: ts,
			TLS: &TLSPeer{Version: 0x0304, CipherSuite: 0x1301, Subject: "CN=m", Verified: true}},
		Local:  "10.0.0.1:802",
		Fields: FieldUnitID | FieldFunctionCode | FieldSubFunction | FieldException | FieldAddress | FieldQuantity | FieldTransactionID | FieldExpectedTxnID,
		UnitID: 1, FunctionCode: 0x10, SubFunction: 4, Exception: 2, Address: 0, Quantity: 3, TransactionID: 7, ExpectedTxnID: 8,
		Count: 2, FirstSeen: ts, Detail: "d", Err: errors.New("boom"), Raw: []byte{0xde, 0xad}, Technique: "T0814",
	}
	const wantFull = `{"time":"2026-11-21T12:00:00Z","kind":"conn.rejected_limit","category":"connection","severity":"warning","action":"rejected",` +
		`"protocol":"modbus","transport":"tcp+tls","role":"server","conn_id":9,` +
		`"peer":{"network":"tcp","remote_addr":"10.0.0.7:5020","local_addr":"10.0.0.1:802","tls":{"version":772,"cipher_suite":4865,"subject":"CN=m","verified":true},"role":"operator","connected_at":"2026-11-21T12:00:00Z"},` +
		`"local":"10.0.0.1:802","unit_id":1,"function_code":16,"sub_function":4,"exception":2,"address":0,"quantity":3,"transaction_id":7,"expected_transaction_id":8,` +
		`"count":2,"first_seen":"2026-11-21T12:00:00Z","detail":"d","error":"boom","raw":"dead","technique":"T0814"}`
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != wantFull {
		t.Errorf("full JSON changed:\n got %s\nwant %s", b, wantFull)
	}
	const wantMin = `{"time":"2026-11-21T12:00:00Z","kind":"conn.closed","category":"unspecified","severity":"info","action":"observed"}`
	b, err = json.Marshal(Event{Time: ts, Kind: KindConnClosed, Severity: SeverityInfo})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != wantMin {
		t.Errorf("minimal JSON changed:\n got %s\nwant %s", b, wantMin)
	}
}

func TestSinks(t *testing.T) {
	var got []Event
	f := SinkFunc(func(e Event) { got = append(got, e) })
	f.OnEvent(Event{Kind: "a.b"})
	if len(got) != 1 {
		t.Fatal("SinkFunc")
	}

	ch := make(chan Event, 1)
	cs := NewChannelSink(ch)
	cs.OnEvent(Event{Kind: "a.b"})
	cs.OnEvent(Event{Kind: "a.c"}) // full
	if cs.Dropped() != 1 || (<-ch).Kind != "a.b" {
		t.Errorf("ChannelSink dropped %d", cs.Dropped())
	}

	var buf bytes.Buffer
	js := NewJSONLinesSink(&buf)
	js.OnEvent(Event{Kind: "a.b", Time: time.Unix(0, 0).UTC(), Severity: SeverityInfo})
	js.OnEvent(Event{Kind: "a.c", Time: time.Unix(0, 0).UTC(), Severity: SeverityInfo})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || js.Err() != nil || !json.Valid([]byte(lines[1])) {
		t.Errorf("JSONLinesSink: %q err=%v", buf.String(), js.Err())
	}
	bad := NewJSONLinesSink(errWriter{})
	bad.OnEvent(Event{Kind: "a.b"})
	bad.OnEvent(Event{Kind: "a.b"})
	if bad.Err() == nil {
		t.Error("write error not kept")
	}
	enc := NewJSONLinesSink(&buf)
	enc.OnEvent(Event{Kind: "a.b", Time: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}) // a year JSON cannot encode
	if enc.Err() == nil {
		t.Error("encoding error not kept")
	}

	var a, b2 int
	m := MultiSink(SinkFunc(func(Event) { a++ }), SinkFunc(func(Event) { b2++ }))
	m.OnEvent(Event{})
	if a != 1 || b2 != 1 {
		t.Error("MultiSink")
	}

	var kept []Kind
	collect := SinkFunc(func(e Event) { kept = append(kept, e.Kind) })
	fs := FilterSink(SeverityWarning, collect, CategoryConnection, CategoryTLS)
	fs.OnEvent(Event{Kind: "low", Severity: SeverityInfo, Category: CategoryConnection})
	fs.OnEvent(Event{Kind: "wrong", Severity: SeverityAlert, Category: CategoryMeta})
	fs.OnEvent(Event{Kind: "ok", Severity: SeverityAlert, Category: CategoryTLS})
	all := FilterSink(SeverityInfo, collect)
	all.OnEvent(Event{Kind: "any", Severity: SeverityInfo})
	if strings.Join([]string{string(kept[0]), string(kept[1])}, ",") != "ok,any" || len(kept) != 2 {
		t.Errorf("FilterSink kept %v", kept)
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

type levelLogger struct{ lines []string }

func (l *levelLogger) Debugf(f string, a ...any) { l.lines = append(l.lines, "debug") }
func (l *levelLogger) Infof(f string, a ...any)  { l.lines = append(l.lines, "info") }
func (l *levelLogger) Warnf(f string, a ...any)  { l.lines = append(l.lines, "warn") }
func (l *levelLogger) Errorf(f string, a ...any) { l.lines = append(l.lines, "error") }

func TestLoggerSinkLevels(t *testing.T) {
	l := &levelLogger{}
	s := LoggerSink(l)
	for _, sev := range []Severity{SeverityInfo, SeverityNotice, SeverityWarning, SeverityAlert, SeverityCritical} {
		s.OnEvent(Event{Kind: "a.b", Severity: sev})
	}
	if got := strings.Join(l.lines, ","); got != "info,info,warn,error,error" {
		t.Errorf("levels: %s", got)
	}
}

func TestContextReporter(t *testing.T) {
	ctx := context.Background()
	if Report(ctx, Event{Kind: "a.b"}) {
		t.Error("Report succeeded without a reporter")
	}
	if _, ok := PeerFromContext(ctx); ok {
		t.Error("PeerFromContext succeeded without a reporter")
	}

	d, c := newTestDispatcher(t, Config{AggregateWindow: -1})
	peer := PeerInfo{Network: "tcp", RemoteAddr: netip.MustParseAddrPort("10.1.1.1:1234"), Role: "ops"}
	rep := NewConnReporter(d, Event{Protocol: "modbus", Transport: "tcp", Role: "server", ConnID: 5, Local: "l:1", Peer: peer})
	ctx = NewContext(ctx, rep)
	if !Report(ctx, Event{Kind: "app.write_denied", Fields: FieldUnitID, UnitID: 2}) {
		t.Fatal("Report failed")
	}
	// what the event sets is kept
	Report(ctx, Event{Kind: "app.x", ConnID: 77, Transport: "t", Role: "r", Local: "own", Protocol: "p",
		Time: time.Unix(1, 0), Peer: PeerInfo{Network: "udp"}})
	waitFor(t, "2 events", func() bool { return len(c.all()) == 2 })
	e0, e1 := c.all()[0], c.all()[1]
	if e0.ConnID != 5 || e0.Transport != "tcp" || e0.Role != "server" || e0.Local != "l:1" || e0.Peer != peer || e0.Time.IsZero() {
		t.Errorf("not filled from the connection: %+v", e0)
	}
	if e1.ConnID != 77 || e1.Transport != "t" || e1.Role != "r" || e1.Local != "own" || e1.Protocol != "p" ||
		e1.Peer.Network != "udp" || !e1.Time.Equal(time.Unix(1, 0)) {
		t.Errorf("the event's own values were overwritten: %+v", e1)
	}
	got, ok := PeerFromContext(ctx)
	if !ok || got != peer {
		t.Errorf("PeerFromContext = %+v, %v", got, ok)
	}
	if rep.Base().ConnID != 5 {
		t.Error("Base")
	}
}

func TestNextConnID(t *testing.T) {
	a, b := NextConnID(), NextConnID()
	if a == 0 || b != a+1 {
		t.Errorf("%d, %d", a, b)
	}
}

func TestPolicy(t *testing.T) {
	sink := SinkFunc(func(Event) {})
	d, err := NewDispatcher(Config{Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close(context.Background()) }()

	for name, p := range map[string]Policy{
		"both":      {Sink: sink, Dispatcher: d},
		"queue":     {QueueSize: -1},
		"aggregate": {MaxAggregateKeys: -1},
		"raw":       {MaxRaw: -1},
	} {
		if p.Valid() == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if (&Policy{}).Enabled() || !(&Policy{Sink: sink}).Enabled() || !(&Policy{Dispatcher: d}).Enabled() {
		t.Error("Enabled")
	}

	got, owned, err := (&Policy{}).NewDispatcher(nil)
	if got != nil || owned || err != nil {
		t.Errorf("empty policy: %v %v %v", got, owned, err)
	}
	got, owned, err = (&Policy{Dispatcher: d}).NewDispatcher(nil)
	if got != d || owned || err != nil {
		t.Errorf("shared: %v %v %v", got, owned, err)
	}
	got, owned, err = (&Policy{Sink: sink, QueueSize: 8, MaxRaw: 8, MaxAggregateKeys: 8, AggregateWindow: -1}).NewDispatcher(nil)
	if got == nil || !owned || err != nil {
		t.Fatalf("own: %v %v %v", got, owned, err)
	}
	_ = got.Close(context.Background())
}

func TestHas(t *testing.T) {
	e := Event{Fields: FieldUnitID | FieldAddress}
	if !e.Has(FieldUnitID) || !e.Has(FieldUnitID|FieldAddress) || e.Has(FieldQuantity) || e.Has(FieldUnitID|FieldQuantity) {
		t.Error("Has")
	}
}
