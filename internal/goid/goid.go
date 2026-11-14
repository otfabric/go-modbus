// SPDX-License-Identifier: MIT

// Package goid returns the identifier of the calling goroutine.
//
// The server runs the application's code (RequestHandler methods,
// ServerMetrics hooks, the Logger) on goroutines of its own and waits for
// them in Shutdown. A Shutdown called from such a callback would wait for
// itself; with the identifier Shutdown can tell that it runs on a goroutine
// it would wait for. It is read once when such a goroutine starts and once
// per Shutdown, never on a request's path.
package goid

import (
	"bytes"
	"runtime"
	"strconv"
)

// ID returns the identifier of the calling goroutine, 0 if the runtime does
// not report it.
func ID() uint64 {
	var buf [64]byte
	return parse(buf[:runtime.Stack(buf[:], false)])
}

// parse reads the identifier from the first line of a goroutine's stack:
// "goroutine 12 [running]:".
func parse(stack []byte) uint64 {
	rest, ok := bytes.CutPrefix(stack, []byte("goroutine "))
	if !ok {
		return 0
	}
	n := bytes.IndexByte(rest, ' ')
	if n < 0 {
		return 0
	}
	id, err := strconv.ParseUint(string(rest[:n]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
