package wskit

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

// Accept upgrades the HTTP connection to WebSocket, creates a Client, and registers it with the hub. Caller should run ReadPump and WritePump in goroutines. acceptOpts may be nil for default upgrade options
func Accept(ctx context.Context, w http.ResponseWriter, r *http.Request, hub *Hub, acceptOpts *websocket.AcceptOptions, clientOpts ...ClientOption) (*Client, error) {
	conn, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		return nil, err
	}
	client := NewClient(hub, conn, ctx, clientOpts...)
	hub.Register(client)
	return client, nil
}
