package wskit

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestHubKeepsSubscriberOperationOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewHub()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		clients := make([]*SSEClient, 32)
		for i := range clients {
			clients[i] = NewSSEClient(1)
			if err := hub.Register(clients[i]); err != nil {
				t.Fatal(err)
			}
			if err := hub.Unregister(clients[i]); err != nil {
				t.Fatal(err)
			}
		}
		go hub.Run(ctx)
		synctest.Wait()
		if got := hub.SubscriberCount(); got != 0 {
			t.Errorf("retained %d disconnected clients", got)
		}
		for _, client := range clients {
			select {
			case <-client.done:
			default:
				t.Error("client not closed")
			}
		}
	})
}

func TestHubShutdownClosesPendingSubscribers(t *testing.T) {
	hub := NewHub()
	client := NewSSEClient(1)
	if err := hub.Register(client); err != nil {
		t.Fatal(err)
	}
	hub.Run(nil) //nolint:staticcheck // nil context follows the same shutdown path
	select {
	case <-client.done:
	default:
		t.Fatal("pending subscriber retained after shutdown")
	}
	for range 100 {
		if err := hub.Register(client); !errors.Is(err, ErrHubStopped) {
			t.Fatalf("Register: %v", err)
		}
		if err := hub.Unregister(client); !errors.Is(err, ErrHubStopped) {
			t.Fatalf("Unregister: %v", err)
		}
		if err := hub.Broadcast([]byte("event")); !errors.Is(err, ErrHubStopped) {
			t.Fatalf("Broadcast: %v", err)
		}
	}
}

func TestHubShutdownReleasesBlockedEnqueuers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewHub(WithRegisterBuf(1))
		first, second := NewSSEClient(1), NewSSEClient(1)
		if err := hub.Register(first); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() { result <- hub.Register(second) }()
		synctest.Wait()
		hub.Run(nil) //nolint:staticcheck // exercise shutdown without consuming the full queue
		if err := <-result; !errors.Is(err, ErrHubStopped) {
			t.Errorf("blocked Register: %v", err)
		}
		select {
		case <-first.done:
		default:
			t.Error("pending subscriber not closed")
		}
	})
}

func TestHubBroadcastEventCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewHub(WithBroadcastBuf(0))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- hub.BroadcastEvent(ctx, NewEvent("update", nil)) }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("BroadcastEvent: %v", err)
			}
		default:
			t.Error("canceled event still waiting for queue space")
		}
	})
}
