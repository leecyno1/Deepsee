package http

import (
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestEndpointCatalogMatchesRegisteredRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{router: gin.New()}
	service.initRouter()

	registered := make([]string, 0)
	for _, route := range service.router.Routes() {
		if route.Method == "HEAD" || !isCatalogRoute(route.Path) {
			continue
		}
		registered = append(registered, route.Method+" "+normalizeRouteTemplate(route.Path))
	}
	sort.Strings(registered)

	catalog := EndpointCatalog()
	documented := make([]string, 0, len(catalog))
	seenNames := make(map[string]bool, len(catalog))
	seenRoutes := make(map[string]bool, len(catalog))
	for _, endpoint := range catalog {
		if endpoint.Name == "" || endpoint.Method == "" || endpoint.Path == "" {
			t.Fatalf("incomplete endpoint: %+v", endpoint)
		}
		if seenNames[endpoint.Name] {
			t.Fatalf("duplicate endpoint name %q", endpoint.Name)
		}
		seenNames[endpoint.Name] = true
		key := endpoint.Method + " " + endpoint.Path
		if seenRoutes[key] {
			t.Fatalf("duplicate endpoint route %q", key)
		}
		seenRoutes[key] = true
		documented = append(documented, key)
	}
	sort.Strings(documented)

	if strings.Join(registered, "\n") != strings.Join(documented, "\n") {
		t.Fatalf("catalog/router drift\nregistered:\n%s\n\ndocumented:\n%s", strings.Join(registered, "\n"), strings.Join(documented, "\n"))
	}
}

func TestEndpointCatalogContractConsistency(t *testing.T) {
	validModes := map[string]bool{"json": true, "binary": true, "media": true, "format": true}
	for _, endpoint := range EndpointCatalog() {
		if !validModes[endpoint.Response.Mode] {
			t.Errorf("%s has invalid response mode %q", endpoint.Name, endpoint.Response.Mode)
		}
		if endpoint.Response.SuccessStatus < 200 || endpoint.Response.SuccessStatus > 299 {
			t.Errorf("%s has invalid success status %d", endpoint.Name, endpoint.Response.SuccessStatus)
		}
		parameterSet := map[string]bool{}
		for _, parameter := range endpoint.Parameters {
			key := parameter.In + ":" + parameter.Name
			if parameterSet[key] {
				t.Errorf("%s has duplicate parameter %s", endpoint.Name, key)
			}
			parameterSet[key] = true
			if parameter.In == "path" && !strings.Contains(endpoint.Path, "{"+parameter.Name+"}") {
				t.Errorf("%s path parameter %s has no placeholder", endpoint.Name, parameter.Name)
			}
			if parameter.Minimum != nil && parameter.Maximum != nil && *parameter.Minimum > *parameter.Maximum {
				t.Errorf("%s parameter %s has inverted range", endpoint.Name, parameter.Name)
			}
			if parameter.Type == "integer" && parameter.Default != "" {
				if _, err := strconv.ParseInt(parameter.Default, 10, 64); err != nil {
					t.Errorf("%s parameter %s has invalid integer default", endpoint.Name, parameter.Name)
				}
			}
		}
		if endpoint.Response.ControlParameter != "" && !parameterSet["query:"+endpoint.Response.ControlParameter] {
			t.Errorf("%s response control parameter %s is undocumented", endpoint.Name, endpoint.Response.ControlParameter)
		}
		for _, placeholder := range unresolvedCatalogPathParameters(endpoint.Path) {
			if !parameterSet["path:"+placeholder] {
				t.Errorf("%s path placeholder %s is undocumented", endpoint.Name, placeholder)
			}
		}
	}
}

func TestEndpointCatalogReturnsDeepCopies(t *testing.T) {
	first, _ := FindEndpoint("hook_config_set")
	second, _ := FindEndpoint("hook_config_set")
	first.Parameters = append(first.Parameters, APIParameter{Name: "mutated"})
	property := first.RequestBody.Schema.Properties["notify_mode"]
	property.Enum[0] = "mutated"
	first.RequestBody.Schema.Properties["notify_mode"] = property
	if second.RequestBody.Schema.Properties["notify_mode"].Enum[0] == "mutated" {
		t.Fatal("request body schema leaked mutation into shared catalog")
	}
}

func TestNewMessageStateIsDocumentedAsJSONEncodedString(t *testing.T) {
	endpoint, ok := FindEndpoint("new_messages")
	if !ok {
		t.Fatal("new_messages endpoint missing")
	}
	seen := map[string]bool{}
	for _, parameter := range endpoint.Parameters {
		if parameter.Name != "state" && parameter.Name != "X-Chatlog-State" {
			continue
		}
		seen[parameter.Name] = true
		if parameter.Type != "string" {
			t.Errorf("%s type = %q, want string", parameter.Name, parameter.Type)
		}
		if !strings.Contains(parameter.Description, "JSON") {
			t.Errorf("%s description does not explain JSON encoding", parameter.Name)
		}
	}
	if !seen["state"] || !seen["X-Chatlog-State"] {
		t.Fatalf("state parameters missing: %v", seen)
	}
}

func TestConfigRequestSchemasMatchHandlerJSONFields(t *testing.T) {
	tests := []struct {
		endpoint string
		handler  interface{}
	}{
		{endpoint: "ocr_config_set", handler: ocrConfigRequest{}},
		{endpoint: "hook_config_set", handler: hookConfigReq{}},
		{endpoint: "hook_hermes_weixin_set", handler: hookHermesWeixinReq{}},
		{endpoint: "hook_hermes_qq_set", handler: hookHermesQQReq{}},
	}
	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			endpoint, ok := FindEndpoint(tt.endpoint)
			if !ok || endpoint.RequestBody == nil {
				t.Fatalf("missing request contract for %s", tt.endpoint)
			}
			handlerFields := make([]string, 0)
			typ := reflect.TypeOf(tt.handler)
			for i := 0; i < typ.NumField(); i++ {
				name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
				if name != "" && name != "-" {
					handlerFields = append(handlerFields, name)
				}
			}
			schemaFields := make([]string, 0, len(endpoint.RequestBody.Schema.Properties))
			for name := range endpoint.RequestBody.Schema.Properties {
				schemaFields = append(schemaFields, name)
			}
			sort.Strings(handlerFields)
			sort.Strings(schemaFields)
			if strings.Join(handlerFields, "\n") != strings.Join(schemaFields, "\n") {
				t.Fatalf("catalog/handler JSON field drift\nhandler: %v\ncatalog: %v", handlerFields, schemaFields)
			}
		})
	}
}

func unresolvedCatalogPathParameters(path string) []string {
	matches := regexp.MustCompile(`\{([^{}]+)\}`).FindAllStringSubmatch(path, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match[1])
	}
	return out
}

func isCatalogRoute(path string) bool {
	return path == "/health" || strings.HasPrefix(path, "/api/v1/") || strings.HasPrefix(path, "/image/") || strings.HasPrefix(path, "/video/") || strings.HasPrefix(path, "/file/") || strings.HasPrefix(path, "/voice/") || strings.HasPrefix(path, "/data/")
}

var routeParameterPattern = regexp.MustCompile(`[:*]([A-Za-z0-9_]+)`)

func normalizeRouteTemplate(path string) string {
	return routeParameterPattern.ReplaceAllString(path, `{$1}`)
}
