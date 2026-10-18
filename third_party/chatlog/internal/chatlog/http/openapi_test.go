package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOpenAPIDocumentCoversCatalog(t *testing.T) {
	document := OpenAPIDocument("http://127.0.0.1:5030")
	if document["openapi"] != "3.1.0" {
		t.Fatalf("openapi version = %#v", document["openapi"])
	}
	paths, ok := document["paths"].(map[string]interface{})
	if !ok {
		t.Fatalf("paths type = %T", document["paths"])
	}
	for _, endpoint := range EndpointCatalog() {
		pathItem, ok := paths[endpoint.Path].(map[string]interface{})
		if !ok {
			t.Fatalf("missing path %s", endpoint.Path)
		}
		operation, ok := pathItem[lowerMethod(endpoint.Method)].(map[string]interface{})
		if !ok || operation["operationId"] != endpoint.Name {
			t.Fatalf("missing operation %s %s", endpoint.Method, endpoint.Path)
		}
	}
	if _, err := json.Marshal(document); err != nil {
		t.Fatalf("OpenAPI document is not JSON serializable: %v", err)
	}
}

func TestOpenAPIHTTPRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{router: gin.New()}
	service.initRouter()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	service.router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var document map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document["openapi"] != "3.1.0" {
		t.Fatalf("unexpected document: %s", recorder.Body.String())
	}
}

func TestCatalogContainsStructuredContracts(t *testing.T) {
	hook, ok := FindEndpoint("hook_config_set")
	if !ok || hook.RequestBody == nil || hook.RequestBody.Schema.Type != "object" {
		t.Fatalf("missing hook request schema: %+v", hook.RequestBody)
	}
	if hook.Response.Mode != "json" || !hook.SideEffect {
		t.Fatalf("unexpected hook contract: %+v", hook)
	}
	databaseData, _ := FindEndpoint("database_data")
	if databaseData.Response.Mode != "format" || databaseData.Response.ControlParameter != "format" {
		t.Fatalf("unexpected database response contract: %+v", databaseData.Response)
	}
	qq, _ := FindEndpoint("hook_hermes_qq_set")
	if qq.RequestBody == nil {
		t.Fatal("missing Hermes QQ request schema")
	}
	properties := qq.RequestBody.Schema.Properties
	for _, name := range []string{"hermes_home", "app_id", "client_secret", "home_channel", "home_channel_name"} {
		if _, ok := properties[name]; !ok {
			t.Errorf("Hermes QQ request schema missing %q", name)
		}
	}
	for _, obsolete := range []string{"bot_qq", "sandbox", "home_guild_id", "home_channel_id"} {
		if _, ok := properties[obsolete]; ok {
			t.Errorf("Hermes QQ request schema still exposes obsolete field %q", obsolete)
		}
	}
}

func TestOpenAPIErrorsUseObjectEnvelope(t *testing.T) {
	document := OpenAPIDocument("http://127.0.0.1:5030")
	paths := document["paths"].(map[string]interface{})
	history := paths["/api/v1/history"].(map[string]interface{})["get"].(map[string]interface{})
	responses := history["responses"].(map[string]interface{})
	for _, status := range []string{"400", "403", "404", "409", "500", "503"} {
		response, ok := responses[status].(map[string]interface{})
		if !ok {
			t.Fatalf("history response missing status %s", status)
		}
		content := response["content"].(map[string]interface{})
		jsonContent := content["application/json"].(map[string]interface{})
		schema := jsonContent["schema"].(map[string]string)
		if schema["$ref"] != "#/components/schemas/Error" {
			t.Fatalf("status %s error schema = %+v", status, schema)
		}
	}
}

func lowerMethod(method string) string {
	return strings.ToLower(method)
}
