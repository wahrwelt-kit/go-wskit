package wskit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
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

type redisEnvelope struct {
	Origin string          `json:"origin"`
	Data   json.RawMessage `json:"data"`
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

// WithOnDrop sets a callback invoked when a subscriber rejects a broadcast message
func WithOnDrop(fn func(Subscriber, []byte)) HubOption {
	return func(h *Hub) {
		h.onDrop = fn
	}
}

// WithOnError sets a callback invoked when Redis or hub operations fail asynchronously
func WithOnError(fn func(op string, err error)) HubOption {
	return func(h *Hub) {
		h.onError = fn
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
	redisID        string
	onTimeout      func(op string)
	onDrop         func(Subscriber, []byte)
	onError        func(op string, err error)
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
		redisID:        newRedisID(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	h.normalizeOptions()
	h.broadcast = make(chan broadcastItem, h.broadcastBuf)
	h.register = make(chan Subscriber, h.registerBuf)
	h.unregister = make(chan Subscriber, h.registerBuf)
	h.done = make(chan struct{})
	return h
}

func newRedisID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func (h *Hub) normalizeOptions() {
	if h.broadcastBuf < 0 {
		h.broadcastBuf = DefaultBroadcastBuf
	}
	if h.registerBuf < 0 {
		h.registerBuf = DefaultRegisterBuf
	}
	if h.channelTimeout <= 0 {
		h.channelTimeout = DefaultChannelTimeout
	}
}

func (h *Hub) closeDone() {
	h.doneOnce.Do(func() { close(h.done) })
}

// Run runs the hub loop until ctx is cancelled. Closes all subscribers on exit
func (h *Hub) Run(ctx context.Context) {
	if h == nil {
		return
	}
	defer h.closeDone()
	if ctx == nil {
		h.closeSubscribers()
		return
	}
	for {
		select {
		case <-ctx.Done():
			h.closeSubscribers()
			return
		case sub := <-h.register:
			if sub == nil {
				h.reportError("register", ErrNilSubscriber)
				continue
			}
			if _, exists := h.subscribers[sub]; !exists {
				h.subscribers[sub] = struct{}{}
				atomic.AddInt64(&h.clientCount, 1)
				if h.onConnect != nil {
					h.onConnect(sub)
				}
			}
		case sub := <-h.unregister:
			h.unregisterSubscriber(sub)
		case item := <-h.broadcast:
			h.broadcastToClients(item)
		}
	}
}

func (h *Hub) closeSubscribers() {
	for sub := range h.subscribers {
		sub.Close()
		delete(h.subscribers, sub)
		atomic.AddInt64(&h.clientCount, -1)
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
		if !sub.Send(item.data) {
			if h.onDrop != nil {
				h.onDrop(sub, item.data)
			}
		}
	}
}

func (h *Hub) sendWithTimeout(ch chan<- Subscriber, sub Subscriber, op string) error {
	if h == nil {
		return ErrNilHub
	}
	if sub == nil {
		return ErrNilSubscriber
	}
	t := time.NewTimer(h.channelTimeout)
	defer t.Stop()
	select {
	case ch <- sub:
		return nil
	case <-h.done:
		return ErrHubStopped
	case <-t.C:
		err := ErrOperationTimeout
		if h.onTimeout != nil {
			h.onTimeout(op)
		}
		h.reportError(op, err)
		return err
	}
}

func (h *Hub) broadcastWithTimeout(data []byte) error {
	if h == nil {
		return ErrNilHub
	}
	t := time.NewTimer(h.channelTimeout)
	defer t.Stop()
	select {
	case h.broadcast <- broadcastItem{data: data}:
		return nil
	case <-h.done:
		return ErrHubStopped
	case <-t.C:
		err := ErrOperationTimeout
		if h.onTimeout != nil {
			h.onTimeout("broadcast")
		}
		h.reportError("broadcast", err)
		return err
	}
}

// Register adds the subscriber to the hub. Non-blocking with timeout.
func (h *Hub) Register(sub Subscriber) error {
	if h == nil {
		return ErrNilHub
	}
	return h.sendWithTimeout(h.register, sub, "register")
}

// Unregister removes the subscriber from the hub. Non-blocking with timeout.
func (h *Hub) Unregister(sub Subscriber) error {
	if h == nil {
		return ErrNilHub
	}
	return h.sendWithTimeout(h.unregister, sub, "unregister")
}

// Broadcast sends data to all connected subscribers. Non-blocking with timeout.
func (h *Hub) Broadcast(data []byte) error {
	payload := make([]byte, len(data))
	copy(payload, data)
	return h.broadcastWithTimeout(payload)
}

// BroadcastEvent marshals event as JSON, broadcasts it locally, and publishes it to Redis when configured.
func (h *Hub) BroadcastEvent(ctx context.Context, event any) error {
	if h == nil {
		return ErrNilHub
	}
	if ctx == nil {
		return ErrNilContext
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	localErr := h.Broadcast(data)
	if h.redisClient != nil && h.redisChannel != "" {
		pubCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := h.redisClient.Publish(pubCtx, h.redisChannel, h.wrapRedisPayload(data)).Err()
		cancel()
		if err != nil {
			h.reportError("redis_publish", err)
			return errors.Join(localErr, err)
		}
	}
	return localErr
}

// SubscribeToRedis subscribes to the hub's Redis channel and broadcasts received
// messages to all clients. It automatically reconnects with exponential backoff
// if the subscription is lost. Run in a goroutine; it returns when ctx is cancelled
func (h *Hub) SubscribeToRedis(ctx context.Context) {
	if h == nil || h.redisClient == nil || h.redisChannel == "" {
		return
	}
	if ctx == nil {
		h.reportError("redis_subscribe", ErrNilContext)
		return
	}
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		pubsub := h.redisClient.Subscribe(ctx, h.redisChannel)
		if _, err := pubsub.Receive(ctx); err != nil {
			h.reportError("redis_subscribe", err)
			_ = pubsub.Close()
			if !h.waitRedisBackoff(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}
		ch := pubsub.Channel()
		for msg := range ch {
			payload, ok := h.unwrapRedisPayload(msg.Payload)
			if ok {
				if err := h.Broadcast(payload); err != nil {
					h.reportError("redis_broadcast", err)
				}
			}
			backoff = time.Second // reset on success
		}
		if err := pubsub.Close(); err != nil {
			h.reportError("redis_close", err)
		}
		if ctx.Err() != nil {
			return
		}
		h.reportError("redis_subscribe", ErrRedisSubscriptionClosed)
		if !h.waitRedisBackoff(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff, maxBackoff)
	}
}

// SubscriberCount returns the number of registered subscribers
func (h *Hub) SubscriberCount() int {
	if h == nil {
		return 0
	}
	return int(atomic.LoadInt64(&h.clientCount))
}

func (h *Hub) wrapRedisPayload(data []byte) []byte {
	payload, err := json.Marshal(redisEnvelope{
		Origin: h.redisID,
		Data:   json.RawMessage(data),
	})
	if err != nil {
		return data
	}
	return payload
}

func (h *Hub) unwrapRedisPayload(payload string) ([]byte, bool) {
	var envelope redisEnvelope
	if err := json.Unmarshal([]byte(payload), &envelope); err == nil && len(envelope.Data) > 0 {
		if envelope.Origin == h.redisID {
			return nil, false
		}
		data := make([]byte, len(envelope.Data))
		copy(data, envelope.Data)
		return data, true
	}
	return []byte(payload), true
}

func (h *Hub) waitRedisBackoff(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current, maxValue time.Duration) time.Duration {
	if current >= maxValue/2 {
		return maxValue
	}
	return current * 2
}

func (h *Hub) reportError(op string, err error) {
	if h != nil && h.onError != nil && err != nil {
		h.onError(op, err)
	}
}
