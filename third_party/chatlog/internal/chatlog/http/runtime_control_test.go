package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRuntimeTerminateRequiresExplicitConfirmation(t *testing.T) {
	service := NewService(lifecycleConfig{}, nil, nil, nil)
	runnerCalled := make(chan struct{}, 1)
	service.runtimeControlRunner = func() { runnerCalled <- struct{}{} }
	service.runtimeControlDelay = time.Millisecond

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/terminate", strings.NewReader(`{"confirmation":"wrong"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:12345"
	service.router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-runnerCalled:
		t.Fatal("termination runner started without confirmation")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRuntimeTerminateAcknowledgesBeforeRunning(t *testing.T) {
	service := NewService(lifecycleConfig{}, nil, nil, nil)
	runnerCalled := make(chan struct{}, 1)
	service.runtimeControlRunner = func() { runnerCalled <- struct{}{} }
	service.runtimeControlDelay = time.Millisecond

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/terminate", strings.NewReader(`{"confirmation":"terminate-and-restart"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:12345"
	service.router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"accepted":true`) {
		t.Fatalf("response did not acknowledge termination: %s", recorder.Body.String())
	}
	select {
	case <-runnerCalled:
	case <-time.After(time.Second):
		t.Fatal("termination runner did not start")
	}

	// Repeated clicks are idempotent and do not schedule another runner.
	second := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/runtime/terminate", strings.NewReader(`{"confirmation":"terminate-and-restart"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:12345"
	service.router.ServeHTTP(second, request)
	if second.Code != http.StatusAccepted || !strings.Contains(second.Body.String(), `"pending":true`) {
		t.Fatalf("repeat status = %d, body=%s", second.Code, second.Body.String())
	}
	select {
	case <-runnerCalled:
		t.Fatal("termination runner was scheduled twice")
	case <-time.After(20 * time.Millisecond):
	}
}
