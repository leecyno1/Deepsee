package http

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/model"
)

type databaseAuditIncrementalFixture struct {
	Database
	file       string
	tableCalls atomic.Int64
}

func (fixture *databaseAuditIncrementalFixture) GetDecryptedDBs() (map[string][]string, error) {
	return map[string][]string{"message": {fixture.file}}, nil
}

func (fixture *databaseAuditIncrementalFixture) GetTables(_, _ string) ([]string, error) {
	fixture.tableCalls.Add(1)
	return []string{}, nil
}

func (fixture *databaseAuditIncrementalFixture) ExecuteSQL(_, _, _ string) ([]map[string]interface{}, error) {
	return []map[string]interface{}{}, nil
}

func TestDatabaseMessageFormatSupported(t *testing.T) {
	tests := []struct {
		name     string
		msgType  int64
		subType  int64
		expected bool
	}{
		{name: "text", msgType: model.MessageTypeText, expected: true},
		{name: "voip", msgType: model.MessageTypeVOIP, expected: true},
		{name: "share quote", msgType: model.MessageTypeShare, subType: model.MessageSubTypeQuote, expected: true},
		{name: "share upload", msgType: model.MessageTypeShare, subType: model.MessageSubTypeFileUploading, expected: true},
		{name: "unknown share", msgType: model.MessageTypeShare, subType: 9999, expected: false},
		{name: "system notification", msgType: model.MessageTypeSystemNotification, subType: 17, expected: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if actual := databaseMessageFormatSupported(tt.msgType, tt.subType); actual != tt.expected {
				t.Fatalf("databaseMessageFormatSupported(%d, %d) = %v, want %v", tt.msgType, tt.subType, actual, tt.expected)
			}
		})
	}
}

func TestDatabaseWatermarkStatus(t *testing.T) {
	base := databaseAuditReport{
		Inventory: databaseAuditInventory{MessageShards: 2},
		Watermark: databaseWatermarkAudit{
			MessageLatestTimestamp: 1_000,
			SessionLatestTimestamp: 1_001,
		},
	}
	if actual := databaseWatermarkStatus(&base, true); actual != "near_current" {
		t.Fatalf("near current status = %q", actual)
	}
	base.Watermark.SessionLatestTimestamp = 2_000
	if actual := databaseWatermarkStatus(&base, true); actual != "stale" {
		t.Fatalf("stale status = %q", actual)
	}
	base.ShardErrors = []databaseAuditShardError{{Error: "fixture"}}
	if actual := databaseWatermarkStatus(&base, true); actual != "incomplete" {
		t.Fatalf("incomplete status = %q", actual)
	}
}

func TestFoldedOfficialSessionUsesMessageSortWatermark(t *testing.T) {
	if got := effectiveSessionWatermark("brandsessionholder", 2_000, 1_000); got != 1_000 {
		t.Fatalf("brandsessionholder watermark = %d", got)
	}
	if got := effectiveSessionWatermark("brandprivatemsg@hardcode", 2_000, 1_100); got != 1_100 {
		t.Fatalf("brand private watermark = %d", got)
	}
	if got := effectiveSessionWatermark("wxid_fixture", 2_000, 3_000); got != 2_000 {
		t.Fatalf("regular session watermark = %d", got)
	}
}

func TestDatabaseAuditShardMergeTracksNewestFTSExpectedText(t *testing.T) {
	report := &databaseAuditReport{Messages: databaseMessageAudit{FTSExpectedLatest: 100}}
	mergeDatabaseAuditShard(report, databaseAuditShardCache{
		Messages:   databaseMessageAudit{FTSExpectedLatest: 200},
		TypeCounts: map[databaseAuditTypeKey]int64{},
	}, map[databaseAuditTypeKey]int64{})
	if report.Messages.FTSExpectedLatest != 200 {
		t.Fatalf("expected FTS text watermark = %d", report.Messages.FTSExpectedLatest)
	}
}

func TestBuildMessageResourceQueryUsesParsedIntegersOnly(t *testing.T) {
	query := buildMessageResourceQuery(messageResourceFilters{
		MessageID:      11,
		MessageLocalID: 22,
		MessageSvrID:   33,
		ChatID:         44,
	})
	for _, expected := range []string{
		"i.message_id = 11",
		"i.message_local_id = 22",
		"i.message_svr_id = 33",
		"i.chat_id = 44",
		"LEFT JOIN MessageResourceDetail",
	} {
		if !strings.Contains(query, expected) {
			t.Fatalf("query missing %q:\n%s", expected, query)
		}
	}
}

func TestParseOptionalDatabaseInt64(t *testing.T) {
	if value, err := parseOptionalDatabaseInt64("9223372036854775807"); err != nil || value != int64(9223372036854775807) {
		t.Fatalf("large int64 = %d, %v", value, err)
	}
	for _, value := range []string{"-1", "1.5", "x", "9223372036854775808"} {
		if _, err := parseOptionalDatabaseInt64(value); err == nil {
			t.Fatalf("parseOptionalDatabaseInt64(%q) accepted invalid value", value)
		}
	}
}

func TestAuditCoverageKeepsSmallUnknownSetVisible(t *testing.T) {
	actual := roundPercent(115_818, 115_821)
	if actual != 99.997 {
		t.Fatalf("coverage = %.3f, want 99.997", actual)
	}
}

func TestStripAuditChatRoomSenderRequiresExactSender(t *testing.T) {
	if actual := stripAuditChatRoomSender("wxid_sender:\nbody", "wxid_sender"); actual != "body" {
		t.Fatalf("matching sender prefix = %q", actual)
	}
	content := "<sysmsg>label:\nbody</sysmsg>"
	if actual := stripAuditChatRoomSender(content, "wxid_sender"); actual != content {
		t.Fatalf("non-prefix content was truncated: %q", actual)
	}
}

func TestSplitMessageResourceTypeUsesSixteenBitEncoding(t *testing.T) {
	base, subType := splitMessageResourceType(262145)
	if base != 1 || subType != 4 {
		t.Fatalf("split resource type = %d/%d, want 1/4", base, subType)
	}
	base, subType = splitMessageResourceType(131074)
	if base != 2 || subType != 2 {
		t.Fatalf("split resource type = %d/%d, want 2/2", base, subType)
	}
}

func TestDecorateMessageResourceRowsPreservesIdentifierPrecision(t *testing.T) {
	rows := []map[string]interface{}{{
		"message_id":         int64(58_352),
		"message_local_id":   int64(1_424),
		"message_svr_id":     int64(7_759_022_800_757_975_011),
		"chat_id":            int64(16),
		"resource_id":        int64(185_890),
		"message_local_type": int64(3),
		"resource_type":      int64(262_145),
	}}
	decorateMessageResourceRows(rows)
	row := rows[0]
	if actual := row["message_svr_id"]; actual != "7759022800757975011" {
		t.Fatalf("message_svr_id = %#v", actual)
	}
	if row["resource_type_base"] != int64(1) || row["resource_sub_type"] != int64(4) {
		t.Fatalf("resource type = %#v/%#v", row["resource_type_base"], row["resource_sub_type"])
	}
}

func TestDatabaseAuditReusesUnchangedShardAndRescansWALChange(t *testing.T) {
	file := filepath.Join(t.TempDir(), "message_0.db")
	if err := os.WriteFile(file, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &databaseAuditIncrementalFixture{file: file}
	service := &Service{db: fixture}

	first, err := service.buildDatabaseAudit(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Scan.Mode != "full" || first.Scan.ScannedShards != 1 || fixture.tableCalls.Load() != 1 {
		t.Fatalf("first scan = %+v, table calls=%d", first.Scan, fixture.tableCalls.Load())
	}

	second, err := service.buildDatabaseAudit(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Scan.Mode != "reuse" || second.Scan.ReusedShards != 1 || fixture.tableCalls.Load() != 1 {
		t.Fatalf("unchanged scan = %+v, table calls=%d", second.Scan, fixture.tableCalls.Load())
	}

	if err := os.WriteFile(file+"-wal", []byte("new committed frames"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := service.buildDatabaseAudit(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if third.Scan.Mode != "incremental" || third.Scan.ScannedShards != 1 || fixture.tableCalls.Load() != 2 {
		t.Fatalf("WAL-changed scan = %+v, table calls=%d", third.Scan, fixture.tableCalls.Load())
	}

	forced, err := service.buildDatabaseAudit(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if forced.Scan.Mode != "full" || forced.Scan.ScannedShards != 1 || fixture.tableCalls.Load() != 3 {
		t.Fatalf("forced scan = %+v, table calls=%d", forced.Scan, fixture.tableCalls.Load())
	}
}

func TestDatabaseAuditConcurrentPressureDeduplicatesScans(t *testing.T) {
	file := filepath.Join(t.TempDir(), "message_0.db")
	if err := os.WriteFile(file, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &databaseAuditIncrementalFixture{file: file}
	service := &Service{db: fixture}

	runWave := func(wave int) {
		t.Helper()
		if wave > 0 {
			if err := os.WriteFile(file+"-wal", []byte(strings.Repeat("w", wave)), 0o600); err != nil {
				t.Fatal(err)
			}
			service.databaseAuditMu.Lock()
			service.databaseAudit = nil
			service.databaseAuditMu.Unlock()
		}
		const workers = 24
		var wait sync.WaitGroup
		wait.Add(workers)
		errors := make(chan error, workers)
		for index := 0; index < workers; index++ {
			go func() {
				defer wait.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				report, err := service.getDatabaseAudit(ctx, false)
				if err != nil {
					errors <- err
					return
				}
				if report == nil {
					errors <- stderrors.New("nil audit report")
				}
			}()
		}
		wait.Wait()
		close(errors)
		for err := range errors {
			t.Error(err)
		}
	}

	for wave := 0; wave < 8; wave++ {
		runWave(wave)
	}
	if calls := fixture.tableCalls.Load(); calls != 8 {
		t.Fatalf("table scans = %d, want one scan per fingerprint wave", calls)
	}
}

type auditSampleConfig struct{ root string }

func (config auditSampleConfig) GetHTTPAddr() string                      { return "127.0.0.1:0" }
func (config auditSampleConfig) GetDataDir() string                       { return "" }
func (config auditSampleConfig) GetRuntimeDir() string                    { return config.root }
func (config auditSampleConfig) GetSaveDecryptedMedia() bool              { return false }
func (config auditSampleConfig) GetDataKey() string                       { return "" }
func (config auditSampleConfig) GetWorkDir() string                       { return "" }
func (config auditSampleConfig) GetVersion() int                          { return 4 }
func (config auditSampleConfig) GetMessageHook() *conf.MessageHook        { return &conf.MessageHook{} }
func (config auditSampleConfig) UpdateMessageHook(conf.MessageHook) error { return nil }

func TestUnknownMessageSamplesPersistStructureOnly(t *testing.T) {
	root := t.TempDir()
	service := &Service{conf: auditSampleConfig{root: root}}
	report := &databaseAuditReport{
		GeneratedAt: time.Now().Format(time.RFC3339Nano),
		Messages: databaseMessageAudit{UnknownSamples: []databaseAuditUnknownSample{{
			File: "message_0.db", Table: "Msg_fixture", LocalID: 7,
			Type: 11000, SubType: 17, ContentHash: "abcdef",
			ContentKind: "text", ContentBytes: 18, DecodedBytes: 18,
			NonEmpty: true, Reason: "unsupported_type",
		}}},
	}
	service.persistUnknownMessageSamples(report)
	if report.Messages.UnknownSampleFile == "" {
		t.Fatal("unknown sample file was not recorded")
	}
	raw, err := os.ReadFile(report.Messages.UnknownSampleFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "raw secret payload") {
		t.Fatal("persisted structural sample contains raw content")
	}
	if !strings.Contains(string(raw), `"content_kind": "text"`) {
		t.Fatalf("persisted sample missing structural metadata: %s", raw)
	}
}

func TestDatabaseAuditAlertsExposeThresholdsAndOpaqueResearchState(t *testing.T) {
	report := &databaseAuditReport{
		Messages: databaseMessageAudit{
			TotalRows:              100,
			SupportedRows:          98,
			UnsupportedRows:        2,
			CoveragePercent:        98,
			ZstdErrors:             1,
			OpaqueControlRows:      3,
			OpaqueControlEmptyRows: 2,
		},
		Watermark: databaseWatermarkAudit{Status: "stale", GapSeconds: 900},
		Resources: databaseResourceAudit{Available: true, LinkCoverage: 94},
		FTS: databaseFTSAudit{
			MessageWatermarkStatus:     "stale",
			MessageWatermarkGapSeconds: 600,
		},
	}
	finalizeDatabaseAuditStatuses(report)
	if report.Messages.OpaqueControlStatus != "samples_available" {
		t.Fatalf("opaque control status = %q", report.Messages.OpaqueControlStatus)
	}
	codes := make(map[string]bool, len(report.Alerts))
	for _, alert := range report.Alerts {
		codes[alert.Code] = true
	}
	for _, code := range []string{
		"watermark_stale",
		"zstd_decode_error",
		"unsupported_message_format",
		"resource_link_coverage",
		"fts_message_watermark_stale",
		"opaque_control_sample",
	} {
		if !codes[code] {
			t.Errorf("missing audit alert %q: %+v", code, report.Alerts)
		}
	}
}

func TestDatabaseAuditRegressionAlertsDetectNewFailures(t *testing.T) {
	previous := &databaseAuditReport{Messages: databaseMessageAudit{
		UnsupportedRows: 1,
		CoveragePercent: 99.999,
	}}
	current := &databaseAuditReport{Messages: databaseMessageAudit{
		UnsupportedRows: 3,
		CoveragePercent: 99.997,
	}}
	appendDatabaseAuditRegressionAlerts(current, previous)
	codes := make(map[string]bool, len(current.Alerts))
	for _, alert := range current.Alerts {
		codes[alert.Code] = true
	}
	if !codes["unsupported_rows_increased"] || !codes["coverage_decreased"] {
		t.Fatalf("regression alerts = %+v", current.Alerts)
	}
}

func TestClassifyFTSProbeError(t *testing.T) {
	tests := []struct {
		message string
		want    string
	}{
		{message: "no such module: fts5", want: "fts5_module_unavailable"},
		{message: "no such tokenizer: MMFtsTokenizer", want: "private_tokenizer_unavailable"},
		{message: "database disk image is malformed", want: "probe_error"},
	}
	for _, tt := range tests {
		if actual := classifyFTSProbeError(stderrors.New(tt.message)); actual != tt.want {
			t.Errorf("classifyFTSProbeError(%q) = %q, want %q", tt.message, actual, tt.want)
		}
	}
}
