// SPDX-License-Identifier: MIT

package session

import "context"

// Transport is the minimal interface required by Pool. It is intentionally
// generic: the concrete request/response types are supplied via type
// parameters on Pool.
type Transport[Req any, Res any] interface {
	Close() error
	ExecuteRequest(context.Context, Req) (Res, error)
}
