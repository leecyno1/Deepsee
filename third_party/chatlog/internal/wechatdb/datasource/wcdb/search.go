package wcdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sjzar/chatlog/internal/model"
	"github.com/sjzar/chatlog/pkg/util/zstd"
)

func (ds *DataSource) SearchAll(keyword string, limit int, deep bool) ([]map[string]interface{}, error) {
	mode := "quick"
	if deep {
		mode = "deep"
	}
	result, err := ds.SearchAllDetailed(context.Background(), model.DatabaseSearchRequest{
		Keyword: keyword,
		Mode:    mode,
		Limit:   limit,
	})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (ds *DataSource) SearchAllDetailed(
	ctx context.Context,
	request model.DatabaseSearchRequest,
) (model.DatabaseSearchResult, error) {
	startedAt := time.Now()
	result := model.DatabaseSearchResult{
		Items: make([]map[string]interface{}, 0),
		Stats: model.DatabaseSearchStats{
			Path:   "schema_scan",
			Errors: make([]model.DatabaseSearchError, 0),
		},
	}
	keyword := strings.TrimSpace(request.Keyword)
	if keyword == "" {
		return result, fmt.Errorf("keyword is empty")
	}
	matchMode, terms, err := normalizeDatabaseSearchTerms(keyword, request.Match)
	if err != nil {
		return result, err
	}
	sortMode := strings.ToLower(strings.TrimSpace(request.Sort))
	if sortMode == "" {
		sortMode = "relevance"
	}
	if sortMode != "relevance" && sortMode != "source" {
		return result, fmt.Errorf("invalid database search sort")
	}
	if len(terms) > 1 && matchMode != "phrase" {
		return ds.searchAllMultiTerm(ctx, request, matchMode, terms, sortMode)
	}
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	if mode == "" {
		mode = "quick"
	}
	if mode != "quick" && mode != "deep" {
		return result, fmt.Errorf("invalid search mode")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := request.Offset
	if offset < 0 {
		offset = 0
	}
	target := limit + offset
	if target > 5000 {
		target = 5000
	}
	deep := mode == "deep"

	dbs, err := ds.GetDBs()
	if err != nil {
		return result, err
	}

	groups := orderedDatabaseSearchGroups(dbs, request.Groups)
	fileFilter := normalizedDatabaseSearchFilter(request.Files)
	type searchFile struct {
		group string
		file  string
	}
	searchFiles := make([]searchFile, 0)
	for _, group := range groups {
		groupFiles := append([]string(nil), dbs[group]...)
		sort.Strings(groupFiles)
		for _, file := range groupFiles {
			if !databaseSearchFileSelected(file, fileFilter) {
				continue
			}
			searchFiles = append(searchFiles, searchFile{group: group, file: file})
		}
	}

	hits := make([]map[string]interface{}, 0, minInt(target, 64))
	processedFTS := make(map[string]struct{})
	for _, item := range searchFiles {
		if err := ctx.Err(); err != nil {
			result.Stats.Partial = true
			break
		}
		if !isFTSDatabaseFile(item.file) {
			continue
		}
		result.Stats.FilesScanned++
		fileHits, tables, searchErrors := ds.searchFTSShadowFile(
			ctx,
			item.group,
			item.file,
			keyword,
			target-len(hits),
		)
		result.Stats.TablesScanned += tables
		result.Stats.FTSTables += tables
		result.Stats.Errors = append(result.Stats.Errors, searchErrors...)
		hits = append(hits, fileHits...)
		processedFTS[item.file] = struct{}{}
		if len(hits) >= target {
			break
		}
	}

	if len(hits) < target {
		for _, item := range searchFiles {
			if err := ctx.Err(); err != nil {
				result.Stats.Partial = true
				break
			}
			if _, ok := processedFTS[item.file]; ok {
				continue
			}
			result.Stats.FilesScanned++
			tables, err := ds.getSearchSchema(item.group, item.file)
			if err != nil {
				result.Stats.Errors = append(result.Stats.Errors, model.DatabaseSearchError{
					Group: item.group,
					File:  filepath.Base(item.file),
					Error: err.Error(),
				})
				continue
			}
			for _, table := range tables {
				if isFTSInternalShadowTable(table.Name) {
					continue
				}
				result.Stats.TablesScanned++
				remaining := target - len(hits)
				if remaining <= 0 {
					break
				}
				var tableHits []map[string]interface{}
				if deep {
					tableHits, err = ds.deepSearchTableContext(ctx, item.group, item.file, table, keyword, remaining)
				} else {
					tableHits, err = ds.searchTableContext(ctx, item.group, item.file, table, keyword, minInt(remaining, 5))
				}
				if err != nil {
					result.Stats.Errors = append(result.Stats.Errors, model.DatabaseSearchError{
						Group: item.group,
						File:  filepath.Base(item.file),
						Table: table.Name,
						Error: err.Error(),
					})
					continue
				}
				hits = append(hits, tableHits...)
			}
			if len(hits) >= target {
				break
			}
		}
	}

	result.Stats.Partial = result.Stats.Partial || len(result.Stats.Errors) > 0
	result.Stats.CandidateCapped = len(hits) >= target
	if result.Stats.FTSTables > 0 {
		result.Stats.Path = "fts_shadow_content_then_schema_scan"
	}
	if offset < len(hits) {
		end := offset + limit
		if end > len(hits) {
			end = len(hits)
		}
		result.Items = hits[offset:end]
	}
	result.Total = len(hits)
	for _, item := range result.Items {
		decorateDatabaseSearchHit(item, terms)
	}
	if sortMode == "relevance" {
		sort.SliceStable(result.Items, func(i, j int) bool {
			return databaseSearchHitBefore(result.Items[i], result.Items[j])
		})
	}
	result.Stats.DurationMS = float64(time.Since(startedAt).Microseconds()) / 1000
	return result, nil
}

func (ds *DataSource) searchFTSShadowFile(
	ctx context.Context,
	group, file, keyword string,
	limit int,
) ([]map[string]interface{}, int, []model.DatabaseSearchError) {
	if limit <= 0 {
		return nil, 0, nil
	}
	rows, err := ds.client.QueryWithLimitContext(ctx, group, file, `
SELECT name
FROM sqlite_master
WHERE type = 'table' AND name LIKE '%_content'
ORDER BY name`, 0)
	if err != nil {
		return nil, 0, []model.DatabaseSearchError{{
			Group: group,
			File:  filepath.Base(file),
			Error: err.Error(),
		}}
	}
	hits := make([]map[string]interface{}, 0, minInt(limit, 32))
	errors := make([]model.DatabaseSearchError, 0)
	scanned := 0
	for _, row := range rows {
		if len(hits) >= limit {
			break
		}
		table := toString(row["name"])
		if table == "" {
			continue
		}
		scanned++
		escaped := strings.ReplaceAll(table, `"`, `""`)
		needle := escapeSearchLiteral(strings.ToLower(keyword))
		query := fmt.Sprintf(
			`SELECT rowid AS "__rowid__", c0 FROM "%s" WHERE INSTR(LOWER(CAST(c0 AS TEXT)), '%s') > 0 LIMIT %d`,
			escaped,
			needle,
			limit-len(hits),
		)
		matches, queryErr := ds.client.QueryWithLimitContext(ctx, group, file, query, limit-len(hits))
		if queryErr != nil {
			errors = append(errors, model.DatabaseSearchError{
				Group: group,
				File:  filepath.Base(file),
				Table: table,
				Error: queryErr.Error(),
			})
			continue
		}
		hits = append(hits, ds.rowsToSearchHits(group, file, table, []string{"c0"}, matches, strings.ToLower(keyword))...)
	}
	return hits, scanned, errors
}

func orderedDatabaseSearchGroups(dbs map[string][]string, requested []string) []string {
	filter := normalizedDatabaseSearchFilter(requested)
	priority := []string{"message", "contact", "favorite", "session", "sns", "hardlink", "bizchat"}
	out := make([]string, 0, len(dbs))
	seen := make(map[string]struct{}, len(dbs))
	appendGroup := func(group string) {
		group = strings.ToLower(strings.TrimSpace(group))
		if group == "" || len(dbs[group]) == 0 {
			return
		}
		if len(filter) > 0 {
			if _, ok := filter[group]; !ok {
				return
			}
		}
		if _, ok := seen[group]; ok {
			return
		}
		seen[group] = struct{}{}
		out = append(out, group)
	}
	for _, group := range priority {
		appendGroup(group)
	}
	remaining := make([]string, 0, len(dbs))
	for group := range dbs {
		remaining = append(remaining, strings.ToLower(group))
	}
	sort.Strings(remaining)
	for _, group := range remaining {
		appendGroup(group)
	}
	return out
}

func normalizedDatabaseSearchFilter(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func databaseSearchFileSelected(file string, filter map[string]struct{}) bool {
	if len(filter) == 0 {
		return true
	}
	for _, value := range []string{
		strings.ToLower(strings.TrimSpace(file)),
		strings.ToLower(filepath.Base(file)),
	} {
		if _, ok := filter[value]; ok {
			return true
		}
	}
	return false
}

func isFTSDatabaseFile(file string) bool {
	return strings.HasSuffix(strings.ToLower(filepath.Base(file)), "_fts.db")
}

func isFTSInternalShadowTable(table string) bool {
	name := strings.ToLower(strings.TrimSpace(table))
	for _, suffix := range []string{"_data", "_idx", "_docsize", "_config", "_content"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func (ds *DataSource) getSearchSchema(group, file string) ([]searchTableMeta, error) {
	group = normalizeGroupKind(group)
	cacheKey := group + "::" + file

	ds.searchSchemaMu.RLock()
	if cached, ok := ds.searchSchema[cacheKey]; ok {
		out := make([]searchTableMeta, len(cached))
		copy(out, cached)
		ds.searchSchemaMu.RUnlock()
		return out, nil
	}
	ds.searchSchemaMu.RUnlock()

	tableNames, err := ds.GetTables(group, file)
	if err != nil {
		return nil, err
	}

	metas := make([]searchTableMeta, 0, len(tableNames))
	for _, tableName := range tableNames {
		if tableName == "" || strings.HasPrefix(strings.ToLower(tableName), "sqlite_") {
			continue
		}
		escapedTable := strings.ReplaceAll(tableName, `"`, `""`)
		infoSQL := fmt.Sprintf(`PRAGMA table_info("%s")`, escapedTable)
		cols, err := ds.client.Query(group, file, infoSQL)
		if err != nil {
			continue
		}
		searchable := make([]string, 0, len(cols))
		allColumns := make([]searchColumnMeta, 0, len(cols))
		deepCandidates := make([]searchColumnMeta, 0, len(cols))
		for _, col := range cols {
			name := toString(col["name"])
			typ := toString(col["type"])
			if name == "" {
				continue
			}
			meta := searchColumnMeta{Name: name, Type: typ}
			allColumns = append(allColumns, meta)
			if isSearchableColumn(name, typ) {
				searchable = append(searchable, name)
			}
			if isDeepSearchCandidate(name, typ) {
				deepCandidates = append(deepCandidates, meta)
			}
		}
		if len(searchable) == 0 && len(deepCandidates) == 0 {
			continue
		}
		metas = append(metas, searchTableMeta{
			Name:          tableName,
			Columns:       searchable,
			AllColumns:    allColumns,
			DeepCandidate: deepCandidates,
		})
	}

	ds.searchSchemaMu.Lock()
	ds.searchSchema[cacheKey] = metas
	ds.searchSchemaMu.Unlock()

	out := make([]searchTableMeta, len(metas))
	copy(out, metas)
	return out, nil
}

func (ds *DataSource) searchTable(group, file string, table searchTableMeta, keyword string, limit int) ([]map[string]interface{}, error) {
	return ds.searchTableContext(context.Background(), group, file, table, keyword, limit)
}

func (ds *DataSource) searchTableContext(
	ctx context.Context,
	group, file string,
	table searchTableMeta,
	keyword string,
	limit int,
) ([]map[string]interface{}, error) {
	if limit <= 0 {
		return nil, nil
	}

	quotedCols := make([]string, 0, len(table.Columns))
	conds := make([]string, 0, len(table.Columns))
	kw := strings.ReplaceAll(strings.ToLower(keyword), `'`, `''`)

	for _, col := range table.Columns {
		escapedCol := strings.ReplaceAll(col, `"`, `""`)
		quoted := fmt.Sprintf(`"%s"`, escapedCol)
		quotedCols = append(quotedCols, quoted)
		conds = append(conds, fmt.Sprintf(`INSTR(LOWER(CAST(%s AS TEXT)), '%s') > 0`, quoted, kw))
	}
	if len(conds) == 0 {
		return nil, nil
	}

	escapedTable := strings.ReplaceAll(table.Name, `"`, `""`)
	sql := fmt.Sprintf(
		`SELECT rowid AS "__rowid__", %s FROM "%s" WHERE %s LIMIT %d`,
		strings.Join(quotedCols, ", "),
		escapedTable,
		strings.Join(conds, " OR "),
		limit,
	)
	rows, err := ds.client.QueryWithLimitContext(ctx, group, file, sql, limit)
	if err != nil {
		return nil, err
	}
	return ds.rowsToSearchHits(group, file, table.Name, table.Columns, rows, strings.ToLower(keyword)), nil
}

func (ds *DataSource) deepSearchTable(group, file string, table searchTableMeta, keyword string, limit int) ([]map[string]interface{}, error) {
	return ds.deepSearchTableContext(context.Background(), group, file, table, keyword, limit)
}

func (ds *DataSource) deepSearchTableContext(
	ctx context.Context,
	group, file string,
	table searchTableMeta,
	keyword string,
	limit int,
) ([]map[string]interface{}, error) {
	if limit <= 0 || len(table.DeepCandidate) == 0 {
		return nil, nil
	}

	escapedTable := strings.ReplaceAll(table.Name, `"`, `""`)
	allCols := make([]string, 0, len(table.AllColumns))
	for _, col := range table.AllColumns {
		escapedCol := strings.ReplaceAll(col.Name, `"`, `""`)
		allCols = append(allCols, fmt.Sprintf(`"%s"`, escapedCol))
	}
	if len(allCols) == 0 {
		return nil, nil
	}

	const batchSize = 200
	out := make([]map[string]interface{}, 0, minInt(limit, 16))
	lastRowID := int64(-1)
	needle := strings.ToLower(keyword)

	for len(out) < limit {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		sql := fmt.Sprintf(
			`SELECT rowid AS "__rowid__", %s FROM "%s" WHERE rowid > %d ORDER BY rowid LIMIT %d`,
			strings.Join(allCols, ", "),
			escapedTable,
			lastRowID,
			batchSize,
		)
		rows, err := ds.client.QueryWithLimitContext(ctx, group, file, sql, batchSize)
		if err != nil {
			return out, err
		}
		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			rowIDVal := extractRowID(row["__rowid__"])
			if id, ok := rowIDVal.(int64); ok {
				lastRowID = id
			}
			matchedRow := make(map[string]interface{}, 4)
			matchedCols := make([]string, 0, 2)
			for _, col := range table.DeepCandidate {
				raw, ok := row[col.Name]
				if !ok {
					continue
				}
				val := extractDeepSearchText(col, raw)
				if val == "" || !strings.Contains(strings.ToLower(val), needle) {
					continue
				}
				matchedRow[col.Name] = val
				matchedCols = append(matchedCols, col.Name)
			}
			if len(matchedCols) == 0 {
				continue
			}
			sort.Strings(matchedCols)
			for _, colName := range matchedCols {
				out = append(out, map[string]interface{}{
					"group":   group,
					"file":    file,
					"db_name": filepath.Base(file),
					"table":   table.Name,
					"column":  colName,
					"row_id":  rowIDVal,
					"preview": matchedRow[colName],
					"row":     matchedRow,
				})
				if len(out) >= limit {
					return out, nil
				}
			}
		}

		if len(rows) < batchSize {
			break
		}
	}

	return out, nil
}

func (ds *DataSource) rowsToSearchHits(group, file, table string, columns []string, rows []map[string]interface{}, needle string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		rowID := extractRowID(row["__rowid__"])
		matchedRow := make(map[string]interface{}, len(columns))
		for _, col := range columns {
			raw, ok := row[col]
			if !ok {
				continue
			}
			val := sanitizeSearchValue(raw)
			if val == "" || !strings.Contains(strings.ToLower(val), needle) {
				continue
			}
			matchedRow[col] = val
		}
		if len(matchedRow) == 0 {
			continue
		}

		cols := make([]string, 0, len(matchedRow))
		for col := range matchedRow {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		for _, col := range cols {
			out = append(out, map[string]interface{}{
				"group":   group,
				"file":    file,
				"db_name": filepath.Base(file),
				"table":   table,
				"column":  col,
				"row_id":  rowID,
				"preview": matchedRow[col],
				"row":     matchedRow,
			})
		}
	}
	return out
}

func isSearchableColumn(name, typ string) bool {
	colName := strings.ToLower(strings.TrimSpace(name))
	colType := strings.ToLower(strings.TrimSpace(typ))
	if colName == "" {
		return false
	}
	if strings.Contains(colType, "blob") || strings.Contains(colType, "binary") {
		return false
	}
	if strings.HasSuffix(colName, "_buffer") || strings.HasSuffix(colName, "_blob") {
		return false
	}
	if strings.Contains(colName, "packed_info") || strings.Contains(colName, "ext_buffer") {
		return false
	}
	if strings.Contains(colName, "message_content") || strings.Contains(colName, "bytes_extra") {
		return false
	}
	return true
}

func isDeepSearchCandidate(name, typ string) bool {
	colName := strings.ToLower(strings.TrimSpace(name))
	colType := strings.ToLower(strings.TrimSpace(typ))
	if colName == "" {
		return false
	}
	if strings.Contains(colName, "voice_data") || strings.Contains(colName, "thumbdata") {
		return false
	}
	if strings.Contains(colName, "image") && strings.Contains(colType, "blob") {
		return false
	}
	if strings.Contains(colName, "data") && strings.Contains(colType, "blob") &&
		!strings.Contains(colName, "message_content") &&
		!strings.Contains(colName, "packed_info") &&
		!strings.Contains(colName, "bytes_extra") {
		return false
	}
	return true
}

func sanitizeSearchValue(v interface{}) string {
	s := strings.TrimSpace(toString(v))
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > 240 {
		s = string(runes[:240]) + "..."
	}
	return s
}

func extractDeepSearchText(col searchColumnMeta, v interface{}) string {
	name := strings.ToLower(strings.TrimSpace(col.Name))
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return sanitizeSearchValue(extractBytesText(name, t))
	default:
		return sanitizeSearchValue(v)
	}
}

func extractBytesText(colName string, data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if bytes.HasPrefix(data, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		if b, err := zstd.Decompress(data); err == nil {
			return string(b)
		}
	}
	if strings.Contains(colName, "packed_info") {
		if packed := model.ParsePackedInfo(data); packed != nil {
			if b, err := json.Marshal(packed); err == nil {
				return string(b)
			}
		}
	}
	if utf8.Valid(data) {
		return string(data)
	}
	return ""
}

func extractRowID(v interface{}) interface{} {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return t
	case float64:
		return int64(t)
	default:
		s := strings.TrimSpace(toString(v))
		if s == "" {
			return nil
		}
		return s
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (ds *DataSource) Close() error {
	ds.watchMu.Lock()
	watcher := ds.watcher
	ds.watcher = nil
	ds.watchMu.Unlock()
	var watcherErr error
	if watcher != nil {
		watcherErr = watcher.Close()
		ds.watchWG.Wait()
	}
	if ds.client != nil {
		if err := ds.client.Close(); watcherErr == nil {
			watcherErr = err
		}
	}
	return watcherErr
}
