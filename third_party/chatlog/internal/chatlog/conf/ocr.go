package conf

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	OCRProviderVLLM = "vllm"
	OCRProviderMaaS = "maas"

	DefaultOCRVLLMEndpoint = "http://127.0.0.1:8081/glmocr/parse"
	DefaultOCRMaaSEndpoint = "https://open.bigmodel.cn/api/paas/v4/layout_parsing"
	legacyOCRVLLMEndpoint  = "http://127.0.0.1:5002/glmocr/parse"
)

// OCRConfig controls the optional image OCR pipeline. The default provider is
// the self-hosted GLM-OCR SDK server backed by vLLM. MaaS uses the same result
// contract and only requires switching provider/endpoint plus an API key.
type OCRConfig struct {
	Enabled              bool   `mapstructure:"enabled" json:"enabled"`
	BackfillEnabled      bool   `mapstructure:"backfill_enabled" json:"backfill_enabled"`
	AutoDownloadOriginal bool   `mapstructure:"auto_download_original" json:"auto_download_original"`
	LocalAutoStart       *bool  `mapstructure:"local_auto_start" json:"local_auto_start,omitempty"`
	LocalAutoRestart     *bool  `mapstructure:"local_auto_restart" json:"local_auto_restart,omitempty"`
	Provider             string `mapstructure:"provider" json:"provider"`
	Endpoint             string `mapstructure:"endpoint" json:"endpoint"`
	APIKey               string `mapstructure:"api_key" json:"api_key,omitempty"`
	Model                string `mapstructure:"model" json:"model"`
	PollIntervalSec      int    `mapstructure:"poll_interval_sec" json:"poll_interval_sec"`
	RequestTimeout       int    `mapstructure:"request_timeout_sec" json:"request_timeout_sec"`
	BackfillOnStart      bool   `mapstructure:"backfill_on_start" json:"backfill_on_start"`
	ReceivedOnly         bool   `mapstructure:"received_only" json:"received_only"`
	ListenContacts       string `mapstructure:"listen_contacts" json:"listen_contacts"`
	ListenChatRooms      string `mapstructure:"listen_chatrooms" json:"listen_chatrooms"`
}

func DefaultOCRConfig() OCRConfig {
	enabled := true
	return OCRConfig{
		LocalAutoStart:   &enabled,
		LocalAutoRestart: &enabled,
		Provider:         OCRProviderVLLM,
		Endpoint:         DefaultOCRVLLMEndpoint,
		Model:            "glm-ocr",
		PollIntervalSec:  5,
		RequestTimeout:   180,
		ReceivedOnly:     true,
	}
}

// NormalizeOCRConfig applies provider-aware defaults and conservative runtime
// bounds. It deliberately does not copy or log the API key.
func NormalizeOCRConfig(input OCRConfig) OCRConfig {
	if input == (OCRConfig{}) {
		return DefaultOCRConfig()
	}
	out := input
	if out.LocalAutoStart == nil {
		value := true
		out.LocalAutoStart = &value
	}
	if out.LocalAutoRestart == nil {
		value := true
		out.LocalAutoRestart = &value
	}
	out.Provider = strings.ToLower(strings.TrimSpace(out.Provider))
	if out.Provider == "" {
		out.Provider = OCRProviderVLLM
	}
	if out.Provider != OCRProviderVLLM && out.Provider != OCRProviderMaaS {
		out.Provider = OCRProviderVLLM
	}
	out.Endpoint = strings.TrimSpace(out.Endpoint)
	// Migrate the previously shipped local SDK endpoint. Custom/remote URLs are
	// preserved; only the old loopback default moves to the new managed port.
	switch strings.ToLower(strings.TrimSuffix(out.Endpoint, "/")) {
	case legacyOCRVLLMEndpoint, "http://localhost:5002/glmocr/parse", "http://[::1]:5002/glmocr/parse":
		if out.Provider == OCRProviderVLLM {
			out.Endpoint = DefaultOCRVLLMEndpoint
		}
	}
	if out.Endpoint == "" {
		if out.Provider == OCRProviderMaaS {
			out.Endpoint = DefaultOCRMaaSEndpoint
		} else {
			out.Endpoint = DefaultOCRVLLMEndpoint
		}
	}
	out.APIKey = strings.TrimSpace(out.APIKey)
	out.Model = strings.TrimSpace(out.Model)
	out.ListenContacts = canonicalOCRScope(out.ListenContacts)
	out.ListenChatRooms = canonicalOCRScope(out.ListenChatRooms)
	if out.Model == "" {
		out.Model = "glm-ocr"
	}
	if out.PollIntervalSec < 1 {
		out.PollIntervalSec = 5
	}
	if out.PollIntervalSec > 3600 {
		out.PollIntervalSec = 3600
	}
	if out.RequestTimeout < 10 {
		out.RequestTimeout = 180
	}
	if out.RequestTimeout > 1800 {
		out.RequestTimeout = 1800
	}
	return out
}

func (c OCRConfig) PollInterval() time.Duration {
	return time.Duration(NormalizeOCRConfig(c).PollIntervalSec) * time.Second
}

func (c OCRConfig) Timeout() time.Duration {
	return time.Duration(NormalizeOCRConfig(c).RequestTimeout) * time.Second
}

// OCRConfigWithEnv lets the TUI/ops composition path use the same settings as
// server mode. Server mode already receives these values through Viper; the
// explicit overlay keeps both launch paths consistent.
func OCRConfigWithEnv(input OCRConfig) OCRConfig {
	// Materialize defaults before applying a partial environment overlay.
	// Otherwise enabling OCR through only CHATLOG_OCR_ENABLED/PROVIDER leaves
	// zero-value booleans such as ReceivedOnly at false, even though the
	// conservative default is to process received images only.
	out := NormalizeOCRConfig(input)
	setString := func(name string, target *string) {
		if value, ok := os.LookupEnv(name); ok {
			*target = value
		}
	}
	setBool := func(name string, target *bool) {
		if value, ok := os.LookupEnv(name); ok {
			if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
				*target = parsed
			}
		}
	}
	setOptionalBool := func(name string, target **bool) {
		if value, ok := os.LookupEnv(name); ok {
			if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
				*target = &parsed
			}
		}
	}
	setInt := func(name string, target *int) {
		if value, ok := os.LookupEnv(name); ok {
			if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				*target = parsed
			}
		}
	}
	setBool("CHATLOG_OCR_ENABLED", &out.Enabled)
	setBool("CHATLOG_OCR_BACKFILL_ENABLED", &out.BackfillEnabled)
	setBool("CHATLOG_OCR_AUTO_DOWNLOAD_ORIGINAL", &out.AutoDownloadOriginal)
	// Legacy force-enable used during early Frida integration.
	if value, ok := os.LookupEnv("CHATLOG_IMAGE_DOWNLOAD_TRIGGER"); ok {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil && parsed {
			out.AutoDownloadOriginal = true
		}
	}
	setOptionalBool("CHATLOG_OCR_LOCAL_AUTO_START", &out.LocalAutoStart)
	setOptionalBool("CHATLOG_OCR_LOCAL_AUTO_RESTART", &out.LocalAutoRestart)
	setString("CHATLOG_OCR_PROVIDER", &out.Provider)
	if value, ok := os.LookupEnv("CHATLOG_OCR_ENDPOINT"); ok {
		out.Endpoint = value
	} else if strings.TrimSpace(input.Endpoint) == "" {
		// Re-resolve the endpoint after a provider-only overlay instead of
		// retaining the vLLM endpoint materialized above.
		out.Endpoint = ""
	}
	setString("CHATLOG_OCR_API_KEY", &out.APIKey)
	setString("CHATLOG_OCR_MODEL", &out.Model)
	setInt("CHATLOG_OCR_POLL_INTERVAL_SEC", &out.PollIntervalSec)
	setInt("CHATLOG_OCR_REQUEST_TIMEOUT_SEC", &out.RequestTimeout)
	setBool("CHATLOG_OCR_BACKFILL_ON_START", &out.BackfillOnStart)
	setBool("CHATLOG_OCR_RECEIVED_ONLY", &out.ReceivedOnly)
	setString("CHATLOG_OCR_LISTEN_CONTACTS", &out.ListenContacts)
	setString("CHATLOG_OCR_LISTEN_CHATROOMS", &out.ListenChatRooms)
	return NormalizeOCRConfig(out)
}

func (c OCRConfig) LocalAutoStartEnabled() bool {
	c = NormalizeOCRConfig(c)
	return c.LocalAutoStart != nil && *c.LocalAutoStart
}

func (c OCRConfig) LocalAutoRestartEnabled() bool {
	c = NormalizeOCRConfig(c)
	return c.LocalAutoRestart != nil && *c.LocalAutoRestart
}

func canonicalOCRScope(raw string) string {
	items := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', '\n', '\r', '\t', '，', '；':
			return true
		default:
			return false
		}
	})
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return strings.Join(out, ",")
}

func ocrScopeList(raw string) []string {
	raw = canonicalOCRScope(raw)
	if raw == "" {
		return []string{}
	}
	return strings.Split(raw, ",")
}

func (c OCRConfig) ListenContactsList() []string {
	return ocrScopeList(c.ListenContacts)
}

func (c OCRConfig) ListenChatRoomsList() []string {
	return ocrScopeList(c.ListenChatRooms)
}

func (c OCRConfig) ScopeAll() bool {
	return strings.TrimSpace(c.ListenContacts) == "" && strings.TrimSpace(c.ListenChatRooms) == ""
}

func (c OCRConfig) AllowsTalker(talker string) bool {
	c = NormalizeOCRConfig(c)
	talker = strings.TrimSpace(talker)
	if talker == "" {
		return false
	}
	if c.ScopeAll() {
		return true
	}
	values := c.ListenContactsList()
	if strings.HasSuffix(strings.ToLower(talker), "@chatroom") {
		values = c.ListenChatRoomsList()
	}
	for _, value := range values {
		if strings.EqualFold(value, talker) {
			return true
		}
	}
	return false
}
