package conf

import (
	"testing"
	"time"
)

func TestNormalizeOCRConfigDefaults(t *testing.T) {
	config := NormalizeOCRConfig(OCRConfig{})
	if config.Provider != OCRProviderVLLM ||
		config.Endpoint != DefaultOCRVLLMEndpoint ||
		config.Model != "glm-ocr" ||
		!config.LocalAutoStartEnabled() ||
		!config.LocalAutoRestartEnabled() ||
		!config.ReceivedOnly ||
		config.PollInterval() != 5*time.Second {
		t.Fatalf("defaults = %#v", config)
	}
}

func TestNormalizeOCRConfigMigratesLegacyLocalSDKPort(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1:5002/glmocr/parse",
		"http://localhost:5002/glmocr/parse/",
		"http://[::1]:5002/glmocr/parse",
	} {
		config := NormalizeOCRConfig(OCRConfig{
			Provider: OCRProviderVLLM,
			Endpoint: endpoint,
		})
		if config.Endpoint != DefaultOCRVLLMEndpoint {
			t.Fatalf("endpoint %q normalized to %q, want %q", endpoint, config.Endpoint, DefaultOCRVLLMEndpoint)
		}
	}

	custom := NormalizeOCRConfig(OCRConfig{
		Provider: OCRProviderVLLM,
		Endpoint: "http://127.0.0.1:9000/glmocr/parse",
	})
	if custom.Endpoint != "http://127.0.0.1:9000/glmocr/parse" {
		t.Fatalf("custom endpoint = %q", custom.Endpoint)
	}
}

func TestOCRConfigEnvironmentOverlay(t *testing.T) {
	t.Setenv("CHATLOG_OCR_ENABLED", "true")
	t.Setenv("CHATLOG_OCR_PROVIDER", "maas")
	t.Setenv("CHATLOG_OCR_ENDPOINT", "https://example.test/layout")
	t.Setenv("CHATLOG_OCR_API_KEY", "fixture-secret")
	t.Setenv("CHATLOG_OCR_POLL_INTERVAL_SEC", "7")
	t.Setenv("CHATLOG_OCR_RECEIVED_ONLY", "false")
	t.Setenv("CHATLOG_OCR_LOCAL_AUTO_START", "false")
	t.Setenv("CHATLOG_OCR_LOCAL_AUTO_RESTART", "false")

	config := OCRConfigWithEnv(DefaultOCRConfig())
	if !config.Enabled || config.Provider != OCRProviderMaaS ||
		config.Endpoint != "https://example.test/layout" ||
		config.APIKey != "fixture-secret" ||
		config.PollIntervalSec != 7 ||
		config.ReceivedOnly ||
		config.LocalAutoStartEnabled() ||
		config.LocalAutoRestartEnabled() {
		t.Fatalf("config = %#v", config)
	}
}

func TestOCRConfigPartialEnvironmentOverlayKeepsSafeDefaults(t *testing.T) {
	t.Setenv("CHATLOG_OCR_ENABLED", "true")
	t.Setenv("CHATLOG_OCR_PROVIDER", "maas")

	config := OCRConfigWithEnv(OCRConfig{})
	if !config.Enabled || config.Provider != OCRProviderMaaS ||
		config.Endpoint != DefaultOCRMaaSEndpoint ||
		!config.ReceivedOnly {
		t.Fatalf("config = %#v", config)
	}
}

func TestOCRAPIKeyIsSensitive(t *testing.T) {
	if !isSensitiveConfigKey("ocr.api_key") || !isSensitiveConfigKey("ocr_api_key") {
		t.Fatal("OCR API key was not classified as sensitive")
	}
}

func TestOCRListenScopeNormalizesAndFiltersTalkers(t *testing.T) {
	config := NormalizeOCRConfig(OCRConfig{
		ListenContacts:  "wxid_a， wxid_b\nwxid_a",
		ListenChatRooms: "100@chatroom;200@chatroom",
	})
	if config.ListenContacts != "wxid_a,wxid_b" {
		t.Fatalf("contacts = %q", config.ListenContacts)
	}
	if config.ListenChatRooms != "100@chatroom,200@chatroom" {
		t.Fatalf("chatrooms = %q", config.ListenChatRooms)
	}
	for _, talker := range []string{"wxid_a", "wxid_b", "100@chatroom", "200@chatroom"} {
		if !config.AllowsTalker(talker) {
			t.Errorf("scope rejected %q", talker)
		}
	}
	for _, talker := range []string{"wxid_other", "300@chatroom"} {
		if config.AllowsTalker(talker) {
			t.Errorf("scope accepted %q", talker)
		}
	}
}
