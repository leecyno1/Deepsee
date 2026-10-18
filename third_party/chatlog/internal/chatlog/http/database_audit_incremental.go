package http

import (
	"context"
	"os"
	"path/filepath"
)

type databaseAuditScan struct {
	Mode          string   `json:"mode"`
	ScannedShards int      `json:"scanned_shards"`
	ReusedShards  int      `json:"reused_shards"`
	ScannedFiles  []string `json:"scanned_files,omitempty"`
	ChangedFiles  []string `json:"changed_files,omitempty"`
}

type databaseAuditFileFingerprint struct {
	SourceMTime int64
	SourceSize  int64
	WALMTime    int64
	WALSize     int64
}

type databaseAuditShardCache struct {
	Fingerprint   databaseAuditFileFingerprint
	Messages      databaseMessageAudit
	TypeCounts    map[databaseAuditTypeKey]int64
	Watermark     databaseAuditShardWatermark
	MessageTables int
	Errors        []databaseAuditShardError
}

func databaseAuditFingerprint(path string) databaseAuditFileFingerprint {
	sourceMTime, sourceSize := databaseAuditFileStat(path)
	walMTime, walSize := databaseAuditFileStat(path + "-wal")
	return databaseAuditFileFingerprint{
		SourceMTime: sourceMTime,
		SourceSize:  sourceSize,
		WALMTime:    walMTime,
		WALSize:     walSize,
	}
}

func databaseAuditFileStat(path string) (int64, int64) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0
	}
	return info.ModTime().UnixNano(), info.Size()
}

func (s *Service) databaseAuditShardCacheSnapshot() map[string]databaseAuditShardCache {
	s.databaseAuditMu.RLock()
	defer s.databaseAuditMu.RUnlock()
	out := make(map[string]databaseAuditShardCache, len(s.databaseAuditShards))
	for file, cached := range s.databaseAuditShards {
		out[file] = cloneDatabaseAuditShardCache(cached)
	}
	return out
}

func (s *Service) replaceDatabaseAuditShardCache(next map[string]databaseAuditShardCache) {
	s.databaseAuditMu.Lock()
	s.databaseAuditShards = next
	s.databaseAuditMu.Unlock()
}

func cloneDatabaseAuditShardCache(cached databaseAuditShardCache) databaseAuditShardCache {
	out := cached
	out.Messages.ByType = append([]databaseAuditMessageType(nil), cached.Messages.ByType...)
	out.Messages.UnknownSamples = append([]databaseAuditUnknownSample(nil), cached.Messages.UnknownSamples...)
	out.TypeCounts = make(map[databaseAuditTypeKey]int64, len(cached.TypeCounts))
	for key, count := range cached.TypeCounts {
		out.TypeCounts[key] = count
	}
	out.Errors = append([]databaseAuditShardError(nil), cached.Errors...)
	return out
}

func (s *Service) scanOrReuseDatabaseAuditShard(
	ctx context.Context,
	file string,
	forceFull bool,
	previous map[string]databaseAuditShardCache,
) (databaseAuditShardCache, bool) {
	fingerprint := databaseAuditFingerprint(file)
	if cached, ok := previous[file]; ok &&
		!forceFull &&
		len(cached.Errors) == 0 &&
		cached.Fingerprint == fingerprint {
		return cloneDatabaseAuditShardCache(cached), true
	}

	localReport := &databaseAuditReport{
		Messages:    databaseMessageAudit{UnknownSamples: make([]databaseAuditUnknownSample, 0)},
		Watermark:   databaseWatermarkAudit{Shards: make([]databaseAuditShardWatermark, 0, 1)},
		ShardErrors: make([]databaseAuditShardError, 0),
	}
	typeCounts := make(map[databaseAuditTypeKey]int64)
	s.auditMessageShard(ctx, localReport, file, typeCounts)
	watermark := databaseAuditShardWatermark{File: filepath.Base(file)}
	if len(localReport.Watermark.Shards) > 0 {
		watermark = localReport.Watermark.Shards[0]
	}
	return databaseAuditShardCache{
		Fingerprint:   fingerprint,
		Messages:      localReport.Messages,
		TypeCounts:    typeCounts,
		Watermark:     watermark,
		MessageTables: localReport.Inventory.MessageTables,
		Errors:        append([]databaseAuditShardError(nil), localReport.ShardErrors...),
	}, false
}

func mergeDatabaseAuditShard(
	report *databaseAuditReport,
	result databaseAuditShardCache,
	typeCounts map[databaseAuditTypeKey]int64,
) {
	report.Inventory.MessageTables += result.MessageTables
	report.Watermark.Shards = append(report.Watermark.Shards, result.Watermark)
	if result.Watermark.LatestTimestamp > report.Watermark.MessageLatestTimestamp {
		report.Watermark.MessageLatestTimestamp = result.Watermark.LatestTimestamp
	}
	report.ShardErrors = append(report.ShardErrors, result.Errors...)

	target := &report.Messages
	source := result.Messages
	target.TotalRows += source.TotalRows
	target.ParsedRows += source.ParsedRows
	target.SupportedRows += source.SupportedRows
	target.UnsupportedRows += source.UnsupportedRows
	target.ParseErrors += source.ParseErrors
	target.ZstdRows += source.ZstdRows
	target.ZstdErrors += source.ZstdErrors
	target.PackedRows += source.PackedRows
	target.PackedErrors += source.PackedErrors
	target.RawPreservedRows += source.RawPreservedRows
	target.OpaqueControlRows += source.OpaqueControlRows
	target.OpaqueControlEmptyRows += source.OpaqueControlEmptyRows
	if source.FTSExpectedLatest > target.FTSExpectedLatest {
		target.FTSExpectedLatest = source.FTSExpectedLatest
	}
	for _, sample := range source.UnknownSamples {
		appendDatabaseAuditSample(report, sample)
	}
	for key, count := range result.TypeCounts {
		typeCounts[key] += count
	}
}
