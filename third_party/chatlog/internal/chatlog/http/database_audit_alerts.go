package http

import "fmt"

type databaseAuditAlert struct {
	Level  string `json:"level"`
	Code   string `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func finalizeDatabaseAuditStatuses(report *databaseAuditReport) {
	if report == nil {
		return
	}
	switch {
	case report.Messages.OpaqueControlRows == 0:
		report.Messages.OpaqueControlStatus = "none"
	case report.Messages.OpaqueControlRows == report.Messages.OpaqueControlEmptyRows:
		report.Messages.OpaqueControlStatus = "empty_only"
	default:
		report.Messages.OpaqueControlStatus = "samples_available"
	}
	report.Alerts = buildDatabaseAuditAlerts(report)
}

func buildDatabaseAuditAlerts(report *databaseAuditReport) []databaseAuditAlert {
	alerts := make([]databaseAuditAlert, 0, 8)
	add := func(level, code, title, detail string) {
		alerts = append(alerts, databaseAuditAlert{Level: level, Code: code, Title: title, Detail: detail})
	}
	if len(report.ShardErrors) > 0 {
		add("error", "shard_audit_failed", "数据库分片审计失败",
			fmt.Sprintf("%d 个分片或数据表读取异常。", len(report.ShardErrors)))
	}
	switch report.Watermark.Status {
	case "stale":
		add("warning", "watermark_stale", "消息分片水位落后",
			fmt.Sprintf("会话水位领先消息分片 %d 秒。", report.Watermark.GapSeconds))
	case "incomplete", "missing", "session_missing":
		add("error", "watermark_incomplete", "消息分片水位不完整",
			fmt.Sprintf("当前水位状态为 %s。", report.Watermark.Status))
	}
	if report.Messages.ZstdErrors > 0 {
		add("error", "zstd_decode_error", "压缩消息解码异常",
			fmt.Sprintf("%d 条 zstd 消息解压失败。", report.Messages.ZstdErrors))
	}
	if report.Messages.PackedErrors > 0 {
		add("warning", "packed_info_error", "附加消息数据解析异常",
			fmt.Sprintf("%d 条 packed_info_data 解析失败。", report.Messages.PackedErrors))
	}
	if report.Messages.UnsupportedRows > 0 {
		add("warning", "unsupported_message_format", "存在待补齐的消息格式",
			fmt.Sprintf("%d 条消息尚未进入完整结构化解析，覆盖率 %.3f%%。",
				report.Messages.UnsupportedRows, report.Messages.CoveragePercent))
	}
	if report.Resources.Available && report.Resources.LinkCoverage < 100 {
		level := "warning"
		if report.Resources.LinkCoverage < 95 {
			level = "error"
		}
		add(level, "resource_link_coverage", "消息资源关联率下降",
			fmt.Sprintf("当前资源关联覆盖率为 %.3f%%。", report.Resources.LinkCoverage))
	}
	if report.FTS.MessageWatermarkStatus == "stale" {
		add("warning", "fts_message_watermark_stale", "消息搜索索引水位落后",
			fmt.Sprintf("FTS 内容影子表落后可索引文本消息 %d 秒。", report.FTS.MessageWatermarkGapSeconds))
	}
	if report.Messages.OpaqueControlStatus == "samples_available" {
		add("warning", "opaque_control_sample", "发现可研究的控制消息样本",
			fmt.Sprintf("%d 条控制记录中已有非空载荷，可进入格式解析阶段。", report.Messages.OpaqueControlRows))
	}
	return alerts
}

func appendDatabaseAuditRegressionAlerts(current, previous *databaseAuditReport) {
	if current == nil || previous == nil {
		return
	}
	add := func(code, title, detail string) {
		for _, alert := range current.Alerts {
			if alert.Code == code {
				return
			}
		}
		current.Alerts = append(current.Alerts, databaseAuditAlert{
			Level: "warning", Code: code, Title: title, Detail: detail,
		})
	}
	if current.Messages.UnsupportedRows > previous.Messages.UnsupportedRows {
		add("unsupported_rows_increased", "待解析消息数量增加",
			fmt.Sprintf("较上次审计增加 %d 条。", current.Messages.UnsupportedRows-previous.Messages.UnsupportedRows))
	}
	if current.Messages.ZstdErrors > previous.Messages.ZstdErrors {
		add("zstd_errors_increased", "zstd 解压错误增加",
			fmt.Sprintf("较上次审计增加 %d 条。", current.Messages.ZstdErrors-previous.Messages.ZstdErrors))
	}
	if current.Messages.PackedErrors > previous.Messages.PackedErrors {
		add("packed_errors_increased", "packed_info 错误增加",
			fmt.Sprintf("较上次审计增加 %d 条。", current.Messages.PackedErrors-previous.Messages.PackedErrors))
	}
	if current.Messages.CoveragePercent+0.001 < previous.Messages.CoveragePercent {
		add("coverage_decreased", "消息解析覆盖率下降",
			fmt.Sprintf("由 %.3f%% 下降至 %.3f%%。", previous.Messages.CoveragePercent, current.Messages.CoveragePercent))
	}
}
