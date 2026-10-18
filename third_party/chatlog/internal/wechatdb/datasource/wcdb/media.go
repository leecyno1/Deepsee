package wcdb

import (
	"context"
	stderrors "errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/model"
)

func (ds *DataSource) GetMedia(ctx context.Context, _type, key string) (*model.Media, error) {
	_ = ctx
	if _type == "voice" {
		// VoiceInfo is sharded across media_N.db files. Querying only the first
		// shard made older voice messages disappear after database rotation.
		dbs, err := ds.client.ListVoiceDBs()
		if err != nil {
			return nil, err
		}
		queryErrors := make([]error, 0)

		// Server ID is the only globally stable voice identifier. Look it up
		// first and independently: local_id repeats inside the same VoiceInfo
		// table and across shards, so an OR query can return another chat's
		// audio before the matching server ID row.
		serverSQL := `SELECT voice_data FROM VoiceInfo WHERE CAST(svr_id AS TEXT) = ` + strconv.Quote(key) + ` LIMIT 1`
		for _, dbPath := range dbs {
			rows, queryErr := ds.client.Query("voice", dbPath, serverSQL)
			if queryErr != nil {
				queryErrors = append(queryErrors, fmt.Errorf("%s: %w", filepath.Base(dbPath), queryErr))
				continue
			}
			if len(rows) == 0 {
				continue
			}
			data := toBytes(rows[0]["voice_data"])
			if len(data) != 0 {
				return &model.Media{Type: "voice", Key: key, Data: data}, nil
			}
		}

		// Older records can have no server ID. Retain local-ID compatibility
		// only when it resolves to exactly one row across every shard. Returning
		// the first of several matches leaks an unrelated voice into the chat.
		localSQL := `SELECT voice_data FROM VoiceInfo WHERE CAST(local_id AS TEXT) = ` + strconv.Quote(key) + ` LIMIT 2`
		localMatches := make([][]byte, 0, 2)
		for _, dbPath := range dbs {
			rows, queryErr := ds.client.Query("voice", dbPath, localSQL)
			if queryErr != nil {
				queryErrors = append(queryErrors, fmt.Errorf("%s: %w", filepath.Base(dbPath), queryErr))
				continue
			}
			for _, row := range rows {
				if data := toBytes(row["voice_data"]); len(data) != 0 {
					localMatches = append(localMatches, data)
					if len(localMatches) > 1 {
						return nil, errors.ErrMediaNotFound
					}
				}
			}
		}
		if len(localMatches) == 1 {
			return &model.Media{Type: "voice", Key: key, Data: localMatches[0]}, nil
		}
		if len(queryErrors) != 0 {
			return nil, fmt.Errorf("voice query failed: %w", stderrors.Join(queryErrors...))
		}
		return nil, errors.ErrMediaNotFound
	}

	table, err := hardlinkTableForMediaType(_type)
	if err != nil {
		return nil, err
	}
	sql := fmt.Sprintf(`
SELECT
	f.md5,
	f.type AS hardlink_type,
	f.file_name,
	f.file_size,
	f.modify_time,
	f.extra_buffer,
	IFNULL(d1.username,"") AS dir1,
	IFNULL(d2.username,"") AS dir2
FROM %s f
LEFT JOIN dir2id d1 ON d1.rowid = f.dir1
LEFT JOIN dir2id d2 ON d2.rowid = f.dir2
WHERE f.md5 = %s OR f.file_name LIKE %s || '%%'
`, table, strconv.Quote(key), strconv.Quote(key))
	rows, err := ds.client.Query("media", "", sql)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.ErrMediaNotFound
	}
	return mediaFromHardlinkRows(_type, rows)
}

func (ds *DataSource) GetMediaByName(ctx context.Context, mediaType, name string, size int64) (*model.Media, error) {
	_ = ctx
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.ErrKeyEmpty
	}
	table, err := hardlinkTableForMediaType(mediaType)
	if err != nil {
		return nil, err
	}
	sql := fmt.Sprintf(`
SELECT
	f.md5,
	f.type AS hardlink_type,
	f.file_name,
	f.file_size,
	f.modify_time,
	f.extra_buffer,
	IFNULL(d1.username,"") AS dir1,
	IFNULL(d2.username,"") AS dir2
FROM %s f
LEFT JOIN dir2id d1 ON d1.rowid = f.dir1
LEFT JOIN dir2id d2 ON d2.rowid = f.dir2
WHERE f.file_name = %s
`, table, strconv.Quote(name))
	if size > 0 {
		sql += fmt.Sprintf(" AND f.file_size = %d", size)
	}
	rows, err := ds.client.Query("media", "", sql)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.ErrMediaNotFound
	}
	return mediaFromHardlinkRows(mediaType, rows)
}

func hardlinkTableForMediaType(mediaType string) (string, error) {
	switch mediaType {
	case "image":
		return "image_hardlink_info_v4", nil
	case "video":
		return "video_hardlink_info_v4", nil
	case "file":
		return "file_hardlink_info_v4", nil
	default:
		return "", errors.MediaTypeUnsupported(mediaType)
	}
}

func mediaFromHardlinkRows(mediaType string, rows []map[string]interface{}) (*model.Media, error) {
	if len(rows) == 0 {
		return nil, errors.ErrMediaNotFound
	}
	best := rows[0]
	switch mediaType {
	case "image":
		best = bestImageRow(rows)
	case "video":
		best = bestVideoRow(rows)
	case "file":
		best = bestFileRow(rows)
	}
	mv4 := model.MediaV4{
		Type:         mediaType,
		Key:          toString(best["md5"]),
		Name:         toString(best["file_name"]),
		Size:         toInt64(best["file_size"]),
		ModifyTime:   toInt64(best["modify_time"]),
		ExtraBuffer:  toString(best["extra_buffer"]),
		Dir1:         toString(best["dir1"]),
		Dir2:         toString(best["dir2"]),
		HardLinkType: toInt64(best["hardlink_type"]),
	}
	return mv4.Wrap(), nil
}

func bestImageRow(rows []map[string]interface{}) map[string]interface{} {
	if len(rows) == 0 {
		return nil
	}
	best := rows[0]
	bestTier := imageDATTier(toString(best["file_name"]))
	bestSize := toInt64(best["file_size"])
	bestMtime := toInt64(best["modify_time"])
	for i := 1; i < len(rows); i++ {
		candidate := rows[i]
		candidateTier := imageDATTier(toString(candidate["file_name"]))
		candidateSize := toInt64(candidate["file_size"])
		candidateMtime := toInt64(candidate["modify_time"])
		if candidateTier > bestTier ||
			(candidateTier == bestTier && candidateSize > bestSize) ||
			(candidateTier == bestTier && candidateSize == bestSize && candidateMtime > bestMtime) {
			best = candidate
			bestTier = candidateTier
			bestSize = candidateSize
			bestMtime = candidateMtime
		}
	}
	return best
}

func imageDATTier(name string) int {
	lower := strings.ToLower(strings.TrimSpace(name))
	base := strings.TrimSuffix(lower, ".dat")
	switch {
	case strings.HasSuffix(base, "_h"), strings.HasSuffix(base, ".h"), strings.HasSuffix(base, "_hd"), strings.HasSuffix(base, ".hd"):
		return 4
	case strings.HasSuffix(base, "_b"), strings.HasSuffix(base, ".b"):
		return 3
	case strings.HasSuffix(base, "_t"), strings.HasSuffix(base, ".t"), strings.HasSuffix(base, "_thumb"), strings.HasSuffix(base, ".thumb"):
		return 1
	case strings.HasSuffix(lower, ".dat"):
		return 2
	default:
		return 0
	}
}

func bestVideoRow(rows []map[string]interface{}) map[string]interface{} {
	if len(rows) == 0 {
		return nil
	}
	best := rows[0]
	bestTier := videoNameTier(toString(best["file_name"]))
	bestSize := toInt64(best["file_size"])
	bestMtime := toInt64(best["modify_time"])
	for i := 1; i < len(rows); i++ {
		candidate := rows[i]
		candidateTier := videoNameTier(toString(candidate["file_name"]))
		candidateSize := toInt64(candidate["file_size"])
		candidateMtime := toInt64(candidate["modify_time"])
		if candidateTier > bestTier ||
			(candidateTier == bestTier && candidateSize > bestSize) ||
			(candidateTier == bestTier && candidateSize == bestSize && candidateMtime > bestMtime) {
			best = candidate
			bestTier = candidateTier
			bestSize = candidateSize
			bestMtime = candidateMtime
		}
	}
	return best
}

func videoNameTier(name string) int {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasSuffix(lower, ".mp4"):
		return 3
	case strings.HasSuffix(lower, ".mov"), strings.HasSuffix(lower, ".m4v"), strings.HasSuffix(lower, ".mkv"), strings.HasSuffix(lower, ".avi"):
		return 2
	case strings.HasSuffix(lower, "_thumb.jpg"), strings.HasSuffix(lower, "_thumb.jpeg"), strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"), strings.HasSuffix(lower, ".png"):
		return 1
	default:
		return 0
	}
}

func bestFileRow(rows []map[string]interface{}) map[string]interface{} {
	if len(rows) == 0 {
		return nil
	}
	best := rows[0]
	bestSize := toInt64(best["file_size"])
	bestMtime := toInt64(best["modify_time"])
	for i := 1; i < len(rows); i++ {
		candidate := rows[i]
		candidateSize := toInt64(candidate["file_size"])
		candidateMtime := toInt64(candidate["modify_time"])
		if candidateSize > bestSize || (candidateSize == bestSize && candidateMtime > bestMtime) {
			best = candidate
			bestSize = candidateSize
			bestMtime = candidateMtime
		}
	}
	return best
}
