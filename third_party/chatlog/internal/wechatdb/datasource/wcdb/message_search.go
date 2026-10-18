package wcdb

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/sjzar/chatlog/internal/model"
)

const (
	messageSearchPathFTSShadow   = "fts_shadow_content"
	messageSearchPathMMFTSNative = "mmfts_native_exact"
)

// SearchMessages uses the plaintext content shadow maintained beside WeChat's
// private FTS5 tokenizer. Reading the shadow avoids depending on the
// MMFtsTokenizer extension while retaining the FTS database's decoded text.
func (ds *DataSource) SearchMessages(
	ctx context.Context,
	startTime, endTime time.Time,
	talkers []string,
	keyword string,
	messageType int64,
	limit, offset int,
) ([]*model.Message, int, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, messageSearchPathFTSShadow, err
	}
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, 0, messageSearchPathFTSShadow, fmt.Errorf("keyword is empty")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	target := limit + offset
	if target > 5000 {
		target = 5000
	}
	ftsTalkers, officialTalkers, includeAllOfficial, restrictFTS := splitMessageSearchTalkers(talkers)

	file := filepath.Join(ds.dataDir, "message", "message_fts.db")
	if !ds.client.CanQueryDB(file) {
		return nil, 0, messageSearchPathFTSShadow, fmt.Errorf("message FTS database is not queryable")
	}
	tableRows, err := ds.client.Query("message", file, `
SELECT name
FROM sqlite_master
WHERE type = 'table'
  AND name GLOB 'message_fts_v4_[0-9]*_content'
ORDER BY name`)
	if err != nil {
		return nil, 0, messageSearchPathFTSShadow, err
	}
	tables := make([]string, 0, len(tableRows))
	for _, row := range tableRows {
		name := toString(row["name"])
		if name != "" {
			tables = append(tables, name)
		}
	}
	if len(tables) == 0 {
		return nil, 0, messageSearchPathFTSShadow, fmt.Errorf("message FTS content shadow is missing")
	}
	ftsLatest := ds.messageFTSLatestTimestamp(file, tables)

	ftsMessages := make([]*model.Message, 0, target)
	ftsTotal := 0
	searchPath := messageSearchPathFTSShadow
	if !restrictFTS || len(ftsTalkers) > 0 {
		conditions := []string{
			fmt.Sprintf("INSTR(LOWER(CAST(content AS TEXT)), '%s') > 0", escapeSearchLiteral(strings.ToLower(keyword))),
		}
		if !startTime.IsZero() {
			conditions = append(conditions, fmt.Sprintf("create_time >= %d", startTime.Unix()))
		}
		if !endTime.IsZero() {
			conditions = append(conditions, fmt.Sprintf("create_time <= %d", endTime.Unix()))
		}
		if messageType > 0 {
			conditions = append(conditions, fmt.Sprintf("(local_type & 4294967295) = %d", messageType))
		}
		if len(ftsTalkers) > 0 {
			quoted := make([]string, 0, len(ftsTalkers))
			for _, value := range ftsTalkers {
				quoted = append(quoted, "'"+escapeSearchLiteral(value)+"'")
			}
			conditions = append(conditions, fmt.Sprintf(
				"session_id IN (SELECT rowid FROM name2id WHERE username IN (%s))",
				strings.Join(quoted, ","),
			))
		} else if includeAllOfficial {
			// The public-account shard is not represented in message_fts.db.
			// Keep the two result sets disjoint before their totals are merged.
			conditions = append(conditions,
				"session_id NOT IN (SELECT rowid FROM name2id WHERE username LIKE 'gh\\_%' ESCAPE '\\')")
		}

		buildCTE := func(native bool) string {
			union := make([]string, 0, len(tables))
			for _, table := range tables {
				if native {
					virtualTable := strings.TrimSuffix(table, "_content")
					match := `"` + strings.ReplaceAll(keyword, `"`, `""`) + `"`
					union = append(union, fmt.Sprintf(`
SELECT acontent AS content, message_local_id AS local_id, sort_seq, local_type,
       session_id, sender_id, create_time
FROM %s
WHERE %s MATCH '%s'`,
						quoteSearchIdentifier(virtualTable),
						quoteSearchIdentifier(virtualTable),
						escapeSearchLiteral(match),
					))
					continue
				}
				union = append(union, fmt.Sprintf(`
SELECT c0 AS content, c1 AS local_id, c2 AS sort_seq, c3 AS local_type,
       c4 AS session_id, c5 AS sender_id, c6 AS create_time
FROM %s`, quoteSearchIdentifier(table)))
			}
			return fmt.Sprintf(`
WITH all_message_content AS (
%s
), filtered AS (
SELECT content, local_id, sort_seq, local_type, session_id, sender_id, create_time
FROM all_message_content
WHERE %s
)
`,
				strings.Join(union, "\nUNION ALL\n"),
				strings.Join(conditions, " AND "),
			)
		}
		native := isHanSearchPhrase(keyword)
		cte := buildCTE(native)
		buildQuery := func(value string) string {
			return value + fmt.Sprintf(`
SELECT content, local_id, sort_seq, local_type, create_time,
       COALESCE((SELECT username FROM name2id WHERE rowid = filtered.session_id), '') AS talker,
       COALESCE((SELECT username FROM name2id WHERE rowid = filtered.sender_id), '') AS sender,
       COUNT(*) OVER() AS total_count
FROM filtered
ORDER BY create_time DESC, sort_seq DESC, local_id DESC
LIMIT %d OFFSET 0`,
				target,
			)
		}
		rows, queryErr := ds.client.QueryWithLimit("message", file, buildQuery(cte), target)
		if queryErr != nil && native {
			native = false
			cte = buildCTE(false)
			rows, queryErr = ds.client.QueryWithLimit("message", file, buildQuery(cte), target)
		}
		if queryErr != nil {
			return nil, 0, messageSearchPathFTSShadow, queryErr
		}
		if native {
			searchPath = messageSearchPathMMFTSNative
		}
		if len(rows) == 0 {
			countRows, countErr := ds.client.QueryWithLimit(
				"message",
				file,
				cte+"SELECT COUNT(*) AS total_count FROM filtered",
				1,
			)
			if countErr == nil && len(countRows) > 0 {
				ftsTotal = int(toInt64(countRows[0]["total_count"]))
			}
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, 0, messageSearchPathFTSShadow, err
			}
			if ftsTotal == 0 {
				ftsTotal = int(toInt64(row["total_count"]))
			}
			talker := strings.TrimSpace(toString(row["talker"]))
			if talker == "" {
				continue
			}
			content := strings.ReplaceAll(toString(row["content"]), "\b", " ")
			messageV4 := model.MessageV4{
				LocalID:        toInt64(row["local_id"]),
				SortSeq:        toInt64(row["sort_seq"]),
				LocalType:      toInt64(row["local_type"]),
				UserName:       toString(row["sender"]),
				CreateTime:     toInt64(row["create_time"]),
				MessageContent: []byte(content),
			}
			if message := messageV4.Wrap(talker); message != nil {
				ftsMessages = append(ftsMessages, message)
			}
		}
	}

	officialMessages, err := ds.searchOfficialMessages(
		ctx,
		startTime,
		endTime,
		officialTalkers,
		includeAllOfficial,
		keyword,
		messageType,
	)
	if err != nil {
		return nil, 0, messageSearchPathFTSShadow, err
	}
	recentMessages, err := ds.searchMessagesNewerThanFTS(
		ctx,
		ftsLatest,
		startTime,
		endTime,
		ftsTalkers,
		restrictFTS,
		keyword,
		messageType,
	)
	if err != nil {
		return nil, 0, messageSearchPathFTSShadow, err
	}
	path := searchPath
	if len(recentMessages) > 0 {
		path += "+recent_message_tables"
	}
	if includeAllOfficial || len(officialTalkers) > 0 {
		path += "+official_message_tables"
	}
	out := make([]*model.Message, 0, len(ftsMessages)+len(recentMessages)+len(officialMessages))
	seen := make(map[string]struct{}, len(ftsMessages)+len(recentMessages)+len(officialMessages))
	appendUnique := func(messages []*model.Message) {
		for _, message := range messages {
			if message == nil {
				continue
			}
			key := fmt.Sprintf("%s:%d", message.Talker, message.Seq)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, message)
		}
	}
	appendUnique(ftsMessages)
	appendUnique(recentMessages)
	appendUnique(officialMessages)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Time.Equal(out[j].Time) {
			return out[i].DBLocalID > out[j].DBLocalID
		}
		return out[i].Time.After(out[j].Time)
	})
	total := ftsTotal + len(recentMessages) + len(officialMessages)
	if offset >= len(out) {
		return []*model.Message{}, total, path, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], total, path, nil
}

// SearchMessagesAdvanced layers multi-term matching, relevance ordering and a
// stable tuple cursor over the optimized FTS/ordinary/official merged search.
// The legacy phrase/time path stays on the single-query fast path.
func (ds *DataSource) SearchMessagesAdvanced(
	ctx context.Context,
	request model.MessageSearchRequest,
) (model.MessageSearchResult, error) {
	result := model.MessageSearchResult{
		Messages: make([]*model.Message, 0),
		Path:     messageSearchPathFTSShadow,
		Scores:   make(map[string]int),
	}
	match, terms, err := normalizeMessageSearchTerms(request.Keyword, request.Match)
	if err != nil {
		return result, err
	}
	sortMode := strings.ToLower(strings.TrimSpace(request.Sort))
	if sortMode == "" {
		sortMode = "time"
	}
	if sortMode != "time" && sortMode != "relevance" {
		return result, fmt.Errorf("invalid message search sort")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	offset := request.Offset
	if offset < 0 {
		offset = 0
	}
	if request.Cursor != nil {
		offset = 0
	}
	result.Terms = append([]string(nil), terms...)
	result.Match = match
	result.Sort = sortMode

	if match == "phrase" && sortMode == "time" && request.Cursor == nil {
		messages, total, path, searchErr := ds.SearchMessages(
			ctx,
			request.StartTime,
			request.EndTime,
			request.Talkers,
			strings.TrimSpace(request.Keyword),
			request.MessageType,
			limit,
			offset,
		)
		result.Messages, result.Total, result.Path = messages, total, path
		for _, message := range messages {
			result.Scores[model.MessageSearchResultKey(message)] = 1
		}
		return result, searchErr
	}

	type candidate struct {
		message *model.Message
		hits    int
	}
	candidates := make(map[string]*candidate)
	pathParts := make(map[string]struct{})
	successfulTerms := 0
	singleTermTotal := 0
	for _, term := range terms {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		messages, total, path, searchErr := ds.searchMessageTermCandidates(
			ctx,
			request.StartTime,
			request.EndTime,
			request.Talkers,
			term,
			request.MessageType,
		)
		if searchErr != nil {
			return result, searchErr
		}
		if len(terms) == 1 {
			singleTermTotal = total
		}
		successfulTerms++
		pathParts[path] = struct{}{}
		termSeen := make(map[string]struct{}, len(messages))
		for _, message := range messages {
			key := messageSearchIdentity(message)
			if key == "" {
				continue
			}
			if _, exists := termSeen[key]; exists {
				continue
			}
			termSeen[key] = struct{}{}
			entry := candidates[key]
			if entry == nil {
				entry = &candidate{message: message}
				candidates[key] = entry
			}
			entry.hits++
		}
	}

	messages := make([]*model.Message, 0, len(candidates))
	for _, entry := range candidates {
		if match == "all" && entry.hits != successfulTerms {
			continue
		}
		messages = append(messages, entry.message)
		result.Scores[model.MessageSearchResultKey(entry.message)] = entry.hits
	}
	sort.SliceStable(messages, func(i, j int) bool {
		return messageSearchBefore(messages[i], messages[j], result.Scores, sortMode)
	})
	result.Total = len(messages)
	if len(terms) == 1 && singleTermTotal > result.Total {
		result.Total = singleTermTotal
	}
	if request.Cursor != nil {
		filtered := messages[:0]
		for _, message := range messages {
			if messageSearchAfterCursor(message, result.Scores, sortMode, *request.Cursor) {
				filtered = append(filtered, message)
			}
		}
		messages = filtered
	}
	if offset < len(messages) {
		end := offset + limit
		if end > len(messages) {
			end = len(messages)
		}
		result.Messages = messages[offset:end]
	}
	paths := make([]string, 0, len(pathParts))
	for path := range pathParts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) > 0 {
		result.Path = strings.Join(paths, "|") + "+multi_term_merge"
	}
	return result, nil
}

func (ds *DataSource) searchMessageTermCandidates(
	ctx context.Context,
	startTime, endTime time.Time,
	talkers []string,
	term string,
	messageType int64,
) ([]*model.Message, int, string, error) {
	const (
		pageSize       = 500
		candidateLimit = 5000
	)
	out := make([]*model.Message, 0, pageSize)
	total := 0
	path := messageSearchPathFTSShadow
	for offset := 0; offset < candidateLimit; offset += pageSize {
		page, pageTotal, pagePath, err := ds.SearchMessages(
			ctx, startTime, endTime, talkers, term, messageType, pageSize, offset,
		)
		if err != nil {
			return nil, total, path, err
		}
		if pagePath != "" {
			path = pagePath
		}
		if pageTotal > total {
			total = pageTotal
		}
		out = append(out, page...)
		if len(page) < pageSize || len(out) >= total {
			break
		}
	}
	return out, total, path, nil
}

func normalizeMessageSearchTerms(keyword, match string) (string, []string, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return "", nil, fmt.Errorf("keyword is empty")
	}
	match = strings.ToLower(strings.TrimSpace(match))
	if match == "" {
		match = "phrase"
	}
	if match != "phrase" && match != "all" && match != "any" {
		return "", nil, fmt.Errorf("invalid message search match mode")
	}
	if match == "phrase" {
		return match, []string{keyword}, nil
	}
	fields := strings.Fields(keyword)
	terms := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, term := range fields {
		term = strings.TrimSpace(term)
		key := strings.ToLower(term)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		terms = append(terms, term)
	}
	if len(terms) == 0 {
		return "", nil, fmt.Errorf("message search has no terms")
	}
	if len(terms) > 8 {
		terms = terms[:8]
	}
	return match, terms, nil
}

func messageSearchIdentity(message *model.Message) string {
	if message == nil {
		return ""
	}
	return fmt.Sprintf("%s:%d", message.Talker, message.Seq)
}

func messageSearchBefore(
	left, right *model.Message,
	scores map[string]int,
	sortMode string,
) bool {
	if sortMode == "relevance" {
		leftScore := scores[model.MessageSearchResultKey(left)]
		rightScore := scores[model.MessageSearchResultKey(right)]
		if leftScore != rightScore {
			return leftScore > rightScore
		}
	}
	if !left.Time.Equal(right.Time) {
		return left.Time.After(right.Time)
	}
	if left.Seq != right.Seq {
		return left.Seq > right.Seq
	}
	if left.DBLocalID != right.DBLocalID {
		return left.DBLocalID > right.DBLocalID
	}
	return left.Talker < right.Talker
}

func messageSearchAfterCursor(
	message *model.Message,
	scores map[string]int,
	sortMode string,
	cursor model.MessageSearchCursor,
) bool {
	if message == nil {
		return false
	}
	if sortMode == "relevance" {
		score := scores[model.MessageSearchResultKey(message)]
		if score != cursor.Score {
			return score < cursor.Score
		}
	}
	timestamp := message.Time.Unix()
	if timestamp != cursor.Timestamp {
		return timestamp < cursor.Timestamp
	}
	if message.Seq != cursor.Seq {
		return message.Seq < cursor.Seq
	}
	if message.DBLocalID != cursor.LocalID {
		return message.DBLocalID < cursor.LocalID
	}
	return message.Talker > cursor.Talker
}

func (ds *DataSource) messageFTSLatestTimestamp(file string, tables []string) int64 {
	parts := make([]string, 0, len(tables))
	for _, table := range tables {
		parts = append(parts, fmt.Sprintf(
			"SELECT COALESCE(MAX(c6), 0) AS latest_timestamp FROM %s",
			quoteSearchIdentifier(table),
		))
	}
	if len(parts) == 0 {
		return 0
	}
	rows, err := ds.client.QueryWithLimit(
		"message",
		file,
		"SELECT COALESCE(MAX(latest_timestamp), 0) AS latest_timestamp FROM ("+
			strings.Join(parts, " UNION ALL ")+")",
		1,
	)
	if err != nil || len(rows) == 0 {
		return 0
	}
	return toInt64(rows[0]["latest_timestamp"])
}

func (ds *DataSource) searchMessagesNewerThanFTS(
	ctx context.Context,
	ftsLatest int64,
	startTime, endTime time.Time,
	scopedTalkers []string,
	restricted bool,
	keyword string,
	messageType int64,
) ([]*model.Message, error) {
	sessionRows, err := ds.client.GetSessions()
	if err != nil {
		return []*model.Message{}, nil
	}
	scope := make(map[string]struct{}, len(scopedTalkers))
	for _, talker := range scopedTalkers {
		scope[talker] = struct{}{}
	}
	candidates := make([]string, 0)
	for _, row := range sessionRows {
		talker := strings.TrimSpace(toString(row["username"]))
		if talker == "" || strings.HasPrefix(strings.ToLower(talker), "gh_") {
			continue
		}
		switch strings.ToLower(talker) {
		case "brandsessionholder", "brandprivatemsg@hardcode":
			continue
		}
		if restricted {
			if _, ok := scope[talker]; !ok {
				continue
			}
		}
		if toInt64(row["last_timestamp"]) <= ftsLatest {
			continue
		}
		candidates = append(candidates, talker)
	}
	if len(candidates) == 0 {
		return []*model.Message{}, nil
	}

	since := startTime
	ftsBoundary := time.Unix(ftsLatest+1, 0)
	if ftsLatest > 0 && (since.IsZero() || since.Before(ftsBoundary)) {
		since = ftsBoundary
	}
	if !endTime.IsZero() && !since.IsZero() && since.After(endTime) {
		return []*model.Message{}, nil
	}
	pattern := regexp.QuoteMeta(keyword)
	out := make([]*model.Message, 0)
	for _, talker := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		messages, err := ds.GetMessages(ctx, since, endTime, talker, "", pattern, 0, 0)
		if err != nil {
			return nil, err
		}
		for _, message := range messages {
			if messageType > 0 && message.Type != messageType {
				continue
			}
			out = append(out, message)
		}
	}
	return out, nil
}

func splitMessageSearchTalkers(values []string) (regular, official []string, allOfficial, restrictFTS bool) {
	values = normalizedSearchTalkers(values)
	if len(values) == 0 {
		return nil, nil, true, false
	}
	restrictFTS = true
	for _, value := range values {
		lower := strings.ToLower(value)
		switch {
		case lower == "brandsessionholder", lower == "brandprivatemsg@hardcode":
			allOfficial = true
		case strings.HasPrefix(lower, "gh_"):
			official = append(official, value)
		default:
			regular = append(regular, value)
		}
	}
	return regular, official, allOfficial, restrictFTS
}

func (ds *DataSource) searchOfficialMessages(
	ctx context.Context,
	startTime, endTime time.Time,
	talkers []string,
	all bool,
	keyword string,
	messageType int64,
) ([]*model.Message, error) {
	if all {
		discovered, err := ds.discoverOfficialMessageTalkers()
		if err != nil {
			return nil, err
		}
		talkers = append(talkers, discovered...)
	}
	talkers = normalizedSearchTalkers(talkers)
	if len(talkers) == 0 {
		return []*model.Message{}, nil
	}
	pattern := regexp.QuoteMeta(keyword)
	out := make([]*model.Message, 0)
	for _, talker := range talkers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		messages, err := ds.GetMessages(ctx, startTime, endTime, talker, "", pattern, 0, 0)
		if err != nil {
			return nil, err
		}
		for _, message := range messages {
			if messageType > 0 && message.Type != messageType {
				continue
			}
			out = append(out, message)
		}
	}
	return out, nil
}

func (ds *DataSource) discoverOfficialMessageTalkers() ([]string, error) {
	files, err := ds.client.ListMessageDBs()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0)
	for _, file := range files {
		if !strings.HasPrefix(strings.ToLower(filepath.Base(file)), "biz_message_") {
			continue
		}
		tableRows, err := ds.client.Query("message", file, `
SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'Msg_%'`)
		if err != nil {
			return nil, err
		}
		tableHashes := make(map[string]struct{}, len(tableRows))
		for _, row := range tableRows {
			table := toString(row["name"])
			if strings.HasPrefix(table, "Msg_") {
				tableHashes[strings.TrimPrefix(table, "Msg_")] = struct{}{}
			}
		}
		nameRows, err := ds.client.Query("message", file, `SELECT user_name FROM Name2Id`)
		if err != nil {
			return nil, err
		}
		for _, row := range nameRows {
			username := strings.TrimSpace(toString(row["user_name"]))
			if username == "" {
				continue
			}
			hash := md5.Sum([]byte(username))
			if _, ok := tableHashes[hex.EncodeToString(hash[:])]; ok {
				result = append(result, username)
			}
		}
	}
	return normalizedSearchTalkers(result), nil
}

func quoteSearchIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func escapeSearchLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func isHanSearchPhrase(value string) bool {
	found := false
	for _, character := range value {
		if !unicode.Is(unicode.Han, character) {
			return false
		}
		found = true
	}
	return found
}

func normalizedSearchTalkers(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
