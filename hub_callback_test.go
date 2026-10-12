package wskit

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestHubTimeoutCallbackCanCallStoppedHub(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resume := make(chan struct{})
		var hub *Hub
		hub = NewHub(WithBroadcastBuf(0), WithChannelTimeout(time.Second), WithOnTimeout(func(string) {
			<-resume
			if err := hub.Register(NewSSEClient(1)); !errors.Is(err, ErrHubStopped) {
				t.Errorf("Register after shutdown: %v", err)
			}
		}))
		finished := make(chan struct{})
		go func() { defer close(finished); _ = hub.Broadcast([]byte("event")) }()
		time.Sleep(2 * time.Second)
		stopped := make(chan struct{})
		go func() { defer close(stopped); hub.Run(nil) }() //nolint:staticcheck // exercise shutdown with a pending callback
		synctest.Wait()
		close(resume)
		<-finished
		<-stopped
	})
}
