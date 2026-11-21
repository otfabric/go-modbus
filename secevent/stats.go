// SPDX-License-Identifier: MIT

package secevent

// Stats is a snapshot of what a Dispatcher has seen. It is a copy: changing
// it changes nothing.
type Stats struct {
	// Events counts every event handed to the dispatcher, merged and dropped
	// ones included, by kind and by category.
	Events     map[Kind]uint64
	Categories map[Category]uint64
	// Delivered counts the events the sink received, synthetic ones included.
	Delivered uint64
	// Merged counts the events folded into an earlier one inside the
	// aggregation window.
	Merged uint64
	// Dropped counts the events lost because the queue was full.
	Dropped uint64
	// SinkFailures counts the sink calls that did not return normally.
	SinkFailures uint64
}
