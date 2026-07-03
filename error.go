package wskit

import "errors"

var (
	// ErrHubStopped is returned when a hub operation is attempted after shutdown
	ErrHubStopped = errors.New("wskit: hub is stopped")
	// ErrFlusherNotSupported is returned by AcceptSSE when the ResponseWriter does not implement http.Flusher
	ErrFlusherNotSupported = errors.New("wskit: http.Flusher not supported")
	// ErrNilContext is returned when a required context is nil
	ErrNilContext = errors.New("wskit: context is nil")
	// ErrNilHub is returned when a required hub is nil
	ErrNilHub = errors.New("wskit: hub is nil")
	// ErrNilConn is returned when a required WebSocket connection is nil
	ErrNilConn = errors.New("wskit: websocket connection is nil")
	// ErrNilRequest is returned when a required HTTP request is nil
	ErrNilRequest = errors.New("wskit: http request is nil")
	// ErrNilResponseWriter is returned when a required HTTP response writer is nil
	ErrNilResponseWriter = errors.New("wskit: http response writer is nil")
	// ErrNilSubscriber is returned when a required subscriber is nil
	ErrNilSubscriber = errors.New("wskit: subscriber is nil")
	// ErrOperationTimeout is returned when a hub channel operation times out
	ErrOperationTimeout = errors.New("wskit: operation timeout")
	// ErrSubscriberClosed is returned when sending to a closed subscriber
	ErrSubscriberClosed = errors.New("wskit: subscriber is closed")
	// ErrSubscriberBufferFull is returned when a subscriber send buffer is full
	ErrSubscriberBufferFull = errors.New("wskit: subscriber buffer is full")
	// ErrRedisSubscriptionClosed is reported when a Redis subscription channel closes unexpectedly
	ErrRedisSubscriptionClosed = errors.New("wskit: redis subscription closed")
)
