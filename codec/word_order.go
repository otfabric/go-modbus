package codec

// WordOrder controls the order of 16-bit words within multi-register values.
type WordOrder uint

const (
	HighWordFirst WordOrder = 1
	LowWordFirst  WordOrder = 2
)
