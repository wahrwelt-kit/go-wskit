package wskit

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// SSEClient represents a Server-Sent Events connection attached to a Hub
// It implements the Subscriber interface
type SSEClient struct {
	send       chan []byte
	done       chan struct{}
	closeOnce  sync.Once
	sendClosed atomic.Bool
}

// compile-time interface check
var _ Subscriber = (*SSEClient)(nil)

// NewSSEClient creates an SSE subscriber for the given hub with the specified buffer size
func NewSSEClient(bufSize int) *SSEClient {
	if bufSize <= 0 {
		bufSize = DefaultSendBufSize
	}
	return &SSEClient{
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

// SendErr is like Send but reports whether the subscriber is closed or its buffer is full.
func (c *SSEClient) SendErr(data []byte) error {
	if c.sendClosed.Load() {
		return ErrSubscriberClosed
	}
	select {
	case <-c.done:
		return ErrSubscriberClosed
	case c.send <- data:
		return nil
	default:
		return ErrSubscriberBufferFull
	}
}

// AcceptSSE upgrades an HTTP request to an SSE stream. It registers with the hub,
// writes SSE-formatted messages, and blocks until the client disconnects or the
// hub shuts down
func AcceptSSE(w http.ResponseWriter, r *http.Request, hub *Hub) error {
	if w == nil {
		return ErrNilResponseWriter
	}
	if r == nil {
		http.Error(w, ErrNilRequest.Error(), http.StatusBadRequest)
		return ErrNilRequest
	}
	if hub == nil {
		http.Error(w, ErrNilHub.Error(), http.StatusInternalServerError)
		return ErrNilHub
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return ErrFlusherNotSupported
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	client := NewSSEClient(DefaultSendBufSize)
	if err := hub.Register(client); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return err
	}
	defer func() { _ = hub.Unregister(client) }()

	flusher.Flush() // flush headers

	ctx := r.Context()
	for {
		select {
		case msg := <-client.send:
			if err := writeSSEData(w, msg); err != nil {
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

func writeSSEData(w io.Writer, msg []byte) error {
	data := strings.ReplaceAll(string(msg), "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	for {
		line, rest, found := strings.Cut(data, "\n")
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
		if !found {
			break
		}
		data = rest
	}
	_, err := io.WriteString(w, "\n")
	return err
}
