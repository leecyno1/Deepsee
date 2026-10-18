package hermespush

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSendWeixinContextCancelsBlockedRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseHandler
	}))
	defer func() {
		close(releaseHandler)
		server.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- SendWeixinContext(ctx, server.Client(), &WeixinConfig{
			Token:       "test-token",
			BaseURL:     server.URL,
			HomeChannel: "test-channel",
		}, WeixinSendRequest{Text: "shutdown"})
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("Hermes Weixin request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled Hermes Weixin request unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Hermes Weixin request ignored shutdown context")
	}
}
