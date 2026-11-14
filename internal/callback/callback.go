// SPDX-License-Identifier: MIT

// Package callback is how the library calls code of the application: the
// methods of a RequestHandler, ServerMetrics, ClientMetrics, AttemptMetrics,
// PDUTap or RetryPolicy, a Config.Dialer and the connections it returns, the
// private key of a TLS certificate during a handshake. A call that does not
// return normally is stopped where the library makes it and turned into a
// Failure, which the caller logs (once it holds no lock) and handles as an
// error of that call, so that the goroutine, and with it the process, goes on.
//
// The application's Logger is guarded in internal/logging, which cannot log a
// failure of the logger through that logger's ordinary path.
package callback

import (
	"fmt"
	"runtime/debug"
)

// Failure is a call of application code that did not return normally.
type Failure struct {
	// Name says what was called, for example "ClientMetrics.OnRequest".
	Name string
	// Value is the value the call ended with.
	Value any
	// Stack is the stack of the goroutine where the call ended.
	Stack []byte
}

// Error describes the call, for example
// "ClientMetrics.OnRequest did not return normally: index out of range".
func (f *Failure) Error() string {
	return fmt.Sprintf("%s did not return normally: %v", f.Name, f.Value)
}

// Run calls fn, the application code that name names. When fn returns, Run
// returns a nil Failure and fn's error. When fn does not return normally, Run
// returns the Failure twice, as the Failure to log and as the error of the
// call. Run does not log: the caller does, once it holds no lock.
func Run(name string, fn func() error) (fail *Failure, err error) {
	defer func() {
		if r := recover(); r != nil {
			fail = &Failure{Name: name, Value: r, Stack: debug.Stack()}
			err = fail
		}
	}()
	return nil, fn()
}
