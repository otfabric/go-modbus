// SPDX-License-Identifier: MIT

package secevent

import (
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
)

// Sink receives events. A Dispatcher calls it from one goroutine, one event
// at a time; it may block briefly, but a slow sink makes the queue fill and
// events drop. A sink that does not return normally is stopped there and
// reported once (meta.sink_panic).
type Sink interface {
	OnEvent(Event)
}

// SinkFunc adapts a function to the Sink interface.
type SinkFunc func(Event)

// OnEvent calls f(e).
func (f SinkFunc) OnEvent(e Event) { f(e) }

// NewChannelSink returns a Sink that sends events to ch without blocking: an
// event is dropped, and counted, when ch is full.
func NewChannelSink(ch chan<- Event) *ChannelSink {
	return &ChannelSink{ch: ch}
}

// ChannelSink is the Sink NewChannelSink returns.
type ChannelSink struct {
	ch      chan<- Event
	dropped atomic.Uint64
}

// OnEvent sends e to the channel, or counts it as dropped.
func (c *ChannelSink) OnEvent(e Event) {
	select {
	case c.ch <- e:
	default:
		c.dropped.Add(1)
	}
}

// Dropped returns the number of events lost to a full channel.
func (c *ChannelSink) Dropped() uint64 { return c.dropped.Load() }

// NewJSONLinesSink returns a Sink that writes each event as one JSON object on a
// line of w (a file, stdout, a socket). The first write error is kept (Err)
// and stops further writes.
func NewJSONLinesSink(w io.Writer) *JSONLinesSink {
	return &JSONLinesSink{w: w}
}

// JSONLinesSink is the Sink NewJSONLinesSink returns.
type JSONLinesSink struct {
	mu  sync.Mutex
	w   io.Writer
	err error
}

// OnEvent writes e as a line of JSON.
func (s *JSONLinesSink) OnEvent(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		s.err = err
		return
	}
	_, s.err = s.w.Write(append(b, '\n'))
}

// Err returns the first error a write or an encoding returned.
func (s *JSONLinesSink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// MultiSink returns a Sink that passes every event to each of sinks, in
// order. One that does not return normally ends the call: the ones after it
// do not see that event.
func MultiSink(sinks ...Sink) Sink {
	return SinkFunc(func(e Event) {
		for _, s := range sinks {
			s.OnEvent(e)
		}
	})
}

// FilterSink returns a Sink that passes on the events of at least severity min
// and, when categories are given, of one of them.
func FilterSink(min Severity, s Sink, categories ...Category) Sink {
	return SinkFunc(func(e Event) {
		if e.Severity < min {
			return
		}
		if len(categories) > 0 {
			ok := false
			for _, c := range categories {
				if c == e.Category {
					ok = true
					break
				}
			}
			if !ok {
				return
			}
		}
		s.OnEvent(e)
	})
}

// Logger is what LoggerSink needs of a logger; modbus.Logger satisfies it.
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// LoggerSink returns a Sink that writes each event to l with the prefix
// "[CYBER]": info and notice events at info level, warnings at warn level,
// alerts and critical events at error level.
func LoggerSink(l Logger) Sink {
	return SinkFunc(func(e Event) {
		switch {
		case e.Severity >= SeverityAlert:
			l.Errorf("[CYBER] %s", e)
		case e.Severity == SeverityWarning:
			l.Warnf("[CYBER] %s", e)
		default:
			l.Infof("[CYBER] %s", e)
		}
	})
}
