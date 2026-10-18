package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
)

type databaseSearchConsistencyCheck struct {
	ID       string      `json:"id"`
	Status   string      `json:"status"`
	Actual   interface{} `json:"actual"`
	Expected string      `json:"expected"`
	Detail   string      `json:"detail"`
}

func (s *Service) handleDatabaseSearchConsistency(c *gin.Context) {
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
	checks := buildDatabaseSearchConsistencyChecks(report)
	status := "pass"
	for _, check := range checks {
		if check.Status == "fail" {
			status = "fail"
			break
		}
		if check.Status == "warn" {
			status = "warn"
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"status":       status,
		"generated_at": report.GeneratedAt,
		"checks":       checks,
		"summary": gin.H{
			"message_rows":                   report.Messages.TotalRows,
			"searchable_rows":                report.FTS.MessageContentRows,
			"message_watermark":              report.Watermark.MessageLatestTimestamp,
			"fts_expected_message_watermark": report.FTS.ExpectedLatestTimestamp,
			"fts_watermark":                  report.FTS.MessageLatestTimestamp,
			"watermark_gap_secs":             report.FTS.MessageWatermarkGapSeconds,
			"fallback_path":                  report.FTS.FallbackPath,
		},
	})
}

func buildDatabaseSearchConsistencyChecks(report *databaseAuditReport) []databaseSearchConsistencyCheck {
	if report == nil {
		return []databaseSearchConsistencyCheck{{
			ID: "audit", Status: "fail", Actual: "missing", Expected: "available",
			Detail: "数据库审计结果缺失。",
		}}
	}
	coverageStatus := "pass"
	if report.Messages.CoveragePercent < 99.9 {
		coverageStatus = "fail"
	}
	messageWatermarkStatus := consistencyStatus(report.Watermark.Status)
	ftsWatermarkStatus := consistencyStatus(report.FTS.MessageWatermarkStatus)
	shadowStatus := "pass"
	if report.FTS.ShadowContentTables == 0 || report.FTS.MessageContentRows == 0 {
		shadowStatus = "fail"
	}
	resourceStatus := "pass"
	if report.Resources.Available && report.Resources.LinkCoverage < 99.9 {
		resourceStatus = "warn"
	}
	nonEmptyUnknown := int64(0)
	for _, sample := range report.Messages.UnknownSamples {
		if sample.NonEmpty {
			nonEmptyUnknown++
		}
	}
	unknownStatus := "pass"
	if nonEmptyUnknown > 0 {
		unknownStatus = "warn"
	}
	return []databaseSearchConsistencyCheck{
		{
			ID: "message_parse_coverage", Status: coverageStatus,
			Actual: report.Messages.CoveragePercent, Expected: ">= 99.9%",
			Detail: "搜索前先保证消息内容解析覆盖率稳定。",
		},
		{
			ID: "message_session_watermark", Status: messageWatermarkStatus,
			Actual: report.Watermark.Status, Expected: "current or near_current",
			Detail: "消息分片水位与会话水位保持一致。",
		},
		{
			ID: "fts_shadow_inventory", Status: shadowStatus,
			Actual: report.FTS.ShadowContentTables, Expected: "> 0",
			Detail: "私有 FTS 的明文内容影子表可查询。",
		},
		{
			ID: "fts_message_watermark", Status: ftsWatermarkStatus,
			Actual: report.FTS.MessageWatermarkGapSeconds, Expected: "<= 300 seconds",
			Detail: "FTS 水位之后的消息由增量普通表补齐。",
		},
		{
			ID: "resource_link_coverage", Status: resourceStatus,
			Actual: report.Resources.LinkCoverage, Expected: ">= 99.9%",
			Detail: "消息资源关联保持完整，搜索结果可继续定位媒体。",
		},
		{
			ID: "non_empty_unknown_formats", Status: unknownStatus,
			Actual: nonEmptyUnknown, Expected: "0",
			Detail: "存在内容的未知格式会进入结构化诊断样本。",
		},
	}
}

func consistencyStatus(status string) string {
	switch status {
	case "current", "near_current":
		return "pass"
	case "stale":
		return "warn"
	default:
		return "fail"
	}
}
