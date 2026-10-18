package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSSEHubFiltersDeduplicatesAndReplays(t *testing.T) {
	hub := newSSEHub()
	_, events, unsubscribe := hub.subscribe(map[string]struct{}{"runtime": {}}, 0)
	defer unsubscribe()
	if err := hub.publish("runtime", "runtime", map[string]int{"value": 1}, true); err != nil {
		t.Fatal(err)
	}
	if err := hub.publish("runtime", "runtime", map[string]int{"value": 1}, true); err != nil {
		t.Fatal(err)
	}
	if err := hub.publish("ocr", "ocr_status", map[string]int{"pending": 2}, true); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-events:
		if event.Name != "runtime" || !strings.Contains(string(event.Data), `"value":1`) {
			t.Fatalf("unexpected replay event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime replay event was not delivered")
	}
	select {
	case event := <-events:
		t.Fatalf("topic filter or state deduplication failed: %#v", event)
	default:
	}
}

func TestSSEHubLastEventID(t *testing.T) {
	hub := newSSEHub()
	for value := 1; value <= 3; value++ {
		if err := hub.publish("message", "message", map[string]int{"value": value}, false); err != nil {
			t.Fatal(err)
		}
	}
	_, events, unsubscribe := hub.subscribe(map[string]struct{}{"message": {}}, 2)
	defer unsubscribe()
	select {
	case event := <-events:
		if event.ID != 3 {
			t.Fatalf("replayed event ID = %d, want 3", event.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("last-event-id replay was not delivered")
	}
}

func TestEventStreamWritesSSEFrameAndHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{events: newSSEHub()}
	router := gin.New()
	router.GET("/api/v1/events", service.handleEventStream)

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events?topics=runtime", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(recorder, request)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for len(service.events.activeTopics()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := service.events.publish("runtime", "runtime", map[string]string{"status": "ok"}, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event stream did not stop after request cancellation")
	}

	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q", got)
	}
	body := recorder.Body.String()
	for _, expected := range []string{"retry: 2000", "event: runtime", `data: {"status":"ok"}`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("SSE body missing %q: %q", expected, body)
		}
	}
}
