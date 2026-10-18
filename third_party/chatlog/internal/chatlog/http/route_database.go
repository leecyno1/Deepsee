package http

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/internal/model"
)

const maxDatabaseResultRows = 10_000

func (s *Service) initDatabaseRoutes(api *gin.RouterGroup) {
	api.GET("/db", s.handleGetDBs)
	api.GET("/db/modules", s.handleDatabaseModules)
	api.GET("/db/auxiliary", s.handleAuxiliaryDatasets)
	api.GET("/db/auxiliary/:dataset", s.handleAuxiliaryDataset)
	api.GET("/db/search", s.handleSearchAllDBs)
	api.GET("/db/search/consistency", s.handleDatabaseSearchConsistency)
	api.GET("/db/tables", s.handleGetDBTables)
	api.GET("/db/data", s.handleGetDBTableData)
	api.GET("/db/query", s.handleExecuteSQL)
	api.GET("/db/audit", s.handleDatabaseAudit)
	api.GET("/db/message_resources", s.handleMessageResources)
}

func (s *Service) handleGetDBs(c *gin.Context) {
	dbs, err := s.db.GetDecryptedDBs()
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.JSON(http.StatusOK, dbs)
}

func (s *Service) handleSearchAllDBs(c *gin.Context) {
	keyword := strings.TrimSpace(c.Query("keyword"))
	if keyword == "" {
		errors.Err(c, errors.InvalidArg("keyword"))
		return
	}

	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 100, 1, 500)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	offset, err := parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 5000)
	if err != nil {
		errors.Err(c, errors.InvalidArg("offset"))
		return
	}
	timeoutMS, err := parseDatabaseQueryInteger(c.Query("timeout_ms"), 15_000, 100, 60_000)
	if err != nil {
		errors.Err(c, errors.InvalidArg("timeout_ms"))
		return
	}
	mode := strings.ToLower(strings.TrimSpace(c.DefaultQuery("mode", "quick")))
	var deep bool
	switch mode {
	case "quick":
	case "deep":
		deep = true
	default:
		errors.Err(c, errors.InvalidArg("mode"))
		return
	}
	matchMode := strings.ToLower(strings.TrimSpace(c.DefaultQuery("match", "phrase")))
	if matchMode != "phrase" && matchMode != "all" && matchMode != "any" {
		errors.Err(c, errors.InvalidArg("match"))
		return
	}
	sortMode := strings.ToLower(strings.TrimSpace(c.DefaultQuery("sort", "relevance")))
	if sortMode != "relevance" && sortMode != "source" {
		errors.Err(c, errors.InvalidArg("sort"))
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	if searcher, ok := s.db.(DetailedDatabaseSearch); ok {
		result, searchErr := searcher.SearchAllDetailed(ctx, model.DatabaseSearchRequest{
			Keyword: keyword,
			Mode:    mode,
			Limit:   limit,
			Offset:  offset,
			Groups:  databaseSearchQueryValues(c, "group"),
			Files:   databaseSearchQueryValues(c, "file"),
			Match:   matchMode,
			Sort:    sortMode,
		})
		if searchErr != nil {
			errors.Err(c, searchErr)
			return
		}
		c.Set(databaseQueryRowsContextKey, len(result.Items))
		c.Set(databaseQueryPathContextKey, result.Stats.Path)
		c.Set(databaseQueryGroupContextKey, strings.Join(databaseSearchQueryValues(c, "group"), ","))
		c.JSON(http.StatusOK, gin.H{
			"keyword":  keyword,
			"mode":     mode,
			"match":    matchMode,
			"sort":     sortMode,
			"count":    len(result.Items),
			"total":    result.Total,
			"limit":    limit,
			"offset":   offset,
			"has_more": offset+len(result.Items) < result.Total || len(result.Items) == limit,
			"items":    result.Items,
			"stats":    result.Stats,
		})
		return
	}

	items, err := s.db.SearchAll(keyword, limit, deep)
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.Set(databaseQueryRowsContextKey, len(items))
	c.Set(databaseQueryPathContextKey, "legacy_schema_scan")
	c.JSON(http.StatusOK, gin.H{
		"keyword":  keyword,
		"mode":     map[bool]string{true: "deep", false: "quick"}[deep],
		"count":    len(items),
		"total":    len(items),
		"limit":    limit,
		"offset":   offset,
		"has_more": len(items) == limit,
		"items":    items,
	})
}

func databaseSearchQueryValues(c *gin.Context, name string) []string {
	values := append([]string(nil), c.QueryArray(name)...)
	if raw := strings.TrimSpace(c.Query(name)); raw != "" {
		values = append(values, strings.Split(raw, ",")...)
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (s *Service) handleGetDBTables(c *gin.Context) {
	group := strings.TrimSpace(c.Query("group"))
	file := strings.TrimSpace(c.Query("file"))

	if group == "" || file == "" {
		errors.Err(c, errors.InvalidArg("group or file"))
		return
	}

	tables, err := s.db.GetTables(group, file)
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.JSON(http.StatusOK, tables)
}

func (s *Service) handleGetDBTableData(c *gin.Context) {
	group := strings.TrimSpace(c.Query("group"))
	file := strings.TrimSpace(c.Query("file"))
	table := strings.TrimSpace(c.Query("table"))
	keyword := c.Query("keyword")

	if group == "" || file == "" || table == "" {
		errors.Err(c, errors.InvalidArg("group, file or table"))
		return
	}

	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 20, 1, maxDatabaseResultRows)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	offset, err := parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("offset"))
		return
	}
	format, err := parseDatabaseResponseFormat(c.Query("format"))
	if err != nil {
		errors.Err(c, errors.InvalidArg("format"))
		return
	}

	if isDatabaseExportFormat(format) {
		s.exportDatabaseRows(c, format, table, func(visit databaseRowVisitor) (int64, error) {
			return s.db.StreamTableData(c.Request.Context(), group, file, table, keyword, visit)
		})
		return
	}
	c.Header("X-Chatlog-Row-Limit", strconv.Itoa(maxDatabaseResultRows))

	data, err := s.db.GetTableData(group, file, table, limit, offset, keyword)
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.Set(databaseQueryGroupContextKey, group)
	c.Set(databaseQueryTableContextKey, table)
	c.Set(databaseQueryRowsContextKey, len(data))

	c.JSON(http.StatusOK, data)
}

func (s *Service) handleExecuteSQL(c *gin.Context) {
	group := strings.TrimSpace(c.Query("group"))
	file := strings.TrimSpace(c.Query("file"))
	query := strings.TrimSpace(c.Query("sql"))

	if group == "" || file == "" || query == "" {
		errors.Err(c, errors.InvalidArg("group, file or sql"))
		return
	}
	format, err := parseDatabaseResponseFormat(c.Query("format"))
	if err != nil {
		errors.Err(c, errors.InvalidArg("format"))
		return
	}
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), maxDatabaseResultRows, 1, maxDatabaseResultRows)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	offset, err := parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 0)
	if err != nil {
		errors.Err(c, errors.InvalidArg("offset"))
		return
	}

	stream := func(visit databaseRowVisitor) (int64, error) {
		return s.db.StreamSQL(c.Request.Context(), group, file, query, visit)
	}
	if isDatabaseExportFormat(format) {
		s.exportDatabaseRows(c, format, "query_result", stream)
		return
	}

	data, hasMore, err := collectDatabaseRowPage(stream, limit, offset)
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.Set(databaseQueryGroupContextKey, group)
	c.Set(databaseQueryTableContextKey, "sql")
	c.Set(databaseQueryRowsContextKey, len(data))
	c.Header("X-Chatlog-Row-Limit", strconv.Itoa(limit))
	c.Header("X-Chatlog-Row-Offset", strconv.Itoa(offset))
	c.Header("X-Chatlog-Has-More", strconv.FormatBool(hasMore))
	if hasMore {
		c.Header("X-Chatlog-Next-Offset", strconv.Itoa(offset+limit))
	}
	c.JSON(http.StatusOK, data)
}

func parseDatabaseQueryInteger(raw string, fallback, minimum, maximum int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || (maximum > 0 && value > maximum) {
		return 0, fmt.Errorf("invalid integer")
	}
	return value, nil
}

func parseDatabaseResponseFormat(raw string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(raw))
	switch format {
	case "", "json":
		return format, nil
	case "csv", "xlsx", "excel":
		return format, nil
	default:
		return "", fmt.Errorf("invalid response format")
	}
}
