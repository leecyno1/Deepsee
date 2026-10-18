package hermespush

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type QQConfig struct {
	HermesHome      string `json:"hermes_home"`
	EnvFile         string `json:"env_file,omitempty"`
	ConfigFile      string `json:"config_file,omitempty"`
	AppID           string `json:"app_id,omitempty"`
	ClientSecret    string `json:"client_secret,omitempty"`
	HomeChannel     string `json:"home_channel,omitempty"`
	HomeChannelName string `json:"home_channel_name,omitempty"`
}

type QQSendRequest struct {
	Text       string
	MediaPaths []string
}

func DiscoverQQConfig() (*QQConfig, error) {
	for _, home := range hermesHomeCandidates() {
		cfg, err := loadQQConfig(home)
		if err == nil {
			return cfg, nil
		}
	}
	return nil, fmt.Errorf("未找到可用的 Hermes QQ 配置（需要 HERMES_HOME 或 ~/.hermes 下存在 QQ_APP_ID、QQ_CLIENT_SECRET、QQBOT_HOME_CHANNEL）")
}

func DiscoverQQConfigAt(hermesHome string) (*QQConfig, error) {
	hermesHome = strings.TrimSpace(hermesHome)
	if hermesHome == "" {
		return DiscoverQQConfig()
	}
	return loadQQConfig(hermesHome)
}

func SaveQQConfig(input QQConfig) (*QQConfig, error) {
	home := strings.TrimSpace(input.HermesHome)
	if home == "" {
		status := DetectInstallation()
		home = strings.TrimSpace(status.HermesHome)
	}
	if home == "" {
		return nil, fmt.Errorf("未找到 Hermes Home，无法保存 QQ 配置")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(home, 0o700)
	if strings.TrimSpace(input.ClientSecret) == "" {
		if current, err := DiscoverQQConfigAt(home); err == nil {
			input.ClientSecret = current.ClientSecret
		}
	}
	if err := validateQQHomeChannel(input.HomeChannel); err != nil {
		return nil, err
	}
	envPath := filepath.Join(home, ".env")
	updates := map[string]string{
		"QQ_APP_ID":               strings.TrimSpace(input.AppID),
		"QQ_CLIENT_SECRET":        strings.TrimSpace(input.ClientSecret),
		"QQBOT_HOME_CHANNEL":      strings.TrimSpace(input.HomeChannel),
		"QQBOT_HOME_CHANNEL_NAME": strings.TrimSpace(input.HomeChannelName),
	}
	if err := upsertEnvFile(envPath, updates); err != nil {
		return nil, err
	}
	return DiscoverQQConfigAt(home)
}

func SendQQ(cfg *QQConfig, req QQSendRequest) error {
	return SendQQContext(context.Background(), cfg, req)
}

func SendQQContext(ctx context.Context, cfg *QQConfig, req QQSendRequest) error {
	req.Text = strings.TrimSpace(req.Text)
	req.MediaPaths = compactMediaPaths(req.MediaPaths)
	if req.Text == "" && len(req.MediaPaths) == 0 {
		return nil
	}
	return sendQQViaHermesPython(ctx, cfg, req)
}

func loadQQConfig(hermesHome string) (*QQConfig, error) {
	_ = os.Chmod(hermesHome, 0o700)
	cfg := &QQConfig{HermesHome: hermesHome}

	envPath := filepath.Join(hermesHome, ".env")
	envMap, err := parseEnvFile(envPath)
	if err == nil {
		cfg.EnvFile = envPath
		_ = os.Chmod(envPath, 0o600)
	}
	cfg.AppID = strings.TrimSpace(envMap["QQ_APP_ID"])
	cfg.ClientSecret = strings.TrimSpace(envMap["QQ_CLIENT_SECRET"])
	cfg.HomeChannel = strings.TrimSpace(envMap["QQBOT_HOME_CHANNEL"])
	if cfg.HomeChannel == "" {
		cfg.HomeChannel = strings.TrimSpace(envMap["QQ_HOME_CHANNEL"])
	}
	cfg.HomeChannelName = strings.TrimSpace(envMap["QQBOT_HOME_CHANNEL_NAME"])
	if cfg.HomeChannelName == "" {
		cfg.HomeChannelName = strings.TrimSpace(envMap["QQ_HOME_CHANNEL_NAME"])
	}

	configPath := filepath.Join(hermesHome, "config.yaml")
	yamlCfg, err := parseHermesConfigYAML(configPath)
	if err == nil {
		cfg.ConfigFile = configPath
		platforms := []hermesQQPlatformYAML{yamlCfg.Platforms.QQBot, yamlCfg.Platforms.QQ}
		for _, platformCfg := range platforms {
			if cfg.AppID == "" {
				cfg.AppID = strings.TrimSpace(anyToString(platformCfg.Extra["app_id"]))
			}
			if cfg.ClientSecret == "" {
				cfg.ClientSecret = strings.TrimSpace(anyToString(platformCfg.Extra["client_secret"]))
			}
			if cfg.HomeChannel == "" && platformCfg.HomeChannel != nil {
				cfg.HomeChannel = strings.TrimSpace(platformCfg.HomeChannel.ChatID)
			}
			if cfg.HomeChannelName == "" && platformCfg.HomeChannel != nil {
				cfg.HomeChannelName = strings.TrimSpace(platformCfg.HomeChannel.Name)
			}
		}
	}

	if cfg.HomeChannel == "" || validateQQHomeChannel(cfg.HomeChannel) != nil {
		if candidate := discoverQQHomeChannelCandidate(hermesHome); candidate != "" {
			cfg.HomeChannel = candidate
			cfg.HomeChannelName = candidate
		}
	}

	if cfg.HomeChannelName == "" {
		cfg.HomeChannelName = cfg.HomeChannel
	}
	switch {
	case cfg.AppID == "":
		return nil, fmt.Errorf("Hermes QQ 配置缺少 QQ_APP_ID/app_id")
	case cfg.ClientSecret == "":
		return nil, fmt.Errorf("Hermes QQ 配置缺少 QQ_CLIENT_SECRET/client_secret")
	case cfg.HomeChannel == "":
		return nil, fmt.Errorf("Hermes QQ 配置缺少 QQBOT_HOME_CHANNEL")
	default:
		if err := validateQQHomeChannel(cfg.HomeChannel); err != nil {
			return nil, err
		}
		return cfg, nil
	}
}

func validateQQHomeChannel(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("Hermes QQ 配置缺少 QQBOT_HOME_CHANNEL")
	}
	lower := strings.ToLower(raw)
	invalidExact := map[string]struct{}{
		"qq":      {},
		"qqbot":   {},
		"c2c":     {},
		"group":   {},
		"guild":   {},
		"channel": {},
	}
	if _, ok := invalidExact[lower]; ok {
		return fmt.Errorf("Hermes QQ home channel 不能是 %q，需要填写真实 user/group OpenID；群聊用 group:group_openid，频道用 channel:channel_id", raw)
	}
	prefixes := []string{"qqbot:c2c:", "qqbot:group:", "qqbot:guild:", "qqbot:channel:", "c2c:", "group:", "guild:", "channel:"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			target := strings.TrimSpace(raw[len(prefix):])
			if target == "" || len(target) < 8 {
				return fmt.Errorf("Hermes QQ home channel %q 缺少有效目标 OpenID", raw)
			}
			if _, ok := invalidExact[strings.ToLower(target)]; ok {
				return fmt.Errorf("Hermes QQ home channel %q 不是有效目标 OpenID", raw)
			}
			return nil
		}
	}
	if len(raw) < 8 {
		return fmt.Errorf("Hermes QQ home channel %q 太短，需要填写真实 user/group OpenID", raw)
	}
	return nil
}

func discoverQQHomeChannelCandidate(hermesHome string) string {
	type sessionRecord struct {
		Platform string `json:"platform"`
		ChatType string `json:"chat_type"`
		Origin   struct {
			Platform string `json:"platform"`
			ChatID   string `json:"chat_id"`
			ChatType string `json:"chat_type"`
			UserID   string `json:"user_id"`
		} `json:"origin"`
	}
	sessionPath := filepath.Join(hermesHome, "sessions", "sessions.json")
	if data, err := os.ReadFile(sessionPath); err == nil {
		var sessions map[string]sessionRecord
		if err := json.Unmarshal(data, &sessions); err == nil {
			for key, sess := range sessions {
				if !strings.Contains(strings.ToLower(key), ":qqbot:") &&
					!strings.EqualFold(sess.Platform, "qqbot") &&
					!strings.EqualFold(sess.Origin.Platform, "qqbot") {
					continue
				}
				if chatID := strings.TrimSpace(sess.Origin.ChatID); validateQQHomeChannel(chatID) == nil {
					if strings.EqualFold(sess.Origin.ChatType, "group") || strings.EqualFold(sess.ChatType, "group") {
						return "group:" + chatID
					}
					return chatID
				}
				if userID := strings.TrimSpace(sess.Origin.UserID); validateQQHomeChannel(userID) == nil {
					return userID
				}
			}
		}
	}

	approvedPath := filepath.Join(hermesHome, "pairing", "qqbot-approved.json")
	if data, err := os.ReadFile(approvedPath); err == nil {
		var approved map[string]any
		if err := json.Unmarshal(data, &approved); err == nil {
			for openID := range approved {
				openID = strings.TrimSpace(openID)
				if validateQQHomeChannel(openID) == nil {
					return openID
				}
			}
		}
	}
	return ""
}
