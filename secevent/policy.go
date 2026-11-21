// SPDX-License-Identifier: MIT

package secevent

import (
	"errors"
	"time"
)

// Policy is the part of an endpoint's security configuration that does not
// depend on the protocol. modbus.SecurityPolicy embeds it.
type Policy struct {
	// Sink receives the endpoint's events. The endpoint creates a Dispatcher
	// for it when it starts and closes it when it stops. Nil, with no
	// Dispatcher, leaves event delivery off.
	Sink Sink
	// Dispatcher is a dispatcher shared with other endpoints; the endpoint
	// uses it and does not close it. It replaces Sink: set one of the two.
	Dispatcher *Dispatcher

	// QueueSize, AggregateWindow, MaxAggregateKeys and MaxRaw configure the
	// dispatcher created for Sink (see Config); they have no effect on a
	// shared Dispatcher.
	QueueSize        int
	AggregateWindow  time.Duration
	MaxAggregateKeys int
	MaxRaw           int
}

// Valid checks the policy.
func (p *Policy) Valid() error {
	if p.Sink != nil && p.Dispatcher != nil {
		return errors.New("set Sink or Dispatcher, not both")
	}
	if p.QueueSize < 0 {
		return errors.New("QueueSize must not be negative")
	}
	if p.MaxAggregateKeys < 0 {
		return errors.New("MaxAggregateKeys must not be negative")
	}
	if p.MaxRaw < 0 {
		return errors.New("MaxRaw must not be negative")
	}
	return nil
}

// Enabled reports whether the policy delivers events: it has a Sink or a
// Dispatcher.
func (p *Policy) Enabled() bool { return p.Sink != nil || p.Dispatcher != nil }

// NewDispatcher returns the dispatcher the policy asks for: its shared one
// (owned false: the caller must not close it), or a new one for its Sink
// (owned true). It returns nil, false when the policy delivers no events.
func (p *Policy) NewDispatcher(logf func(format string, args ...any)) (d *Dispatcher, owned bool, err error) {
	if p.Dispatcher != nil {
		return p.Dispatcher, false, nil
	}
	if p.Sink == nil {
		return nil, false, nil
	}
	d, err = NewDispatcher(Config{
		Sink:             p.Sink,
		QueueSize:        p.QueueSize,
		AggregateWindow:  p.AggregateWindow,
		MaxAggregateKeys: p.MaxAggregateKeys,
		MaxRaw:           p.MaxRaw,
		Logf:             logf,
	})
	return d, true, err
}
