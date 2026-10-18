package wcdb

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sjzar/chatlog/internal/errors"
)

const maxInspectionResultRows = 10_000

func (ds *DataSource) GetDBs() (map[string][]string, error) {
	result := make(map[string][]string)
	seen := map[string]map[string]struct{}{}
	seenFiles := map[string]struct{}{}
	add := func(group, file string) {
		group = strings.TrimSpace(strings.ToLower(group))
		file = strings.TrimSpace(file)
		if group == "" || file == "" {
			return
		}
		fileKey := filepath.Clean(file)
		if _, ok := seenFiles[fileKey]; ok {
			return
		}
		if _, ok := seen[group]; !ok {
			seen[group] = map[string]struct{}{}
		}
		if _, ok := seen[group][file]; ok {
			return
		}
		seen[group][file] = struct{}{}
		seenFiles[fileKey] = struct{}{}
		result[group] = append(result[group], file)
	}

	// 1) 按 all_keys.json 全量展示（仅存在于磁盘的数据库）
	for _, file := range ds.client.ListAllKeyDBs() {
		rel, err := filepath.Rel(ds.dataDir, file)
		if err != nil || strings.HasPrefix(rel, "..") {
			add("misc", file)
			continue
		}
		rel = strings.ReplaceAll(filepath.ToSlash(rel), "\\", "/")
		parts := strings.Split(rel, "/")
		group := "misc"
		if len(parts) > 1 && parts[0] != "" {
			group = parts[0]
		}
		add(group, file)
	}

	// 2) 兜底补充（兼容无 all_keys 或明文可查场景）
	msg, _ := ds.client.ListMessageDBs()
	for _, f := range msg {
		add("message", f)
	}
	media, _ := ds.client.ListMediaDBs()
	for _, f := range media {
		add("media", f)
	}

	session := filepath.Join(ds.dataDir, "session", "session.db")
	if ds.client.CanQueryDB(session) {
		add("session", session)
	}

	contact := filepath.Join(ds.dataDir, "contact", "contact.db")
	if ds.client.CanQueryDB(contact) {
		add("contact", contact)
	}

	sns := filepath.Join(ds.dataDir, "sns", "sns.db")
	if ds.client.CanQueryDB(sns) {
		add("sns", sns)
	}

	for g := range result {
		sort.Strings(result[g])
	}
	return result, nil
}

func normalizeGroupKind(group string) string {
	switch strings.ToLower(group) {
	case "chatroom":
		return "contact"
	default:
		return strings.ToLower(group)
	}
}

func (ds *DataSource) GetTables(group, file string) ([]string, error) {
	group = normalizeGroupKind(group)
	rows, err := ds.client.Query(group, file, `SELECT name, COALESCE(sql, '') AS sql FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	tables := make([]string, 0, len(rows))
	for _, row := range rows {
		if isVirtualTableDefinition(toString(row["sql"])) {
			continue
		}
		if name := toString(row["name"]); name != "" {
			tables = append(tables, name)
		}
	}
	return tables, nil
}

func isVirtualTableDefinition(sql string) bool {
	fields := strings.Fields(strings.ToUpper(strings.TrimSpace(sql)))
	return len(fields) >= 3 && fields[0] == "CREATE" && fields[1] == "VIRTUAL" && fields[2] == "TABLE"
}

func (ds *DataSource) GetTableData(group, file, table string, limit, offset int, keyword string) ([]map[string]interface{}, error) {
	group = normalizeGroupKind(group)
	sql := ds.buildTableDataSQL(group, file, table, keyword)

	// 通用检查接口统一设置结果上限，避免 JSON 分页一次性常驻过多内存。
	switch {
	case limit < 0 || limit > maxInspectionResultRows:
		limit = maxInspectionResultRows
		sql += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	case limit == 0:
		limit = 200
		sql += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	default:
		sql += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
	return ds.client.QueryWithLimit(group, file, sql, maxInspectionResultRows)
}

func (ds *DataSource) buildTableDataSQL(group, file, table, keyword string) string {
	escapedTable := strings.ReplaceAll(table, `"`, `""`)
	sql := fmt.Sprintf(`SELECT * FROM "%s"`, escapedTable)

	// 与原控制台兼容：keyword 不为空时，对该表所有列做 LIKE 过滤。
	if strings.TrimSpace(keyword) != "" {
		infoSQL := fmt.Sprintf(`PRAGMA table_info("%s")`, escapedTable)
		cols, err := ds.client.Query(group, file, infoSQL)
		if err == nil && len(cols) > 0 {
			conds := make([]string, 0, len(cols))
			kw := strings.ReplaceAll(keyword, `'`, `''`)
			for _, col := range cols {
				name := toString(col["name"])
				if name == "" {
					continue
				}
				escapedCol := strings.ReplaceAll(name, `"`, `""`)
				conds = append(conds, fmt.Sprintf(`CAST("%s" AS TEXT) LIKE '%%%s%%'`, escapedCol, kw))
			}
			if len(conds) > 0 {
				sql += " WHERE " + strings.Join(conds, " OR ")
			}
		}
	}
	return sql
}

func (ds *DataSource) ExecuteSQL(group, file, query string) ([]map[string]interface{}, error) {
	group = normalizeGroupKind(group)
	if !isReadOnlySQL(query) {
		return nil, errors.InvalidArg("sql (only read-only SELECT, WITH, EXPLAIN, or PRAGMA queries are allowed)")
	}
	return ds.client.QueryWithLimit(group, file, query, maxInspectionResultRows)
}

func (ds *DataSource) StreamTableData(
	ctx context.Context,
	group string,
	file string,
	table string,
	keyword string,
	visit func(columns []string, values []interface{}) error,
) (int64, error) {
	group = normalizeGroupKind(group)
	query := ds.buildTableDataSQL(group, file, table, keyword)
	return ds.client.StreamQuery(ctx, group, file, query, visit)
}

func (ds *DataSource) StreamSQL(
	ctx context.Context,
	group string,
	file string,
	query string,
	visit func(columns []string, values []interface{}) error,
) (int64, error) {
	group = normalizeGroupKind(group)
	if !isReadOnlySQL(query) {
		return 0, errors.InvalidArg("sql (only read-only SELECT, WITH, EXPLAIN, or PRAGMA queries are allowed)")
	}
	return ds.client.StreamQuery(ctx, group, file, query, visit)
}

func isReadOnlySQL(query string) bool {
	query = strings.TrimSpace(query)
	if !isSingleSQLStatement(query) {
		return false
	}
	keywords := sqlKeywords(query)
	if len(keywords) == 0 {
		return false
	}
	switch keywords[0] {
	case "SELECT", "WITH", "EXPLAIN", "PRAGMA":
		// WITH and EXPLAIN can prefix DML. Reject those at the contract layer;
		// mode=ro + query_only remains the final SQLite enforcement boundary.
		for _, keyword := range keywords[1:] {
			switch keyword {
			case "INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "DROP",
				"ALTER", "ATTACH", "DETACH", "VACUUM", "REINDEX":
				return false
			}
		}
		if keywords[0] == "PRAGMA" && strings.Contains(query, "=") {
			return false
		}
		return true
	default:
		return false
	}
}

func sqlKeywords(query string) []string {
	out := make([]string, 0, 16)
	for i := 0; i < len(query); {
		switch query[i] {
		case '\'', '"', '`':
			quote := query[i]
			i++
			for i < len(query) {
				if query[i] == quote {
					if i+1 < len(query) && query[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case '[':
			i++
			for i < len(query) && query[i] != ']' {
				i++
			}
			if i < len(query) {
				i++
			}
		case '-':
			if i+1 < len(query) && query[i+1] == '-' {
				i += 2
				for i < len(query) && query[i] != '\n' {
					i++
				}
			} else {
				i++
			}
		case '/':
			if i+1 < len(query) && query[i+1] == '*' {
				i += 2
				if end := strings.Index(query[i:], "*/"); end >= 0 {
					i += end + 2
				} else {
					return out
				}
			} else {
				i++
			}
		default:
			start := i
			for i < len(query) && ((query[i] >= 'a' && query[i] <= 'z') ||
				(query[i] >= 'A' && query[i] <= 'Z') || query[i] == '_') {
				i++
			}
			if i > start {
				out = append(out, strings.ToUpper(query[start:i]))
			} else {
				i++
			}
		}
	}
	return out
}

func isSingleSQLStatement(query string) bool {
	var quote byte
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if quote != 0 {
			if ch == quote {
				if i+1 < len(query) && query[i+1] == quote && quote != ']' {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			quote = ch
		case '[':
			quote = ']'
		case '-':
			if i+1 < len(query) && query[i+1] == '-' {
				if end := strings.IndexByte(query[i+2:], '\n'); end >= 0 {
					i += end + 2
				} else {
					return true
				}
			}
		case '/':
			if i+1 < len(query) && query[i+1] == '*' {
				end := strings.Index(query[i+2:], "*/")
				if end < 0 {
					return false
				}
				i += end + 3
			}
		case ';':
			remainder := strings.Trim(strings.TrimSpace(query[i+1:]), ";")
			return remainder == ""
		}
	}
	return quote == 0
}
