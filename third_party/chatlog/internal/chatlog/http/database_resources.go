package http

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/pkg/util"
)

func (s *Service) handleMessageResources(c *gin.Context) {
	filters, err := parseMessageResourceFilters(c)
	if err != nil {
		errors.Err(c, err)
		return
	}
	dbs, err := s.db.GetDecryptedDBs()
	if err != nil {
		errors.Err(c, err)
		return
	}
	file, group := findAuditDBFileAnyGroup(dbs, "message_resource.db")
	if file == "" {
		errors.Err(c, fmt.Errorf("message_resource.db not found"))
		return
	}
	query := buildMessageResourceQuery(filters)
	rows, hasMore, err := collectDatabaseRowPage(func(visit databaseRowVisitor) (int64, error) {
		return s.db.StreamSQL(c.Request.Context(), normalizeAuditGroup(group), file, query, visit)
	}, filters.Limit, filters.Offset)
	if err != nil {
		errors.Err(c, err)
		return
	}
	decorateMessageResourceRows(rows)
	c.JSON(http.StatusOK, gin.H{
		"file":     filepath.Base(file),
		"filters":  filters,
		"count":    len(rows),
		"has_more": hasMore,
		"items":    rows,
	})
}

func decorateMessageResourceRows(rows []map[string]interface{}) {
	for _, row := range rows {
		messageType, messageSubType := util.SplitInt64ToTwoInt32(toInt64(row["message_local_type"]))
		resourceType, resourceSubType := splitMessageResourceType(toInt64(row["resource_type"]))
		row["message_type"] = messageType
		row["message_sub_type"] = messageSubType
		row["resource_type_base"] = resourceType
		row["resource_sub_type"] = resourceSubType
		// JSON numbers above 2^53 lose precision in browsers. Return every
		// identity field as a decimal string while keeping type/count fields numeric.
		for _, key := range []string{"message_id", "message_local_id", "message_svr_id", "chat_id", "resource_id"} {
			if value, ok := row[key]; ok && value != nil {
				row[key] = toString(value)
			}
		}
	}
}

func splitMessageResourceType(value int64) (int64, int64) {
	// MessageResourceDetail.type 使用低 16 位表示资源大类，高位表示
	// 具体规格（例如 262145 == base 1 / subtype 4），与消息
	// local_type 的高低 32 位编码不同。
	return value & 0xFFFF, value >> 16
}

type messageResourceFilters struct {
	MessageID      int64 `json:"message_id,omitempty"`
	MessageLocalID int64 `json:"message_local_id,omitempty"`
	MessageSvrID   int64 `json:"message_svr_id,omitempty"`
	ChatID         int64 `json:"chat_id,omitempty"`
	Limit          int   `json:"limit"`
	Offset         int   `json:"offset"`
}

func parseMessageResourceFilters(c *gin.Context) (messageResourceFilters, error) {
	var filters messageResourceFilters
	var err error
	if filters.MessageID, err = parseOptionalDatabaseInt64(c.Query("message_id")); err != nil {
		return filters, errors.InvalidArg("message_id")
	}
	if filters.MessageLocalID, err = parseOptionalDatabaseInt64(c.Query("message_local_id")); err != nil {
		return filters, errors.InvalidArg("message_local_id")
	}
	if filters.MessageSvrID, err = parseOptionalDatabaseInt64(c.Query("message_svr_id")); err != nil {
		return filters, errors.InvalidArg("message_svr_id")
	}
	if filters.ChatID, err = parseOptionalDatabaseInt64(c.Query("chat_id")); err != nil {
		return filters, errors.InvalidArg("chat_id")
	}
	if filters.Limit, err = parseDatabaseQueryInteger(c.Query("limit"), 100, 1, 500); err != nil {
		return filters, errors.InvalidArg("limit")
	}
	if filters.Offset, err = parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 0); err != nil {
		return filters, errors.InvalidArg("offset")
	}
	return filters, nil
}

func parseOptionalDatabaseInt64(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid integer")
	}
	return value, nil
}

func buildMessageResourceQuery(filters messageResourceFilters) string {
	conditions := make([]string, 0, 4)
	if filters.MessageID > 0 {
		conditions = append(conditions, fmt.Sprintf("i.message_id = %d", filters.MessageID))
	}
	if filters.MessageLocalID > 0 {
		conditions = append(conditions, fmt.Sprintf("i.message_local_id = %d", filters.MessageLocalID))
	}
	if filters.MessageSvrID > 0 {
		conditions = append(conditions, fmt.Sprintf("i.message_svr_id = %d", filters.MessageSvrID))
	}
	if filters.ChatID > 0 {
		conditions = append(conditions, fmt.Sprintf("i.chat_id = %d", filters.ChatID))
	}
	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}
	return fmt.Sprintf(`
SELECT i.message_id,
       i.message_local_id,
       i.message_svr_id,
       i.message_local_type,
       i.message_create_time,
       i.chat_id,
       d.resource_id,
       d.type AS resource_type,
       d.status AS resource_status,
       d.size AS resource_size,
       d.data_index,
       LENGTH(d.packed_info) AS packed_info_bytes
FROM MessageResourceInfo i
LEFT JOIN MessageResourceDetail d ON d.message_id = i.message_id
%s
ORDER BY i.message_create_time DESC, i.message_local_id DESC, d.resource_id
`, where)
}
