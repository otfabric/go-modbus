// SPDX-License-Identifier: MIT

// Package connid allocates the identifiers of transport connections, shared by
// the clients and servers of a process, and carries one in a context.
package connid

import (
	"context"
	"sync/atomic"
)

var last atomic.Uint64

// Next returns a new identifier: the next value of a counter shared by the
// whole process, starting at 1. It is never 0 and never returned twice in the
// life of the process (a uint64 does not wrap in practice).
func Next() uint64 { return last.Add(1) }

type key struct{}

// With returns ctx carrying id.
func With(ctx context.Context, id uint64) context.Context {
	return context.WithValue(ctx, key{}, id)
}

// From returns the identifier ctx carries, 0 if none.
func From(ctx context.Context) uint64 {
	id, _ := ctx.Value(key{}).(uint64)
	return id
}
