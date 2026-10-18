package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func runCORSTest(t *testing.T, bind, host, origin, method string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(corsMiddleware(bind))
	router.Any("/test", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(method, "http://"+host+"/test", nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestCORSMiddlewareAllowsSameOrigin(t *testing.T) {
	recorder := runCORSTest(t, "127.0.0.1:5030", "127.0.0.1:5030", "http://127.0.0.1:5030", http.MethodGet)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5030" {
		t.Fatalf("allow origin = %q", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "X-Chatlog-State") {
		t.Fatalf("allow headers = %q, missing X-Chatlog-State", got)
	}
	if got := recorder.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Chatlog-Next-Offset") {
		t.Fatalf("expose headers = %q, missing pagination headers", got)
	}
}

func TestCORSMiddlewareRejectsCrossSiteOriginAndReboundHost(t *testing.T) {
	for _, tc := range []struct {
		name   string
		host   string
		origin string
	}{
		{name: "cross-site origin", host: "127.0.0.1:5030", origin: "https://example.com"},
		{name: "different loopback port", host: "127.0.0.1:5030", origin: "http://127.0.0.1:3000"},
		{name: "rebound host", host: "example.com", origin: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := runCORSTest(t, "127.0.0.1:5030", tc.host, tc.origin, http.MethodGet)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", recorder.Code)
			}
		})
	}
}

func TestValidateListenAddressRequiresLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:5030", "localhost:5030", "[::1]:5030"} {
		if err := ValidateListenAddress(addr); err != nil {
			t.Fatalf("ValidateListenAddress(%q): %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:5030", ":5030", "192.168.1.20:5030", "example.com:5030"} {
		if err := ValidateListenAddress(addr); err == nil {
			t.Fatalf("ValidateListenAddress(%q) unexpectedly succeeded", addr)
		}
	}
}

func TestRequestBodyLimitMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(requestBodyLimitMiddleware())
	router.POST("/test", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(strings.Repeat("x", (2<<20)+1)))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
}
