package wskit

import (
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"github.com/redis/go-redis/v9"
)

// DefaultBroadcastBuf is the default broadcast channel buffer size (number of messages)
const (
	DefaultBroadcastBuf   = 256             // Default broadcast channel buffer size (number of messages)
	DefaultRegisterBuf    = 64              // Default shared register/unregister queue size (number of operations)
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

type subscriberOperation struct {
	sub        Subscriber
	unregister bool
}

type redisEnvelope struct {
	Origin string         `json:"origin"`
	Data   jsontext.Value `json:"data"`
}

var (
	hubDurationMarshalers = jsonv2.MarshalFunc(func(value time.Duration) ([]byte, error) {
		return strconv.AppendInt(nil, int64(value), 10), nil
	})
	hubJSONMarshalOptions = jsonv2.JoinOptions(
		jsonv2.Deterministic(true),
		jsonv2.FormatNilMapAsNull(true),
		jsonv2.FormatNilSliceAsNull(true),
		jsontext.EscapeForHTML(true),
		jsonv2.WithMarshalers(hubDurationMarshalers),
	)
	hubJSONUnmarshalOptions = jsonv2.MatchCaseInsensitiveNames(true)
)

func marshalJSON(value any) ([]byte, error) {
	return jsonv2.Marshal(value, hubJSONMarshalOptions)
}

func unmarshalJSON(data []byte, value any) error {
	return jsonv2.Unmarshal(data, value, hubJSONUnmarshalOptions)
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

// WithRegisterBuf sets the shared register/unregister queue size
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
	operation      chan subscriberOperation
	enqueueMu      sync.RWMutex
	stopped        bool
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
	h.operation = make(chan subscriberOperation, h.registerBuf)
	h.done = make(chan struct{})
	return h
}

func newRedisID() string {
	return uuid.NewV4().String()
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
	defer h.stop()
	if ctx == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case operation := <-h.operation:
			sub := operation.sub
			if operation.unregister {
				h.unregisterSubscriber(sub)
				continue
			}
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
		case item := <-h.broadcast:
			h.broadcastToClients(item)
		}
	}
}

func (h *Hub) stop() {
	h.closeDone()
	// Wait for enqueuers that started before shutdown, then release queued clients.
	h.enqueueMu.Lock()
	h.stopped = true
	h.enqueueMu.Unlock()
	for {
		select {
		case operation := <-h.operation:
			operation.sub.Close()
		case <-h.broadcast:
		default:
			h.closeSubscribers()
			return
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

func (h *Hub) sendWithTimeout(sub Subscriber, op string) error {
	if h == nil {
		return ErrNilHub
	}
	if sub == nil {
		return ErrNilSubscriber
	}
	h.enqueueMu.RLock()
	defer h.enqueueMu.RUnlock()
	if h.stopped {
		return ErrHubStopped
	}
	t := time.NewTimer(h.channelTimeout)
	defer t.Stop()
	select {
	case h.operation <- subscriberOperation{sub: sub, unregister: op == "unregister"}:
		select {
		case <-h.done:
			return ErrHubStopped
		default:
		}
		return nil
	case <-h.done:
		return ErrHubStopped
	case <-t.C:
		return ErrOperationTimeout
	}
}

func (h *Hub) broadcastWithTimeout(ctx context.Context, data []byte) error {
	if h == nil {
		return ErrNilHub
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	h.enqueueMu.RLock()
	defer h.enqueueMu.RUnlock()
	if h.stopped {
		return ErrHubStopped
	}
	t := time.NewTimer(h.channelTimeout)
	defer t.Stop()
	select {
	case h.broadcast <- broadcastItem{data: data}:
		select {
		case <-h.done:
			return ErrHubStopped
		default:
		}
		return nil
	case <-h.done:
		return ErrHubStopped
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return ErrOperationTimeout
	}
}

// Register queues a subscriber for addition. Operations share a FIFO queue with Unregister.
// A nil error means the operation was queued; Run applies it asynchronously.
func (h *Hub) Register(sub Subscriber) error {
	if h == nil {
		return ErrNilHub
	}
	return h.reportOperation("register", h.sendWithTimeout(sub, "register"))
}

// Unregister queues removal after any previously queued Register for this subscriber.
// It can return ErrOperationTimeout when the operation queue stays full.
func (h *Hub) Unregister(sub Subscriber) error {
	if h == nil {
		return ErrNilHub
	}
	return h.reportOperation("unregister", h.sendWithTimeout(sub, "unregister"))
}

// Broadcast sends data to all connected subscribers. Non-blocking with timeout.
func (h *Hub) Broadcast(data []byte) error {
	payload := make([]byte, len(data))
	copy(payload, data)
	return h.reportOperation("broadcast", h.broadcastWithTimeout(context.Background(), payload))
}

// User callbacks run after enqueue locks have been released.
func (h *Hub) reportOperation(op string, err error) error {
	if errors.Is(err, ErrOperationTimeout) {
		if h.onTimeout != nil {
			h.onTimeout(op)
		}
		h.reportError(op, err)
	}
	return err
}

// BroadcastEvent marshals event as JSON, broadcasts it locally, and publishes it to Redis when configured.
func (h *Hub) BroadcastEvent(ctx context.Context, event any) error {
	if h == nil {
		return ErrNilHub
	}
	if ctx == nil {
		return ErrNilContext
	}
	data, err := marshalJSON(event)
	if err != nil {
		return err
	}
	localErr := h.reportOperation("broadcast", h.broadcastWithTimeout(ctx, data))
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
		if ctx.Err() != nil {
			return
		}
		pubsub := h.redisClient.Subscribe(ctx, h.redisChannel)
		// Closing the subscription also interrupts an idle Receive or Channel read.
		stop := context.AfterFunc(ctx, func() { _ = pubsub.Close() })
		if _, err := pubsub.Receive(ctx); err != nil {
			stop()
			_ = pubsub.Close()
			if ctx.Err() != nil {
				return
			}
			h.reportError("redis_subscribe", err)
			if !h.waitRedisBackoff(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}
		if h.forwardRedisMessages(ctx, pubsub.Channel()) {
			backoff = time.Second
		}
		stop()
		if err := pubsub.Close(); err != nil && ctx.Err() == nil {
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

func (h *Hub) forwardRedisMessages(ctx context.Context, messages <-chan *redis.Message) bool {
	received := false
	for {
		var msg *redis.Message
		select {
		case <-ctx.Done():
			return received
		case next, ok := <-messages:
			if !ok {
				return received
			}
			msg = next
		}
		received = true
		payload, ok := h.unwrapRedisPayload(msg.Payload)
		if !ok {
			continue
		}
		err := h.reportOperation("broadcast", h.broadcastWithTimeout(ctx, payload))
		if ctx.Err() != nil {
			return received
		}
		if err != nil {
			h.reportError("redis_broadcast", err)
		}
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
	payload, err := marshalJSON(redisEnvelope{
		Origin: h.redisID,
		Data:   jsontext.Value(data),
	})
	if err != nil {
		return data
	}
	return payload
}

func (h *Hub) unwrapRedisPayload(payload string) ([]byte, bool) {
	var envelope redisEnvelope
	if err := unmarshalJSON([]byte(payload), &envelope); err == nil && len(envelope.Data) > 0 {
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
