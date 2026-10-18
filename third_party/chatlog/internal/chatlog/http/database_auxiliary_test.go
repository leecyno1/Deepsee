package http

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type auxiliaryDatabaseFixture struct {
	Database
	root string
}

func (f auxiliaryDatabaseFixture) GetDecryptedDBs() (map[string][]string, error) {
	return map[string][]string{
		"general": {filepath.Join(f.root, "general", "general.db")},
	}, nil
}

func (f auxiliaryDatabaseFixture) GetTables(_, _ string) ([]string, error) {
	return []string{"SearchRecent"}, nil
}

func (f auxiliaryDatabaseFixture) ExecuteSQL(_, _, query string) ([]map[string]interface{}, error) {
	if strings.Contains(query, "COUNT(*)") {
		return []map[string]interface{}{{"total": int64(1)}}, nil
	}
	return []map[string]interface{}{{
		"username":        "fixture-user",
		"query":           "fixture search",
		"score":           int64(7),
		"last_click_time": int64(100),
	}}, nil
}

func TestAuxiliaryDatasetCatalogAndSemanticQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{db: auxiliaryDatabaseFixture{root: t.TempDir()}}

	catalogRecorder := httptest.NewRecorder()
	catalogContext, _ := gin.CreateTestContext(catalogRecorder)
	catalogContext.Request = httptest.NewRequest(http.MethodGet, "/api/v1/db/auxiliary", nil)
	service.handleAuxiliaryDatasets(catalogContext)
	if catalogRecorder.Code != http.StatusOK ||
		!strings.Contains(catalogRecorder.Body.String(), `"recent_searches"`) ||
		!strings.Contains(catalogRecorder.Body.String(), `"ready"`) {
		t.Fatalf("auxiliary catalog status=%d body=%s", catalogRecorder.Code, catalogRecorder.Body.String())
	}

	queryRecorder := httptest.NewRecorder()
	queryContext, _ := gin.CreateTestContext(queryRecorder)
	queryContext.Params = gin.Params{{Key: "dataset", Value: "recent_searches"}}
	queryContext.Request = httptest.NewRequest(
		http.MethodGet,
		"/api/v1/db/auxiliary/recent_searches?query=fixture&limit=10",
		nil,
	)
	service.handleAuxiliaryDataset(queryContext)
	if queryRecorder.Code != http.StatusOK ||
		!strings.Contains(queryRecorder.Body.String(), `"total":1`) ||
		!strings.Contains(queryRecorder.Body.String(), `"fixture search"`) {
		t.Fatalf("auxiliary query status=%d body=%s", queryRecorder.Code, queryRecorder.Body.String())
	}
}

func TestAuxiliaryDatasetSQLUsesBoundedKnownShape(t *testing.T) {
	spec, ok := auxiliaryDatasetSpecByID("unsupported_messages")
	if !ok || spec.Table != "UnsupportMessage" {
		t.Fatalf("unexpected unsupported-message spec: %+v", spec)
	}
	where := auxiliarySearchWhere(spec.SearchColumns, "a'b")
	if !strings.Contains(where, "a''b") || strings.Contains(where, ";") {
		t.Fatalf("unsafe auxiliary where clause: %s", where)
	}
}
