package http

import (
	"fmt"
	"path/filepath"
	"strings"
)

func (s *Service) auditMessageResources(report *databaseAuditReport, dbs map[string][]string) {
	file, group := findAuditDBFileAnyGroup(dbs, "message_resource.db")
	if file == "" {
		return
	}
	report.Resources.Available = true
	report.Resources.File = filepath.Base(file)

	group = normalizeAuditGroup(group)
	if rows, err := s.db.ExecuteSQL(group, file, `
SELECT COUNT(*) AS info_rows,
       COALESCE(MAX(message_create_time), 0) AS latest_timestamp,
       COALESCE(SUM(CASE WHEN EXISTS (
           SELECT 1 FROM MessageResourceDetail d WHERE d.message_id = i.message_id
       ) THEN 1 ELSE 0 END), 0) AS linked_info_rows
FROM MessageResourceInfo i`); err == nil && len(rows) > 0 {
		report.Resources.InfoRows = toInt64(rows[0]["info_rows"])
		report.Resources.LatestTimestamp = toInt64(rows[0]["latest_timestamp"])
		report.Resources.LinkedInfoRows = toInt64(rows[0]["linked_info_rows"])
	} else if err != nil {
		report.ShardErrors = append(report.ShardErrors, databaseAuditShardError{
			Group: group, File: filepath.Base(file), Error: err.Error(),
		})
	}
	if rows, err := s.db.ExecuteSQL(group, file, `
SELECT COUNT(*) AS detail_rows,
       COUNT(DISTINCT message_id) AS distinct_messages,
       COALESCE(SUM(size), 0) AS bytes_total
FROM MessageResourceDetail`); err == nil && len(rows) > 0 {
		report.Resources.DetailRows = toInt64(rows[0]["detail_rows"])
		report.Resources.DistinctMessages = toInt64(rows[0]["distinct_messages"])
		report.Resources.BytesTotal = toInt64(rows[0]["bytes_total"])
	}
	if report.Resources.InfoRows > 0 {
		report.Resources.LinkCoverage = roundPercent(report.Resources.LinkedInfoRows, report.Resources.InfoRows)
	}
	if rows, err := s.db.ExecuteSQL(group, file, `
SELECT status, COUNT(*) AS rows, COALESCE(SUM(size), 0) AS bytes
FROM MessageResourceDetail
GROUP BY status
ORDER BY rows DESC`); err == nil {
		report.Resources.StatusDistribution = auditResourceBuckets(rows, false)
	}
	if rows, err := s.db.ExecuteSQL(group, file, `
SELECT type, COUNT(*) AS rows, COALESCE(SUM(size), 0) AS bytes
FROM MessageResourceDetail
GROUP BY type
ORDER BY rows DESC`); err == nil {
		report.Resources.TypeDistribution = auditResourceBuckets(rows, true)
	}
}

func auditResourceBuckets(rows []map[string]interface{}, useType bool) []databaseAuditResourceBucket {
	out := make([]databaseAuditResourceBucket, 0, len(rows))
	for _, row := range rows {
		item := databaseAuditResourceBucket{
			Rows:  toInt64(row["rows"]),
			Bytes: toInt64(row["bytes"]),
		}
		if useType {
			item.Type = toInt64(row["type"])
			item.TypeBase, item.SubType = splitMessageResourceType(item.Type)
		} else {
			item.Status = toInt64(row["status"])
		}
		out = append(out, item)
	}
	return out
}

func (s *Service) auditFTS(report *databaseAuditReport, dbs map[string][]string) {
	report.FTS.ProbeStatus = "not_present"
	report.FTS.ModuleStatus = "not_present"
	report.FTS.FallbackPath = "ordinary_message_tables"
	for group, files := range dbs {
		for _, file := range files {
			if !strings.HasSuffix(strings.ToLower(filepath.Base(file)), "_fts.db") {
				continue
			}
			report.FTS.Files++
			if report.FTS.ModuleStatus == "not_present" {
				moduleRows, moduleErr := s.db.ExecuteSQL(normalizeAuditGroup(group), file,
					"SELECT sqlite_compileoption_used('ENABLE_FTS5') AS enabled")
				switch {
				case moduleErr != nil:
					report.FTS.ModuleStatus = "probe_error"
				case len(moduleRows) > 0 && toInt64(moduleRows[0]["enabled"]) == 1:
					report.FTS.ModuleAvailable = true
					report.FTS.ModuleStatus = "available"
				default:
					report.FTS.ModuleStatus = "unavailable"
				}
			}
			rows, err := s.db.ExecuteSQL(normalizeAuditGroup(group), file, `
SELECT name, COALESCE(sql, '') AS sql
FROM sqlite_master
WHERE type = 'table' AND UPPER(COALESCE(sql, '')) LIKE 'CREATE VIRTUAL TABLE%'`)
			if err != nil {
				report.FTS.ProbeStatus = "inventory_error"
				report.FTS.ProbeError = compactDatabaseAuditError(err)
				continue
			}
			report.FTS.VirtualTables += len(rows)
			shadowRows, shadowErr := s.db.ExecuteSQL(normalizeAuditGroup(group), file, `
SELECT name
FROM sqlite_master
WHERE type = 'table' AND name LIKE '%_content'`)
			if shadowErr == nil {
				report.FTS.ShadowContentTables += len(shadowRows)
				for _, shadow := range shadowRows {
					table := toString(shadow["name"])
					if !strings.HasPrefix(strings.ToLower(table), "message_fts_v4_") {
						continue
					}
					statsQuery := fmt.Sprintf(
						"SELECT COUNT(*) AS rows, COALESCE(MAX(c6), 0) AS latest_timestamp FROM %s",
						quoteSQLiteIdentifier(table),
					)
					statsRows, statsErr := s.db.ExecuteSQL(normalizeAuditGroup(group), file, statsQuery)
					if statsErr != nil || len(statsRows) == 0 {
						continue
					}
					report.FTS.MessageContentRows += toInt64(statsRows[0]["rows"])
					latest := toInt64(statsRows[0]["latest_timestamp"])
					if latest > report.FTS.MessageLatestTimestamp {
						report.FTS.MessageLatestTimestamp = latest
					}
				}
			}
			for _, row := range rows {
				if strings.Contains(strings.ToLower(toString(row["sql"])), "mmftstokenizer") {
					report.FTS.MMTokenizerTables++
					if report.FTS.NativeQuery {
						continue
					}
					table := toString(row["name"])
					query := fmt.Sprintf(
						"SELECT rowid FROM %s WHERE %s MATCH 'chatlog_probe_token' LIMIT 1",
						quoteSQLiteIdentifier(table),
						quoteSQLiteIdentifier(table),
					)
					if _, probeErr := s.db.ExecuteSQL(normalizeAuditGroup(group), file, query); probeErr == nil {
						report.FTS.NativeQuery = true
						report.FTS.ProbeStatus = "available"
						report.FTS.ProbeError = ""
					} else {
						report.FTS.ProbeStatus = classifyFTSProbeError(probeErr)
						report.FTS.ProbeError = compactDatabaseAuditError(probeErr)
					}
				}
			}
		}
	}
	if report.FTS.Files > 0 && report.FTS.MMTokenizerTables == 0 && report.FTS.ProbeStatus == "not_present" {
		report.FTS.ProbeStatus = "no_private_tokenizer_table"
	}
	if report.FTS.ShadowContentTables > 0 {
		report.FTS.FallbackPath = "fts_shadow_content_then_ordinary_message_tables"
	}
	report.FTS.ExpectedLatestTimestamp = report.Messages.FTSExpectedLatest
	if report.FTS.ExpectedLatestTimestamp <= 0 {
		report.FTS.ExpectedLatestTimestamp = report.Watermark.MessageLatestTimestamp
	}
	report.FTS.MessageWatermarkGapSeconds = report.FTS.ExpectedLatestTimestamp - report.FTS.MessageLatestTimestamp
	switch {
	case report.FTS.MessageLatestTimestamp <= 0:
		report.FTS.MessageWatermarkStatus = "missing"
	case report.FTS.MessageWatermarkGapSeconds <= 0:
		report.FTS.MessageWatermarkStatus = "current"
	case report.FTS.MessageWatermarkGapSeconds <= 300:
		report.FTS.MessageWatermarkStatus = "near_current"
	default:
		report.FTS.MessageWatermarkStatus = "stale"
	}
}

func classifyFTSProbeError(err error) string {
	message := strings.ToLower(compactDatabaseAuditError(err))
	switch {
	case strings.Contains(message, "no such module: fts5"):
		return "fts5_module_unavailable"
	case strings.Contains(message, "no such tokenizer"):
		return "private_tokenizer_unavailable"
	case message == "":
		return "available"
	default:
		return "probe_error"
	}
}

func compactDatabaseAuditError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	const limit = 240
	if len(message) > limit {
		message = message[:limit] + "…"
	}
	return message
}

func finalizeDatabaseAuditWarnings(report *databaseAuditReport) {
	if len(report.ShardErrors) > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d 个分片或数据表审计失败", len(report.ShardErrors)))
	}
	if report.Messages.UnsupportedRows > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d 条消息尚未进入完整结构化解析", report.Messages.UnsupportedRows))
	}
	if report.Messages.ZstdErrors > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d 条 zstd 消息解压失败", report.Messages.ZstdErrors))
	}
	if report.Messages.PackedErrors > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d 条 packed_info_data 解析失败", report.Messages.PackedErrors))
	}
	switch report.Watermark.Status {
	case "stale":
		report.Warnings = append(report.Warnings, fmt.Sprintf("会话水位领先消息分片 %d 秒", report.Watermark.GapSeconds))
	case "incomplete", "missing", "session_missing":
		report.Warnings = append(report.Warnings, "消息分片或会话水位信息不完整")
	}
	if report.FTS.Files > 0 && report.FTS.MMTokenizerTables > 0 {
		if report.FTS.NativeQuery {
			report.Warnings = append(report.Warnings, "SQLite FTS5 与 MMFtsTokenizer 兼容直查探测通过，搜索同时保留内容影子表与普通消息表回退")
		} else if !report.FTS.ModuleAvailable {
			report.Warnings = append(report.Warnings, "SQLite FTS5 模块未加载，搜索继续使用内容影子表与普通消息表回退")
		} else if report.FTS.ShadowContentTables > 0 {
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"SQLite FTS5 已加载；微信私有 MMFtsTokenizer 未注册，搜索已启用 %d 张 FTS 内容影子表",
				report.FTS.ShadowContentTables,
			))
		} else {
			report.Warnings = append(report.Warnings, "MMFtsTokenizer 原生读取探测未通过，搜索使用普通消息表链路")
		}
	}
}
