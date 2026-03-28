package wskit

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
)

// SSEClient represents a Server-Sent Events connection attached to a Hub
// It implements the Subscriber interface
type SSEClient struct {
	hub        *Hub
	send       chan []byte
	done       chan struct{}
	closeOnce  sync.Once
	sendClosed atomic.Bool
}

// compile-time interface check
var _ Subscriber = (*SSEClient)(nil)

// NewSSEClient creates an SSE subscriber for the given hub with the specified buffer size
func NewSSEClient(hub *Hub, bufSize int) *SSEClient {
	if bufSize <= 0 {
		bufSize = DefaultSendBufSize
	}
	return &SSEClient{
		hub:  hub,
		send: make(chan []byte, bufSize),
		done: make(chan struct{}),
	}
}

// Send enqueues data for delivery to the SSE stream. Non-blocking: returns false if the
// send buffer is full or the client has been closed, true if the message was accepted
func (c *SSEClient) Send(data []byte) bool {
	if c.sendClosed.Load() {
		return false
	}
	select {
	case c.send <- data:
		return true
	default:
		return false
	}
}

// Close signals the SSE client to shut down. It is idempotent and safe to call from
// any goroutine. AcceptSSE returns once the done channel is closed
func (c *SSEClient) Close() {
	c.closeOnce.Do(func() {
		c.sendClosed.Store(true)
		close(c.done)
	})
}

// AcceptSSE upgrades an HTTP request to an SSE stream. It registers with the hub,
// writes SSE-formatted messages, and blocks until the client disconnects or the
// hub shuts down
func AcceptSSE(w http.ResponseWriter, r *http.Request, hub *Hub) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return ErrFlusherNotSupported
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	client := NewSSEClient(hub, DefaultSendBufSize)
	hub.Register(client)
	defer hub.Unregister(client)

	flusher.Flush() // flush headers

	ctx := r.Context()
	for {
		select {
		case msg := <-client.send:
			// Write SSE format: "data: <payload>\n\n"
			if _, err := fmt.Fprintf(w, "data: %s\n\n", msg); err != nil {
				return err
			}
			flusher.Flush()
		case <-client.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
