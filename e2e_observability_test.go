// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"errors"
	"log"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

// e2eCheckPairs asserts that events is a sequence of request/outcome pairs with
// matching unit ID and function code, i.e. exactly one outcome per request.
func e2eCheckPairs(t *testing.T, what string, events []e2eEvent) {
	t.Helper()
	if len(events)%2 != 0 {
		t.Errorf("%s: %d metric events, want request/outcome pairs: %v", what, len(events), events)
		return
	}
	for i := 0; i < len(events); i += 2 {
		req, out := events[i], events[i+1]
		if req.Kind != "request" || out.Kind == "request" || req.FC != out.FC || req.Unit != out.Unit {
			t.Errorf("%s: events %d/%d are not a request/outcome pair: %v %v", what, i, i+1, req, out)
		}
	}
}

// Every client method reports exactly one outcome per request it puts on the
// wire, and the server reports exactly one outcome per request it receives.
func TestE2E_Metrics_OneOutcomePerRequest(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		cm, sm := &e2eMetrics{}, &e2eMetrics{}
		p := e2eStart(t, kind, dev, e2eOpts{
			server: func(c *ServerConfig) { c.Metrics = sm },
			client: func(c *Config) {
				// Exercise the grouped configuration constructor on the way.
				*c = NewConfig(
					TransportConfig{URL: c.URL, TLSClientCert: c.TLSClientCert, TLSRootCAs: c.TLSRootCAs},
					ExecutionConfig{Timeout: c.Timeout},
					ObservabilityConfig{Metrics: cm},
				)
			},
		})
		ctx := context.Background()

		for _, op := range e2eAllOps() {
			err := op.call(ctx, p.client)
			if op.served && err != nil {
				t.Fatalf("%s: %v", op.name, err)
			}
			cevents := cm.take()
			// The server reports its outcome before it writes the response.
			sevents := sm.take()
			e2eCheckPairs(t, op.name+" (client)", cevents)
			e2eCheckPairs(t, op.name+" (server)", sevents)
			if len(cevents) != 2*op.requests || len(sevents) != 2*op.requests {
				t.Errorf("%s: %d client and %d server events, want %d each", op.name, len(cevents), len(sevents), 2*op.requests)
				continue
			}
			if cevents[0].FC != op.fc || cevents[0].Unit != e2eUnit || sevents[0].FC != op.fc || sevents[0].Unit != e2eUnit {
				t.Errorf("%s: first request reported as %v (client) / %v (server)", op.name, cevents[0], sevents[0])
			}
			wantOutcome := "response"
			if !op.served {
				wantOutcome = "error"
			}
			for i := 1; i < len(cevents); i += 2 {
				if cevents[i].Kind != wantOutcome {
					t.Errorf("%s: client outcome %v, want %s", op.name, cevents[i], wantOutcome)
				}
				if !op.served && !op.probe && !errors.Is(cevents[i].Err, ErrIllegalFunction) {
					t.Errorf("%s: client error metric carries %v", op.name, cevents[i].Err)
				}
			}
			if op.served {
				for i := 1; i < len(sevents); i += 2 {
					if sevents[i].Kind != "response" {
						t.Errorf("%s: server outcome %v, want response", op.name, sevents[i])
					}
				}
			}
		}

		// Calls rejected by parameter validation are not requests.
		_, _ = p.client.ReadCoils(ctx, e2eUnit, 0, 0)
		_, _ = p.client.ReadRegisters(ctx, e2eUnit, 0, 126, HoldingRegister)
		_ = p.client.WriteCoils(ctx, e2eUnit, 0, nil)
		_ = p.client.WriteRegisters(ctx, e2eUnit, 0xFFFF, []uint16{1, 2})
		_, _ = p.client.ReadWriteMultipleRegisters(ctx, e2eUnit, 0, 0, 0, []uint16{1})
		_, _ = p.client.ReadDeviceIdentification(ctx, e2eUnit, 9, 0)
		_, _ = p.client.ReadRegisterBit(ctx, e2eUnit, 0, 16, HoldingRegister)
		_, _ = p.client.SupportsFunction(ctx, e2eUnit, FCWriteSingleCoil)
		_, _ = p.client.ReadFileRecords(ctx, e2eUnit, nil)
		if events := cm.take(); len(events) != 0 {
			t.Errorf("rejected calls produced client metrics: %v", events)
		}
		if events := sm.take(); len(events) != 0 {
			t.Errorf("rejected calls produced server metrics: %v", events)
		}

		// A paginated device identification read is one logical client request
		// and one server request per page.
		dev.mu.Lock()
		for i := 0; i < 8; i++ {
			dev.objects = append(dev.objects, DeviceIdentificationObject{ID: DeviceIDObjectID(0x80 + i), Value: string(make([]byte, 200))})
		}
		dev.mu.Unlock()
		if _, err := p.client.ReadAllDeviceIdentification(ctx, e2eUnit); err != nil {
			t.Fatal(err)
		}
		if got := e2eKinds(cm.take()); !reflect.DeepEqual(got, []string{"request:2B", "response:2B"}) {
			t.Errorf("paginated read, client metrics = %v", got)
		}
		sevents := sm.take()
		e2eCheckPairs(t, "paginated read (server)", sevents)
		if len(sevents) != 2*9 {
			t.Errorf("paginated read: %d server events, want 18", len(sevents))
		}
	})
}

// Success, exception, timeout and connection loss are classified the same way
// on both sides.
func TestE2E_Metrics_Outcomes(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		cm, sm := &e2eMetrics{}, &e2eMetrics{}
		p := e2eStart(t, kind, dev, e2eOpts{
			server: func(c *ServerConfig) { c.Metrics = sm },
			client: func(c *Config) { c.Metrics = cm },
		})
		ctx := context.Background()
		// A second client with a short Timeout, reporting to the same metrics.
		impatient := p.newClient(func(c *Config) { c.Timeout = 40 * time.Millisecond })
		if err := impatient.Open(); err != nil {
			t.Fatal(err)
		}

		// Success.
		if err := p.client.WriteRegister(ctx, 1, 1, 1); err != nil {
			t.Fatal(err)
		}
		if got := e2eKinds(cm.take()); !reflect.DeepEqual(got, []string{"request:06", "response:06"}) {
			t.Errorf("success, client metrics = %v", got)
		}
		if got := e2eKinds(sm.take()); !reflect.DeepEqual(got, []string{"request:06", "response:06"}) {
			t.Errorf("success, server metrics = %v", got)
		}

		// Exception from the handler.
		cause := errors.New("valve stuck")
		dev.setHook(func(context.Context, e2eCall) error { return errors.Join(ErrServerDeviceBusy, cause) })
		err := p.client.WriteCoils(ctx, e2eUnit, 0, []bool{true})
		e2eWantException(t, "WriteCoils", err, FCWriteMultipleCoils, exServerDeviceBusy)
		cevents, sevents := cm.take(), sm.take()
		if got := e2eKinds(cevents); !reflect.DeepEqual(got, []string{"request:0F", "error:0F"}) {
			t.Errorf("exception, client metrics = %v", got)
		} else if !errors.Is(cevents[1].Err, ErrServerDeviceBusy) || cevents[1].Unit != e2eUnit {
			t.Errorf("exception, client error metric = %v", cevents[1])
		}
		if got := e2eKinds(sevents); !reflect.DeepEqual(got, []string{"request:0F", "error:0F"}) {
			t.Errorf("exception, server metrics = %v", got)
		} else if !errors.Is(sevents[1].Err, cause) || sevents[1].Unit != e2eUnit {
			t.Errorf("exception, server error metric = %v (want the handler's error)", sevents[1])
		}

		// Timeout: the client gives up and drops the connection the request is on.
		// That cancels the handler's context; the server completes its request.
		gate := e2eNewGate()
		dev.setHook(gate.hook)
		if _, err := impatient.ReadInputRegisters(ctx, e2eUnit, 0, 2); !errors.Is(err, ErrRequestTimedOut) {
			t.Fatalf("request against a blocked handler: %v", err)
		}
		if got := e2eKinds(cm.take()); !reflect.DeepEqual(got, []string{"request:04", "timeout:04"}) {
			t.Errorf("timeout, client metrics = %v", got)
		}
		e2eEventually(t, "the server outcome", func() bool { return sm.len() == 2 })
		if got := e2eKinds(sm.take()); !reflect.DeepEqual(got, []string{"request:04", "response:04"}) {
			t.Errorf("timeout, server metrics = %v", got)
		}
		gate.open()

		// A context deadline is a timeout as well.
		gate = e2eNewGate()
		dev.setHook(gate.hook)
		short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		_, err = p.client.ReadCoils(short, e2eUnit, 0, 1)
		cancel()
		if err == nil {
			t.Fatal("request past its context deadline succeeded")
		}
		if got := e2eKinds(cm.take()); !reflect.DeepEqual(got, []string{"request:01", "timeout:01"}) {
			t.Errorf("context deadline, client metrics = %v", got)
		}
		gate.open()
		e2eEventually(t, "the server outcome", func() bool { return sm.len() == 2 })
		sm.take()

		// Connection dropped by the server: an error on both sides.
		dev.setHook(func(context.Context, e2eCall) error { return ErrProtocolError })
		if _, err := p.client.ReadDiscreteInputs(ctx, e2eUnit, 0, 1); err == nil {
			t.Fatal("request on a dropped connection succeeded")
		}
		cevents = cm.take()
		if got := e2eKinds(cevents); !reflect.DeepEqual(got, []string{"request:02", "error:02"}) {
			t.Errorf("dropped connection, client metrics = %v", got)
		}
		e2eEventually(t, "the server outcome", func() bool { return sm.len() == 2 })
		sevents = sm.take()
		if got := e2eKinds(sevents); !reflect.DeepEqual(got, []string{"request:02", "error:02"}) {
			t.Errorf("dropped connection, server metrics = %v", got)
		} else if !errors.Is(sevents[1].Err, ErrProtocolError) {
			t.Errorf("dropped connection, server error metric = %v", sevents[1])
		}

		// A closed client reports the failure, too.
		_ = p.client.Close()
		if _, err := p.client.ReadCoil(ctx, e2eUnit, 0); !errors.Is(err, ErrClientNotOpen) {
			t.Fatal(err)
		}
		cevents = cm.take()
		if got := e2eKinds(cevents); !reflect.DeepEqual(got, []string{"request:01", "error:01"}) {
			t.Errorf("closed client, client metrics = %v", got)
		} else if !errors.Is(cevents[1].Err, ErrClientNotOpen) {
			t.Errorf("closed client, error metric = %v", cevents[1])
		}
	})
}

// The server must report a request it answers with an exception as an error,
// whichever way the exception came about.
func TestE2E_Metrics_ServerReportsUnservedFunctionAsError(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		sm := &e2eMetrics{}
		p := e2eStart(t, kind, e2eBasicOnly{e2eNewDevice()}, e2eOpts{server: func(c *ServerConfig) { c.Metrics = sm }})
		ctx := context.Background()

		// An optional handler that is not implemented: Illegal Function,
		// reported as an error.
		err := p.client.MaskWriteRegister(ctx, e2eUnit, 0, 0, 0)
		e2eWantException(t, "MaskWriteRegister", err, FCMaskWriteRegister, exIllegalFunction)
		if got := e2eKinds(sm.take()); !reflect.DeepEqual(got, []string{"request:16", "error:16"}) {
			t.Errorf("unimplemented handler, server metrics = %v", got)
		}
		// A function code the server does not know: the same exception ...
		_, err = p.client.ReportServerID(ctx, e2eUnit)
		e2eWantException(t, "ReportServerID", err, FCReportServerID, exIllegalFunction)
		// ... must be reported the same way.
		events := sm.take()
		if got := e2eKinds(events); !reflect.DeepEqual(got, []string{"request:11", "error:11"}) {
			t.Fatalf("the server answers an unknown function code with an Illegal Function exception but "+
				"reports it as a success through ServerMetrics.OnResponse (server_transport.go dispatchRequest "+
				"default branch): server metrics = %v, want [request:11 error:11]", got)
		}
		if !errors.Is(events[1].Err, ErrIllegalFunction) {
			t.Errorf("server error metric carries %v", events[1].Err)
		}
	})
}

// A custom Logger on each side receives the frames as debug lines.
func TestE2E_Logging_Frames(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		dev.setHolding(0x10, 0xCAFE, 0xF00D)
		clog, srvlog := &e2eLogger{}, &e2eLogger{}
		p := e2eStart(t, kind, dev, e2eOpts{
			server: func(c *ServerConfig) { c.Logger = srvlog },
			client: func(c *Config) { c.Logger = clog },
		})
		ctx := context.Background()

		if _, err := p.client.ReadHoldingRegisters(ctx, e2eUnit, 0x10, 2); err != nil {
			t.Fatal(err)
		}
		err := p.client.MaskWriteRegister(ctx, 7, 1, 2, 3)
		e2eWantException(t, "MaskWriteRegister on unit 7", err, FCMaskWriteRegister, exGWPathUnavailable)

		const (
			reqFrame  = "00 01 00 00 00 06 01 03 00 10 00 02"
			resFrame  = "00 01 00 00 00 07 01 03 04 CA FE F0 0D"
			excFrame  = "00 02 00 00 00 03 07 96 0A"
			reqFrame2 = "00 02 00 00 00 08 07 16 00 01 00 02 00 03"
		)
		checks := []struct {
			name string
			log  *e2eLogger
			subs []string
		}{
			{"client TX", clog, []string{"D ", "tcp-transport(", "[debug]: TX: " + reqFrame}},
			{"client RX", clog, []string{"D ", "tcp-transport(", "[debug]: RX: unit=0x01 fc=0x03 payload=04 CA FE F0 0D"}},
			{"client TX (2)", clog, []string{"D ", "[debug]: TX: " + reqFrame2}},
			{"client RX exception", clog, []string{"D ", "[debug]: RX: unit=0x07 fc=0x96 payload=0A"}},
			{"server RX", srvlog, []string{"D ", "tcp-transport(", "[debug]: RX: unit=0x01 fc=0x03 payload=00 10 00 02"}},
			{"server TX", srvlog, []string{"D ", "tcp-transport(", "[debug]: TX: " + resFrame}},
			{"server TX exception", srvlog, []string{"D ", "[debug]: TX: " + excFrame}},
		}
		for _, c := range checks {
			if !c.log.has(c.subs...) {
				t.Errorf("%s: no log line containing %q in:\n%s", c.name, c.subs, c.log.dump())
			}
		}
		// The client log names the server endpoint, the server log the client.
		_, port, _ := e2eSplitHostPort(p.hostPort)
		if !clog.has("tcp-transport(", ":"+port+")") {
			t.Errorf("client log does not name the server endpoint:\n%s", clog.dump())
		}
		call := dev.takeCalls()[0]
		if !srvlog.has("tcp-transport(" + call.ClientAddr + ")") {
			t.Errorf("server log does not name the client %s:\n%s", call.ClientAddr, srvlog.dump())
		}

		// Server-side problems are logged above debug level.
		dev.setHook(func(context.Context, e2eCall) error { return ErrProtocolError })
		_, _ = p.client.ReadCoil(ctx, e2eUnit, 0)
		e2eEventually(t, "the protocol error warning", func() bool {
			return srvlog.has("W ", "protocol error, closing link", call.ClientAddr)
		})
	})
}

// The stock logger adapters work end to end as well.
func TestE2E_Logging_StockAdapters(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{noOpen: true})
		ctx := context.Background()

		var std, text, fields bytes.Buffer
		debug := &slog.HandlerOptions{Level: slog.LevelDebug}
		loggers := []struct {
			name   string
			logger Logger
			buf    *bytes.Buffer
			want   []string
		}{
			{"NewStdLogger", NewStdLogger(log.New(&std, "std: ", 0)), &std,
				[]string{"std: tcp-transport(", "[debug]: TX: 00 01 00 00 00 06 01 01 00 00 00 01"}},
			{"NewSlogLogger", NewSlogLogger(slog.NewTextHandler(&text, debug)), &text,
				[]string{"level=DEBUG", "TX: 00 01 00 00 00 06 01 01 00 00 00 01"}},
			{"NewSlogFieldLogger", NewSlogFieldLogger(slog.NewJSONHandler(&fields, debug)), &fields,
				[]string{`"level":"DEBUG"`, `"component":"tcp-transport(`, `"msg":"TX: 00 01 00 00 00 06 01 01 00 00 00 01"`}},
			{"NopLogger", NopLogger(), nil, nil},
		}
		for _, l := range loggers {
			c := p.newClient(func(conf *Config) { conf.Logger = l.logger })
			if err := c.Open(); err != nil {
				t.Fatalf("%s: %v", l.name, err)
			}
			if _, err := c.ReadCoil(ctx, e2eUnit, 0); err != nil {
				t.Fatalf("%s: %v", l.name, err)
			}
			// The client logs from the calling goroutine only.
			_ = c.Close()
			if l.buf == nil {
				continue
			}
			for _, want := range l.want {
				if !strings.Contains(l.buf.String(), want) {
					t.Errorf("%s: output lacks %q:\n%s", l.name, want, l.buf.String())
				}
			}
		}
	})
}
