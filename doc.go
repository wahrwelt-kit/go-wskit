// Package wskit provides a WebSocket and SSE hub-and-spoke server built on coder/websocket
//
// # Hub and Subscriber
//
// Create a Hub with NewHub (optionally WithRedis for multi-instance broadcast), run Hub.Run(ctx) in a goroutine,
// then use Accept to upgrade HTTP connections and register WebSocket clients, or AcceptSSE for Server-Sent Events
// Any type implementing the Subscriber interface (Send + Close) can participate in hub broadcasts. Register,
// Unregister, and Broadcast return errors for nil input, stopped hubs, or channel operation timeouts
//
// # WebSocket Clients
//
// Use Accept to upgrade HTTP connections and register clients. Run Client.ReadPump and Client.WritePump in separate
// goroutines per connection. Use WithMessageHandler when inbound WebSocket messages should be handled instead of
// only drained
//
// # SSE Clients
//
// Use AcceptSSE to handle SSE connections. It registers an SSEClient with the hub, writes protocol-safe data frames,
// and blocks until the client disconnects or the hub shuts down
//
// # Event envelope
//
// Event and NewEvent provide a standard JSON envelope (type, payload, timestamp). Use Hub.BroadcastEvent or Hub.BroadcastJSON to send to all subscribers
//
// # Redis Pub/Sub
//
// WithRedis(client, channel) enables local-first broadcast plus Redis fanout on BroadcastEvent/BroadcastJSON.
// Other instances run SubscribeToRedis(ctx) to receive and broadcast locally. SubscribeToRedis automatically
// reconnects with exponential backoff and skips messages originating from the same hub
//
// # Options
//
// Hub: WithRedis, WithBroadcastBuf, WithRegisterBuf, WithChannelTimeout, WithOnTimeout, WithOnDrop, WithOnError, WithOnConnect, WithOnDisconnect
// Client: WithWriteWait, WithPingInterval, WithMaxMessageSize, WithSendBufSize, WithMessageHandler
package wskit
