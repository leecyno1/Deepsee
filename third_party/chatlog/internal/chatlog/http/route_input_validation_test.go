package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDatabaseHandlersRejectCatalogInvalidQueryValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{}
	tests := []struct {
		name    string
		target  string
		handler gin.HandlerFunc
	}{
		{name: "search mode", target: "/api/v1/db/search?keyword=x&mode=bogus", handler: service.handleSearchAllDBs},
		{name: "search match", target: "/api/v1/db/search?keyword=x&match=bogus", handler: service.handleSearchAllDBs},
		{name: "search sort", target: "/api/v1/db/search?keyword=x&sort=bogus", handler: service.handleSearchAllDBs},
		{name: "search limit syntax", target: "/api/v1/db/search?keyword=x&limit=abc", handler: service.handleSearchAllDBs},
		{name: "search limit maximum", target: "/api/v1/db/search?keyword=x&limit=501", handler: service.handleSearchAllDBs},
		{name: "data limit syntax", target: "/api/v1/db/data?group=g&file=f&table=t&limit=abc", handler: service.handleGetDBTableData},
		{name: "data limit maximum", target: "/api/v1/db/data?group=g&file=f&table=t&limit=10001", handler: service.handleGetDBTableData},
		{name: "data negative limit", target: "/api/v1/db/data?group=g&file=f&table=t&limit=-1", handler: service.handleGetDBTableData},
		{name: "data negative offset", target: "/api/v1/db/data?group=g&file=f&table=t&offset=-1", handler: service.handleGetDBTableData},
		{name: "data format", target: "/api/v1/db/data?group=g&file=f&table=t&format=pdf", handler: service.handleGetDBTableData},
		{name: "query format", target: "/api/v1/db/query?group=g&file=f&sql=SELECT%201&format=pdf", handler: service.handleExecuteSQL},
		{name: "query limit maximum", target: "/api/v1/db/query?group=g&file=f&sql=SELECT%201&limit=10001", handler: service.handleExecuteSQL},
		{name: "query negative offset", target: "/api/v1/db/query?group=g&file=f&sql=SELECT%201&offset=-1", handler: service.handleExecuteSQL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, tt.target, nil)
			tt.handler(ctx)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}

func TestStructuredQueryInputsRejectInvalidValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{}
	tests := []struct {
		name    string
		target  string
		handler gin.HandlerFunc
	}{
		{name: "unread filter", target: "/api/v1/unread?filter=bogus", handler: service.handleUnread},
		{name: "new message state", target: "/api/v1/new_messages?state=not-json", handler: service.handleNewMessages},
		{name: "new message limit maximum", target: "/api/v1/new_messages?limit=5001", handler: service.handleNewMessages},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, tt.target, nil)
			tt.handler(ctx)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}

func TestCatalogIntegerBoundsRejectInvalidValuesWithOneJSONError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{}
	tests := []struct {
		name    string
		target  string
		handler gin.HandlerFunc
	}{
		{name: "hook events syntax", target: "/api/v1/hook/events?limit=x", handler: service.handleHookEvents},
		{name: "hook events minimum", target: "/api/v1/hook/events?limit=0", handler: service.handleHookEvents},
		{name: "hook events maximum", target: "/api/v1/hook/events?limit=201", handler: service.handleHookEvents},
		{name: "sessions minimum", target: "/api/v1/sessions?limit=0", handler: service.handleSessions},
		{name: "sessions maximum", target: "/api/v1/sessions?limit=501", handler: service.handleSessions},
		{name: "history limit", target: "/api/v1/history?chat=x&limit=0", handler: service.handleHistory},
		{name: "history offset", target: "/api/v1/history?chat=x&offset=-1", handler: service.handleHistory},
		{name: "search limit", target: "/api/v1/search?keyword=x&limit=0", handler: service.handleSearch},
		{name: "search offset", target: "/api/v1/search?keyword=x&offset=-1", handler: service.handleSearch},
		{name: "message search match", target: "/api/v1/search?keyword=x&match=bogus", handler: service.handleSearch},
		{name: "message search sort", target: "/api/v1/search?keyword=x&sort=bogus", handler: service.handleSearch},
		{name: "message search cursor offset", target: "/api/v1/search?keyword=x&cursor=fixture&offset=1", handler: service.handleSearch},
		{name: "unread syntax", target: "/api/v1/unread?limit=x", handler: service.handleUnread},
		{name: "unread minimum", target: "/api/v1/unread?limit=0", handler: service.handleUnread},
		{name: "favorites limit", target: "/api/v1/favorites?limit=0", handler: service.handleFavorites},
		{name: "favorites type", target: "/api/v1/favorites?fav_type=x", handler: service.handleFavorites},
		{name: "sns notifications", target: "/api/v1/sns_notifications?limit=0", handler: service.handleSNSNotifications},
		{name: "sns feed", target: "/api/v1/sns_feed?limit=x", handler: service.handleSNSFeed},
		{name: "sns search", target: "/api/v1/sns_search?keyword=x&limit=0", handler: service.handleSNSSearch},
		{name: "contacts limit", target: "/api/v1/contacts?limit=0", handler: service.handleContacts},
		{name: "contacts offset", target: "/api/v1/contacts?offset=-1", handler: service.handleContacts},
		{name: "chatrooms limit", target: "/api/v1/chatrooms?limit=x", handler: service.handleChatRooms},
		{name: "chatrooms offset", target: "/api/v1/chatrooms?offset=-1", handler: service.handleChatRooms},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, tt.target, nil)
			tt.handler(ctx)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			if !json.Valid(recorder.Body.Bytes()) {
				t.Fatalf("handler emitted malformed or multiple JSON documents: %q", recorder.Body.String())
			}
		})
	}
}

func TestHookURLInputsRejectInvalidValuesBeforeSaving(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{}
	tests := []struct {
		name    string
		target  string
		body    string
		handler gin.HandlerFunc
	}{
		{
			name:    "invalid webhook URL",
			target:  "/api/v1/hook/config",
			body:    `{"notify_mode":"post","post_url":"file:///tmp/hook"}`,
			handler: service.handleHookConfigSet,
		},
		{
			name:    "active post rule without URL",
			target:  "/api/v1/hook/config",
			body:    `{"notify_mode":"post","keywords":"alert","post_url":""}`,
			handler: service.handleHookConfigSet,
		},
		{
			name:    "invalid keyword message mode",
			target:  "/api/v1/hook/config",
			body:    `{"notify_mode":"post","keyword_mode":"video"}`,
			handler: service.handleHookConfigSet,
		},
		{
			name:    "invalid Hermes base URL",
			target:  "/api/v1/hook/hermes/weixin",
			body:    `{"base_url":"not-a-url"}`,
			handler: service.handleHookHermesWeixinSet,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, tt.target, strings.NewReader(tt.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			tt.handler(ctx)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}

func TestValidateAbsoluteHTTPURL(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080/hook", "https://example.com/path", ""} {
		if err := validateAbsoluteHTTPURL(raw); err != nil {
			t.Errorf("validateAbsoluteHTTPURL(%q) = %v", raw, err)
		}
	}
	for _, raw := range []string{"file:///tmp/hook", "example.com/hook", "https://user:pass@example.com/hook"} {
		if err := validateAbsoluteHTTPURL(raw); err == nil {
			t.Errorf("validateAbsoluteHTTPURL(%q) unexpectedly succeeded", raw)
		}
	}
}
