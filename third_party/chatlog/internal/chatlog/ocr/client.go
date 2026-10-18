package ocr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
)

const (
	maxOCRSourceBytes = 64 << 20
	maxOCRUploadBytes = 10 << 20
	maxOCRPixels      = 50_000_000
)

type Result struct {
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	ContentHash string          `json:"content_hash"`
	Description string          `json:"description"`
	Text        string          `json:"text"`
	Markdown    string          `json:"markdown"`
	Layout      json.RawMessage `json:"layout,omitempty"`
}

type Client struct {
	config conf.OCRConfig
	http   *http.Client
}

func NewClient(config conf.OCRConfig) (*Client, error) {
	config = conf.NormalizeOCRConfig(config)
	if strings.TrimSpace(config.Endpoint) == "" {
		return nil, errors.New("OCR endpoint is required")
	}
	if config.Provider == conf.OCRProviderMaaS && config.APIKey == "" {
		return nil, errors.New("CHATLOG_OCR_API_KEY is required for MaaS OCR")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 8
	transport.MaxIdleConnsPerHost = 4
	transport.IdleConnTimeout = 90 * time.Second
	return &Client{
		config: config,
		http: &http.Client{
			Timeout:   config.Timeout(),
			Transport: transport,
		},
	}, nil
}

func (c *Client) Close() {
	if c == nil || c.http == nil {
		return
	}
	c.http.CloseIdleConnections()
}

func (c *Client) Recognize(ctx context.Context, source []byte, contentType string) (Result, error) {
	if c == nil || c.http == nil {
		return Result{}, errors.New("OCR client is not configured")
	}
	data, mediaType, err := prepareOCRImage(source, contentType)
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(data)
	dataURI := "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)

	var payload any
	if c.config.Provider == conf.OCRProviderMaaS {
		payload = map[string]any{
			"model": c.config.Model,
			"file":  dataURI,
		}
	} else {
		// The GLM-OCR SDK server is the production self-hosted front end for
		// vLLM: it adds PP-DocLayout-V3, region-level parallel OCR and formatting.
		payload = map[string]any{"images": []string{dataURI}}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if c.config.Provider == conf.OCRProviderMaaS {
		request.Header.Set("Authorization", c.config.APIKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("%s OCR request: %w", c.config.Provider, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return Result{}, fmt.Errorf("read OCR response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := extractAPIError(responseBody)
		if detail == "" {
			detail = http.StatusText(response.StatusCode)
		}
		return Result{}, fmt.Errorf("%s OCR HTTP %d: %s", c.config.Provider, response.StatusCode, detail)
	}
	var decoded map[string]any
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return Result{}, fmt.Errorf("decode OCR response: %w", err)
	}
	if message := responseError(decoded); message != "" {
		return Result{}, errors.New(message)
	}
	markdown := firstString(decoded, "markdown_result", "md_results", "content", "text")
	layoutValue := firstValue(decoded, "json_result", "layout_details")
	layout := marshalRaw(layoutValue)
	layoutText := extractLayoutText(layoutValue)
	plainMarkdown := markdownToPlainText(markdown)
	text := mergeOCRText(plainMarkdown, layoutText)
	description := BuildDescription(text, layoutValue)
	return Result{
		Provider:    c.config.Provider,
		Model:       firstNonEmptyString(firstString(decoded, "model"), c.config.Model),
		ContentHash: hex.EncodeToString(sum[:]),
		Description: description,
		Text:        text,
		Markdown:    strings.TrimSpace(markdown),
		Layout:      layout,
	}, nil
}

func prepareOCRImage(source []byte, hintedType string) ([]byte, string, error) {
	if len(source) == 0 {
		return nil, "", errors.New("OCR image is empty")
	}
	if len(source) > maxOCRSourceBytes {
		return nil, "", fmt.Errorf("OCR image exceeds %d bytes", maxOCRSourceBytes)
	}
	mediaType := normalizeImageMediaType(hintedType)
	if mediaType == "" {
		mediaType = normalizeImageMediaType(http.DetectContentType(source))
	}
	config, _, configErr := image.DecodeConfig(bytes.NewReader(source))
	if configErr != nil {
		return nil, "", fmt.Errorf("decode OCR image metadata: %w", configErr)
	}
	if config.Width <= 0 || config.Height <= 0 ||
		int64(config.Width)*int64(config.Height) > maxOCRPixels {
		return nil, "", fmt.Errorf("OCR image dimensions are too large: %dx%d", config.Width, config.Height)
	}
	if mediaType == "image/jpeg" && !completeJPEGEnd(source) {
		return nil, "", fmt.Errorf("%w: JPEG end marker is missing", ErrIncompleteImage)
	}
	// DecodeConfig only reads the image header. WeChat can expose the final .dat
	// path before the download writer has appended the last JPEG scan bytes. In
	// that window DecodeConfig succeeds, while Pillow in GLM-OCR reports a
	// truncated image and the SDK responds with an empty HTTP 200 result. Decode
	// the complete payload here so partial files are reloaded by the worker
	// instead of being persisted as a successful empty OCR record.
	decoded, _, decodeErr := image.Decode(bytes.NewReader(source))
	if decodeErr != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrIncompleteImage, decodeErr)
	}
	if len(source) <= maxOCRUploadBytes && (mediaType == "image/jpeg" || mediaType == "image/png") {
		return source, mediaType, nil
	}

	// MaaS accepts only JPEG/PNG and limits a single image to 10 MB. Oversized
	// or WebP/GIF inputs are converted once with high-quality downscaling.
	target := decoded
	maxDimension := 3200
	for attempt := 0; attempt < 4; attempt++ {
		bounds := target.Bounds()
		width, height := bounds.Dx(), bounds.Dy()
		if width > maxDimension || height > maxDimension {
			scale := float64(maxDimension) / float64(max(width, height))
			nextWidth := max(1, int(float64(width)*scale))
			nextHeight := max(1, int(float64(height)*scale))
			resized := image.NewRGBA(image.Rect(0, 0, nextWidth, nextHeight))
			xdraw.CatmullRom.Scale(resized, resized.Bounds(), target, bounds, draw.Over, nil)
			target = resized
		}
		flattened := image.NewRGBA(target.Bounds())
		draw.Draw(flattened, flattened.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
		draw.Draw(flattened, flattened.Bounds(), target, target.Bounds().Min, draw.Over)
		var encoded bytes.Buffer
		quality := 92 - attempt*5
		if err := jpeg.Encode(&encoded, flattened, &jpeg.Options{Quality: quality}); err != nil {
			return nil, "", fmt.Errorf("encode OCR image: %w", err)
		}
		if encoded.Len() <= maxOCRUploadBytes {
			return encoded.Bytes(), "image/jpeg", nil
		}
		target = flattened
		maxDimension = max(1200, maxDimension*3/4)
	}
	return nil, "", fmt.Errorf("OCR image remains larger than %d bytes after optimization", maxOCRUploadBytes)
}

func completeJPEGEnd(source []byte) bool {
	// WeChat may append a small private trailer after the standard JPEG EOI
	// marker. image.Decode above verifies that the encoded stream itself is
	// complete; locating EOI anywhere in the decoded payload accepts that
	// trailer while still rejecting files observed before EOI was written.
	return bytes.LastIndex(source, []byte{0xff, 0xd9}) >= 0
}

func normalizeImageMediaType(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if parsed, _, err := mime.ParseMediaType(value); err == nil {
		value = parsed
	}
	switch value {
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	case "image/webp":
		return "image/webp"
	case "image/gif":
		return "image/gif"
	default:
		return ""
	}
}

func extractAPIError(body []byte) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		if message := responseError(payload); message != "" {
			return truncateText(message, 1000)
		}
	}
	return truncateText(strings.TrimSpace(string(body)), 1000)
}

func responseError(payload map[string]any) string {
	for _, key := range []string{"error", "message", "msg"} {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed)
			}
		case map[string]any:
			for _, nested := range []string{"message", "msg", "detail"} {
				if text, ok := typed[nested].(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return ""
}

func firstValue(payload map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := payload[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func firstString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func marshalRaw(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if json.Valid([]byte(text)) {
			return json.RawMessage(text)
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) == "null" {
		return nil
	}
	return encoded
}

func extractLayoutText(value any) string {
	parts := make([]string, 0, 16)
	var walk func(any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				child := typed[key]
				lower := strings.ToLower(key)
				if lower == "content" || lower == "text" || lower == "title" || lower == "caption" {
					if text, ok := child.(string); ok {
						text = strings.TrimSpace(text)
						if text != "" {
							parts = append(parts, text)
						}
						continue
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return strings.Join(parts, "\n")
}

var (
	// ErrIncompleteImage marks an image that was observed while its producer was
	// still writing it. Callers may reload the source and retry without sending
	// the partial bytes to the OCR backend.
	ErrIncompleteImage = errors.New("OCR image is incomplete")

	markdownImagePattern = regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)
	markdownLinkPattern  = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	markdownMarkup       = regexp.MustCompile(`(?m)^[[:space:]]{0,3}(#{1,6}|[-*+]>)[[:space:]]*|[*_~` + "`" + `]+`)
	spacePattern         = regexp.MustCompile(`[ \t\r\f\v]+`)
	blankLinePattern     = regexp.MustCompile(`\n{3,}`)
)

func markdownToPlainText(value string) string {
	value = markdownImagePattern.ReplaceAllString(value, " ")
	value = markdownLinkPattern.ReplaceAllString(value, "$1")
	value = markdownMarkup.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "<br>", "\n")
	value = strings.ReplaceAll(value, "<br/>", "\n")
	value = strings.ReplaceAll(value, "<br />", "\n")
	value = spacePattern.ReplaceAllString(value, " ")
	value = blankLinePattern.ReplaceAllString(value, "\n\n")
	return strings.TrimSpace(value)
}

func mergeOCRText(values ...string) string {
	seen := make(map[string]struct{})
	lines := make([]string, 0, 32)
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSpace(spacePattern.ReplaceAllString(line, " "))
			if line == "" {
				continue
			}
			key := strings.ToLower(line)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			lines = append(lines, line)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// BuildDescription creates a compact, deterministic description for message
// previews. Full OCR text remains available separately for search and detail.
func BuildDescription(text string, layout any) string {
	text = strings.TrimSpace(text)
	labels := collectLayoutLabels(layout)
	prefix := "图片"
	if len(labels) > 0 {
		prefix += "包含" + strings.Join(labels, "、")
	}
	if text == "" {
		return prefix + "，未识别到可索引文字"
	}
	excerpt := collapseForDescription(text)
	if len(labels) > 0 {
		return prefix + "；识别文字：" + truncateRunes(excerpt, 220)
	}
	return "图片识别文字：" + truncateRunes(excerpt, 240)
}

func collectLayoutLabels(value any) []string {
	labelNames := map[string]string{
		"title": "标题", "text": "正文", "table": "表格", "formula": "公式",
		"figure": "插图", "image": "插图", "code": "代码", "list": "列表",
		"header": "页眉", "footer": "页脚",
	}
	seen := map[string]struct{}{}
	var walk func(any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			for key, child := range typed {
				if strings.EqualFold(key, "label") {
					if raw, ok := child.(string); ok {
						if name := labelNames[strings.ToLower(strings.TrimSpace(raw))]; name != "" {
							seen[name] = struct{}{}
						}
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	order := []string{"标题", "正文", "表格", "公式", "代码", "列表", "插图", "页眉", "页脚"}
	out := make([]string, 0, len(seen))
	for _, item := range order {
		if _, ok := seen[item]; ok {
			out = append(out, item)
		}
	}
	return out
}

func collapseForDescription(text string) string {
	var builder strings.Builder
	previousSpace := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			if !previousSpace && builder.Len() > 0 {
				builder.WriteRune(' ')
			}
			previousSpace = true
			continue
		}
		builder.WriteRune(r)
		previousSpace = false
	}
	return strings.TrimSpace(builder.String())
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

func truncateText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit]) + "…"
}
