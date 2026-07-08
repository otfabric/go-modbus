// SPDX-License-Identifier: MIT

// Package session owns the execution layer above transports.
//
// It manages:
//   - Connection pooling (Pool)
//   - Retry policies (RetryPolicy, ExponentialBackoff)
//   - Request execution with automatic retry and reconnect (Engine)
//
// The public modbus package creates an Engine during Open() and delegates
// all request execution to it. The Engine is parameterised over adu.Request
// and adu.Response so there is no import cycle with the public package.
package session
