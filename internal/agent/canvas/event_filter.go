package canvas

import (
	"context"
	"sync"
)

type eventFilterKey struct{}

type eventFilter struct {
	mu    sync.Mutex
	apply func(RunEvent) []RunEvent
}

// WithEventFilter serializes event transformation and delivery for one run.
// A filter may withhold deltas and emit their safe suffix before a terminal event.
func WithEventFilter(ctx context.Context, apply func(RunEvent) []RunEvent) context.Context {
	return context.WithValue(ctx, eventFilterKey{}, &eventFilter{apply: apply})
}
