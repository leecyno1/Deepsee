package http

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// OpenAPIDocument generates an OpenAPI 3.1 contract from the same catalog used
// by the router tests and CLI. It deliberately stays dependency-free.
func OpenAPIDocument(serverURL string) map[string]interface{} {
	paths := map[string]interface{}{}
	tagSet := map[string]struct{}{}
	for _, endpoint := range EndpointCatalog() {
		tagSet[endpoint.Category] = struct{}{}
		pathItem, _ := paths[endpoint.Path].(map[string]interface{})
		if pathItem == nil {
			pathItem = map[string]interface{}{}
			paths[endpoint.Path] = pathItem
		}
		pathItem[strings.ToLower(endpoint.Method)] = openAPIOperation(endpoint)
	}

	tagNames := make([]string, 0, len(tagSet))
	for name := range tagSet {
		tagNames = append(tagNames, name)
	}
	sort.Strings(tagNames)
	tags := make([]map[string]string, 0, len(tagNames))
	for _, name := range tagNames {
		tags = append(tags, map[string]string{"name": name})
	}

	document := map[string]interface{}{
		"openapi": "3.1.0",
		"info": map[string]interface{}{
			"title":       "Chatlog HTTP API",
			"version":     "v1",
			"description": "Local-only Chatlog data and control API. Process lifecycle operations use chatlog ops.",
		},
		"jsonSchemaDialect": "https://json-schema.org/draft/2020-12/schema",
		"paths":             paths,
		"tags":              tags,
		"components": map[string]interface{}{
			"schemas": map[string]interface{}{
				"Error": map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{"error": map[string]string{"type": "string"}},
					"required":   []string{"error"},
				},
			},
		},
	}
	if serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/"); serverURL != "" {
		document["servers"] = []map[string]string{{"url": serverURL}}
	}
	return document
}

func openAPIOperation(endpoint EndpointSpec) map[string]interface{} {
	operation := map[string]interface{}{
		"operationId":                     endpoint.Name,
		"summary":                         endpoint.Summary,
		"tags":                            []string{endpoint.Category},
		"responses":                       openAPIResponses(endpoint.Response),
		"x-chatlog-requires-database":     endpoint.RequiresDatabase,
		"x-chatlog-side-effect":           endpoint.SideEffect,
		"x-chatlog-local-only":            endpoint.LocalOnly,
		"x-chatlog-confirmation-required": endpoint.Confirmation,
		"x-chatlog-response-mode":         endpoint.Response.Mode,
	}
	if len(endpoint.Parameters) > 0 {
		parameters := make([]map[string]interface{}, 0, len(endpoint.Parameters))
		for _, parameter := range endpoint.Parameters {
			parameters = append(parameters, openAPIParameter(parameter))
		}
		operation["parameters"] = parameters
	}
	if endpoint.RequestBody != nil {
		operation["requestBody"] = map[string]interface{}{
			"required": endpoint.RequestBody.Required,
			"content": map[string]interface{}{
				endpoint.RequestBody.ContentType: map[string]interface{}{"schema": endpoint.RequestBody.Schema},
			},
		}
	}
	if endpoint.Response.ControlParameter != "" {
		operation["x-chatlog-response-control-parameter"] = endpoint.Response.ControlParameter
	}
	if len(endpoint.Response.BinaryValues) > 0 {
		operation["x-chatlog-output-required-values"] = endpoint.Response.BinaryValues
	}
	return operation
}

func openAPIParameter(parameter APIParameter) map[string]interface{} {
	schema := map[string]interface{}{"type": openAPIType(parameter.Type)}
	if parameter.Default != "" {
		schema["default"] = typedParameterValue(parameter.Type, parameter.Default)
	}
	if len(parameter.Enum) > 0 {
		schema["enum"] = parameter.Enum
	}
	if parameter.Minimum != nil {
		schema["minimum"] = *parameter.Minimum
	}
	if parameter.Maximum != nil {
		schema["maximum"] = *parameter.Maximum
	}
	if parameter.Example != "" {
		schema["example"] = typedParameterValue(parameter.Type, parameter.Example)
	}
	return map[string]interface{}{
		"name":        parameter.Name,
		"in":          parameter.In,
		"required":    parameter.Required || parameter.In == "path",
		"description": parameter.Description,
		"schema":      schema,
	}
}

func openAPIResponses(response APIResponseSpec) map[string]interface{} {
	content := map[string]interface{}{}
	for _, contentType := range response.ContentTypes {
		schema := map[string]interface{}{"type": "object", "additionalProperties": true}
		if contentType != "application/json" {
			schema = map[string]interface{}{"type": "string"}
			if contentType != "text/csv" && !strings.HasPrefix(contentType, "text/") {
				schema["format"] = "binary"
			}
		}
		content[contentType] = map[string]interface{}{"schema": schema}
	}
	status := response.SuccessStatus
	if status == 0 {
		status = http.StatusOK
	}
	description := strings.TrimSpace(response.Description)
	if description == "" {
		description = http.StatusText(status)
	}
	return map[string]interface{}{
		strconv.Itoa(status): map[string]interface{}{"description": description, "content": content},
		"400":                map[string]interface{}{"description": "Invalid request", "content": errorResponseContent()},
		"403":                map[string]interface{}{"description": "Local access required or operation forbidden", "content": errorResponseContent()},
		"404":                map[string]interface{}{"description": "Resource not found", "content": errorResponseContent()},
		"409":                map[string]interface{}{"description": "Operation conflicts with current state", "content": errorResponseContent()},
		"500":                map[string]interface{}{"description": "Internal processing error", "content": errorResponseContent()},
		"503":                map[string]interface{}{"description": "Database or service not ready", "content": errorResponseContent()},
	}
}

func errorResponseContent() map[string]interface{} {
	return map[string]interface{}{
		"application/json": map[string]interface{}{
			"schema": map[string]string{"$ref": "#/components/schemas/Error"},
		},
	}
}

func openAPIType(parameterType string) string {
	if parameterType == "json" {
		return "object"
	}
	return parameterType
}

func typedParameterValue(parameterType, raw string) interface{} {
	switch parameterType {
	case "integer":
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return value
		}
	case "number":
		if value, err := strconv.ParseFloat(raw, 64); err == nil {
			return value
		}
	case "boolean":
		if value, err := strconv.ParseBool(raw); err == nil {
			return value
		}
	}
	return raw
}

func (s *Service) handleOpenAPI(c *gin.Context) {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	serverURL := fmt.Sprintf("%s://%s", scheme, c.Request.Host)
	c.JSON(http.StatusOK, OpenAPIDocument(serverURL))
}
