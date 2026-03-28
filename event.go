package wskit

import (
	"context"
	"time"
)

// Event is the default envelope for WebSocket messages: type, payload, and timestamp
type Event struct {
	Type      string    `json:"type"`
	Payload   any       `json:"payload"`
	Timestamp time.Time `json:"timestamp"`
}

// NewEvent builds an Event with Type, Payload, and Timestamp set to now
func NewEvent(typ string, payload any) Event {
	return Event{Type: typ, Payload: payload, Timestamp: time.Now()}
}

// BroadcastJSON marshals Event{typ, payload, now} and broadcasts it via the hub. Returns an error if JSON marshaling fails
func (h *Hub) BroadcastJSON(ctx context.Context, typ string, payload any) error {
	return h.BroadcastEvent(ctx, NewEvent(typ, payload))
}
