package http

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
)

type auxiliaryDatasetSpec struct {
	ID            string
	Label         string
	Description   string
	Group         string
	File          string
	Table         string
	SelectColumns string
	SearchColumns []string
	OrderBy       string
}

func (s *Service) handleAuxiliaryDatasets(c *gin.Context) {
	dbs, err := s.db.GetDecryptedDBs()
	if err != nil {
		errors.Err(c, err)
		return
	}
	available := availableAuxiliaryFiles(dbs)
	tablesByFile := make(map[string]map[string]struct{})
	for _, spec := range auxiliaryDatasetSpecs() {
		key := spec.Group + "\x00" + spec.File
		file := available[key]
		if file == "" {
			continue
		}
		if _, exists := tablesByFile[key]; exists {
			continue
		}
		tables, tableErr := s.db.GetTables(spec.Group, file)
		if tableErr != nil {
			tablesByFile[key] = map[string]struct{}{}
			continue
		}
		tableSet := make(map[string]struct{}, len(tables))
		for _, table := range tables {
			tableSet[strings.ToLower(strings.TrimSpace(table))] = struct{}{}
		}
		tablesByFile[key] = tableSet
	}
	datasets := make([]gin.H, 0, len(auxiliaryDatasetSpecs()))
	ready := 0
	for _, spec := range auxiliaryDatasetSpecs() {
		key := spec.Group + "\x00" + spec.File
		file := available[key]
		status := "missing_file"
		reason := "数据库文件尚未发现"
		if file != "" {
			status = "missing_table"
			reason = "数据库中尚未发现目标数据表"
		}
		if _, exists := tablesByFile[key][strings.ToLower(spec.Table)]; file != "" && exists {
			status = "ready"
			reason = ""
			ready++
		}
		datasets = append(datasets, gin.H{
			"id":          spec.ID,
			"label":       spec.Label,
			"description": spec.Description,
			"status":      status,
			"group":       spec.Group,
			"file":        spec.File,
			"path":        file,
			"table":       spec.Table,
			"reason":      reason,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"datasets": datasets,
		"summary": gin.H{
			"total":   len(datasets),
			"ready":   ready,
			"missing": len(datasets) - ready,
		},
	})
}

func (s *Service) handleAuxiliaryDataset(c *gin.Context) {
	id := strings.ToLower(strings.TrimSpace(c.Param("dataset")))
	spec, ok := auxiliaryDatasetSpecByID(id)
	if !ok {
		errors.Err(c, errors.InvalidArg("dataset"))
		return
	}
	limit, err := parseDatabaseQueryInteger(c.Query("limit"), 50, 1, 500)
	if err != nil {
		errors.Err(c, errors.InvalidArg("limit"))
		return
	}
	offset, err := parseDatabaseQueryInteger(c.Query("offset"), 0, 0, 100_000)
	if err != nil {
		errors.Err(c, errors.InvalidArg("offset"))
		return
	}
	dbs, err := s.db.GetDecryptedDBs()
	if err != nil {
		errors.Err(c, err)
		return
	}
	file := availableAuxiliaryFiles(dbs)[spec.Group+"\x00"+spec.File]
	if file == "" || !auxiliaryTableExists(s.db, spec.Group, file, spec.Table) {
		status := "missing_file"
		reason := "数据库文件尚未发现"
		if file != "" {
			status = "missing_table"
			reason = "数据库中尚未发现目标数据表"
		}
		c.JSON(http.StatusOK, gin.H{
			"dataset": spec.ID,
			"label":   spec.Label,
			"status":  status,
			"reason":  reason,
			"source": gin.H{
				"group": spec.Group, "file": file, "expected_file": spec.File, "table": spec.Table,
			},
			"count": 0,
			"total": 0,
			"items": []map[string]interface{}{},
		})
		return
	}

	where := auxiliarySearchWhere(spec.SearchColumns, c.Query("query"))
	countRows, err := s.db.ExecuteSQL(
		spec.Group,
		file,
		fmt.Sprintf(`SELECT COUNT(*) AS total FROM %s%s`, quoteAuxiliaryIdentifier(spec.Table), where),
	)
	if err != nil {
		errors.Err(c, err)
		return
	}
	total := 0
	if len(countRows) > 0 {
		total = int(toInt64(countRows[0]["total"]))
	}
	query := fmt.Sprintf(
		`SELECT %s FROM %s%s ORDER BY %s LIMIT %d OFFSET %d`,
		spec.SelectColumns,
		quoteAuxiliaryIdentifier(spec.Table),
		where,
		spec.OrderBy,
		limit,
		offset,
	)
	items, err := s.db.ExecuteSQL(spec.Group, file, query)
	if err != nil {
		errors.Err(c, err)
		return
	}
	c.Set(databaseQueryGroupContextKey, spec.Group)
	c.Set(databaseQueryTableContextKey, spec.Table)
	c.Set(databaseQueryRowsContextKey, len(items))
	c.JSON(http.StatusOK, gin.H{
		"dataset":  spec.ID,
		"label":    spec.Label,
		"status":   "ready",
		"source":   gin.H{"group": spec.Group, "file": file, "table": spec.Table},
		"query":    strings.TrimSpace(c.Query("query")),
		"count":    len(items),
		"total":    total,
		"limit":    limit,
		"offset":   offset,
		"has_more": offset+len(items) < total,
		"items":    items,
	})
}

func availableAuxiliaryFiles(dbs map[string][]string) map[string]string {
	out := make(map[string]string)
	for group, files := range dbs {
		group = strings.ToLower(strings.TrimSpace(group))
		for _, file := range files {
			key := group + "\x00" + strings.ToLower(filepath.Base(file))
			if _, exists := out[key]; !exists {
				out[key] = file
			}
		}
	}
	return out
}

func auxiliaryTableExists(db Database, group, file, table string) bool {
	tables, err := db.GetTables(group, file)
	if err != nil {
		return false
	}
	for _, candidate := range tables {
		if strings.EqualFold(candidate, table) {
			return true
		}
	}
	return false
}

func auxiliarySearchWhere(columns []string, keyword string) string {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" || len(columns) == 0 {
		return ""
	}
	keyword = strings.ReplaceAll(keyword, "'", "''")
	conditions := make([]string, 0, len(columns))
	for _, column := range columns {
		conditions = append(conditions, fmt.Sprintf(
			`INSTR(LOWER(CAST(%s AS TEXT)), '%s') > 0`,
			quoteAuxiliaryIdentifier(column),
			keyword,
		))
	}
	return " WHERE " + strings.Join(conditions, " OR ")
}

func quoteAuxiliaryIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func auxiliaryDatasetSpecByID(id string) (auxiliaryDatasetSpec, bool) {
	for _, spec := range auxiliaryDatasetSpecs() {
		if spec.ID == id {
			return spec, true
		}
	}
	return auxiliaryDatasetSpec{}, false
}

func auxiliaryDatasetSpecs() []auxiliaryDatasetSpec {
	specs := []auxiliaryDatasetSpec{
		{
			ID: "avatars", Label: "头像索引", Description: "联系人头像哈希、更新时间与缓存体积。",
			Group: "head_image", File: "head_image.db", Table: "head_image",
			SelectColumns: `username, md5, update_time, LENGTH(image_buffer) AS image_bytes`,
			SearchColumns: []string{"username", "md5"}, OrderBy: `update_time DESC, username ASC`,
		},
		{
			ID: "emoticon_packages", Label: "表情包", Description: "已安装表情包及作者、介绍和排序信息。",
			Group: "emoticon", File: "emoticon.db", Table: "kStoreEmoticonPackageTable",
			SelectColumns: `package_id_, package_name_, author_, introduction_, download_status_, install_time_, sort_order_, store_icon_url_, panel_url_`,
			SearchColumns: []string{"package_id_", "package_name_", "author_", "introduction_"}, OrderBy: `sort_order_ ASC, install_time_ DESC`,
		},
		{
			ID: "emoticon_captions", Label: "表情说明", Description: "表情哈希、语言和文字说明。",
			Group: "emoticon", File: "emoticon.db", Table: "kStoreEmoticonCaptionsTable",
			SelectColumns: `package_id_, md5_, language_, caption_`,
			SearchColumns: []string{"package_id_", "md5_", "language_", "caption_"}, OrderBy: `package_id_ ASC, md5_ ASC`,
		},
		{
			ID: "recent_searches", Label: "最近搜索", Description: "最近搜索对象、关键词、评分和点击时间。",
			Group: "general", File: "general.db", Table: "SearchRecent",
			SelectColumns: `username, query, score, last_click_time`,
			SearchColumns: []string{"username", "query"}, OrderBy: `last_click_time DESC, score DESC`,
		},
		{
			ID: "recent_forwards", Label: "最近转发", Description: "最近使用的转发目标和时间。",
			Group: "general", File: "general.db", Table: "ForwardRecent",
			SelectColumns: `username, forward_time`,
			SearchColumns: []string{"username"}, OrderBy: `forward_time DESC`,
		},
		{
			ID: "friend_requests", Label: "好友请求", Description: "好友请求来源、备注、标签和验证内容。",
			Group: "general", File: "general.db", Table: "FMessageTable",
			SelectColumns: `user_name_, type_, timestamp_, encrypt_user_name_, content_, is_sender_, scene_, remark_, label_ids_`,
			SearchColumns: []string{"user_name_", "encrypt_user_name_", "content_", "remark_", "label_ids_"}, OrderBy: `timestamp_ DESC`,
		},
		{
			ID: "transfers", Label: "转账记录", Description: "转账消息关联、付款角色、状态和时间。",
			Group: "general", File: "general.db", Table: "transferTable",
			SelectColumns: `transfer_id, transcation_id, message_server_id, second_message_server_id, session_name, pay_sub_type, pay_receiver, pay_payer, begin_transfer_time, last_modified_time, invalid_time, delay_confirm_flag`,
			SearchColumns: []string{"transfer_id", "transcation_id", "session_name", "pay_receiver", "pay_payer"}, OrderBy: `last_modified_time DESC`,
		},
		{
			ID: "red_envelopes", Label: "红包记录", Description: "红包消息关联、发送者、类型及领取状态。",
			Group: "general", File: "general.db", Table: "redEnvelopeTable",
			SelectColumns: `message_server_id, session_name, sender_user_name, send_id, scene_id, hb_status, hb_type, receive_status`,
			SearchColumns: []string{"session_name", "sender_user_name", "send_id"}, OrderBy: `message_server_id DESC`,
		},
		{
			ID: "revoked_batches", Label: "批量撤回", Description: "批量撤回事件与原消息定位字段。",
			Group: "general", File: "general.db", Table: "revokebatchmessage",
			SelectColumns: `local_id, batch_id, msg_unique_id, session_name, msg_local_id, msg_create_time`,
			SearchColumns: []string{"msg_unique_id", "session_name"}, OrderBy: `msg_create_time DESC, local_id DESC`,
		},
		{
			ID: "mini_apps", Label: "小程序联系人", Description: "小程序账号、应用标识和头像状态。",
			Group: "general", File: "general.db", Table: "wacontact",
			SelectColumns: `user_name, type, brand_icon_url, external_info, wx_app_opt, head_image_status, app_id`,
			SearchColumns: []string{"user_name", "external_info", "app_id"}, OrderBy: `user_name ASC`,
		},
		{
			ID: "unsupported_messages", Label: "迁移兼容消息", Description: "微信迁移库保留的未识别消息及原始定位字段。",
			Group: "migrate", File: "unspportmsg.db", Table: "UnsupportMessage",
			SelectColumns: `svr_id, type, sub_type, from_user, to_user, content, create_time, msg_source, sequent_id, msg_status`,
			SearchColumns: []string{"from_user", "to_user", "content", "msg_source"}, OrderBy: `create_time DESC, sequent_id DESC`,
		},
	}
	sort.SliceStable(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs
}
