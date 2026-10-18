package http

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/pkg/util"
	"github.com/sjzar/chatlog/pkg/util/zstd"
)

const (
	databaseAuditTTL       = 5 * time.Minute
	databaseAuditMaxSample = 20
)

var messageShardNamePattern = regexp.MustCompile(`^(?:biz_)?message_\d+\.db$`)

type databaseAuditReport struct {
	GeneratedAt string                    `json:"generated_at"`
	DurationMS  float64                   `json:"duration_ms"`
	Cached      bool                      `json:"cached"`
	Scan        databaseAuditScan         `json:"scan"`
	Inventory   databaseAuditInventory    `json:"inventory"`
	Messages    databaseMessageAudit      `json:"messages"`
	Watermark   databaseWatermarkAudit    `json:"watermark"`
	Resources   databaseResourceAudit     `json:"resources"`
	FTS         databaseFTSAudit          `json:"fts"`
	Alerts      []databaseAuditAlert      `json:"alerts,omitempty"`
	Warnings    []string                  `json:"warnings"`
	ShardErrors []databaseAuditShardError `json:"shard_errors,omitempty"`
}

type databaseAuditInventory struct {
	Groups        int `json:"groups"`
	Files         int `json:"files"`
	MessageShards int `json:"message_shards"`
	MessageTables int `json:"message_tables"`
}

type databaseMessageAudit struct {
	TotalRows              int64                        `json:"total_rows"`
	ParsedRows             int64                        `json:"parsed_rows"`
	SupportedRows          int64                        `json:"supported_rows"`
	UnsupportedRows        int64                        `json:"unsupported_rows"`
	ParseErrors            int64                        `json:"parse_errors"`
	ZstdRows               int64                        `json:"zstd_rows"`
	ZstdErrors             int64                        `json:"zstd_errors"`
	PackedRows             int64                        `json:"packed_rows"`
	PackedErrors           int64                        `json:"packed_errors"`
	RawPreservedRows       int64                        `json:"raw_preserved_rows"`
	OpaqueControlRows      int64                        `json:"opaque_control_rows"`
	OpaqueControlEmptyRows int64                        `json:"opaque_control_empty_rows"`
	OpaqueControlStatus    string                       `json:"opaque_control_status"`
	FTSExpectedLatest      int64                        `json:"fts_expected_latest_timestamp"`
	CoveragePercent        float64                      `json:"coverage_percent"`
	ByType                 []databaseAuditMessageType   `json:"by_type"`
	UnknownSamples         []databaseAuditUnknownSample `json:"unknown_samples,omitempty"`
	UnknownSampleFile      string                       `json:"unknown_sample_file,omitempty"`
}

type databaseAuditMessageType struct {
	Type      int64 `json:"type"`
	SubType   int64 `json:"sub_type"`
	Rows      int64 `json:"rows"`
	Supported bool  `json:"supported"`
}

type databaseAuditUnknownSample struct {
	File         string `json:"file"`
	Table        string `json:"table"`
	LocalID      int64  `json:"local_id"`
	ServerID     string `json:"server_id,omitempty"`
	Type         int64  `json:"type"`
	SubType      int64  `json:"sub_type"`
	CreateTime   int64  `json:"create_time"`
	ContentHash  string `json:"content_hash"`
	ContentKind  string `json:"content_kind"`
	ContentBytes int    `json:"content_bytes"`
	DecodedBytes int    `json:"decoded_bytes"`
	NonEmpty     bool   `json:"non_empty"`
	Reason       string `json:"reason"`
}

type databaseWatermarkAudit struct {
	Status                 string                        `json:"status"`
	MessageLatestTimestamp int64                         `json:"message_latest_timestamp"`
	SessionLatestTimestamp int64                         `json:"session_latest_timestamp"`
	GapSeconds             int64                         `json:"gap_seconds"`
	LatestSession          string                        `json:"latest_session,omitempty"`
	Shards                 []databaseAuditShardWatermark `json:"shards"`
}

type databaseAuditShardWatermark struct {
	File            string `json:"file"`
	Tables          int    `json:"tables"`
	Rows            int64  `json:"rows"`
	LatestTimestamp int64  `json:"latest_timestamp"`
}

type databaseResourceAudit struct {
	Available          bool                          `json:"available"`
	File               string                        `json:"file,omitempty"`
	InfoRows           int64                         `json:"info_rows"`
	DetailRows         int64                         `json:"detail_rows"`
	DistinctMessages   int64                         `json:"distinct_messages"`
	BytesTotal         int64                         `json:"bytes_total"`
	LatestTimestamp    int64                         `json:"latest_timestamp"`
	LinkedInfoRows     int64                         `json:"linked_info_rows"`
	LinkCoverage       float64                       `json:"link_coverage_percent"`
	StatusDistribution []databaseAuditResourceBucket `json:"status_distribution,omitempty"`
	TypeDistribution   []databaseAuditResourceBucket `json:"type_distribution,omitempty"`
}

type databaseAuditResourceBucket struct {
	Type     int64 `json:"type,omitempty"`
	TypeBase int64 `json:"type_base,omitempty"`
	SubType  int64 `json:"sub_type,omitempty"`
	Status   int64 `json:"status,omitempty"`
	Rows     int64 `json:"rows"`
	Bytes    int64 `json:"bytes"`
}

type databaseFTSAudit struct {
	Files                      int    `json:"files"`
	VirtualTables              int    `json:"virtual_tables"`
	MMTokenizerTables          int    `json:"mm_tokenizer_tables"`
	ShadowContentTables        int    `json:"shadow_content_tables"`
	MessageContentRows         int64  `json:"message_content_rows"`
	MessageLatestTimestamp     int64  `json:"message_latest_timestamp"`
	ExpectedLatestTimestamp    int64  `json:"expected_latest_timestamp"`
	MessageWatermarkGapSeconds int64  `json:"message_watermark_gap_seconds"`
	MessageWatermarkStatus     string `json:"message_watermark_status"`
	ModuleAvailable            bool   `json:"module_available"`
	ModuleStatus               string `json:"module_status"`
	NativeQuery                bool   `json:"native_query"`
	ProbeStatus                string `json:"probe_status"`
	ProbeError                 string `json:"probe_error,omitempty"`
	FallbackPath               string `json:"fallback_path"`
}

type databaseAuditShardError struct {
	Group string `json:"group"`
	File  string `json:"file"`
	Table string `json:"table,omitempty"`
	Error string `json:"error"`
}

type databaseAuditTypeKey struct {
	Type    int64
	SubType int64
}

func (s *Service) handleDatabaseAudit(c *gin.Context) {
	refresh, err := queryBoolean(c.Query("refresh"))
	if err != nil {
		errors.Err(c, errors.InvalidArg("refresh"))
		return
	}
	report, err := s.getDatabaseAudit(c.Request.Context(), refresh)
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}

func queryBoolean(raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, nil
	}
	return strconv.ParseBool(raw)
}

func (s *Service) getDatabaseAudit(ctx context.Context, refresh bool) (*databaseAuditReport, error) {
	if !refresh {
		if report := s.cachedDatabaseAudit(false); report != nil {
			return report, nil
		}
	}

	s.databaseAuditRunMu.Lock()
	defer s.databaseAuditRunMu.Unlock()
	if !refresh {
		if report := s.cachedDatabaseAudit(false); report != nil {
			return report, nil
		}
	}

	previous := s.cachedDatabaseAudit(true)
	report, err := s.buildDatabaseAudit(ctx, refresh)
	if err != nil {
		return nil, err
	}
	appendDatabaseAuditRegressionAlerts(report, previous)
	s.databaseAuditMu.Lock()
	s.databaseAudit = report
	s.databaseAuditMu.Unlock()
	return cloneDatabaseAudit(report, false), nil
}

func (s *Service) cachedDatabaseAudit(allowExpired bool) *databaseAuditReport {
	s.databaseAuditMu.RLock()
	report := s.databaseAudit
	s.databaseAuditMu.RUnlock()
	if report == nil {
		return nil
	}
	generatedAt, err := time.Parse(time.RFC3339Nano, report.GeneratedAt)
	if !allowExpired && (err != nil || time.Since(generatedAt) > databaseAuditTTL) {
		return nil
	}
	return cloneDatabaseAudit(report, true)
}

func cloneDatabaseAudit(report *databaseAuditReport, cached bool) *databaseAuditReport {
	if report == nil {
		return nil
	}
	out := *report
	out.Cached = cached
	out.Scan.ScannedFiles = append([]string(nil), report.Scan.ScannedFiles...)
	out.Scan.ChangedFiles = append([]string(nil), report.Scan.ChangedFiles...)
	out.Warnings = append([]string(nil), report.Warnings...)
	out.Alerts = append([]databaseAuditAlert(nil), report.Alerts...)
	out.ShardErrors = append([]databaseAuditShardError(nil), report.ShardErrors...)
	out.Messages.ByType = append([]databaseAuditMessageType(nil), report.Messages.ByType...)
	out.Messages.UnknownSamples = append([]databaseAuditUnknownSample(nil), report.Messages.UnknownSamples...)
	out.Watermark.Shards = append([]databaseAuditShardWatermark(nil), report.Watermark.Shards...)
	out.Resources.StatusDistribution = append([]databaseAuditResourceBucket(nil), report.Resources.StatusDistribution...)
	out.Resources.TypeDistribution = append([]databaseAuditResourceBucket(nil), report.Resources.TypeDistribution...)
	return &out
}

func (s *Service) cachedDatabaseAuditSummary() gin.H {
	report := s.cachedDatabaseAudit(true)
	if report == nil {
		return gin.H{"available": false}
	}
	return gin.H{
		"available":              true,
		"generated_at":           report.GeneratedAt,
		"age_seconds":            databaseAuditAgeSeconds(report.GeneratedAt),
		"total_rows":             report.Messages.TotalRows,
		"supported_rows":         report.Messages.SupportedRows,
		"parse_errors":           report.Messages.ParseErrors,
		"unsupported_rows":       report.Messages.UnsupportedRows,
		"coverage_percent":       report.Messages.CoveragePercent,
		"watermark_status":       report.Watermark.Status,
		"watermark_gap_seconds":  report.Watermark.GapSeconds,
		"resource_link_coverage": report.Resources.LinkCoverage,
		"resource_detail_rows":   report.Resources.DetailRows,
		"warnings":               append([]string(nil), report.Warnings...),
		"alerts":                 append([]databaseAuditAlert(nil), report.Alerts...),
		"zstd_errors":            report.Messages.ZstdErrors,
		"packed_errors":          report.Messages.PackedErrors,
		"opaque_control_status":  report.Messages.OpaqueControlStatus,
		"scan":                   report.Scan,
	}
}

func (s *Service) databaseAuditCacheEntries() int64 {
	s.databaseAuditMu.RLock()
	defer s.databaseAuditMu.RUnlock()
	if s.databaseAudit == nil {
		return 0
	}
	return 1
}

func databaseAuditAgeSeconds(generatedAt string) int64 {
	value, err := time.Parse(time.RFC3339Nano, generatedAt)
	if err != nil {
		return 0
	}
	age := time.Since(value)
	if age < 0 {
		return 0
	}
	return int64(age.Seconds())
}

func (s *Service) buildDatabaseAudit(ctx context.Context, forceFull bool) (*databaseAuditReport, error) {
	startedAt := time.Now()
	report := &databaseAuditReport{
		GeneratedAt: startedAt.Format(time.RFC3339Nano),
		Messages: databaseMessageAudit{
			ByType:         make([]databaseAuditMessageType, 0),
			UnknownSamples: make([]databaseAuditUnknownSample, 0),
		},
		Watermark: databaseWatermarkAudit{
			Status: "missing",
			Shards: make([]databaseAuditShardWatermark, 0),
		},
		Warnings:    make([]string, 0),
		ShardErrors: make([]databaseAuditShardError, 0),
	}

	dbs, err := s.db.GetDecryptedDBs()
	if err != nil {
		return nil, err
	}
	report.Inventory.Groups = len(dbs)
	for _, files := range dbs {
		report.Inventory.Files += len(files)
	}

	messageFiles := make([]string, 0)
	for _, file := range dbs["message"] {
		if messageShardNamePattern.MatchString(strings.ToLower(filepath.Base(file))) {
			messageFiles = append(messageFiles, file)
		}
	}
	sort.Strings(messageFiles)
	report.Inventory.MessageShards = len(messageFiles)

	previousShards := s.databaseAuditShardCacheSnapshot()
	nextShards := make(map[string]databaseAuditShardCache, len(messageFiles))
	if forceFull || len(previousShards) == 0 {
		report.Scan.Mode = "full"
	} else {
		report.Scan.Mode = "incremental"
	}
	typeCounts := make(map[databaseAuditTypeKey]int64)
	for _, file := range messageFiles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, reused := s.scanOrReuseDatabaseAuditShard(ctx, file, forceFull, previousShards)
		mergeDatabaseAuditShard(report, result, typeCounts)
		if reused {
			report.Scan.ReusedShards++
		} else {
			report.Scan.ScannedShards++
			report.Scan.ScannedFiles = append(report.Scan.ScannedFiles, filepath.Base(file))
			if previous, ok := previousShards[file]; !ok || previous.Fingerprint != result.Fingerprint {
				report.Scan.ChangedFiles = append(report.Scan.ChangedFiles, filepath.Base(file))
			}
		}
		if len(result.Errors) == 0 {
			nextShards[file] = cloneDatabaseAuditShardCache(result)
		}
	}
	if report.Scan.ScannedShards == 0 && report.Scan.ReusedShards > 0 {
		report.Scan.Mode = "reuse"
	}
	s.replaceDatabaseAuditShardCache(nextShards)
	report.Messages.ByType = sortedDatabaseAuditTypes(typeCounts)
	if report.Messages.TotalRows > 0 {
		report.Messages.CoveragePercent = roundPercent(report.Messages.SupportedRows, report.Messages.TotalRows)
	}

	s.auditSessionWatermark(report, dbs)
	s.auditMessageResources(report, dbs)
	s.auditFTS(report, dbs)
	finalizeDatabaseAuditWarnings(report)
	finalizeDatabaseAuditStatuses(report)
	s.persistUnknownMessageSamples(report)
	report.DurationMS = roundedMilliseconds(time.Since(startedAt))
	return report, nil
}

func (s *Service) auditMessageShard(
	ctx context.Context,
	report *databaseAuditReport,
	file string,
	typeCounts map[databaseAuditTypeKey]int64,
) {
	shard := databaseAuditShardWatermark{File: filepath.Base(file)}
	tables, err := s.db.GetTables("message", file)
	if err != nil {
		report.ShardErrors = append(report.ShardErrors, databaseAuditShardError{
			Group: "message", File: filepath.Base(file), Error: err.Error(),
		})
		report.Watermark.Shards = append(report.Watermark.Shards, shard)
		return
	}

	talkerByHash := s.auditTalkerHashIndex(file)
	for _, table := range tables {
		if !strings.HasPrefix(table, "Msg_") {
			continue
		}
		shard.Tables++
		report.Inventory.MessageTables++
		talker := talkerByHash[strings.TrimPrefix(table, "Msg_")]
		query := fmt.Sprintf(`
SELECT m.local_id, m.server_id, m.local_type, m.create_time,
       COALESCE(n.user_name, '') AS user_name,
       COALESCE(m.message_content, X'') AS message_content,
       COALESCE(m.packed_info_data, X'') AS packed_info_data
FROM %s m
LEFT JOIN Name2Id n ON m.real_sender_id = n.rowid
`, quoteSQLiteIdentifier(table))
		_, streamErr := s.db.StreamSQL(ctx, "message", file, query, func(columns []string, values []interface{}) error {
			row := databaseRowMap(columns, values)
			localID := toInt64(row["local_id"])
			serverID := toInt64(row["server_id"])
			localType := toInt64(row["local_type"])
			createTime := toInt64(row["create_time"])
			sender := toString(row["user_name"])
			rawContent := toDatabaseBytes(row["message_content"])
			packed := toDatabaseBytes(row["packed_info_data"])

			shard.Rows++
			report.Messages.TotalRows++
			if createTime > shard.LatestTimestamp {
				shard.LatestTimestamp = createTime
			}

			content, zstdRow, decodeErr := decodeAuditMessageContent(rawContent)
			if zstdRow {
				report.Messages.ZstdRows++
			}
			if decodeErr != nil {
				report.Messages.ZstdErrors++
				report.Messages.ParseErrors++
			}
			if strings.HasSuffix(talker, "@chatroom") {
				content = stripAuditChatRoomSender(content, sender)
			}

			parsed := &model.Message{Type: localType, Contents: make(map[string]interface{})}
			parseErr := error(nil)
			if decodeErr == nil {
				parseErr = parsed.ParseMediaInfo(content)
				if parseErr != nil {
					report.Messages.ParseErrors++
					report.Messages.RawPreservedRows++
				} else {
					report.Messages.ParsedRows++
				}
			}
			baseType, subType := util.SplitInt64ToTwoInt32(localType)
			if parseErr == nil && decodeErr == nil {
				baseType, subType = parsed.Type, parsed.SubType
			}
			// Media-only rows do not receive WeChat FTS content entries. Use the
			// newest non-empty text message as the expected search-index watermark.
			if baseType == model.MessageTypeText && strings.TrimSpace(content) != "" &&
				createTime > report.Messages.FTSExpectedLatest {
				report.Messages.FTSExpectedLatest = createTime
			}
			supported := databaseMessageFormatSupported(baseType, subType)
			typeCounts[databaseAuditTypeKey{Type: baseType, SubType: subType}]++
			if supported && parseErr == nil && decodeErr == nil {
				report.Messages.SupportedRows++
			} else {
				report.Messages.UnsupportedRows++
				reason := "unsupported_type"
				if decodeErr != nil {
					reason = "zstd_decode"
				} else if parseErr != nil {
					reason = "payload_parse"
				}
				appendDatabaseAuditSample(report, databaseAuditUnknownSample{
					File:         filepath.Base(file),
					Table:        table,
					LocalID:      localID,
					ServerID:     strconv.FormatInt(serverID, 10),
					Type:         baseType,
					SubType:      subType,
					CreateTime:   createTime,
					ContentHash:  auditContentHash(rawContent),
					ContentKind:  classifyAuditMessagePayload(rawContent, content, decodeErr),
					ContentBytes: len(rawContent),
					DecodedBytes: len(content),
					NonEmpty:     len(bytes.TrimSpace(rawContent)) > 0,
					Reason:       reason,
				})
			}
			if baseType == 11000 {
				report.Messages.OpaqueControlRows++
				if len(bytes.TrimSpace(rawContent)) == 0 {
					report.Messages.OpaqueControlEmptyRows++
				}
			}
			if len(packed) > 0 {
				report.Messages.PackedRows++
				if model.ParsePackedInfo(packed) == nil {
					report.Messages.PackedErrors++
				}
			}
			return ctx.Err()
		})
		if streamErr != nil {
			report.ShardErrors = append(report.ShardErrors, databaseAuditShardError{
				Group: "message", File: filepath.Base(file), Table: table, Error: streamErr.Error(),
			})
		}
	}
	report.Watermark.Shards = append(report.Watermark.Shards, shard)
	if shard.LatestTimestamp > report.Watermark.MessageLatestTimestamp {
		report.Watermark.MessageLatestTimestamp = shard.LatestTimestamp
	}
}

func (s *Service) auditTalkerHashIndex(file string) map[string]string {
	result := make(map[string]string)
	rows, err := s.db.ExecuteSQL("message", file, `SELECT user_name FROM Name2Id`)
	if err != nil {
		return result
	}
	for _, row := range rows {
		username := toString(row["user_name"])
		if username == "" {
			continue
		}
		sum := md5.Sum([]byte(username))
		result[hex.EncodeToString(sum[:])] = username
	}
	return result
}

func (s *Service) auditSessionWatermark(report *databaseAuditReport, dbs map[string][]string) {
	file := findAuditDBFile(dbs, "session", "session.db")
	if file == "" {
		report.Watermark.Status = databaseWatermarkStatus(report, false)
		return
	}
	rows, err := s.db.ExecuteSQL("session", file, `
SELECT username, last_timestamp, sort_timestamp
FROM SessionTable
WHERE last_timestamp > 0
ORDER BY last_timestamp DESC
LIMIT 100`)
	if err != nil {
		report.ShardErrors = append(report.ShardErrors, databaseAuditShardError{
			Group: "session", File: filepath.Base(file), Error: err.Error(),
		})
		report.Watermark.Status = databaseWatermarkStatus(report, false)
		return
	}
	for _, row := range rows {
		username := toString(row["username"])
		latest := effectiveSessionWatermark(
			username,
			toInt64(row["last_timestamp"]),
			toInt64(row["sort_timestamp"]),
		)
		if latest > report.Watermark.SessionLatestTimestamp {
			report.Watermark.LatestSession = username
			report.Watermark.SessionLatestTimestamp = latest
		}
	}
	report.Watermark.GapSeconds = report.Watermark.SessionLatestTimestamp - report.Watermark.MessageLatestTimestamp
	report.Watermark.Status = databaseWatermarkStatus(report, true)
}

func effectiveSessionWatermark(username string, lastTimestamp, sortTimestamp int64) int64 {
	switch strings.ToLower(strings.TrimSpace(username)) {
	case "brandsessionholder", "brandprivatemsg@hardcode":
		// Folded official-account sessions refresh last_timestamp for summary or
		// unread state changes. Their actual message order follows sort_timestamp
		// and the underlying rows live in biz_message_N.db.
		if sortTimestamp > 0 {
			return sortTimestamp
		}
	}
	return lastTimestamp
}

func databaseWatermarkStatus(report *databaseAuditReport, sessionAvailable bool) string {
	if report.Inventory.MessageShards == 0 {
		return "missing"
	}
	if len(report.ShardErrors) > 0 {
		return "incomplete"
	}
	if report.Watermark.MessageLatestTimestamp <= 0 {
		return "empty"
	}
	if !sessionAvailable || report.Watermark.SessionLatestTimestamp <= 0 {
		return "session_missing"
	}
	gap := report.Watermark.SessionLatestTimestamp - report.Watermark.MessageLatestTimestamp
	switch {
	case gap <= 0:
		return "current"
	case gap <= 300:
		return "near_current"
	default:
		return "stale"
	}
}

func decodeAuditMessageContent(raw []byte) (string, bool, error) {
	if bytes.HasPrefix(raw, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		decoded, err := zstd.Decompress(raw)
		if err != nil {
			return "", true, err
		}
		return string(decoded), true, nil
	}
	return string(raw), false, nil
}

func stripAuditChatRoomSender(content, sender string) string {
	if strings.TrimSpace(sender) == "" {
		return content
	}
	prefix, body, ok := strings.Cut(content, ":\n")
	if ok && prefix == sender {
		return body
	}
	return content
}

func databaseMessageFormatSupported(messageType, subType int64) bool {
	switch messageType {
	case model.MessageTypeText,
		model.MessageTypeImage,
		model.MessageTypeVoice,
		model.MessageTypeCard,
		model.MessageTypeVideo,
		model.MessageTypeAnimation,
		model.MessageTypeLocation,
		model.MessageTypeVOIP,
		model.MessageTypeSystem,
		model.MessageTypeSystemNotification:
		return true
	case model.MessageTypeShare:
		switch subType {
		case model.MessageSubTypeText,
			model.MessageSubTypeLink,
			model.MessageSubTypeLink2,
			model.MessageSubTypeFile,
			model.MessageSubTypeGIF,
			model.MessageSubTypeRealtimeLocation,
			model.MessageSubTypeMergeForward,
			model.MessageSubTypeNote,
			model.MessageSubTypeMiniProgram,
			model.MessageSubTypeMiniProgram2,
			model.MessageSubTypeChannel,
			model.MessageSubTypeQuote,
			model.MessageSubTypePat,
			model.MessageSubTypeChannelLive,
			model.MessageSubTypeFileUploading,
			model.MessageSubTypeChatRoomNotice,
			model.MessageSubTypeMusic,
			model.MessageSubTypePay,
			model.MessageSubTypeRedEnvelope,
			model.MessageSubTypeRedEnvelopeCover:
			return true
		}
	}
	return false
}

func sortedDatabaseAuditTypes(counts map[databaseAuditTypeKey]int64) []databaseAuditMessageType {
	out := make([]databaseAuditMessageType, 0, len(counts))
	for key, rows := range counts {
		out = append(out, databaseAuditMessageType{
			Type:      key.Type,
			SubType:   key.SubType,
			Rows:      rows,
			Supported: databaseMessageFormatSupported(key.Type, key.SubType),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rows != out[j].Rows {
			return out[i].Rows > out[j].Rows
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].SubType < out[j].SubType
	})
	return out
}

func appendDatabaseAuditSample(report *databaseAuditReport, sample databaseAuditUnknownSample) {
	if len(report.Messages.UnknownSamples) < databaseAuditMaxSample {
		report.Messages.UnknownSamples = append(report.Messages.UnknownSamples, sample)
	}
}

func auditContentHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:6])
}

func roundPercent(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	value := float64(numerator) / float64(denominator) * 100
	return float64(int64(value*1000+0.5)) / 1000
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func databaseRowMap(columns []string, values []interface{}) map[string]interface{} {
	row := make(map[string]interface{}, len(columns))
	for index, column := range columns {
		if index < len(values) {
			row[column] = values[index]
		}
	}
	return row
}

func toDatabaseBytes(value interface{}) []byte {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return typed
	case string:
		return []byte(typed)
	default:
		return []byte(fmt.Sprint(typed))
	}
}

func findAuditDBFile(dbs map[string][]string, group, base string) string {
	for _, file := range dbs[group] {
		if strings.EqualFold(filepath.Base(file), base) {
			return file
		}
	}
	return ""
}

func findAuditDBFileAnyGroup(dbs map[string][]string, base string) (string, string) {
	for group, files := range dbs {
		for _, file := range files {
			if strings.EqualFold(filepath.Base(file), base) {
				return file, group
			}
		}
	}
	return "", ""
}

func normalizeAuditGroup(group string) string {
	group = strings.TrimSpace(strings.ToLower(group))
	if group == "" {
		return "message"
	}
	return group
}
