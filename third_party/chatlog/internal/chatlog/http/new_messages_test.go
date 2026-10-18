package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sjzar/chatlog/internal/model"
)

func TestNewMessageCursorPreservesLimitRemainderAndSameSecond(t *testing.T) {
	baseline := newMessageCursor{Timestamp: 100, LocalID: 0}
	candidates := []*model.Message{
		{Talker: "chat-a", Time: time.Unix(100, 0), DBLocalID: 2},
		{Talker: "chat-a", Time: time.Unix(100, 0), DBLocalID: 1},
	}

	first, hasMore := takeNewMessageBatch(candidates, 1)
	if !hasMore || len(first) != 1 || first[0].DBLocalID != 1 {
		t.Fatalf("first batch = %+v, has_more=%v", first, hasMore)
	}
	cursor := newMessageCursor{Timestamp: first[0].Time.Unix(), LocalID: first[0].DBLocalID}
	if !messageAfterNewCursor(candidates[1], cursor) {
		t.Fatal("second same-second message was skipped by cursor")
	}
	if messageAfterNewCursor(first[0], cursor) {
		t.Fatal("already emitted message would be repeated")
	}
	if !cursor.After(baseline) {
		t.Fatal("cursor did not advance")
	}
}

func TestNewMessageCursorOrdersOldestAcrossChats(t *testing.T) {
	candidates := []*model.Message{
		{Talker: "chat-b", Time: time.Unix(101, 0), DBLocalID: 1},
		{Talker: "chat-c", Time: time.Unix(100, 0), DBLocalID: 3},
		{Talker: "chat-a", Time: time.Unix(100, 0), DBLocalID: 2},
	}
	batch, hasMore := takeNewMessageBatch(candidates, 2)
	if !hasMore {
		t.Fatal("expected pending remainder")
	}
	if batch[0].Talker != "chat-a" || batch[1].Talker != "chat-c" {
		t.Fatalf("batch order = %s, %s", batch[0].Talker, batch[1].Talker)
	}
}

func TestNewMessageStateAcceptsLegacyAndEmitsPreciseStrings(t *testing.T) {
	state, err := parseNewMessageState(`{"legacy":1700000000,"precise":"1700000001:42"}`)
	if err != nil {
		t.Fatal(err)
	}
	if state["legacy"].Timestamp != 1_700_000_000 || state["legacy"].LocalID != int64(1<<63-1) {
		t.Fatalf("legacy cursor = %+v", state["legacy"])
	}
	if state["precise"] != (newMessageCursor{Timestamp: 1_700_000_001, LocalID: 42}) {
		t.Fatalf("precise cursor = %+v", state["precise"])
	}
	encoded := encodeNewMessageState(state)
	raw, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip map[string]string
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip["precise"] != "1700000001:42" {
		t.Fatalf("round-trip cursor = %q", roundTrip["precise"])
	}
}

func TestNewMessageStateRejectsMalformedCursor(t *testing.T) {
	for _, raw := range []string{
		`not-json`,
		`{"chat":"bad"}`,
		`{"chat":"1:-1"}`,
		`{"":1}`,
	} {
		if _, err := parseNewMessageState(raw); err == nil {
			t.Errorf("state %q unexpectedly accepted", raw)
		}
	}
}

func TestNewMessageStateHeaderTakesPrecedence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{}
	router := gin.New()
	router.GET("/api/v1/new_messages", service.handleNewMessages)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/new_messages?state=%7B%7D", nil)
	req.Header.Set("X-Chatlog-State", "also-not-json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if body := recorder.Body.String(); !containsAll(body, "invalid argument", "state") {
		t.Fatalf("unexpected response: %s", body)
	}
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
