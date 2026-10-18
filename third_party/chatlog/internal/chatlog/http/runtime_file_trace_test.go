package http

import (
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type runtimeFileTraceTestSession struct {
	stopped atomic.Bool
}

func (s *runtimeFileTraceTestSession) Read(_ []byte) (int, error) { return 0, io.EOF }
func (s *runtimeFileTraceTestSession) Close() error {
	s.Stop()
	return nil
}
func (s *runtimeFileTraceTestSession) Stop() { s.stopped.Store(true) }

func TestParseRuntimeFileTraceLineExtractsWriteDetails(t *testing.T) {
	now := time.Date(2026, 8, 2, 17, 30, 0, 0, time.Local)
	event, ok := parseRuntimeFileTraceLine(
		"17:20:31.123456  WrData[A]  F=12 B=0x1000 /tmp/chatlog/Message_0.db-wal  0.001250 W chatlog.44528",
		now,
	)
	if !ok {
		t.Fatal("fs_usage line was not parsed")
	}
	if event.Kind != "write" || event.Operation != "WrData[A]" {
		t.Fatalf("unexpected operation: %+v", event)
	}
	if event.Bytes != 4096 || event.Path != "/tmp/chatlog/Message_0.db-wal" {
		t.Fatalf("unexpected byte/path parsing: %+v", event)
	}
	if event.Process != "chatlog.44528" || event.ThreadID != 44528 || event.PID != 0 {
		t.Fatalf("unexpected process parsing: %+v", event)
	}
	if !event.hasFD || event.FD != 12 || event.PathSource != "fs_usage" {
		t.Fatalf("unexpected fd/path source parsing: %+v", event)
	}
	if event.DurationMS != 1.25 {
		t.Fatalf("duration = %fms, want 1.25ms", event.DurationMS)
	}
}

func TestResolveRuntimeFileTraceEventMapsThreadNameAndFD(t *testing.T) {
	targets := runtimeFileTraceTargetPIDs([]runtimeFileTraceTarget{
		{PID: 101, Name: "chatlog"},
		{PID: 202, Name: "python"},
		{PID: 303, Name: "Python"},
	})
	files := map[int]map[int]runtimeFileTraceOpenFile{
		101: {3: {Path: "/tmp/chatlog.log", Type: "REG"}, 87: {Path: "/dev/tty", Type: "CHR"}},
		202: {8: {Path: "/tmp/sdk.log", Type: "REG"}},
		303: {8: {Path: "/tmp/sdk.log", Type: "REG"}},
	}
	logEvent := resolveRuntimeFileTraceEvent(runtimeFileTraceEvent{Process: "chatlog.2730938", FD: 3, hasFD: true}, targets, files)
	if logEvent.PID != 101 || logEvent.Path != "/tmp/chatlog.log" || logEvent.PathSource != "fd_snapshot" {
		t.Fatalf("log event = %+v", logEvent)
	}
	ttyEvent := resolveRuntimeFileTraceEvent(runtimeFileTraceEvent{Process: "chatlog.2730938", FD: 87, hasFD: true}, targets, files)
	if runtimeFileTraceEventIsFile(ttyEvent) {
		t.Fatalf("tty event was accepted: %+v", ttyEvent)
	}
	ambiguous := resolveRuntimeFileTraceEvent(runtimeFileTraceEvent{Process: "Python.42", FD: 8, hasFD: true}, targets, files)
	if ambiguous.PID != 0 || ambiguous.Path != "/tmp/sdk.log" {
		t.Fatalf("same-path multi-process event = %+v", ambiguous)
	}
}

func TestParseRuntimeFileTraceOpenFiles(t *testing.T) {
	files := parseRuntimeFileTraceOpenFiles("p101\nf3u\ntREG\nn/tmp/chatlog.log\nf4\ntKQUEUE\nncount=0\np202\nf0r\ntCHR\nn/dev/ttys001\n")
	if got := files[101][3]; got.Path != "/tmp/chatlog.log" || got.Type != "REG" {
		t.Fatalf("regular file = %+v", got)
	}
	if got := files[101][4]; got.Type != "KQUEUE" {
		t.Fatalf("kqueue fd = %+v", got)
	}
	if got := files[202][0]; got.Path != "/dev/ttys001" || got.Type != "CHR" {
		t.Fatalf("terminal file = %+v", got)
	}
}

func TestRuntimeFileTraceFiltersKernelAndNonFileNoise(t *testing.T) {
	for _, event := range []runtimeFileTraceEvent{
		{Operation: "kevent", Kind: "metadata"},
		{Operation: "WrData", Kind: "write", Path: "/dev/ttys001", fileType: "CHR"},
		{Operation: "WrData", Kind: "write", Path: "->0x123", fileType: "PIPE"},
		{Operation: "RdData", Kind: "read"},
	} {
		if runtimeFileTraceEventIsFile(event) {
			t.Fatalf("noise event was accepted: %+v", event)
		}
	}
	for _, event := range []runtimeFileTraceEvent{
		{Operation: "WrData", Kind: "write", Path: "/tmp/chatlog.log", fileType: "REG"},
		{Operation: "getattrlist", Kind: "metadata", Path: "/tmp/chatlog.db"},
	} {
		if !runtimeFileTraceEventIsFile(event) {
			t.Fatalf("file event was filtered: %+v", event)
		}
	}
}

func TestParseRuntimeFileTraceLineClassifiesReadsAndMetadata(t *testing.T) {
	now := time.Now()
	readEvent, ok := parseRuntimeFileTraceLine(
		"09:10:11.000001  RdData[A]  F=8 B=8192 /tmp/chatlog.db  0.000100 W chatlog.123",
		now,
	)
	if !ok || readEvent.Kind != "read" || readEvent.Bytes != 8192 {
		t.Fatalf("read event = %+v, ok=%v", readEvent, ok)
	}
	metadataEvent, ok := parseRuntimeFileTraceLine(
		"09:10:12.000001  getattrlist  /tmp/chatlog.db  0.000050 W chatlog.123",
		now,
	)
	if !ok || metadataEvent.Kind != "metadata" {
		t.Fatalf("metadata event = %+v, ok=%v", metadataEvent, ok)
	}
}

func TestRuntimeFileTraceRetainsNewestBoundedEvents(t *testing.T) {
	service := &Service{}
	service.runtimeFileTrace.generation = 7
	service.runtimeFileTrace.status = "running"
	for index := 0; index < runtimeFileTraceEventLimit+25; index++ {
		service.appendRuntimeFileTraceEvent(7, runtimeFileTraceEvent{
			At:        time.Now().Format(time.RFC3339Nano),
			Kind:      "write",
			Operation: "WrData",
			Path:      fmt.Sprintf("/tmp/file-%04d", index),
			Bytes:     1,
		})
	}
	details := service.runtimeFileTraceDetails("write", "", runtimeFileTraceEventLimit)
	if details.Summary.RetainedEvents != runtimeFileTraceEventLimit || details.Summary.DroppedEvents != 25 {
		t.Fatalf("summary = %+v", details.Summary)
	}
	if len(details.Events) != 500 {
		t.Fatalf("events = %d, want API cap 500", len(details.Events))
	}
	if details.Events[0].Path != "/tmp/file-1024" {
		t.Fatalf("newest event = %+v", details.Events[0])
	}
	if details.Summary.WriteBytes != runtimeFileTraceEventLimit+25 {
		t.Fatalf("write bytes = %d", details.Summary.WriteBytes)
	}
}

func TestRuntimeFileTraceFiltersKindAndQuery(t *testing.T) {
	service := &Service{}
	service.runtimeFileTrace.generation = 3
	service.runtimeFileTrace.status = "running"
	for _, event := range []runtimeFileTraceEvent{
		{At: time.Now().Format(time.RFC3339Nano), Kind: "read", Operation: "RdData", Path: "/tmp/Message.db"},
		{At: time.Now().Format(time.RFC3339Nano), Kind: "write", Operation: "WrData", Path: "/tmp/Message.db-wal"},
		{At: time.Now().Format(time.RFC3339Nano), Kind: "write", Operation: "WrData", Path: "/tmp/ocr.db-wal"},
	} {
		service.appendRuntimeFileTraceEvent(3, event)
	}
	details := service.runtimeFileTraceDetails("write", "message", 20)
	if len(details.Events) != 1 || details.Events[0].Path != "/tmp/Message.db-wal" {
		t.Fatalf("filtered events = %+v", details.Events)
	}
}

func TestRuntimeFileTraceRanksTopFilesAcrossWholeSession(t *testing.T) {
	service := &Service{}
	service.runtimeFileTrace.generation = 5
	service.runtimeFileTrace.status = "running"
	now := time.Now().Format(time.RFC3339Nano)
	for _, event := range []runtimeFileTraceEvent{
		{At: now, Kind: "write", Operation: "WrData", Path: "/tmp/a.db", Process: "chatlog.10", PID: 10, Bytes: 100},
		{At: now, Kind: "write", Operation: "WrData", Path: "/tmp/b.db", Process: "chatlog.10", PID: 10, Bytes: 400},
		{At: now, Kind: "write", Operation: "WrData", Path: "/tmp/a.db", Process: "WeChat.11", PID: 11, Bytes: 500},
		{At: now, Kind: "read", Operation: "RdData", Path: "/tmp/c.db", Process: "chatlog.10", PID: 10, Bytes: 700},
	} {
		service.appendRuntimeFileTraceEvent(5, event)
	}
	details := service.runtimeFileTraceDetails("all", "", 20)
	if len(details.WriteRanking) != 2 || details.WriteRanking[0].Path != "/tmp/a.db" || details.WriteRanking[0].Bytes != 600 || details.WriteRanking[0].Operations != 2 {
		t.Fatalf("write ranking=%+v", details.WriteRanking)
	}
	if details.WriteRanking[0].Process != "多个进程" || details.WriteRanking[0].PID != 0 {
		t.Fatalf("same-file multi-process aggregation=%+v", details.WriteRanking[0])
	}
	if len(details.ReadRanking) != 1 || details.ReadRanking[0].Path != "/tmp/c.db" || details.ReadRanking[0].Bytes != 700 {
		t.Fatalf("read ranking=%+v", details.ReadRanking)
	}
}

func TestRuntimeFileTraceAutoStopsAtSessionWriteThreshold(t *testing.T) {
	service := &Service{}
	session := &runtimeFileTraceTestSession{}
	service.runtimeFileTrace.generation = 9
	service.runtimeFileTrace.status = "running"
	service.runtimeFileTrace.session = session
	service.runtimeFileTrace.writeBytes = 1_000
	service.runtimeFileTrace.sessionWriteBase = 1_000
	service.runtimeFileTrace.autoStopEnabled = true
	service.runtimeFileTrace.autoStopWriteMB = 0.1

	service.appendRuntimeFileTraceEvent(9, runtimeFileTraceEvent{
		At: time.Now().Format(time.RFC3339Nano), Kind: "write", Operation: "WrData", Bytes: 64 * 1024,
	})
	if service.runtimeFileTrace.status != "running" || session.stopped.Load() {
		t.Fatalf("trace stopped before threshold: %+v", service.runtimeFileTraceSummary())
	}
	service.appendRuntimeFileTraceEvent(9, runtimeFileTraceEvent{
		At: time.Now().Format(time.RFC3339Nano), Kind: "write", Operation: "WrData", Bytes: 48 * 1024,
	})
	summary := service.runtimeFileTraceSummary()
	if summary.Status != "paused" || !summary.AutoStopTriggered || !session.stopped.Load() {
		t.Fatalf("auto-stop state = %+v, stopped=%v", summary, session.stopped.Load())
	}
	if summary.SessionWriteBytes != 112*1024 || summary.AutoStoppedBytes != 112*1024 {
		t.Fatalf("trigger bytes = session %d, stopped %d", summary.SessionWriteBytes, summary.AutoStoppedBytes)
	}
	if summary.RetainedEvents != 2 || summary.WriteOperations != 2 {
		t.Fatalf("triggering event was not retained: %+v", summary)
	}
}

func TestRuntimeFileTraceAutoStopRequestValidation(t *testing.T) {
	request, err := normalizeRuntimeFileTraceStartRequest(runtimeFileTraceStartRequest{})
	if err != nil || request.AutoStopWriteMB != runtimeFileTraceDefaultAutoStopWriteMB {
		t.Fatalf("default request = %+v, %v", request, err)
	}
	for _, value := range []float64{0.09, runtimeFileTraceMaxAutoStopWriteMB + 1} {
		if _, err := normalizeRuntimeFileTraceStartRequest(runtimeFileTraceStartRequest{AutoStopEnabled: true, AutoStopWriteMB: value}); err == nil {
			t.Fatalf("threshold %f passed validation", value)
		}
	}
}

func TestRuntimeFileTraceClearResetsRunningSessionThreshold(t *testing.T) {
	service := &Service{}
	service.runtimeFileTrace.generation = 4
	service.runtimeFileTrace.status = "running"
	service.runtimeFileTrace.autoStopEnabled = true
	service.runtimeFileTrace.autoStopWriteMB = 1
	service.runtimeFileTrace.writeBytes = 900 * 1024
	service.runtimeFileTrace.sessionWriteBase = 800 * 1024

	service.clearRuntimeFileTrace()
	service.appendRuntimeFileTraceEvent(4, runtimeFileTraceEvent{
		At: time.Now().Format(time.RFC3339Nano), Kind: "write", Operation: "WrData", Bytes: 200 * 1024,
	})
	summary := service.runtimeFileTraceSummary()
	if summary.Status != "running" || summary.AutoStopTriggered || summary.SessionWriteBytes != 200*1024 {
		t.Fatalf("clear did not restart the running-session threshold: %+v", summary)
	}
}
