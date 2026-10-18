package errors

import (
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestErrAlwaysReturnsObjectEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{name: "application", err: InvalidArg("time"), wantStatus: http.StatusBadRequest, wantError: "invalid argument: time"},
		{name: "generic", err: stderrors.New("boom"), wantStatus: http.StatusInternalServerError, wantError: "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			Err(context, tt.err)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			var payload map[string]string
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("response is not an object: %s", recorder.Body.String())
			}
			if payload["error"] != tt.wantError {
				t.Fatalf("error = %q, want %q", payload["error"], tt.wantError)
			}
		})
	}
}
