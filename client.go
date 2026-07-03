package wskit

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// DefaultWriteWait is the timeout for writing a single message or ping frame to the WebSocket connection
const (
	DefaultWriteWait      = 10 * time.Second // Timeout for writing a single message or ping frame to the WebSocket connection
	DefaultPingInterval   = 30 * time.Second // Interval between outgoing ping frames to keep the connection alive
	DefaultMaxMessageSize = 64 * 1024        // Maximum allowed size of a single incoming message in bytes
	DefaultSendBufSize    = 256              // Default send channel buffer size (number of messages) for Client and SSEClient
)

// ClientOption configures a Client
type ClientOption func(*ClientConfig)

// MessageHandler handles inbound WebSocket messages read by Client.ReadPump.
// Returning an error stops the client read loop and unregisters the client.
type MessageHandler func(ctx context.Context, client *Client, messageType websocket.MessageType, data []byte) error

// ClientConfig holds configuration parameters for a Client or SSEClient
type ClientConfig struct {
	WriteWait      time.Duration
	PingInterval   time.Duration
	MaxMessageSize int64
	SendBufSize    int
	OnMessage      MessageHandler
}

func applyClientOptions(opts []ClientOption) ClientConfig {
	cfg := ClientConfig{
		WriteWait:      DefaultWriteWait,
		PingInterval:   DefaultPingInterval,
		MaxMessageSize: DefaultMaxMessageSize,
		SendBufSize:    DefaultSendBufSize,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// WithWriteWait sets the timeout for writing a message or ping
func WithWriteWait(d time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.WriteWait = d
	}
}

// WithPingInterval sets the interval between ping frames
func WithPingInterval(d time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.PingInterval = d
	}
}

// WithMaxMessageSize sets the maximum size of a single incoming message
func WithMaxMessageSize(n int64) ClientOption {
	return func(c *ClientConfig) {
		c.MaxMessageSize = n
	}
}

// WithSendBufSize sets the send channel buffer size
func WithSendBufSize(n int) ClientOption {
	return func(c *ClientConfig) {
		c.SendBufSize = n
	}
}

// WithMessageHandler sets the inbound WebSocket message handler used by ReadPump.
func WithMessageHandler(fn MessageHandler) ClientOption {
	return func(c *ClientConfig) {
		c.OnMessage = fn
	}
}

// Client represents a single WebSocket connection attached to a Hub
// It implements the Subscriber interface
type Client struct {
	hub           *Hub
	conn          *websocket.Conn
	send          chan []byte
	done          chan struct{}
	ctx           context.Context
	closeOnce     sync.Once
	connCloseOnce sync.Once
	writeWait     time.Duration
	pingInt       time.Duration
	sendClosed    atomic.Bool
	onMessage     MessageHandler
}

// compile-time interface check
var _ Subscriber = (*Client)(nil)

// NewClient creates a client for the given hub and connection. Call Register on the hub, then run ReadPump and WritePump in separate goroutines
func NewClient(hub *Hub, conn *websocket.Conn, ctx context.Context, opts ...ClientOption) (*Client, error) {
	if hub == nil {
		return nil, ErrNilHub
	}
	if conn == nil {
		return nil, ErrNilConn
	}
	if ctx == nil {
		return nil, ErrNilContext
	}
	cfg := applyClientOptions(opts)
	if cfg.SendBufSize <= 0 {
		cfg.SendBufSize = DefaultSendBufSize
	}
	if cfg.MaxMessageSize <= 0 {
		cfg.MaxMessageSize = DefaultMaxMessageSize
	}
	c := &Client{
		hub:       hub,
		conn:      conn,
		send:      make(chan []byte, cfg.SendBufSize),
		done:      make(chan struct{}),
		ctx:       ctx,
		writeWait: cfg.WriteWait,
		pingInt:   cfg.PingInterval,
		onMessage: cfg.OnMessage,
	}
	if c.pingInt <= 0 {
		c.pingInt = DefaultPingInterval
	}
	if c.writeWait <= 0 {
		c.writeWait = DefaultWriteWait
	}
	conn.SetReadLimit(cfg.MaxMessageSize)
	return c, nil
}

func (c *Client) closeConn() {
	c.connCloseOnce.Do(func() {
		_ = c.conn.Close(websocket.StatusNormalClosure, "")
	})
}

// Send enqueues data for writing. Non-blocking; returns false if the send buffer
// is full or the client has been closed
func (c *Client) Send(data []byte) bool {
	if c.sendClosed.Load() {
		return false
	}
	select {
	case c.send <- data:
		return true
	default:
		return false
	}
}

// Close signals the client to shut down. It is idempotent and safe to call
// from any goroutine. The underlying WebSocket connection is closed by
// WritePump/ReadPump defers
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		c.sendClosed.Store(true)
		close(c.done)
	})
}

// SendErr is like Send but reports whether the client is closed or its buffer is full.
func (c *Client) SendErr(data []byte) error {
	if c.sendClosed.Load() {
		return ErrSubscriberClosed
	}
	select {
	case <-c.done:
		return ErrSubscriberClosed
	case c.send <- data:
		return nil
	default:
		return ErrSubscriberBufferFull
	}
}

// ReadPump reads messages from the connection until it closes or errors. On exit it unregisters the client and closes the connection. Run in a goroutine
func (c *Client) ReadPump() {
	defer func() {
		_ = c.hub.Unregister(c)
		c.closeConn()
	}()
	for {
		messageType, data, err := c.conn.Read(c.ctx)
		if err != nil {
			return
		}
		if c.onMessage != nil {
			if err := c.onMessage(c.ctx, c, messageType, data); err != nil {
				c.hub.reportError("message_handler", err)
				return
			}
		}
	}
}

// WritePump writes messages from the send channel and sends ping frames at the configured interval. Run in a goroutine
func (c *Client) WritePump() {
	ticker := time.NewTicker(c.pingInt)
	defer func() {
		ticker.Stop()
		c.closeConn()
	}()

	for {
		select {
		case message := <-c.send:
			ctx, cancel := context.WithTimeout(c.ctx, c.writeWait)
			w, err := c.conn.Writer(ctx, websocket.MessageText)
			if err != nil {
				cancel()
				return
			}
			if _, err := w.Write(message); err != nil {
				cancel()
				return
			}
			if err := w.Close(); err != nil {
				cancel()
				return
			}
			cancel()
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(c.ctx, c.writeWait)
			if err := c.conn.Ping(ctx); err != nil {
				cancel()
				return
			}
			cancel()
		case <-c.done:
			return
		case <-c.ctx.Done():
			return
		}
	}
}
