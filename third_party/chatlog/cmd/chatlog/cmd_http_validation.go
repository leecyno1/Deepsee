package chatlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	chathttp "github.com/sjzar/chatlog/internal/chatlog/http"
)

func validateEndpointInput(endpoint chathttp.EndpointSpec, pathValues map[string]string, queryValues url.Values, body []byte, output string, confirmed bool) error {
	parameters := make(map[string]chathttp.APIParameter, len(endpoint.Parameters))
	for _, parameter := range endpoint.Parameters {
		parameters[parameter.In+":"+parameter.Name] = parameter
	}
	for name := range pathValues {
		if _, ok := parameters["path:"+name]; !ok {
			return fmt.Errorf("unknown path parameter %q", name)
		}
	}
	for name, values := range queryValues {
		parameter, ok := parameters["query:"+name]
		if !ok {
			return fmt.Errorf("unknown query parameter %q; run `chatlog api describe %s`", name, endpoint.Name)
		}
		for _, value := range values {
			if err := validateParameterValue(parameter, value); err != nil {
				return err
			}
		}
	}
	for _, parameter := range endpoint.Parameters {
		switch parameter.In {
		case "path":
			if parameter.Required && strings.TrimSpace(pathValues[parameter.Name]) == "" {
				return fmt.Errorf("missing --path-param %s", parameter.Name)
			}
		case "query":
			if parameter.Required && strings.TrimSpace(queryValues.Get(parameter.Name)) == "" {
				return fmt.Errorf("missing required --param %s=<value>", parameter.Name)
			}
		}
	}
	if err := validateRequestBody(endpoint, body); err != nil {
		return err
	}
	if endpointRequiresOutput(endpoint, queryValues) && strings.TrimSpace(output) == "" {
		return fmt.Errorf("response mode %q requires --output <file>", endpoint.Response.Mode)
	}
	if endpoint.Confirmation && !confirmed {
		return fmt.Errorf("endpoint %s requires explicit --confirm", endpoint.Name)
	}
	return nil
}

func validateParameterValue(parameter chathttp.APIParameter, raw string) error {
	raw = strings.TrimSpace(raw)
	if parameter.Required && raw == "" {
		return fmt.Errorf("parameter %s cannot be empty", parameter.Name)
	}
	if raw == "" {
		return nil
	}
	if len(parameter.Enum) > 0 && !containsFold(parameter.Enum, raw) {
		return fmt.Errorf("parameter %s must be one of %s", parameter.Name, strings.Join(parameter.Enum, ", "))
	}
	switch parameter.Type {
	case "integer":
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("parameter %s must be an integer", parameter.Name)
		}
		if parameter.Minimum != nil && float64(value) < *parameter.Minimum {
			return fmt.Errorf("parameter %s must be >= %s", parameter.Name, formatConstraint(*parameter.Minimum))
		}
		if parameter.Maximum != nil && float64(value) > *parameter.Maximum {
			return fmt.Errorf("parameter %s must be <= %s", parameter.Name, formatConstraint(*parameter.Maximum))
		}
	case "number":
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("parameter %s must be a number", parameter.Name)
		}
		if parameter.Minimum != nil && value < *parameter.Minimum {
			return fmt.Errorf("parameter %s must be >= %s", parameter.Name, formatConstraint(*parameter.Minimum))
		}
		if parameter.Maximum != nil && value > *parameter.Maximum {
			return fmt.Errorf("parameter %s must be <= %s", parameter.Name, formatConstraint(*parameter.Maximum))
		}
	case "boolean":
		if !containsFold([]string{"1", "0", "true", "false", "yes", "no", "y", "n"}, raw) {
			return fmt.Errorf("parameter %s must be a boolean", parameter.Name)
		}
	case "json":
		var value interface{}
		if json.Unmarshal([]byte(raw), &value) != nil {
			return fmt.Errorf("parameter %s must be valid JSON", parameter.Name)
		}
		if _, ok := value.(map[string]interface{}); !ok {
			return fmt.Errorf("parameter %s must be a JSON object", parameter.Name)
		}
	}
	return nil
}

func validateRequestBody(endpoint chathttp.EndpointSpec, body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if endpoint.RequestBody == nil {
		if len(trimmed) > 0 {
			return fmt.Errorf("endpoint %s does not accept a request body", endpoint.Name)
		}
		return nil
	}
	if len(trimmed) == 0 {
		if endpoint.RequestBody.Required {
			return fmt.Errorf("endpoint %s requires --body or --body-file", endpoint.Name)
		}
		return nil
	}
	var value interface{}
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return fmt.Errorf("request body must be valid JSON: %w", err)
	}
	return validateSchemaValue(endpoint.RequestBody.Schema, value, "body")
}

func validateSchemaValue(schema chathttp.APIValueSchema, value interface{}, path string) error {
	switch schema.Type {
	case "object":
		object, ok := value.(map[string]interface{})
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		for _, required := range schema.Required {
			item, exists := object[required]
			if !exists || item == nil || strings.TrimSpace(fmt.Sprint(item)) == "" {
				return fmt.Errorf("%s.%s is required", path, required)
			}
		}
		for name, item := range object {
			property, exists := schema.Properties[name]
			if !exists {
				return fmt.Errorf("unknown field %s.%s", path, name)
			}
			if err := validateSchemaValue(property, item, path+"."+name); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		if len(schema.Enum) > 0 && !containsFold(schema.Enum, text) {
			return fmt.Errorf("%s must be one of %s", path, strings.Join(schema.Enum, ", "))
		}
		if schema.Format == "uri" && strings.TrimSpace(text) != "" {
			parsed, err := url.ParseRequestURI(text)
			if err != nil || parsed.Scheme == "" {
				return fmt.Errorf("%s must be an absolute URI", path)
			}
		}
	case "integer":
		numberValue, ok := value.(float64)
		if !ok || numberValue != float64(int64(numberValue)) {
			return fmt.Errorf("%s must be an integer", path)
		}
		if schema.Minimum != nil && numberValue < *schema.Minimum {
			return fmt.Errorf("%s must be >= %s", path, formatConstraint(*schema.Minimum))
		}
		if schema.Maximum != nil && numberValue > *schema.Maximum {
			return fmt.Errorf("%s must be <= %s", path, formatConstraint(*schema.Maximum))
		}
	case "number":
		numberValue, ok := value.(float64)
		if !ok {
			return fmt.Errorf("%s must be a number", path)
		}
		if schema.Minimum != nil && numberValue < *schema.Minimum {
			return fmt.Errorf("%s must be >= %s", path, formatConstraint(*schema.Minimum))
		}
		if schema.Maximum != nil && numberValue > *schema.Maximum {
			return fmt.Errorf("%s must be <= %s", path, formatConstraint(*schema.Maximum))
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case "array":
		items, ok := value.([]interface{})
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if schema.Items != nil {
			for index, item := range items {
				if err := validateSchemaValue(*schema.Items, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func endpointRequiresOutput(endpoint chathttp.EndpointSpec, queryValues url.Values) bool {
	switch endpoint.Response.Mode {
	case "binary":
		return true
	case "media":
		return strings.TrimSpace(queryValues.Get(endpoint.Response.ControlParameter)) == ""
	case "format":
		return containsFold(endpoint.Response.BinaryValues, strings.TrimSpace(queryValues.Get(endpoint.Response.ControlParameter)))
	default:
		return false
	}
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func formatConstraint(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
