// SPDX-License-Identifier: MIT

package modbus

import "github.com/otfabric/go-modbus/secevent"

// SecurityPolicy configures what an endpoint reports about the connections
// and requests it sees, and, in later releases, what it refuses. The zero
// value reports nothing and changes nothing: an endpoint behaves as if it had
// no policy until a Sink or a Dispatcher is set.
//
// See package secevent for the events and how they are delivered, and
// ServerConfig.Security for what a server reports.
type SecurityPolicy struct {
	secevent.Policy
}

// valid checks the policy, as a configuration error of field.
func (p *SecurityPolicy) valid() error {
	if err := p.Valid(); err != nil {
		return newConfigurationError("Security", err.Error())
	}
	return nil
}
