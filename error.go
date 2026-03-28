package wskit

import "errors"

var (
	// ErrHubStopped is returned by Client.SendErr when the send channel is closed (hub shut down or client unregistered)
	ErrHubStopped = errors.New("wskit: hub is stopped")
	// ErrFlusherNotSupported is returned by AcceptSSE when the ResponseWriter does not implement http.Flusher
	ErrFlusherNotSupported = errors.New("wskit: http.Flusher not supported")
)
