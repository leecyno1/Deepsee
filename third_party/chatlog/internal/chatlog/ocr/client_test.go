package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
)

func fixturePNG(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 5), B: 120, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func fixtureJPEG(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestClientRecognizeSelfHostedSDK(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/glmocr/parse" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"model":"glm-ocr",
			"markdown_result":"# 会议通知\n项目复盘 14:00",
			"json_result":[[{"label":"title","content":"会议通知"},{"label":"text","content":"项目复盘 14:00"}]]
		}`))
	}))
	defer server.Close()

	client, err := NewClient(conf.OCRConfig{
		Enabled: true, Provider: conf.OCRProviderVLLM, Endpoint: server.URL + "/glmocr/parse",
		Model: "glm-ocr", RequestTimeout: 30, ReceivedOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Recognize(context.Background(), fixturePNG(t), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	images, ok := received["images"].([]any)
	if !ok || len(images) != 1 || !strings.HasPrefix(images[0].(string), "data:image/png;base64,") {
		t.Fatalf("images payload = %#v", received["images"])
	}
	if !strings.Contains(result.Text, "会议通知") || !strings.Contains(result.Text, "项目复盘") {
		t.Fatalf("text = %q", result.Text)
	}
	if !strings.Contains(result.Description, "标题") || !strings.Contains(result.Description, "项目复盘") {
		t.Fatalf("description = %q", result.Description)
	}
	if len(result.ContentHash) != 64 {
		t.Fatalf("content hash = %q", result.ContentHash)
	}
}

func TestClientRejectsIncompleteImageBeforeOCRRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"markdown_result":"should not run"}`))
	}))
	defer server.Close()

	client, err := NewClient(conf.OCRConfig{
		Enabled: true, Provider: conf.OCRProviderVLLM, Endpoint: server.URL,
		Model: "glm-ocr", RequestTimeout: 30, ReceivedOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	complete := fixtureJPEG(t)
	for _, missing := range []int{2, 15, 206} {
		partial := complete[:len(complete)-missing]
		_, err = client.Recognize(context.Background(), partial, "image/jpeg")
		if !errors.Is(err, ErrIncompleteImage) {
			t.Fatalf("missing=%d error=%v, want ErrIncompleteImage", missing, err)
		}
	}
	if requests != 0 {
		t.Fatalf("OCR requests = %d, want 0", requests)
	}
}

func TestClientAcceptsCompleteJPEGWithWeChatTrailer(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"markdown_result":"尾部格式识别成功"}`))
	}))
	defer server.Close()

	client, err := NewClient(conf.OCRConfig{
		Enabled: true, Provider: conf.OCRProviderVLLM, Endpoint: server.URL,
		Model: "glm-ocr", RequestTimeout: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// Current WeChat images can carry opaque bytes after the JPEG EOI marker.
	withTrailer := append(fixtureJPEG(t), []byte("wechat-private-image-trailer-31")...)
	result, err := client.Recognize(context.Background(), withTrailer, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || result.Text != "尾部格式识别成功" {
		t.Fatalf("requests=%d result=%#v", requests, result)
	}
}

func TestClientRecognizeMaaSContract(t *testing.T) {
	const apiKey = "fixture-key"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != apiKey {
			t.Fatalf("authorization = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "glm-ocr" {
			t.Fatalf("model = %#v", payload["model"])
		}
		if file, _ := payload["file"].(string); !strings.HasPrefix(file, "data:image/png;base64,") {
			t.Fatalf("file = %.40q", file)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"model":"glm-ocr",
			"md_results":"订单号 A-1024",
			"layout_details":[[{"label":"text","content":"订单号 A-1024"}]]
		}`))
	}))
	defer server.Close()

	client, err := NewClient(conf.OCRConfig{
		Enabled: true, Provider: conf.OCRProviderMaaS, Endpoint: server.URL,
		APIKey: apiKey, Model: "glm-ocr", RequestTimeout: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Recognize(context.Background(), fixturePNG(t), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != conf.OCRProviderMaaS || !strings.Contains(result.Text, "A-1024") {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientRejectsMaaSWithoutKey(t *testing.T) {
	_, err := NewClient(conf.OCRConfig{Provider: conf.OCRProviderMaaS})
	if err == nil || !strings.Contains(err.Error(), "CHATLOG_OCR_API_KEY") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildDescriptionWithoutText(t *testing.T) {
	description := BuildDescription("", []any{map[string]any{"label": "table"}})
	if description != "图片包含表格，未识别到可索引文字" {
		t.Fatalf("description = %q", description)
	}
}
