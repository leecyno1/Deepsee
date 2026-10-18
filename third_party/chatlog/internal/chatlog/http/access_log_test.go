package http

import (
	"slices"
	"testing"
)

func TestRuntimeMonitoringRoutesDoNotWriteAccessLogs(t *testing.T) {
	for _, path := range []string{
		"/health",
		"/api/v1/runtime/status",
		"/api/v1/runtime/logs",
		"/api/v1/db/audit",
		"/api/v1/hook/status",
		"/api/v1/hook/events",
		"/api/v1/ocr/status",
		"/api/v1/events",
	} {
		if !slices.Contains(runtimeAccessLogSkipPaths, path) {
			t.Fatalf("runtime monitoring route %q still emits access logs", path)
		}
	}
}
