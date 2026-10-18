package chatlog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chathttp "github.com/sjzar/chatlog/internal/chatlog/http"
	"github.com/spf13/cobra"
)

func TestRunAPICatalogUsesSharedContract(t *testing.T) {
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := runAPICatalog(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var document struct {
		OK        bool                    `json:"ok"`
		Total     int                     `json:"total"`
		Endpoints []chathttp.EndpointSpec `json:"endpoints"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if !document.OK || document.Total != len(chathttp.EndpointCatalog()) || len(document.Endpoints) != document.Total {
		t.Fatalf("unexpected catalog: %+v", document)
	}
}

func TestRunAPICallReturnsJSONEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sessions" || r.URL.Query().Get("limit") != "12" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"sessions":[]}`))
	}))
	defer server.Close()

	resetAPIFlagsForTest()
	apiAddr = server.URL
	apiParams = []string{"limit=12"}
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := runAPICall(cmd, []string{"sessions"}); err != nil {
		t.Fatal(err)
	}
	var envelope apiEnvelope
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.Endpoint != "sessions" || envelope.Status != http.StatusOK {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
}

func TestRunAPICallIncludesDatabasePagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Chatlog-Row-Limit", "10")
		w.Header().Set("X-Chatlog-Row-Offset", "20")
		w.Header().Set("X-Chatlog-Has-More", "true")
		w.Header().Set("X-Chatlog-Next-Offset", "30")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	resetAPIFlagsForTest()
	apiAddr = server.URL
	apiParams = []string{"group=message", "file=message_0.db", "sql=SELECT 1", "limit=10", "offset=20"}
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := runAPICall(cmd, []string{"database_query"}); err != nil {
		t.Fatal(err)
	}
	var envelope apiEnvelope
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Pagination == nil || envelope.Pagination.Limit != 10 || envelope.Pagination.Offset != 20 ||
		!envelope.Pagination.HasMore || envelope.Pagination.NextOffset != 30 {
		t.Fatalf("pagination = %#v", envelope.Pagination)
	}
}

func TestRunAPICallRejectsMissingPathParameter(t *testing.T) {
	resetAPIFlagsForTest()
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	err := runAPICall(cmd, []string{"image"})
	if err == nil || !strings.Contains(err.Error(), "missing --path-param key") {
		t.Fatalf("unexpected error: %v", err)
	}
	var envelope apiEnvelope
	if json.Unmarshal(output.Bytes(), &envelope) != nil || envelope.OK || envelope.Endpoint != "image" {
		t.Fatalf("unexpected envelope: %s", output.String())
	}
}

func resetAPIFlagsForTest() {
	apiAddr = "127.0.0.1:5030"
	apiParams = nil
	apiHeaders = nil
	apiPathKVs = nil
	apiBody = ""
	apiBodyFile = ""
	apiTimeoutS = 30
	apiOutput = ""
	apiConfirm = false
	apiCatalogCategory = ""
	apiCatalogCompact = false
}
