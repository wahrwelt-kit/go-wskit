//go:build integration

package wskit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func startRedis(t *testing.T) *redis.Client {
	t.Helper()
	ctx := context.Background()
	container, err := tcredis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	endpoint, err := container.Endpoint(ctx, "")
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{Addr: endpoint})
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(ctx).Err())
	return client
}

func startIntegrationHub(t *testing.T, redisClient *redis.Client, channel string, opts ...HubOption) (*Hub, context.CancelFunc) {
	t.Helper()
	allOpts := append([]HubOption{WithRedis(redisClient, channel)}, opts...)
	hub := NewHub(allOpts...)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel() })
	go hub.Run(ctx)
	go hub.SubscribeToRedis(ctx)

	waitForSubscription(t, redisClient, channel)
	return hub, cancel
}

func waitForSubscription(t *testing.T, rc *redis.Client, channel string, minSubs ...int64) {
	t.Helper()
	var need int64 = 1
	if len(minSubs) > 0 {
		need = minSubs[0]
	}
	deadline := time.After(5 * time.Second)
	for {
		subs, err := rc.PubSubNumSub(context.Background(), channel).Result()
		if err == nil && subs[channel] >= need {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("Redis subscription for %q not ready: need %d (timeout)", channel, need)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func startIntegrationServer(t *testing.T, hub *Hub) *httptest.Server {
	t.Helper()
	connCtx, connCancel := context.WithCancel(context.Background())
	t.Cleanup(connCancel)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, err := Accept(connCtx, w, r, hub, nil)
		if err != nil {
			return
		}
		go client.ReadPump()
		go client.WritePump()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dialIntegration(t *testing.T, srvURL string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srvURL[4:], nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func readEvent(t *testing.T, conn *websocket.Conn) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	require.NoError(t, err)
	var ev Event
	require.NoError(t, json.Unmarshal(data, &ev))
	return ev
}

func waitCount(t *testing.T, hub *Hub, want int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if hub.SubscriberCount() == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("SubscriberCount = %d, want %d (timeout)", hub.SubscriberCount(), want)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestIntegration_Redis_BroadcastEvent(t *testing.T) {
	t.Parallel()
	rc := startRedis(t)
	hub, _ := startIntegrationHub(t, rc, "test:broadcast")
	srv := startIntegrationServer(t, hub)
	conn := dialIntegration(t, srv.URL)
	waitCount(t, hub, 1)

	err := hub.BroadcastEvent(context.Background(), NewEvent("redis-event", "payload"))
	require.NoError(t, err)

	ev := readEvent(t, conn)
	assert.Equal(t, "redis-event", ev.Type)
}

func TestIntegration_Redis_BroadcastJSON(t *testing.T) {
	t.Parallel()
	rc := startRedis(t)
	hub, _ := startIntegrationHub(t, rc, "test:json")
	srv := startIntegrationServer(t, hub)
	conn := dialIntegration(t, srv.URL)
	waitCount(t, hub, 1)

	err := hub.BroadcastJSON(context.Background(), "json-test", map[string]int{"n": 1})
	require.NoError(t, err)

	ev := readEvent(t, conn)
	assert.Equal(t, "json-test", ev.Type)
}

func TestIntegration_Redis_MultiInstance(t *testing.T) {
	t.Parallel()
	rc := startRedis(t)
	channel := "test:multi-instance"

	hub1, _ := startIntegrationHub(t, rc, channel)
	hub2, _ := startIntegrationHub(t, rc, channel)
	waitForSubscription(t, rc, channel, 2)

	srv1 := startIntegrationServer(t, hub1)
	srv2 := startIntegrationServer(t, hub2)

	conn1 := dialIntegration(t, srv1.URL)
	conn2 := dialIntegration(t, srv2.URL)
	waitCount(t, hub1, 1)
	waitCount(t, hub2, 1)

	err := hub1.BroadcastEvent(context.Background(), NewEvent("from-hub1", nil))
	require.NoError(t, err)

	ev1 := readEvent(t, conn1)
	assert.Equal(t, "from-hub1", ev1.Type)

	ev2 := readEvent(t, conn2)
	assert.Equal(t, "from-hub1", ev2.Type)
}

func TestIntegration_Redis_MultipleClients(t *testing.T) {
	t.Parallel()
	rc := startRedis(t)
	hub, _ := startIntegrationHub(t, rc, "test:multi-client")
	srv := startIntegrationServer(t, hub)

	conns := make([]*websocket.Conn, 5)
	for i := range conns {
		conns[i] = dialIntegration(t, srv.URL)
	}
	waitCount(t, hub, 5)

	err := hub.BroadcastJSON(context.Background(), "all", nil)
	require.NoError(t, err)

	for i, conn := range conns {
		ev := readEvent(t, conn)
		assert.Equal(t, "all", ev.Type, "client %d", i)
	}
}

func TestIntegration_Redis_ClientDisconnectAndReconnect(t *testing.T) {
	t.Parallel()
	rc := startRedis(t)
	hub, _ := startIntegrationHub(t, rc, "test:reconnect")
	srv := startIntegrationServer(t, hub)

	conn := dialIntegration(t, srv.URL)
	waitCount(t, hub, 1)

	conn.Close(websocket.StatusNormalClosure, "bye")
	waitCount(t, hub, 0)

	conn2 := dialIntegration(t, srv.URL)
	waitCount(t, hub, 1)

	err := hub.BroadcastJSON(context.Background(), "after-reconnect", nil)
	require.NoError(t, err)

	ev := readEvent(t, conn2)
	assert.Equal(t, "after-reconnect", ev.Type)
}

func TestIntegration_Redis_OnConnect(t *testing.T) {
	t.Parallel()
	rc := startRedis(t)
	hub, _ := startIntegrationHub(t, rc, "test:onconnect", WithOnConnect(func(sub Subscriber) {
		data, _ := json.Marshal(NewEvent("welcome", nil))
		sub.Send(data)
	}))
	srv := startIntegrationServer(t, hub)
	conn := dialIntegration(t, srv.URL)

	ev := readEvent(t, conn)
	assert.Equal(t, "welcome", ev.Type)
}

func TestIntegration_Redis_GracefulShutdown(t *testing.T) {
	t.Parallel()
	rc := startRedis(t)
	hub, cancel := startIntegrationHub(t, rc, "test:shutdown")
	srv := startIntegrationServer(t, hub)
	conn := dialIntegration(t, srv.URL)
	waitCount(t, hub, 1)

	cancel()
	time.Sleep(200 * time.Millisecond)

	ctx, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	_, _, err := conn.Read(ctx)
	assert.Error(t, err)
}
