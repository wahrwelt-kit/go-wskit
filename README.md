# go-wskit

[![CI](https://github.com/wahrwelt-kit/go-wskit/actions/workflows/ci.yml/badge.svg)](https://github.com/wahrwelt-kit/go-wskit/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/wahrwelt-kit/go-wskit.svg)](https://pkg.go.dev/github.com/wahrwelt-kit/go-wskit)
[![Go Report Card](https://goreportcard.com/badge/github.com/wahrwelt-kit/go-wskit)](https://goreportcard.com/report/github.com/wahrwelt-kit/go-wskit)

WebSocket hub-and-spoke server on [coder/websocket](https://github.com/coder/websocket) with optional Redis Pub/Sub for multi-instance broadcast.

## Install

```bash
go get github.com/wahrwelt-kit/go-wskit
```

```go
import "github.com/wahrwelt-kit/go-wskit"
```

## Features

- **Hub** - single-goroutine dispatcher with register/unregister/broadcast channels, graceful shutdown via context, atomic subscriber count
- **Client** - ReadPump (drain incoming) + WritePump (send + ping/pong), safe concurrent Send with close protection
- **SSEClient** - Server-Sent Events subscriber; `AcceptSSE` registers it with the hub and streams data until disconnect or shutdown
- **Redis Pub/Sub** - BroadcastEvent with JSON serialization, fallback to local broadcast on Redis failure, SubscribeToRedis for horizontal scaling
- **Event envelope** - `Event{Type, Payload, Timestamp}` standard JSON format, BroadcastJSON helper
- **Accept / AcceptSSE helpers** - upgrade HTTP to WebSocket or SSE + create client + register in one call
- **Functional options** - configurable timeouts, buffer sizes, callbacks

## Example

```go
hub := wskit.NewHub(
    wskit.WithRedis(redisClient, "ws:events"),
    wskit.WithOnConnect(func(sub wskit.Subscriber) {
        data, _ := json.Marshal(wskit.NewEvent("connected", nil))
        sub.Send(data)
    }),
)
go hub.Run(ctx)
go hub.SubscribeToRedis(ctx)

// WebSocket endpoint
http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
    client, err := wskit.Accept(r.Context(), w, r, hub, nil)
    if err != nil {
        return
    }
    go client.ReadPump()
    go client.WritePump()
})

// SSE endpoint
http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
    wskit.AcceptSSE(w, r, hub)
})

hub.BroadcastJSON(ctx, "notification", map[string]string{"message": "hello"})
```

## Options

**Hub:** `WithRedis`, `WithBroadcastBuf`, `WithRegisterBuf`, `WithChannelTimeout`, `WithOnTimeout`, `WithOnConnect`, `WithOnDisconnect`

**Client:** `WithWriteWait`, `WithPingInterval`, `WithMaxMessageSize`, `WithSendBufSize`
