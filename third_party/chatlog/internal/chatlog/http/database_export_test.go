package http

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

type databaseStreamFixture struct {
	Database
	stream databaseRowStream
}

func (fixture *databaseStreamFixture) StreamSQL(_ context.Context, _, _, _ string, visit func([]string, []interface{}) error) (int64, error) {
	return fixture.stream(visit)
}

func (fixture *databaseStreamFixture) StreamTableData(_ context.Context, _, _, _, _ string, visit func([]string, []interface{}) error) (int64, error) {
	return fixture.stream(visit)
}

func TestHandleExecuteSQLReturnsCompletePages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{db: &databaseStreamFixture{stream: syntheticDatabaseRows(25)}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/db/query?group=g&file=f&sql=SELECT%20id&limit=10&offset=10", nil)

	service.handleExecuteSQL(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 10 || rows[0]["id"] != float64(11) || rows[9]["id"] != float64(20) {
		t.Fatalf("unexpected JSON page: %#v", rows)
	}
	if recorder.Header().Get("X-Chatlog-Has-More") != "true" || recorder.Header().Get("X-Chatlog-Next-Offset") != "20" {
		t.Fatalf("pagination headers = %#v", recorder.Header())
	}
}

func TestCollectDatabaseRowPagePreservesCompletePagination(t *testing.T) {
	stream := syntheticDatabaseRows(25)
	rows, hasMore, err := collectDatabaseRowPage(stream, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || len(rows) != 10 {
		t.Fatalf("page length = %d, has_more=%v", len(rows), hasMore)
	}
	if rows[0]["id"] != int64(11) || rows[9]["id"] != int64(20) {
		t.Fatalf("unexpected page boundaries: first=%v last=%v", rows[0]["id"], rows[9]["id"])
	}

	last, hasMore, err := collectDatabaseRowPage(stream, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(last) != 5 || last[4]["id"] != int64(25) {
		t.Fatalf("last page = %#v, has_more=%v", last, hasMore)
	}
}

func TestStreamDatabaseCSVExportsBeyondJSONRowLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	const total = maxDatabaseResultRows + 5

	if err := streamDatabaseCSV(ctx, "rows", syntheticDatabaseRows(total)); err != nil {
		t.Fatal(err)
	}
	reader := csv.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != total+1 {
		t.Fatalf("CSV records = %d, want %d including header", len(records), total+1)
	}
	if records[len(records)-1][0] != strconv.Itoa(total) {
		t.Fatalf("last CSV id = %q, want %d", records[len(records)-1][0], total)
	}
}

func TestStreamDatabaseXLSXExportsBeyondJSONRowLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	const total = maxDatabaseResultRows + 1

	if err := streamDatabaseXLSX(ctx, "rows", syntheticDatabaseRows(total)); err != nil {
		t.Fatal(err)
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer workbook.Close()
	rows, err := workbook.Rows("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	lastID := ""
	for rows.Next() {
		values, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		count++
		if count > 1 && len(values) > 0 {
			lastID = values[0]
		}
	}
	if err := rows.Error(); err != nil {
		t.Fatal(err)
	}
	if count != total+1 {
		t.Fatalf("XLSX rows = %d, want %d including header", count, total+1)
	}
	if lastID != strconv.Itoa(total) {
		t.Fatalf("last XLSX id = %q, want %d", lastID, total)
	}
}

func syntheticDatabaseRows(total int) databaseRowStream {
	return func(visit databaseRowVisitor) (int64, error) {
		columns := []string{"id", "value"}
		for index := 1; index <= total; index++ {
			if err := visit(columns, []interface{}{int64(index), "row-" + strconv.Itoa(index)}); err != nil {
				return int64(index - 1), err
			}
		}
		return int64(total), nil
	}
}
