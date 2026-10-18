package wcdb

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sjzar/chatlog/internal/model"
)

// AnalyzeMessages aggregates every literal keyword match selected by the FTS
// candidate set. Only grouped rows leave SQLite, so common keywords do not
// require loading every matching message body into Go memory.
func (ds *DataSource) AnalyzeMessages(
	ctx context.Context,
	request model.MessageAnalyticsRequest,
) (model.MessageAnalyticsResult, error) {
	result := model.MessageAnalyticsResult{
		Keyword: strings.TrimSpace(request.Keyword),
		Path:    messageSearchPathFTSShadow,
	}
	if result.Keyword == "" {
		return result, fmt.Errorf("keyword is empty")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	ftsTalkers, officialTalkers, includeAllOfficial, restrictFTS := splitMessageSearchTalkers(request.Talkers)
	file := filepath.Join(ds.dataDir, "message", "message_fts.db")
	if !ds.client.CanQueryDB(file) {
		return result, fmt.Errorf("message FTS database is not queryable")
	}
	tableRows, err := ds.client.QueryWithLimitContext(ctx, "message", file, `
SELECT name
FROM sqlite_master
WHERE type = 'table'
  AND name GLOB 'message_fts_v4_[0-9]*_content'
ORDER BY name`, 0)
	if err != nil {
		return result, err
	}
	tables := make([]string, 0, len(tableRows))
	for _, row := range tableRows {
		if name := strings.TrimSpace(toString(row["name"])); name != "" {
			tables = append(tables, name)
		}
	}
	if len(tables) == 0 {
		return result, fmt.Errorf("message FTS content shadow is missing")
	}

	accumulator := newMessageAnalyticsAccumulator(result.Keyword)
	searchPath := messageSearchPathFTSShadow
	if !restrictFTS || len(ftsTalkers) > 0 {
		rows, native, queryErr := ds.queryMessageAnalyticsFTS(
			ctx,
			file,
			tables,
			ftsTalkers,
			includeAllOfficial,
			request,
		)
		if queryErr != nil {
			return result, queryErr
		}
		if native {
			searchPath = messageSearchPathMMFTSNative
		}
		for _, row := range rows {
			accumulator.addGrouped(
				toString(row["talker"]),
				toInt64(row["message_type"]),
				toString(row["day"]),
				toInt64(row["match_count"]),
				toInt64(row["first_timestamp"]),
				toInt64(row["last_timestamp"]),
			)
		}
	}

	ftsLatest := ds.messageFTSLatestTimestamp(file, tables)
	recentMessages, err := ds.searchMessagesNewerThanFTS(
		ctx,
		ftsLatest,
		request.StartTime,
		request.EndTime,
		ftsTalkers,
		restrictFTS,
		result.Keyword,
		request.MessageType,
	)
	if err != nil {
		return result, err
	}
	for _, message := range recentMessages {
		accumulator.addMessage(message)
	}
	if len(recentMessages) > 0 {
		searchPath += "+recent_message_tables"
	}

	officialMessages, err := ds.searchOfficialMessages(
		ctx,
		request.StartTime,
		request.EndTime,
		officialTalkers,
		includeAllOfficial,
		result.Keyword,
		request.MessageType,
	)
	if err != nil {
		return result, err
	}
	for _, message := range officialMessages {
		accumulator.addMessage(message)
	}
	if includeAllOfficial || len(officialTalkers) > 0 {
		searchPath += "+official_message_tables"
	}
	result = accumulator.result(searchPath)
	return result, nil
}

func (ds *DataSource) queryMessageAnalyticsFTS(
	ctx context.Context,
	file string,
	tables []string,
	talkers []string,
	includeAllOfficial bool,
	request model.MessageAnalyticsRequest,
) ([]map[string]interface{}, bool, error) {
	conditions := []string{
		fmt.Sprintf(
			"INSTR(LOWER(CAST(content AS TEXT)), '%s') > 0",
			escapeSearchLiteral(strings.ToLower(strings.TrimSpace(request.Keyword))),
		),
	}
	if !request.StartTime.IsZero() {
		conditions = append(conditions, fmt.Sprintf("create_time >= %d", request.StartTime.Unix()))
	}
	if !request.EndTime.IsZero() {
		conditions = append(conditions, fmt.Sprintf("create_time <= %d", request.EndTime.Unix()))
	}
	if request.MessageType > 0 {
		conditions = append(conditions, fmt.Sprintf("(local_type & 4294967295) = %d", request.MessageType))
	}
	if len(talkers) > 0 {
		quoted := make([]string, 0, len(talkers))
		for _, value := range talkers {
			quoted = append(quoted, "'"+escapeSearchLiteral(value)+"'")
		}
		conditions = append(conditions, fmt.Sprintf(
			"session_id IN (SELECT rowid FROM name2id WHERE username IN (%s))",
			strings.Join(quoted, ","),
		))
	} else if includeAllOfficial {
		conditions = append(conditions,
			"session_id NOT IN (SELECT rowid FROM name2id WHERE username LIKE 'gh\\_%' ESCAPE '\\')")
	}

	buildQuery := func(native bool) string {
		union := make([]string, 0, len(tables))
		for _, table := range tables {
			if native {
				virtualTable := strings.TrimSuffix(table, "_content")
				match := `"` + strings.ReplaceAll(strings.TrimSpace(request.Keyword), `"`, `""`) + `"`
				union = append(union, fmt.Sprintf(`
SELECT acontent AS content, local_type, session_id, create_time
FROM %s
WHERE %s MATCH '%s'`,
					quoteSearchIdentifier(virtualTable),
					quoteSearchIdentifier(virtualTable),
					escapeSearchLiteral(match),
				))
				continue
			}
			union = append(union, fmt.Sprintf(`
SELECT c0 AS content, c3 AS local_type, c4 AS session_id, c6 AS create_time
FROM %s`, quoteSearchIdentifier(table)))
		}
		return fmt.Sprintf(`
WITH all_message_content AS (
%s
), filtered AS (
SELECT content, local_type, session_id, create_time
FROM all_message_content
WHERE %s
)
SELECT COALESCE((SELECT username FROM name2id WHERE rowid = filtered.session_id), '') AS talker,
       (local_type & 4294967295) AS message_type,
       strftime('%%Y-%%m-%%d', create_time, 'unixepoch', 'localtime') AS day,
       COUNT(*) AS match_count,
       MIN(create_time) AS first_timestamp,
       MAX(create_time) AS last_timestamp
FROM filtered
GROUP BY session_id, message_type, day`,
			strings.Join(union, "\nUNION ALL\n"),
			strings.Join(conditions, " AND "),
		)
	}

	native := isHanSearchPhrase(request.Keyword)
	rows, err := ds.client.QueryWithLimitContext(ctx, "message", file, buildQuery(native), 0)
	if err != nil && native {
		native = false
		rows, err = ds.client.QueryWithLimitContext(ctx, "message", file, buildQuery(false), 0)
	}
	return rows, native, err
}

type messageAnalyticsAccumulator struct {
	keyword string
	total   int64
	first   int64
	last    int64
	byType  map[string]int64
	byChat  map[string]int64
	byDay   map[string]int64
}

func newMessageAnalyticsAccumulator(keyword string) *messageAnalyticsAccumulator {
	return &messageAnalyticsAccumulator{
		keyword: keyword,
		byType:  make(map[string]int64),
		byChat:  make(map[string]int64),
		byDay:   make(map[string]int64),
	}
}

func (a *messageAnalyticsAccumulator) addGrouped(
	talker string,
	messageType int64,
	day string,
	count int64,
	first int64,
	last int64,
) {
	if count <= 0 {
		return
	}
	a.total += count
	a.byType[strconv.FormatInt(messageType, 10)] += count
	if talker = strings.TrimSpace(talker); talker != "" {
		a.byChat[talker] += count
	}
	if day = strings.TrimSpace(day); day != "" {
		a.byDay[day] += count
	}
	if first > 0 && (a.first == 0 || first < a.first) {
		a.first = first
	}
	if last > a.last {
		a.last = last
	}
}

func (a *messageAnalyticsAccumulator) addMessage(message *model.Message) {
	if message == nil {
		return
	}
	timestamp := message.Time.Unix()
	a.addGrouped(
		message.Talker,
		message.Type,
		message.Time.Format("2006-01-02"),
		1,
		timestamp,
		timestamp,
	)
}

func (a *messageAnalyticsAccumulator) result(path string) model.MessageAnalyticsResult {
	result := model.MessageAnalyticsResult{
		Keyword:          a.keyword,
		Total:            a.total,
		FirstMessageTime: a.first,
		LastMessageTime:  a.last,
		Path:             path,
		ByType:           make([]model.MessageAnalyticsCount, 0, len(a.byType)),
		ByChat:           make([]model.MessageAnalyticsChatCount, 0, len(a.byChat)),
		ByDay:            make([]model.MessageAnalyticsCount, 0, len(a.byDay)),
	}
	for key, count := range a.byType {
		result.ByType = append(result.ByType, model.MessageAnalyticsCount{Key: key, Count: count})
	}
	for username, count := range a.byChat {
		result.ByChat = append(result.ByChat, model.MessageAnalyticsChatCount{UserName: username, Count: count})
	}
	for day, count := range a.byDay {
		result.ByDay = append(result.ByDay, model.MessageAnalyticsCount{Key: day, Count: count})
	}
	sort.Slice(result.ByType, func(i, j int) bool {
		if result.ByType[i].Count == result.ByType[j].Count {
			return result.ByType[i].Key < result.ByType[j].Key
		}
		return result.ByType[i].Count > result.ByType[j].Count
	})
	sort.Slice(result.ByChat, func(i, j int) bool {
		if result.ByChat[i].Count == result.ByChat[j].Count {
			return result.ByChat[i].UserName < result.ByChat[j].UserName
		}
		return result.ByChat[i].Count > result.ByChat[j].Count
	})
	sort.Slice(result.ByDay, func(i, j int) bool {
		return result.ByDay[i].Key < result.ByDay[j].Key
	})
	return result
}
