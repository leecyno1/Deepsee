package conf

import (
	"sync"
	"time"
)

const (
	DefaultHTTPAddr = "127.0.0.1:5030"
)

type ServerConfig struct {
	mu                 sync.RWMutex
	ConfigDir          string       `mapstructure:"-"`
	Type               string       `mapstructure:"type"`
	Platform           string       `mapstructure:"platform"`
	Version            int          `mapstructure:"version"`
	FullVersion        string       `mapstructure:"full_version"`
	DataDir            string       `mapstructure:"data_dir"`
	DataKey            string       `mapstructure:"data_key"`
	ImgKey             string       `mapstructure:"img_key"`
	WorkDir            string       `mapstructure:"work_dir"`
	HTTPAddr           string       `mapstructure:"http_addr"`
	SaveDecryptedMedia bool         `mapstructure:"save_decrypted_media"`
	OCR                OCRConfig    `mapstructure:"ocr"`
	MessageHook        *MessageHook `mapstructure:"message_hook"`
}

var ServerDefaults = map[string]any{
	"save_decrypted_media":    true,
	"ocr.enabled":             false,
	"ocr.backfill_enabled":    false,
	"ocr.local_auto_start":    true,
	"ocr.local_auto_restart":  true,
	"ocr.provider":            OCRProviderVLLM,
	"ocr.endpoint":            DefaultOCRVLLMEndpoint,
	"ocr.model":               "glm-ocr",
	"ocr.poll_interval_sec":   5,
	"ocr.request_timeout_sec": 180,
	"ocr.backfill_on_start":   false,
	"ocr.received_only":       true,
	"ocr.listen_contacts":     "",
	"ocr.listen_chatrooms":    "",
}

func (c *ServerConfig) GetDataDir() string {
	return c.DataDir
}

func (c *ServerConfig) GetWorkDir() string {
	return c.WorkDir
}

func (c *ServerConfig) GetRuntimeDir() string {
	return c.ConfigDir
}

func (c *ServerConfig) GetVersion() int {
	return c.Version
}

func (c *ServerConfig) GetDataKey() string {
	return c.DataKey
}

func (c *ServerConfig) GetImgKey() string {
	return c.ImgKey
}

func (c *ServerConfig) GetHTTPAddr() string {
	if c.HTTPAddr == "" {
		c.HTTPAddr = DefaultHTTPAddr
	}
	return c.HTTPAddr
}

func (c *ServerConfig) GetMessageHook() *MessageHook {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.MessageHook == nil {
		snapshot := NormalizeMessageHook(MessageHook{
			NotifyMode:  HookNotifyPost,
			BeforeCount: 5,
			AfterCount:  5,
		})
		return &snapshot
	}
	snapshot := NormalizeMessageHook(*c.MessageHook)
	return &snapshot
}

func (c *ServerConfig) UpdateMessageHook(next MessageHook) error {
	next = NormalizeMessageHook(next)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.MessageHook == nil || !MessageHookRulesEqual(*c.MessageHook, next) {
		if next.EffectiveAt <= 0 {
			next.EffectiveAt = time.Now().Unix()
		}
	} else {
		next.EffectiveAt = c.MessageHook.EffectiveAt
	}
	c.MessageHook = &next
	return nil
}

// GetAccount 在 server 模式下没有"激活账号"的概念（启动时直接传入 data_dir/data_key），
// 返回空串。webhook 校验时会把空 owner_wxid 当作"旧版兼容"放行。
func (c *ServerConfig) GetAccount() string {
	return ""
}

func (c *ServerConfig) GetSaveDecryptedMedia() bool {
	return c.SaveDecryptedMedia
}

func (c *ServerConfig) GetOCRConfig() OCRConfig {
	if c == nil {
		return DefaultOCRConfig()
	}
	return OCRConfigWithEnv(c.OCR)
}
