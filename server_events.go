// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-modbus/secevent"
)

// SecurityStats returns the counters of the event dispatcher of the current
// run of the server, or of the last one after it stopped: how many events of
// each kind and category it saw, delivered, merged and dropped. It is the
// zero Stats when ServerConfig.Security delivers no events or the server has
// not been started.
func (ms *Server) SecurityStats() secevent.Stats {
	ms.lock.Lock()
	run := ms.run
	ms.lock.Unlock()
	if run == nil {
		return secevent.Stats{}
	}
	return run.events.Stats()
}

// startEvents creates the event dispatcher of run, when the security policy
// asks for events. ms.lock is held.
func (ms *Server) startEvents(run *serverRun) error {
	if ms.sec == nil || !ms.sec.Enabled() {
		return nil
	}
	d, owned, err := ms.sec.NewDispatcher(ms.logger.Errorf)
	if err != nil {
		return err
	}
	run.events, run.ownsEvents = d, owned
	return nil
}

// emit hands e to the dispatcher of the run, if it has one.
func (r *serverRun) emit(e secevent.Event) {
	if r.events != nil {
		r.events.Emit(e)
	}
}

// connEvents is what the events of one served connection share.
type connEvents struct {
	id       uint64
	rep      *secevent.ConnReporter
	started  time.Time
	requests atomic.Int64
}

// eventBase returns the event that carries what every event of a connection
// shares.
func (ms *Server) eventBase(id uint64, sock net.Conn) secevent.Event {
	transport := "tcp"
	if ms.transportType == modbusTCPOverTLS {
		transport = "tcp+tls"
	}
	e := secevent.Event{
		Protocol:  "modbus",
		Transport: transport,
		Role:      "server",
		ConnID:    id,
		Peer:      peerOf(sock),
	}
	if a := sock.LocalAddr(); a != nil {
		e.Local = a.String()
	}
	return e
}

// newConnEvents returns the events of a connection that is about to be served,
// and its context, which carries the reporter handlers reach through
// secevent.Report. tlsState is nil on a plain connection.
func (ms *Server) newConnEvents(run *serverRun, ctx context.Context, id uint64, sock net.Conn, role string, tlsState *tls.ConnectionState) (*connEvents, context.Context) {
	if run.events == nil {
		return nil, ctx
	}
	base := ms.eventBase(id, sock)
	base.Peer.Role = role
	if tlsState != nil {
		base.Peer.TLS = tlsPeerOf(tlsState)
	}
	ce := &connEvents{id: id, rep: secevent.NewConnReporter(run.events, base), started: time.Now()}
	return ce, secevent.NewContext(ctx, ce.rep)
}

// emitAccepted, emitRefused and emitEvicted report connections the accept loop
// saw; the zero id of an evicted one is a connection of an earlier accept.
func (ms *Server) emitAccepted(run *serverRun, id uint64, sock net.Conn) {
	e := ms.eventBase(id, sock)
	e.Kind = secevent.KindConnAccepted
	e.Detail = "connection accepted"
	run.emit(e)
}

func (ms *Server) emitRefused(run *serverRun, id uint64, sock net.Conn, reason RejectReason) {
	e := ms.eventBase(id, sock)
	e.Action = secevent.ActionRejected
	switch reason {
	case RejectNotAllowed:
		e.Kind = secevent.KindConnUnexpected
		e.Detail = "refused by ServerConfig.AllowClient"
	case RejectMaxClients:
		e.Kind = secevent.KindConnRejectedLimit
		e.Detail = fmt.Sprintf("all %d client slots are taken", ms.conf.MaxClients)
	case RejectTLSHandshakeEvicted:
		e.Kind = secevent.KindTLSHandshakeEvicted
		e.Detail = "closed to make room for a newer connection before its TLS handshake completed"
	default:
		return
	}
	run.emit(e)
}

func (ms *Server) emitAcceptError(run *serverRun, err error, retry time.Duration) {
	run.emit(secevent.Event{
		Kind:      secevent.KindConnAcceptError,
		Protocol:  "modbus",
		Role:      "server",
		Err:       err,
		Detail:    fmt.Sprintf("accept failed, next attempt in %v", retry),
		Transport: ms.transportName(),
	})
}

func (ms *Server) transportName() string {
	if ms.transportType == modbusTCPOverTLS {
		return "tcp+tls"
	}
	return "tcp"
}

// emitClosed reports how a served connection ended: the event that says what
// the reason means for a defender, then conn.closed.
func (ms *Server) emitClosed(run *serverRun, ce *connEvents, reason CloseReason, err error) {
	if ce == nil {
		return
	}
	n := ce.requests.Load()
	spec := ce.rep.Base()
	spec.Err = err
	emit := func(kind secevent.Kind, action secevent.Action, detail string) {
		e := spec
		e.Kind, e.Action, e.Detail = kind, action, detail
		run.emit(e)
	}
	switch {
	case n == 0 && reason == CloseIdleTimeout:
		emit(secevent.KindConnNoRequest, secevent.ActionDisconnected, "no request within the idle timeout")
	case n == 0 && (reason == CloseClientClosed || reason == CloseReadFailed):
		// A read that fails is usually a reset: how connect scanners close.
		emit(secevent.KindConnNoRequest, secevent.ActionObserved, "the connection ended without a request: "+reason.String())
	case reason == CloseIdleTimeout:
		emit(secevent.KindConnIdleTimeout, secevent.ActionDisconnected, fmt.Sprintf("no request for %v", ms.conf.Timeout))
	case reason == CloseMalformedFrame:
		emit(secevent.KindProtoBadMBAP, secevent.ActionDisconnected, "the MBAP header cannot be framed")
	case reason == CloseProtocolError:
		emit(secevent.KindProtoBadPDU, secevent.ActionDisconnected, "a request violated the protocol")
	case reason == CloseWriteFailed:
		emit(secevent.KindDoSWriteStall, secevent.ActionDisconnected, "a response could not be written in time")
	}
	action := secevent.ActionDisconnected
	if reason == CloseClientClosed || reason == CloseReadFailed || reason == CloseServerStopped {
		action = secevent.ActionObserved
	}
	emit(secevent.KindConnClosed, action, fmt.Sprintf("%s after %v, %d requests",
		reason, time.Since(ce.started).Round(time.Millisecond), n))
}

// emitUnserved reports the end of an accepted connection that was never
// served: one whose TLS handshake failed, or one accepted while the server was
// stopping. With it, every conn.accepted is followed by one conn.closed or one
// refusal event.
func (ms *Server) emitUnserved(run *serverRun, id uint64, sock net.Conn, detail string, err error) {
	if run.events == nil {
		return
	}
	e := ms.eventBase(id, sock)
	e.Kind = secevent.KindConnClosed
	e.Action = secevent.ActionDisconnected
	e.Detail = detail
	e.Err = err
	run.emit(e)
}

// peerOf describes the remote end of sock.
func peerOf(sock net.Conn) secevent.PeerInfo {
	return secevent.PeerInfo{
		Network:     "tcp",
		RemoteAddr:  addrPortOf(sock.RemoteAddr()),
		LocalAddr:   addrPortOf(sock.LocalAddr()),
		ConnectedAt: time.Now(),
	}
}

func addrPortOf(a net.Addr) netip.AddrPort {
	if a == nil {
		return netip.AddrPort{}
	}
	if t, ok := a.(*net.TCPAddr); ok {
		ap := t.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	}
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.AddrPort{}
	}
	return ap
}

// tlsPeerOf describes the verified client certificate of a completed
// handshake.
func tlsPeerOf(st *tls.ConnectionState) *secevent.TLSPeer {
	p := &secevent.TLSPeer{
		Version:     st.Version,
		CipherSuite: st.CipherSuite,
		ServerName:  st.ServerName,
		Verified:    len(st.VerifiedChains) > 0,
	}
	if len(st.PeerCertificates) > 0 {
		c := st.PeerCertificates[0]
		sum := sha256.Sum256(c.Raw)
		p.Subject = c.Subject.String()
		p.Issuer = c.Issuer.String()
		p.SerialHex = c.SerialNumber.Text(16)
		p.NotAfter = c.NotAfter
		p.FingerprintSHA256 = hex.EncodeToString(sum[:])
	}
	return p
}
