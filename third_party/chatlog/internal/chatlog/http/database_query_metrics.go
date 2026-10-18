package http

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	databaseQueryGroupContextKey = "chatlog.database_query_group"
	databaseQueryTableContextKey = "chatlog.database_query_table"
	databaseQueryRowsContextKey  = "chatlog.database_query_rows"
	databaseQueryPathContextKey  = "chatlog.database_query_path"

	databaseQueryMetricCapacity = 512
	databaseSlowQueryThreshold  = 250 * time.Millisecond
)

type databaseQueryMetric struct {
	ID         uint64  `json:"id"`
	Operation  string  `json:"operation"`
	Group      string  `json:"group,omitempty"`
	Table      string  `json:"table,omitempty"`
	Path       string  `json:"path,omitempty"`
	Status     int     `json:"status"`
	Rows       int     `json:"rows"`
	DurationMS float64 `json:"duration_ms"`
	At         string  `json:"at"`
}

type databaseQueryMetrics struct {
	mu      sync.RWMutex
	nextID  uint64
	samples []databaseQueryMetric
}

func (m *databaseQueryMetrics) Reset() {
	m.mu.Lock()
	m.nextID = 0
	m.samples = nil
	m.mu.Unlock()
}

func (m *databaseQueryMetrics) Record(metric databaseQueryMetric) {
	metric.Operation = strings.TrimSpace(metric.Operation)
	if metric.Operation == "" {
		return
	}
	if metric.At == "" {
		metric.At = time.Now().Format(time.RFC3339Nano)
	}
	m.mu.Lock()
	m.nextID++
	metric.ID = m.nextID
	m.samples = append(m.samples, metric)
	if overflow := len(m.samples) - databaseQueryMetricCapacity; overflow > 0 {
		copy(m.samples, m.samples[overflow:])
		m.samples = m.samples[:databaseQueryMetricCapacity]
	}
	m.mu.Unlock()
}

func (m *databaseQueryMetrics) Snapshot() gin.H {
	m.mu.RLock()
	samples := append([]databaseQueryMetric(nil), m.samples...)
	m.mu.RUnlock()
	durations := make([]float64, 0, len(samples))
	totalRows := 0
	errors := 0
	byOperation := make(map[string]int)
	slow := make([]databaseQueryMetric, 0)
	for _, sample := range samples {
		durations = append(durations, sample.DurationMS)
		totalRows += sample.Rows
		byOperation[sample.Operation]++
		if sample.Status >= 400 {
			errors++
		}
		if sample.DurationMS >= float64(databaseSlowQueryThreshold)/float64(time.Millisecond) {
			slow = append(slow, sample)
		}
	}
	sort.Float64s(durations)
	sort.SliceStable(slow, func(i, j int) bool {
		if slow[i].DurationMS == slow[j].DurationMS {
			return slow[i].ID > slow[j].ID
		}
		return slow[i].DurationMS > slow[j].DurationMS
	})
	if len(slow) > 20 {
		slow = slow[:20]
	}
	return gin.H{
		"samples":           len(samples),
		"capacity":          databaseQueryMetricCapacity,
		"rows_total":        totalRows,
		"errors":            errors,
		"p50_ms":            databaseQueryPercentile(durations, 0.50),
		"p95_ms":            databaseQueryPercentile(durations, 0.95),
		"p99_ms":            databaseQueryPercentile(durations, 0.99),
		"slow_threshold_ms": roundedMilliseconds(databaseSlowQueryThreshold),
		"slow_queries":      slow,
		"by_operation":      byOperation,
	}
}

func databaseQueryPercentile(sorted []float64, percentile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return math.Round(sorted[index]*100) / 100
}

func databaseMetricString(c *gin.Context, key string) string {
	value, exists := c.Get(key)
	if !exists {
		return ""
	}
	return strings.TrimSpace(toString(value))
}

func databaseMetricRows(c *gin.Context) int {
	value, exists := c.Get(databaseQueryRowsContextKey)
	if !exists {
		return 0
	}
	return int(toInt64(value))
}
