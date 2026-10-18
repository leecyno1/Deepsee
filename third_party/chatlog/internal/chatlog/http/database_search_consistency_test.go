package http

import (
	"testing"
)

func TestDatabaseSearchConsistencyChecksWatermarksAndUnknownFormats(t *testing.T) {
	report := &databaseAuditReport{
		Messages: databaseMessageAudit{
			TotalRows:       100,
			CoveragePercent: 100,
			UnknownSamples: []databaseAuditUnknownSample{
				{NonEmpty: false},
			},
		},
		Watermark: databaseWatermarkAudit{
			Status:                 "current",
			MessageLatestTimestamp: 200,
			SessionLatestTimestamp: 200,
		},
		FTS: databaseFTSAudit{
			ShadowContentTables:    2,
			MessageContentRows:     80,
			MessageLatestTimestamp: 200,
			MessageWatermarkStatus: "current",
		},
		Resources: databaseResourceAudit{
			Available:    true,
			LinkCoverage: 100,
		},
	}
	checks := buildDatabaseSearchConsistencyChecks(report)
	for _, check := range checks {
		if check.Status != "pass" {
			t.Fatalf("unexpected consistency check: %+v", check)
		}
	}

	report.FTS.MessageWatermarkStatus = "stale"
	report.FTS.MessageWatermarkGapSeconds = 900
	report.Messages.UnknownSamples = append(report.Messages.UnknownSamples, databaseAuditUnknownSample{NonEmpty: true})
	checks = buildDatabaseSearchConsistencyChecks(report)
	statuses := make(map[string]string, len(checks))
	for _, check := range checks {
		statuses[check.ID] = check.Status
	}
	if statuses["fts_message_watermark"] != "warn" || statuses["non_empty_unknown_formats"] != "warn" {
		t.Fatalf("stale checks = %+v", statuses)
	}
}
