package wcdb

import (
	"context"
	"fmt"
	"strconv"
)

func (ds *DataSource) GetSNSTimeline(ctx context.Context, username string, limit, offset int) ([]map[string]interface{}, error) {
	_ = ctx
	sql := `SELECT tid, user_name, content, pack_info_buf FROM SnsTimeLine`
	if username != "" {
		sql += ` WHERE user_name = ` + strconv.Quote(username)
	}
	sql += ` ORDER BY tid DESC`
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
		if offset > 0 {
			sql += fmt.Sprintf(" OFFSET %d", offset)
		}
	}
	return ds.client.Query("sns", "", sql)
}

func (ds *DataSource) GetSNSCount(ctx context.Context, username string) (int, error) {
	_ = ctx
	sql := `SELECT COUNT(*) AS cnt FROM SnsTimeLine`
	if username != "" {
		sql += ` WHERE user_name = ` + strconv.Quote(username)
	}
	rows, err := ds.client.Query("sns", "", sql)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	return int(toInt64(rows[0]["cnt"])), nil
}
