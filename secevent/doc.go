// SPDX-License-Identifier: MIT

// Package secevent carries security-relevant observations of a Modbus
// endpoint to the application: who connected, which connections the server
// refused and why, how each one ended.
//
// An endpoint (a modbus.Server, through ServerConfig.Security) hands every
// observation to a Dispatcher as an Event. The dispatcher never blocks the
// goroutine that observed it: it queues the event, merges repeats of the same
// kind inside a short window into one event with a count, and calls the
// application's Sink from a goroutine of its own. When the queue is full the
// event is dropped and counted, and the next event the sink receives is
// preceded by a meta.events_dropped event that says how many were lost; the
// library never drops silently.
//
// With no Sink and no Dispatcher configured nothing is built, queued or
// timed: an endpoint without a security policy behaves as it did before this
// package existed.
//
// Event kinds are part of the API. A Kind is never renamed; the catalogue
// (Kinds, Describe) gives the default category and severity of each. The
// kind names that go-iecp5 uses for the same observations (conn.*, tls.*,
// peer.*, meta.*) have the same spelling here.
//
// An event describes what the endpoint observed, not what an attacker did:
// a refused connection may be a scanner or a misconfigured master. Register
// values and write payloads never appear in Detail.
package secevent
