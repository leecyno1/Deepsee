package wcdbapi

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/sjzar/chatlog/internal/wechat/decrypt"
)

func readOnlySQLiteDSN(path string) string {
	if isDirectSQLitePath(path) {
		return directSQLiteDSN(path)
	}
	return fmt.Sprintf("file:%s?mode=ro&_query_only=1", path)
}

func queryRows(dbPath, query string) ([]map[string]interface{}, error) {
	return queryRowsLimited(dbPath, query, 0)
}

func queryRowsLimited(dbPath, query string, maxRows int) ([]map[string]interface{}, error) {
	return queryRowsLimitedContext(context.Background(), dbPath, query, maxRows)
}

func queryRowsLimitedContext(ctx context.Context, dbPath, query string, maxRows int) ([]map[string]interface{}, error) {
	directRelease, err := acquireDirectSQLiteLease(dbPath)
	if err != nil {
		return nil, err
	}
	defer directRelease()
	dsn := readOnlySQLiteDSN(dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA temp_store=MEMORY`); err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	raw := make([]interface{}, len(cols))
	dest := make([]interface{}, len(cols))
	for i := range raw {
		dest[i] = &raw[i]
	}

	out := make([]map[string]interface{}, 0)
	for rows.Next() {
		if maxRows > 0 && len(out) >= maxRows {
			break
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		m := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			v := raw[i]
			switch t := v.(type) {
			case []byte:
				cp := make([]byte, len(t))
				copy(cp, t)
				m[c] = cp
			default:
				m[c] = t
			}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := validateDirectSQLitePath(dbPath); err != nil {
		return nil, err
	}
	return out, nil
}

// streamQueryRows visits one SQLite row at a time. The values slice is reused
// between callbacks and must be consumed before the callback returns.
func streamQueryRows(
	ctx context.Context,
	dbPath string,
	query string,
	visit func(columns []string, values []interface{}) error,
) (int64, error) {
	directRelease, err := acquireDirectSQLiteLease(dbPath)
	if err != nil {
		return 0, err
	}
	defer directRelease()
	dsn := readOnlySQLiteDSN(dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA temp_store=MEMORY`); err != nil {
		return 0, err
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	values := make([]interface{}, len(columns))
	dest := make([]interface{}, len(columns))
	for index := range values {
		dest[index] = &values[index]
	}

	var count int64
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return count, err
		}
		if visit != nil {
			if err := visit(columns, values); err != nil {
				return count, err
			}
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	if err := validateDirectSQLitePath(dbPath); err != nil {
		return count, err
	}
	return count, nil
}

func toInt64(v interface{}) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		n := int64(0)
		fmt.Sscan(strings.TrimSpace(t), &n)
		return n
	default:
		return 0
	}
}

func isReadableSQLite(path string) (bool, error) {
	plain, err := isPlainSQLite(path)
	if err != nil || !plain {
		return false, err
	}
	dsn := readOnlySQLiteDSN(path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	row := db.QueryRow(`SELECT name FROM sqlite_master LIMIT 1`)
	var name string
	if err := row.Scan(&name); err != nil {
		// sqlite_master 为空时也视为可读
		if strings.Contains(strings.ToLower(err.Error()), "no rows") {
			return true, nil
		}
		return false, err
	}
	return true, nil
}

func fileMTimeNS(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.ModTime().UnixNano()
}

func decodeHexKey(hexKey string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("invalid key length: %d", len(b))
	}
	return b, nil
}

func deriveDBEncryptionKey(decryptor decrypt.Decryptor, dbPath string, dataKey []byte) ([]byte, error) {
	file, err := os.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(file, salt); err != nil {
		return nil, err
	}
	encKey, _, err := decryptor.DeriveKeys(dataKey, salt)
	if err != nil {
		return nil, err
	}
	return encKey, nil
}
