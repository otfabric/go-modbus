// SPDX-License-Identifier: MIT

package transport

import "github.com/otfabric/go-modbus/internal/adu"

// AnomalyKind names something a client transport tolerated while it waited
// for the response to a request: it did not fail the request.
type AnomalyKind uint8

const (
	// AnomalyTransactionID: an MBAP frame with another transaction ID than
	// the request's was read and skipped.
	AnomalyTransactionID AnomalyKind = iota + 1
	// AnomalyProtocolID: an MBAP frame with a protocol ID other than 0 was
	// read and skipped.
	AnomalyProtocolID
	// AnomalyDiscarded: bytes the link delivered before the request was
	// written were read and discarded (serial framings).
	AnomalyDiscarded
)

// Anomaly is one tolerated anomaly, reported to an AnomalyFunc.
type Anomaly struct {
	Kind AnomalyKind
	// Expected and TransactionID: the request's transaction ID and the one
	// of the skipped frame (AnomalyTransactionID, AnomalyProtocolID).
	Expected, TransactionID uint16
	// ProtocolID of the skipped frame (AnomalyProtocolID).
	ProtocolID uint16
	// Discarded is the number of bytes discarded (AnomalyDiscarded).
	Discarded int
}

// AnomalyFunc receives the anomalies a client transport tolerated during the
// exchange of req. It runs on the goroutine executing the request.
type AnomalyFunc func(req *adu.Request, a Anomaly)

// reportDiscarded reports n discarded bytes before req to f, when f is set and
// n is positive.
func reportDiscarded(f AnomalyFunc, req *adu.Request, n int) {
	if f != nil && n > 0 {
		f(req, Anomaly{Kind: AnomalyDiscarded, Discarded: n})
	}
}
