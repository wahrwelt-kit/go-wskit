package wskit

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultBroadcastBuf is the default broadcast channel buffer size (number of messages)
const (
	DefaultBroadcastBuf   = 256             // Default broadcast channel buffer size (number of messages)
	DefaultRegisterBuf    = 64              // Default register/unregister channel buffer size (number of operations)
	DefaultChannelTimeout = 5 * time.Second // Default timeout for Register, Unregister, and Broadcast channel operations
)

// Subscriber is the interface that any client (WebSocket, SSE, etc.) must implement
// to participate in hub broadcasts
type Subscriber interface {
	// Send enqueues data for delivery. Returns true if accepted, false if dropped
	Send(data []byte) bool
	// Close signals the subscriber to shut down. Must be idempotent
	Close()
}

type broadcastItem struct {
	data []byte
}

// HubOption configures a Hub
type HubOption func(*Hub)

// WithRedis configures Redis Pub/Sub for multi-instance broadcast. If client is nil, Redis is disabled
func WithRedis(client *redis.Client, channel string) HubOption {
	return func(h *Hub) {
		h.redisClient = client
		h.redisChannel = channel
	}
}

// WithBroadcastBuf sets the broadcast channel buffer size
func WithBroadcastBuf(n int) HubOption {
	return func(h *Hub) {
		h.broadcastBuf = n
	}
}

// WithRegisterBuf sets the register/unregister channel buffer size
func WithRegisterBuf(n int) HubOption {
	return func(h *Hub) {
		h.registerBuf = n
	}
}

// WithChannelTimeout sets the timeout for Register, Unregister, and Broadcast operations
func WithChannelTimeout(d time.Duration) HubOption {
	return func(h *Hub) {
		h.channelTimeout = d
	}
}

// WithOnTimeout sets a callback when a channel operation times out (e.g. for logging)
func WithOnTimeout(fn func(op string)) HubOption {
	return func(h *Hub) {
		h.onTimeout = fn
	}
}

// WithOnConnect sets the callback invoked when a subscriber registers
func WithOnConnect(fn func(Subscriber)) HubOption {
	return func(h *Hub) {
		h.onConnect = fn
	}
}

// WithOnDisconnect sets the callback invoked when a subscriber unregisters
func WithOnDisconnect(fn func(Subscriber)) HubOption {
	return func(h *Hub) {
		h.onDisconnect = fn
	}
}

// Hub is the central dispatcher for subscribers. Run one goroutine with Run(ctx)
type Hub struct {
	subscribers    map[Subscriber]struct{}
	broadcast      chan broadcastItem
	register       chan Subscriber
	unregister     chan Subscriber
	done           chan struct{}
	doneOnce       sync.Once
	clientCount    int64
	redisClient    *redis.Client
	redisChannel   string
	onTimeout      func(op string)
	onConnect      func(Subscriber)
	onDisconnect   func(Subscriber)
	broadcastBuf   int
	registerBuf    int
	channelTimeout time.Duration
}

// NewHub creates a Hub with the given options
func NewHub(opts ...HubOption) *Hub {
	h := &Hub{
		subscribers:    make(map[Subscriber]struct{}),
		broadcastBuf:   DefaultBroadcastBuf,
		registerBuf:    DefaultRegisterBuf,
		channelTimeout: DefaultChannelTimeout,
	}
	for _, opt := range opts {
		opt(h)
	}
	h.broadcast = make(chan broadcastItem, h.broadcastBuf)
	h.register = make(chan Subscriber, h.registerBuf)
	h.unregister = make(chan Subscriber, h.registerBuf)
	h.done = make(chan struct{})
	return h
}

func (h *Hub) closeDone() {
	h.doneOnce.Do(func() { close(h.done) })
}

// Run runs the hub loop until ctx is cancelled. Closes all subscribers on exit
func (h *Hub) Run(ctx context.Context) {
	defer h.closeDone()
	for {
		select {
		case <-ctx.Done():
			for sub := range h.subscribers {
				sub.Close()
				delete(h.subscribers, sub)
				atomic.AddInt64(&h.clientCount, -1)
			}
			return
		case sub := <-h.register:
			h.subscribers[sub] = struct{}{}
			atomic.AddInt64(&h.clientCount, 1)
			if h.onConnect != nil {
				h.onConnect(sub)
			}
		case sub := <-h.unregister:
			h.unregisterSubscriber(sub)
		case item := <-h.broadcast:
			h.broadcastToClients(item)
		}
	}
}

func (h *Hub) unregisterSubscriber(sub Subscriber) {
	if _, ok := h.subscribers[sub]; ok {
		delete(h.subscribers, sub)
		sub.Close()
		atomic.AddInt64(&h.clientCount, -1)
		if h.onDisconnect != nil {
			h.onDisconnect(sub)
		}
	}
}

func (h *Hub) broadcastToClients(item broadcastItem) {
	for sub := range h.subscribers {
		sub.Send(item.data)
	}
}

func (h *Hub) sendWithTimeout(ch chan<- Subscriber, sub Subscriber, op string) {
	t := time.NewTimer(h.channelTimeout)
	defer t.Stop()
	select {
	case ch <- sub:
	case <-h.done:
	case <-t.C:
		if h.onTimeout != nil {
			h.onTimeout(op)
		}
	}
}

func (h *Hub) broadcastWithTimeout(data []byte) {
	t := time.NewTimer(h.channelTimeout)
	defer t.Stop()
	select {
	case h.broadcast <- broadcastItem{data: data}:
	case <-h.done:
	case <-t.C:
		if h.onTimeout != nil {
			h.onTimeout("broadcast")
		}
	}
}

// Register adds the subscriber to the hub. Non-blocking with timeout
func (h *Hub) Register(sub Subscriber) {
	h.sendWithTimeout(h.register, sub, "register")
}

// Unregister removes the subscriber from the hub. Non-blocking with timeout
func (h *Hub) Unregister(sub Subscriber) {
	h.sendWithTimeout(h.unregister, sub, "unregister")
}

// Broadcast sends data to all connected subscribers. Non-blocking with timeout
func (h *Hub) Broadcast(data []byte) {
	h.broadcastWithTimeout(data)
}

// BroadcastEvent marshals event as JSON and broadcasts it. If Redis is configured, publishes to Redis first; on failure falls back to local Broadcast. Returns an error only when JSON marshaling fails
func (h *Hub) BroadcastEvent(ctx context.Context, event any) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if h.redisClient != nil && h.redisChannel != "" {
		pubCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := h.redisClient.Publish(pubCtx, h.redisChannel, data).Err()
		cancel()
		if err == nil {
			return nil
		}
	}
	h.Broadcast(data)
	return nil
}

// SubscribeToRedis subscribes to the hub's Redis channel and broadcasts received
// messages to all clients. It automatically reconnects with exponential backoff
// if the subscription is lost. Run in a goroutine; it returns when ctx is cancelled
func (h *Hub) SubscribeToRedis(ctx context.Context) {
	if h.redisClient == nil || h.redisChannel == "" {
		return
	}
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		pubsub := h.redisClient.Subscribe(ctx, h.redisChannel)
		ch := pubsub.Channel()
		for msg := range ch {
			h.Broadcast([]byte(msg.Payload))
			backoff = time.Second // reset on success
		}
		_ = pubsub.Close()
		if ctx.Err() != nil {
			return
		}
		// Channel closed unexpectedly, reconnect with backoff
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// SubscriberCount returns the number of registered subscribers
func (h *Hub) SubscriberCount() int {
	return int(atomic.LoadInt64(&h.clientCount))
}
