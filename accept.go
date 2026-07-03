package wskit

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

// Accept upgrades the HTTP connection to WebSocket, creates a Client, and registers it with the hub. Caller should run ReadPump and WritePump in goroutines. acceptOpts may be nil for default upgrade options
func Accept(ctx context.Context, w http.ResponseWriter, r *http.Request, hub *Hub, acceptOpts *websocket.AcceptOptions, clientOpts ...ClientOption) (*Client, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if w == nil {
		return nil, ErrNilResponseWriter
	}
	if r == nil {
		return nil, ErrNilRequest
	}
	if hub == nil {
		return nil, ErrNilHub
	}
	conn, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		return nil, err
	}
	client, err := NewClient(hub, conn, ctx, clientOpts...)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, err.Error())
		return nil, err
	}
	if err := hub.Register(client); err != nil {
		_ = conn.Close(websocket.StatusTryAgainLater, err.Error())
		return nil, err
	}
	return client, nil
}
