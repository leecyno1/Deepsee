package http

import (
	"testing"
)

func TestDatabaseQueryMetricsPercentilesAndSlowQueries(t *testing.T) {
	var metrics databaseQueryMetrics
	for index, duration := range []float64{10, 20, 30, 260, 900} {
		metrics.Record(databaseQueryMetric{
			Operation:  "/api/v1/db/search",
			Group:      "message",
			Path:       "fts_shadow_content",
			Status:     200,
			Rows:       index + 1,
			DurationMS: duration,
		})
	}
	snapshot := metrics.Snapshot()
	if snapshot["samples"] != 5 {
		t.Fatalf("samples = %v", snapshot["samples"])
	}
	if snapshot["p50_ms"] != float64(30) || snapshot["p95_ms"] != float64(900) {
		t.Fatalf("percentiles = p50 %v p95 %v", snapshot["p50_ms"], snapshot["p95_ms"])
	}
	slow, ok := snapshot["slow_queries"].([]databaseQueryMetric)
	if !ok || len(slow) != 2 || slow[0].DurationMS != 900 {
		t.Fatalf("slow queries = %#v", snapshot["slow_queries"])
	}
}

func TestDatabaseQueryMetricsStayBounded(t *testing.T) {
	var metrics databaseQueryMetrics
	for index := 0; index < databaseQueryMetricCapacity+100; index++ {
		metrics.Record(databaseQueryMetric{Operation: "fixture", DurationMS: float64(index)})
	}
	snapshot := metrics.Snapshot()
	if snapshot["samples"] != databaseQueryMetricCapacity {
		t.Fatalf("samples = %v", snapshot["samples"])
	}
}
