package http

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	runtimeFileTraceEventLimit             = 1_000
	runtimeFileTraceRankingLimit           = 100
	runtimeFileTraceAggregateLimit         = 10_000
	runtimeFileTraceMaxLine                = 1 << 20
	runtimeFileTraceDefaultAutoStopWriteMB = 50
	runtimeFileTraceMaxAutoStopWriteMB     = 102_400
)

type runtimeFileTraceTarget struct {
	PID  int    `json:"pid"`
	Role string `json:"role"`
	Name string `json:"name"`
}

type runtimeFileTraceEvent struct {
	ID         uint64  `json:"id"`
	At         string  `json:"at"`
	Kind       string  `json:"kind"`
	Operation  string  `json:"operation"`
	Bytes      uint64  `json:"bytes"`
	Path       string  `json:"path,omitempty"`
	Process    string  `json:"process,omitempty"`
	PID        int     `json:"pid,omitempty"`
	ThreadID   int     `json:"thread_id,omitempty"`
	FD         int     `json:"fd,omitempty"`
	PathSource string  `json:"path_source,omitempty"`
	DurationMS float64 `json:"duration_ms"`
	Raw        string  `json:"raw"`
	hasFD      bool
	fileType   string
}

type runtimeFileTraceRanking struct {
	Rank       int    `json:"rank"`
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Process    string `json:"process,omitempty"`
	PID        int    `json:"pid,omitempty"`
	Operations uint64 `json:"operations"`
	Bytes      uint64 `json:"bytes"`
	FirstAt    string `json:"first_at,omitempty"`
	LastAt     string `json:"last_at,omitempty"`
}

type runtimeFileTraceAggregate struct {
	Kind       string
	Path       string
	Process    string
	PID        int
	Operations uint64
	Bytes      uint64
	FirstAt    string
	LastAt     string
}

type runtimeFileTraceSummary struct {
	Supported         bool                     `json:"supported"`
	Status            string                   `json:"status"`
	Message           string                   `json:"message"`
	StartedAt         string                   `json:"started_at,omitempty"`
	StoppedAt         string                   `json:"stopped_at,omitempty"`
	LastEventAt       string                   `json:"last_event_at,omitempty"`
	EventsTotal       uint64                   `json:"events_total"`
	RetainedEvents    int                      `json:"retained_events"`
	DroppedEvents     uint64                   `json:"dropped_events"`
	FilteredOps       uint64                   `json:"filtered_operations"`
	ReadOperations    uint64                   `json:"read_operations"`
	WriteOperations   uint64                   `json:"write_operations"`
	MetadataOps       uint64                   `json:"metadata_operations"`
	ReadBytes         uint64                   `json:"read_bytes"`
	WriteBytes        uint64                   `json:"write_bytes"`
	SessionWriteBytes uint64                   `json:"session_write_bytes"`
	AutoStopEnabled   bool                     `json:"auto_stop_enabled"`
	AutoStopWriteMB   float64                  `json:"auto_stop_write_mb"`
	AutoStopTriggered bool                     `json:"auto_stop_triggered"`
	AutoStoppedAt     string                   `json:"auto_stopped_at,omitempty"`
	AutoStoppedBytes  uint64                   `json:"auto_stopped_bytes,omitempty"`
	Targets           []runtimeFileTraceTarget `json:"targets"`
}

type runtimeFileTraceStartRequest struct {
	AutoStopEnabled bool    `json:"auto_stop_enabled"`
	AutoStopWriteMB float64 `json:"auto_stop_write_mb"`
}

type runtimeFileTraceDetails struct {
	Summary      runtimeFileTraceSummary   `json:"summary"`
	Events       []runtimeFileTraceEvent   `json:"events"`
	ReadRanking  []runtimeFileTraceRanking `json:"read_ranking"`
	WriteRanking []runtimeFileTraceRanking `json:"write_ranking"`
	Filter       gin.H                     `json:"filter"`
}

type runtimeFileTracePlatformSession interface {
	io.ReadCloser
	Stop()
}

type runtimeFileTraceMonitor struct {
	mu sync.Mutex

	generation uint64
	status     string
	message    string
	startedAt  time.Time
	stoppedAt  time.Time
	lastEvent  time.Time
	targets    []runtimeFileTraceTarget
	session    runtimeFileTracePlatformSession

	events     []runtimeFileTraceEvent
	nextEvent  int
	nextID     uint64
	eventsSeen uint64
	dropped    uint64
	filtered   uint64
	readOps    uint64
	writeOps   uint64
	metadata   uint64
	readBytes  uint64
	writeBytes uint64
	aggregates map[string]*runtimeFileTraceAggregate

	sessionWriteBase  uint64
	autoStopEnabled   bool
	autoStopWriteMB   float64
	autoStopTriggered bool
	autoStoppedAt     time.Time
	autoStoppedBytes  uint64
}

func (s *Service) runtimeFileTraceSummary() runtimeFileTraceSummary {
	supported, supportMessage := runtimeFileTracePlatformSupport()
	m := &s.runtimeFileTrace
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.status
	message := m.message
	if status == "" {
		status = "stopped"
		message = "详细追踪尚未开启"
	}
	if !supported && message == "详细追踪尚未开启" {
		message = supportMessage
	}
	return m.summaryLocked(supported, status, message)
}

func (m *runtimeFileTraceMonitor) summaryLocked(supported bool, status, message string) runtimeFileTraceSummary {
	autoStopWriteMB := m.autoStopWriteMB
	if autoStopWriteMB <= 0 {
		autoStopWriteMB = runtimeFileTraceDefaultAutoStopWriteMB
	}
	sessionWriteBytes := uint64(0)
	if m.writeBytes >= m.sessionWriteBase {
		sessionWriteBytes = m.writeBytes - m.sessionWriteBase
	}
	summary := runtimeFileTraceSummary{
		Supported:         supported,
		Status:            status,
		Message:           message,
		EventsTotal:       m.eventsSeen,
		RetainedEvents:    len(m.events),
		DroppedEvents:     m.dropped,
		FilteredOps:       m.filtered,
		ReadOperations:    m.readOps,
		WriteOperations:   m.writeOps,
		MetadataOps:       m.metadata,
		ReadBytes:         m.readBytes,
		WriteBytes:        m.writeBytes,
		SessionWriteBytes: sessionWriteBytes,
		AutoStopEnabled:   m.autoStopEnabled,
		AutoStopWriteMB:   autoStopWriteMB,
		AutoStopTriggered: m.autoStopTriggered,
		AutoStoppedBytes:  m.autoStoppedBytes,
		Targets:           append([]runtimeFileTraceTarget(nil), m.targets...),
	}
	if !m.startedAt.IsZero() {
		summary.StartedAt = m.startedAt.Format(time.RFC3339Nano)
	}
	if !m.stoppedAt.IsZero() {
		summary.StoppedAt = m.stoppedAt.Format(time.RFC3339Nano)
	}
	if !m.lastEvent.IsZero() {
		summary.LastEventAt = m.lastEvent.Format(time.RFC3339Nano)
	}
	if !m.autoStoppedAt.IsZero() {
		summary.AutoStoppedAt = m.autoStoppedAt.Format(time.RFC3339Nano)
	}
	return summary
}

func normalizeRuntimeFileTraceStartRequest(request runtimeFileTraceStartRequest) (runtimeFileTraceStartRequest, error) {
	if request.AutoStopWriteMB == 0 {
		request.AutoStopWriteMB = runtimeFileTraceDefaultAutoStopWriteMB
	}
	if request.AutoStopWriteMB < 0.1 || request.AutoStopWriteMB > runtimeFileTraceMaxAutoStopWriteMB {
		return request, fmt.Errorf("自动暂停写入阈值需在 0.1–%d MB 之间", runtimeFileTraceMaxAutoStopWriteMB)
	}
	return request, nil
}

func runtimeFileTraceBytesForMB(value float64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value * 1024 * 1024)
}

func (s *Service) startRuntimeFileTrace(ctx context.Context, request runtimeFileTraceStartRequest) (runtimeFileTraceSummary, error) {
	request, err := normalizeRuntimeFileTraceStartRequest(request)
	if err != nil {
		return s.runtimeFileTraceSummary(), err
	}
	supported, supportMessage := runtimeFileTracePlatformSupport()
	if !supported {
		return s.runtimeFileTraceSummary(), fmt.Errorf("%s", supportMessage)
	}

	processCtx, cancel := context.WithTimeout(ctx, runtimeFileIOProcessListTimeout)
	defer cancel()
	processes, err := listRuntimeOSProcesses(processCtx)
	if err != nil {
		return s.runtimeFileTraceSummary(), fmt.Errorf("读取关联进程列表: %w", err)
	}
	selected := selectRuntimeFileIOProcesses(processes, os.Getpid())
	targets := make([]runtimeFileTraceTarget, 0, len(selected))
	pids := make([]int, 0, len(selected))
	pidSet := make(map[int]struct{}, len(selected))
	for _, process := range selected {
		if process.PID <= 0 {
			continue
		}
		pids = append(pids, process.PID)
		pidSet[process.PID] = struct{}{}
		targets = append(targets, runtimeFileTraceTarget{
			PID:  process.PID,
			Role: runtimeFileIORole(process.Command, process.PID == os.Getpid()),
			Name: runtimeFileIOProcessName(process.Command),
		})
	}
	if len(pids) == 0 {
		return s.runtimeFileTraceSummary(), fmt.Errorf("当前没有可追踪的关联进程")
	}

	m := &s.runtimeFileTrace
	m.mu.Lock()
	if m.status == "running" || m.status == "authorizing" {
		summary := m.summaryLocked(true, m.status, m.message)
		m.mu.Unlock()
		return summary, nil
	}
	m.generation++
	generation := m.generation
	m.status = "authorizing"
	m.message = "等待 macOS 管理员授权"
	m.startedAt = time.Now()
	m.stoppedAt = time.Time{}
	m.sessionWriteBase = m.writeBytes
	m.autoStopEnabled = request.AutoStopEnabled
	m.autoStopWriteMB = request.AutoStopWriteMB
	m.autoStopTriggered = false
	m.autoStoppedAt = time.Time{}
	m.autoStoppedBytes = 0
	m.targets = targets
	m.aggregates = make(map[string]*runtimeFileTraceAggregate)
	m.session = nil
	m.mu.Unlock()

	session, err := startRuntimeFileTracePlatform(pids)
	if err != nil {
		m.mu.Lock()
		if m.generation == generation {
			m.status = "error"
			m.message = err.Error()
			m.stoppedAt = time.Now()
		}
		summary := m.summaryLocked(true, m.status, m.message)
		m.mu.Unlock()
		return summary, err
	}

	m.mu.Lock()
	if m.generation != generation || m.status != "authorizing" {
		m.mu.Unlock()
		session.Stop()
		return s.runtimeFileTraceSummary(), nil
	}
	m.session = session
	m.status = "running"
	m.message = fmt.Sprintf("正在追踪 %d 个关联进程；记录仅保存在内存中", len(pids))
	summary := m.summaryLocked(true, m.status, m.message)
	m.mu.Unlock()

	go s.scanRuntimeFileTrace(generation, session, pidSet, targets)
	return summary, nil
}

func (s *Service) scanRuntimeFileTrace(generation uint64, session runtimeFileTracePlatformSession, targetPIDs map[int]struct{}, targets []runtimeFileTraceTarget) {
	scanner := bufio.NewScanner(session)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, runtimeFileTraceMaxLine)
	openFiles := runtimeFileTraceOpenFiles(targetPIDs)
	processPIDs := runtimeFileTraceTargetPIDs(targets)
	lastOpenFileRefresh := time.Now()
	for scanner.Scan() {
		event, ok := parseRuntimeFileTraceLine(scanner.Text(), time.Now())
		if !ok {
			continue
		}
		event = resolveRuntimeFileTraceEvent(event, processPIDs, openFiles)
		if strings.TrimSpace(event.Path) == "" && event.hasFD && time.Since(lastOpenFileRefresh) >= time.Second {
			openFiles = runtimeFileTraceOpenFiles(targetPIDs)
			lastOpenFileRefresh = time.Now()
			event = resolveRuntimeFileTraceEvent(event, processPIDs, openFiles)
		}
		if !runtimeFileTraceEventIsFile(event) {
			s.recordRuntimeFileTraceFiltered(generation)
			continue
		}
		s.appendRuntimeFileTraceEvent(generation, event)
	}
	err := scanner.Err()
	m := &s.runtimeFileTrace
	m.mu.Lock()
	if m.generation == generation && m.status == "running" {
		m.status = "error"
		if err != nil {
			m.message = "详细追踪已中断：" + err.Error()
		} else {
			m.message = "详细追踪进程已结束"
		}
		m.stoppedAt = time.Now()
		m.session = nil
	}
	m.mu.Unlock()
	session.Stop()
}

func (s *Service) recordRuntimeFileTraceFiltered(generation uint64) {
	m := &s.runtimeFileTrace
	m.mu.Lock()
	if m.generation == generation && m.status == "running" {
		m.filtered++
	}
	m.mu.Unlock()
}

func (s *Service) appendRuntimeFileTraceEvent(generation uint64, event runtimeFileTraceEvent) {
	m := &s.runtimeFileTrace
	m.mu.Lock()
	if m.generation != generation || m.status != "running" {
		m.mu.Unlock()
		return
	}
	m.nextID++
	event.ID = m.nextID
	m.eventsSeen++
	switch event.Kind {
	case "read":
		m.readOps++
		m.readBytes += event.Bytes
	case "write":
		m.writeOps++
		m.writeBytes += event.Bytes
	default:
		m.metadata++
	}
	m.aggregateEventLocked(event)
	if parsed, parseErr := time.Parse(time.RFC3339Nano, event.At); parseErr == nil {
		m.lastEvent = parsed
	} else {
		m.lastEvent = time.Now()
	}
	if len(m.events) < runtimeFileTraceEventLimit {
		m.events = append(m.events, event)
	} else {
		m.events[m.nextEvent] = event
		m.nextEvent = (m.nextEvent + 1) % runtimeFileTraceEventLimit
		m.dropped++
	}

	var session runtimeFileTracePlatformSession
	if event.Kind == "write" && m.autoStopEnabled {
		sessionWriteBytes := m.writeBytes
		if m.writeBytes >= m.sessionWriteBase {
			sessionWriteBytes = m.writeBytes - m.sessionWriteBase
		}
		thresholdBytes := runtimeFileTraceBytesForMB(m.autoStopWriteMB)
		if thresholdBytes > 0 && sessionWriteBytes >= thresholdBytes {
			m.generation++
			m.status = "paused"
			m.autoStopTriggered = true
			m.autoStoppedAt = time.Now()
			m.autoStoppedBytes = sessionWriteBytes
			m.stoppedAt = m.autoStoppedAt
			m.message = fmt.Sprintf("本轮写入达到 %.2f MB，已自动暂停；明细保留在内存中", float64(sessionWriteBytes)/(1024*1024))
			session = m.session
			m.session = nil
		}
	}
	m.mu.Unlock()
	if session != nil {
		session.Stop()
	}
}

func (s *Service) stopRuntimeFileTrace() runtimeFileTraceSummary {
	m := &s.runtimeFileTrace
	m.mu.Lock()
	supported, _ := runtimeFileTracePlatformSupport()
	if m.status != "running" && m.status != "authorizing" {
		status := m.status
		message := m.message
		if status == "" {
			status = "stopped"
			message = "详细追踪尚未开启"
		}
		summary := m.summaryLocked(supported, status, message)
		m.mu.Unlock()
		return summary
	}
	m.generation++
	session := m.session
	m.session = nil
	m.status = "stopped"
	m.message = "详细追踪已停止；现有记录仍保留在内存中"
	m.stoppedAt = time.Now()
	summary := m.summaryLocked(supported, m.status, m.message)
	m.mu.Unlock()
	if session != nil {
		session.Stop()
	}
	return summary
}

func (s *Service) clearRuntimeFileTrace() runtimeFileTraceSummary {
	m := &s.runtimeFileTrace
	m.mu.Lock()
	m.events = nil
	m.nextEvent = 0
	m.eventsSeen = 0
	m.dropped = 0
	m.filtered = 0
	m.readOps = 0
	m.writeOps = 0
	m.metadata = 0
	m.readBytes = 0
	m.writeBytes = 0
	m.aggregates = nil
	m.sessionWriteBase = 0
	m.autoStopTriggered = false
	m.autoStoppedAt = time.Time{}
	m.autoStoppedBytes = 0
	m.lastEvent = time.Time{}
	supported, supportMessage := runtimeFileTracePlatformSupport()
	status := m.status
	message := m.message
	if status == "paused" {
		status = "stopped"
		message = "详细追踪记录已清空"
		m.status = status
		m.message = message
	}
	if status == "" {
		status = "stopped"
		message = supportMessage
	}
	summary := m.summaryLocked(supported, status, message)
	m.mu.Unlock()
	return summary
}

func (s *Service) runtimeFileTraceDetails(kind, query string, limit int) runtimeFileTraceDetails {
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "read" && kind != "write" && kind != "metadata" {
		kind = "all"
	}
	query = strings.ToLower(strings.TrimSpace(query))

	m := &s.runtimeFileTrace
	m.mu.Lock()
	supported, supportMessage := runtimeFileTracePlatformSupport()
	status := m.status
	message := m.message
	if status == "" {
		status = "stopped"
		message = supportMessage
	}
	summary := m.summaryLocked(supported, status, message)
	readRanking := m.rankingLocked("read")
	writeRanking := m.rankingLocked("write")
	events := make([]runtimeFileTraceEvent, 0, limit)
	for offset := 0; offset < len(m.events) && len(events) < limit; offset++ {
		index := len(m.events) - 1 - offset
		if len(m.events) == runtimeFileTraceEventLimit {
			index = (m.nextEvent - 1 - offset + len(m.events)) % len(m.events)
		}
		event := m.events[index]
		if kind != "all" && event.Kind != kind {
			continue
		}
		if query != "" {
			haystack := strings.ToLower(event.Path + "\n" + event.Process + "\n" + event.Operation + "\n" + event.Raw)
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		events = append(events, event)
	}
	m.mu.Unlock()

	return runtimeFileTraceDetails{
		Summary:      summary,
		Events:       events,
		ReadRanking:  readRanking,
		WriteRanking: writeRanking,
		Filter: gin.H{
			"kind":  kind,
			"query": query,
			"limit": limit,
		},
	}
}

func (m *runtimeFileTraceMonitor) aggregateEventLocked(event runtimeFileTraceEvent) {
	if event.Kind != "read" && event.Kind != "write" {
		return
	}
	path := strings.TrimSpace(event.Path)
	if path == "" {
		return
	}
	key := event.Kind + "\x00" + path
	process := event.Process
	if event.PID > 0 {
		process = runtimeFileTraceProcessName(event.Process)
	}
	if m.aggregates == nil {
		m.aggregates = make(map[string]*runtimeFileTraceAggregate)
	}
	aggregate := m.aggregates[key]
	if aggregate == nil {
		if len(m.aggregates) >= runtimeFileTraceAggregateLimit {
			m.evictSmallestAggregateLocked()
		}
		aggregate = &runtimeFileTraceAggregate{
			Kind: event.Kind, Path: path, Process: process, PID: event.PID, FirstAt: event.At,
		}
		m.aggregates[key] = aggregate
	} else if aggregate.PID > 0 && aggregate.PID == event.PID {
		aggregate.Process = process
	} else if aggregate.Process != process || aggregate.PID != event.PID {
		aggregate.Process = "多个进程"
		aggregate.PID = 0
	}
	aggregate.Operations++
	aggregate.Bytes += event.Bytes
	aggregate.LastAt = event.At
}

func (m *runtimeFileTraceMonitor) evictSmallestAggregateLocked() {
	var smallestKey string
	var smallest *runtimeFileTraceAggregate
	for key, aggregate := range m.aggregates {
		if smallest == nil || aggregate.Bytes < smallest.Bytes ||
			(aggregate.Bytes == smallest.Bytes && aggregate.Operations < smallest.Operations) {
			smallestKey = key
			smallest = aggregate
		}
	}
	if smallest != nil {
		delete(m.aggregates, smallestKey)
	}
}

func (m *runtimeFileTraceMonitor) rankingLocked(kind string) []runtimeFileTraceRanking {
	items := make([]runtimeFileTraceRanking, 0, len(m.aggregates))
	for _, aggregate := range m.aggregates {
		if aggregate == nil || aggregate.Kind != kind {
			continue
		}
		items = append(items, runtimeFileTraceRanking{
			Kind: aggregate.Kind, Path: aggregate.Path, Process: aggregate.Process, PID: aggregate.PID,
			Operations: aggregate.Operations, Bytes: aggregate.Bytes, FirstAt: aggregate.FirstAt, LastAt: aggregate.LastAt,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Bytes == items[j].Bytes {
			if items[i].Operations == items[j].Operations {
				return items[i].Path < items[j].Path
			}
			return items[i].Operations > items[j].Operations
		}
		return items[i].Bytes > items[j].Bytes
	})
	if len(items) > runtimeFileTraceRankingLimit {
		items = items[:runtimeFileTraceRankingLimit]
	}
	for index := range items {
		items[index].Rank = index + 1
	}
	return items
}

func (s *Service) handleRuntimeFileTraceDetails(c *gin.Context) {
	limit, err := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("limit", "200")))
	if err != nil {
		limit = 200
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, s.runtimeFileTraceDetails(c.Query("kind"), c.Query("query"), limit))
}

func (s *Service) handleRuntimeFileTraceStart(c *gin.Context) {
	request := runtimeFileTraceStartRequest{AutoStopWriteMB: runtimeFileTraceDefaultAutoStopWriteMB}
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "详细追踪配置格式有误：" + err.Error()})
			return
		}
	}
	summary, err := s.startRuntimeFileTrace(c.Request.Context(), request)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "summary": summary})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"summary": summary})
}

func (s *Service) handleRuntimeFileTraceStop(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"summary": s.stopRuntimeFileTrace()})
}

func (s *Service) handleRuntimeFileTraceClear(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"summary": s.clearRuntimeFileTrace()})
}

// Stable ordering makes the authorization prompt and UI target list easier to
// compare across repeated diagnostic sessions.
func sortedRuntimeFileTracePIDs(pids []int) []int {
	result := append([]int(nil), pids...)
	sort.Ints(result)
	return result
}
