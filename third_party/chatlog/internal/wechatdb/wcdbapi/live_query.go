package wcdbapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const directLiveQueryAttempts = 4

func isRetryableDirectQueryError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errDirectSnapshotChanged) || errors.Is(err, errDirectSnapshotInvalid) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"file is not a database",
		"database disk image is malformed",
		"database schema has changed",
		"direct sqlite snapshot is no longer registered",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func (c *Client) queryLiveRows(src, query string) ([]map[string]interface{}, error) {
	return c.queryLiveRowsLimitedContext(context.Background(), src, query, 0)
}

func (c *Client) queryLiveRowsLimitedContext(
	ctx context.Context,
	src string,
	query string,
	maxRows int,
) ([]map[string]interface{}, error) {
	var lastErr error
	attempts := 0
	for attempt := 0; attempt < directLiveQueryAttempts; attempt++ {
		attempts = attempt + 1
		readPath, err := c.ensureDirectRead(src)
		if err == nil {
			rows, queryErr := queryRowsLimitedContext(ctx, readPath, query, maxRows)
			if queryErr == nil {
				return rows, nil
			}
			err = queryErr
			if isDirectSQLitePath(readPath) && isRetryableDirectQueryError(err) {
				c.invalidateDirectRead(src, readPath)
			}
		}
		lastErr = err
		if !isRetryableDirectQueryError(err) || attempt+1 == directLiveQueryAttempts {
			break
		}
		if err := waitDirectQueryRetry(ctx, time.Duration(10*(1<<attempt))*time.Millisecond); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("live SQLite query failed after %d attempt(s): %w", attempts, lastErr)
}

func waitDirectQueryRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) liveTableExists(src, tableName string) (bool, error) {
	rows, err := c.queryLiveRows(src, fmt.Sprintf(
		"SELECT 1 AS ok FROM sqlite_master WHERE type='table' AND name='%s' LIMIT 1",
		strings.ReplaceAll(tableName, "'", "''"),
	))
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

func (c *Client) liveTableMaxCreateTime(src, tableName string) (int64, error) {
	rows, err := c.queryLiveRows(src, fmt.Sprintf("SELECT MAX(create_time) AS max_ts FROM [%s]", tableName))
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return toInt64(rows[0]["max_ts"]), nil
}
