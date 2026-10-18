package chatlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	chathttp "github.com/sjzar/chatlog/internal/chatlog/http"
)

type apiEnvelope struct {
	OK          bool           `json:"ok"`
	Endpoint    string         `json:"endpoint,omitempty"`
	Method      string         `json:"method,omitempty"`
	URL         string         `json:"url,omitempty"`
	Status      int            `json:"status,omitempty"`
	ContentType string         `json:"content_type,omitempty"`
	Data        interface{}    `json:"data,omitempty"`
	Pagination  *apiPagination `json:"pagination,omitempty"`
	SavedTo     string         `json:"saved_to,omitempty"`
	Bytes       int64          `json:"bytes,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type apiPagination struct {
	Limit      int  `json:"limit"`
	Offset     int  `json:"offset"`
	HasMore    bool `json:"has_more"`
	NextOffset int  `json:"next_offset,omitempty"`
}

var (
	apiAddr            string
	apiParams          []string
	apiHeaders         []string
	apiPathKVs         []string
	apiBody            string
	apiBodyFile        string
	apiTimeoutS        int
	apiOutput          string
	apiConfirm         bool
	apiCatalogCategory string
	apiCatalogCompact  bool

	apiCmd = &cobra.Command{
		Use:   "api",
		Short: "面向 LLM/脚本的结构化 HTTP API 客户端",
		Long:  "通过内置机器可读目录调用 chatlog HTTP JSON API；stdout 始终为单个 JSON 对象。",
	}
	apiCatalogCmd = &cobra.Command{
		Use:   "catalog",
		Short: "输出全部 endpoint、参数和响应类型",
		Args:  cobra.NoArgs,
		RunE:  runAPICatalog,
	}
	apiDescribeCmd = &cobra.Command{
		Use:   "describe <endpoint>",
		Short: "输出单个 endpoint 的参数、请求体和响应契约",
		Args:  cobra.ExactArgs(1),
		RunE:  runAPIDescribe,
	}
	apiOpenAPICmd = &cobra.Command{
		Use:   "openapi",
		Short: "输出 OpenAPI 3.1 文档",
		Args:  cobra.NoArgs,
		RunE:  runAPIOpenAPI,
	}
	apiCallCmd = &cobra.Command{
		Use:   "call <endpoint>",
		Short: "按 endpoint 名调用 API",
		Args:  cobra.ExactArgs(1),
		Example: `  chatlog api catalog --compact
  chatlog api describe history
  chatlog api call sessions --param limit=50
  chatlog api call history --param chat=wxid_xxx --param limit=100
  chatlog api call image --path-param key=0123456789abcdef --output ./image.jpg`,
		RunE: runAPICall,
	}
)

func init() {
	rootCmd.AddCommand(apiCmd)
	apiCmd.AddCommand(apiCatalogCmd, apiDescribeCmd, apiOpenAPICmd, apiCallCmd)

	apiCmd.PersistentFlags().StringVarP(&apiAddr, "addr", "a", "127.0.0.1:5030", "HTTP 服务地址，可含 http:// 或 https://")
	apiCmd.PersistentFlags().IntVar(&apiTimeoutS, "timeout", 30, "超时秒数；0 表示不限制")

	apiCallCmd.Flags().StringArrayVarP(&apiParams, "param", "p", nil, "query 参数 key=value，可重复")
	apiCallCmd.Flags().StringArrayVar(&apiHeaders, "header", nil, "HTTP header key=value，可重复")
	apiCallCmd.Flags().StringArrayVar(&apiPathKVs, "path-param", nil, "路径参数 key=value，可重复")
	apiCallCmd.Flags().StringVar(&apiBody, "body", "", "JSON 请求体")
	apiCallCmd.Flags().StringVar(&apiBodyFile, "body-file", "", "JSON 请求体文件")
	apiCallCmd.Flags().StringVarP(&apiOutput, "output", "o", "", "二进制响应输出文件")
	apiCallCmd.Flags().BoolVar(&apiConfirm, "confirm", false, "确认执行契约标记的高影响操作")
	apiCatalogCmd.Flags().StringVar(&apiCatalogCategory, "category", "", "只输出指定分类")
	apiCatalogCmd.Flags().BoolVar(&apiCatalogCompact, "compact", false, "仅输出 endpoint 名、方法、路径和摘要")
}

func runAPICatalog(cmd *cobra.Command, _ []string) error {
	items := chathttp.EndpointCatalog()
	category := strings.TrimSpace(strings.ToLower(apiCatalogCategory))
	if category != "" {
		filtered := make([]chathttp.EndpointSpec, 0)
		for _, endpoint := range items {
			if strings.ToLower(endpoint.Category) == category {
				filtered = append(filtered, endpoint)
			}
		}
		if len(filtered) == 0 {
			err := fmt.Errorf("unknown or empty category %q", category)
			_ = writeCommandJSON(cmd.OutOrStdout(), map[string]interface{}{"ok": false, "error": err.Error()})
			return err
		}
		items = filtered
	}
	var endpoints interface{} = items
	if apiCatalogCompact {
		compact := make([]map[string]interface{}, 0, len(items))
		for _, endpoint := range items {
			compact = append(compact, map[string]interface{}{
				"name": endpoint.Name, "method": endpoint.Method, "path": endpoint.Path,
				"category": endpoint.Category, "summary": endpoint.Summary,
				"side_effect": endpoint.SideEffect, "response_mode": endpoint.Response.Mode,
			})
		}
		endpoints = compact
	}
	return writeCommandJSON(cmd.OutOrStdout(), map[string]interface{}{
		"ok": true, "version": "v1", "total": len(items), "category": category,
		"compact": apiCatalogCompact, "endpoints": endpoints,
	})
}

func runAPIDescribe(cmd *cobra.Command, args []string) error {
	name := strings.TrimSpace(args[0])
	endpoint, ok := chathttp.FindEndpoint(name)
	if !ok {
		err := fmt.Errorf("unknown endpoint %q; run `chatlog api catalog --compact`", name)
		_ = writeCommandJSON(cmd.OutOrStdout(), map[string]interface{}{"ok": false, "endpoint": name, "error": err.Error()})
		return err
	}
	return writeCommandJSON(cmd.OutOrStdout(), map[string]interface{}{"ok": true, "endpoint": endpoint})
}

func runAPIOpenAPI(cmd *cobra.Command, _ []string) error {
	serverURL, err := buildAPIURL(apiAddr, "")
	if err != nil {
		return writeAPIInputError(cmd, "openapi", err.Error())
	}
	return writeCommandJSON(cmd.OutOrStdout(), chathttp.OpenAPIDocument(serverURL))
}

func runAPICall(cmd *cobra.Command, args []string) error {
	endpointName := strings.TrimSpace(args[0])
	endpoint, found := chathttp.FindEndpoint(endpointName)
	if !found {
		err := fmt.Errorf("unknown endpoint %q; run `chatlog api catalog`", endpointName)
		_ = writeCommandJSON(cmd.OutOrStdout(), apiEnvelope{OK: false, Endpoint: endpointName, Error: err.Error()})
		return err
	}

	pathParams, err := parseKVList(apiPathKVs)
	if err != nil {
		return writeAPIInputError(cmd, endpoint.Name, "invalid --path-param: "+err.Error())
	}
	path, err := applyPathTemplate(endpoint.Path, pathParams)
	if err != nil {
		return writeAPIInputError(cmd, endpoint.Name, err.Error())
	}
	queryValues, err := parseKVListToValues(apiParams)
	if err != nil {
		return writeAPIInputError(cmd, endpoint.Name, "invalid --param: "+err.Error())
	}
	if encoded := queryValues.Encode(); encoded != "" {
		path += "?" + encoded
	}
	fullURL, err := buildAPIURL(apiAddr, path)
	if err != nil {
		return writeAPIInputError(cmd, endpoint.Name, err.Error())
	}

	bodyData, err := loadAPIRequestBody(apiBody, apiBodyFile)
	if err != nil {
		return writeAPIInputError(cmd, endpoint.Name, err.Error())
	}
	if err := validateEndpointInput(endpoint, pathParams, queryValues, bodyData, apiOutput, apiConfirm); err != nil {
		return writeAPIInputError(cmd, endpoint.Name, "validation failed: "+err.Error())
	}
	var body io.Reader
	if len(bodyData) > 0 {
		body = bytes.NewReader(bodyData)
	}
	req, err := stdhttp.NewRequest(endpoint.Method, fullURL, body)
	if err != nil {
		return writeAPIInputError(cmd, endpoint.Name, "create request: "+err.Error())
	}
	headers, err := parseKVList(apiHeaders)
	if err != nil {
		return writeAPIInputError(cmd, endpoint.Name, "invalid --header: "+err.Error())
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if req.Body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &stdhttp.Client{}
	if apiTimeoutS > 0 {
		client.Timeout = time.Duration(apiTimeoutS) * time.Second
	}
	resp, err := client.Do(req)
	if err != nil {
		envelope := apiEnvelope{OK: false, Endpoint: endpoint.Name, Method: endpoint.Method, URL: fullURL, Error: err.Error()}
		_ = writeCommandJSON(cmd.OutOrStdout(), envelope)
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	envelope := apiEnvelope{
		OK:          resp.StatusCode >= 200 && resp.StatusCode < 300,
		Endpoint:    endpoint.Name,
		Method:      endpoint.Method,
		URL:         fullURL,
		Status:      resp.StatusCode,
		ContentType: contentType,
		Pagination:  parseAPIPagination(resp.Header),
	}
	if apiOutput != "" {
		outputPath, written, writeErr := writeAPIOutputStream(apiOutput, resp.Body)
		if writeErr != nil {
			envelope.OK = false
			envelope.Error = writeErr.Error()
			_ = writeCommandJSON(cmd.OutOrStdout(), envelope)
			return writeErr
		}
		envelope.SavedTo = outputPath
		envelope.Bytes = written
	} else if endpointRequiresOutput(endpoint, queryValues) {
		err = fmt.Errorf("endpoint %s returned binary data; repeat with --output <file>", endpoint.Name)
		envelope.OK = false
		if resp.ContentLength > 0 {
			envelope.Bytes = resp.ContentLength
		}
		envelope.Error = err.Error()
		_ = writeCommandJSON(cmd.OutOrStdout(), envelope)
		return err
	} else {
		responseBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return writeAPIInputError(cmd, endpoint.Name, "read response: "+readErr.Error())
		}
		if !isStructuredResponse(contentType, responseBody) {
			err = fmt.Errorf("endpoint %s returned binary data; repeat with --output <file>", endpoint.Name)
			envelope.OK = false
			envelope.Bytes = int64(len(responseBody))
			envelope.Error = err.Error()
			_ = writeCommandJSON(cmd.OutOrStdout(), envelope)
			return err
		}
		envelope.Data = decodeAPIResponse(responseBody)
	}
	if !envelope.OK {
		envelope.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		if m, ok := envelope.Data.(map[string]interface{}); ok {
			if message, ok := m["error"].(string); ok && strings.TrimSpace(message) != "" {
				envelope.Error = message
			}
		}
	}
	if err := writeCommandJSON(cmd.OutOrStdout(), envelope); err != nil {
		return err
	}
	if !envelope.OK {
		return fmt.Errorf("%s", envelope.Error)
	}
	return nil
}

func writeAPIInputError(cmd *cobra.Command, endpoint, message string) error {
	err := fmt.Errorf("%s", message)
	_ = writeCommandJSON(cmd.OutOrStdout(), apiEnvelope{OK: false, Endpoint: endpoint, Error: message})
	return err
}

func writeCommandJSON(w io.Writer, value interface{}) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func buildAPIURL(addr, path string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("--addr is required")
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	base, err := url.Parse(addr)
	if err != nil || base.Host == "" {
		return "", fmt.Errorf("invalid --addr %q", addr)
	}
	return strings.TrimRight(base.String(), "/") + path, nil
}

func parseKVList(items []string) (map[string]string, error) {
	out := make(map[string]string, len(items))
	for _, item := range items {
		key, value, err := parseKV(item)
		if err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, nil
}

func parseKVListToValues(items []string) (url.Values, error) {
	out := url.Values{}
	for _, item := range items {
		key, value, err := parseKV(item)
		if err != nil {
			return nil, err
		}
		out.Add(key, value)
	}
	return out, nil
}

func parseKV(raw string) (string, string, error) {
	value := strings.TrimSpace(raw)
	index := strings.IndexByte(value, '=')
	if index <= 0 {
		return "", "", fmt.Errorf("expected key=value, got %q", raw)
	}
	key := strings.TrimSpace(value[:index])
	if key == "" {
		return "", "", fmt.Errorf("empty key in %q", raw)
	}
	return key, strings.TrimSpace(value[index+1:]), nil
}

var unresolvedPathParameter = regexp.MustCompile(`\{[^{}]+\}`)

func applyPathTemplate(path string, values map[string]string) (string, error) {
	result := path
	for key, value := range values {
		result = strings.ReplaceAll(result, "{"+key+"}", url.PathEscape(value))
	}
	if placeholder := unresolvedPathParameter.FindString(result); placeholder != "" {
		return "", fmt.Errorf("missing --path-param %s", strings.Trim(placeholder, "{}"))
	}
	return result, nil
}

func loadAPIRequestBody(raw, file string) ([]byte, error) {
	if strings.TrimSpace(raw) != "" && strings.TrimSpace(file) != "" {
		return nil, fmt.Errorf("--body and --body-file are mutually exclusive")
	}
	var data []byte
	if strings.TrimSpace(file) != "" {
		var err error
		data, err = os.ReadFile(strings.TrimSpace(file))
		if err != nil {
			return nil, fmt.Errorf("read --body-file: %w", err)
		}
	} else if strings.TrimSpace(raw) != "" {
		data = []byte(raw)
	} else {
		return nil, nil
	}
	var value interface{}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("request body must be valid JSON: %w", err)
	}
	return data, nil
}

func decodeAPIResponse(data []byte) interface{} {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var value interface{}
	if json.Unmarshal(data, &value) == nil {
		return value
	}
	return string(data)
}

func isStructuredResponse(contentType string, body []byte) bool {
	if strings.Contains(contentType, "json") || strings.HasPrefix(contentType, "text/") || contentType == "" {
		return true
	}
	trimmed := bytes.TrimSpace(body)
	return json.Valid(trimmed)
}

func writeAPIOutput(target string, data []byte) (string, error) {
	path, _, err := writeAPIOutputStream(target, bytes.NewReader(data))
	return path, err
}

func writeAPIOutputStream(target string, source io.Reader) (string, int64, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", 0, fmt.Errorf("empty output path")
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", 0, fmt.Errorf("resolve output path: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".tmp-*")
	if err != nil {
		return "", 0, fmt.Errorf("create output: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	written, copyErr := io.Copy(tmp, source)
	closeErr := tmp.Close()
	if copyErr != nil {
		return "", written, fmt.Errorf("write output: %w", copyErr)
	}
	if closeErr != nil {
		return "", written, fmt.Errorf("close output: %w", closeErr)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return "", written, fmt.Errorf("set output permissions: %w", err)
	}
	if err := os.Rename(tmpPath, abs); err != nil {
		return "", written, fmt.Errorf("replace output: %w", err)
	}
	return abs, written, nil
}

func parseAPIPagination(header stdhttp.Header) *apiPagination {
	rawLimit := strings.TrimSpace(header.Get("X-Chatlog-Row-Limit"))
	if rawLimit == "" {
		return nil
	}
	limit, err := strconv.Atoi(rawLimit)
	if err != nil {
		return nil
	}
	offset, _ := strconv.Atoi(strings.TrimSpace(header.Get("X-Chatlog-Row-Offset")))
	hasMore, _ := strconv.ParseBool(strings.TrimSpace(header.Get("X-Chatlog-Has-More")))
	nextOffset, _ := strconv.Atoi(strings.TrimSpace(header.Get("X-Chatlog-Next-Offset")))
	return &apiPagination{
		Limit:      limit,
		Offset:     offset,
		HasMore:    hasMore,
		NextOffset: nextOffset,
	}
}
