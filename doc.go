// Package wskit provides a WebSocket and SSE hub-and-spoke server built on coder/websocket
//
// # Hub and Subscriber
//
// Create a Hub with NewHub (optionally WithRedis for multi-instance broadcast), run Hub.Run(ctx) in a goroutine,
// then use Accept to upgrade HTTP connections and register WebSocket clients, or AcceptSSE for Server-Sent Events
// Any type implementing the Subscriber interface (Send + Close) can participate in hub broadcasts
//
// # WebSocket Clients
//
// Use Accept to upgrade HTTP connections and register clients. Run Client.ReadPump and Client.WritePump in separate goroutines per connection
//
// # SSE Clients
//
// Use AcceptSSE to handle SSE connections. It registers an SSEClient with the hub and blocks until the client disconnects or the hub shuts down
//
// # Event envelope
//
// Event and NewEvent provide a standard JSON envelope (type, payload, timestamp). Use Hub.BroadcastEvent or Hub.BroadcastJSON to send to all subscribers
//
// # Redis Pub/Sub
//
// WithRedis(client, channel) enables publishing to Redis on BroadcastEvent/BroadcastJSON; other instances run SubscribeToRedis(ctx) to receive and broadcast locally. SubscribeToRedis automatically reconnects with exponential backoff
//
// # Options
//
// Hub: WithRedis, WithBroadcastBuf, WithRegisterBuf, WithChannelTimeout, WithOnTimeout, WithOnConnect, WithOnDisconnect
// Client: WithWriteWait, WithPingInterval, WithMaxMessageSize, WithSendBufSize
package wskit
