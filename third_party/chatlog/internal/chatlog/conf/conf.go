package conf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/sjzar/chatlog/pkg/config"
	"github.com/sjzar/chatlog/pkg/util"
)

const (
	AppName          = "chatlog"
	ServerConfigName = "chatlog-server"
	EnvPrefix        = "CHATLOG"
	EnvConfigDir     = "CHATLOG_DIR"
)

// LoadTUIConfig 加载 TUI 配置
func LoadTUIConfig(configPath string) (*TUIConfig, *config.Manager, error) {

	if configPath == "" {
		configPath = os.Getenv(EnvConfigDir)
	}

	tcm, err := config.New(AppName, configPath, "", "", true)
	if err != nil {
		log.Error().Err(err).Msg("load tui config failed")
		return nil, nil, err
	}

	conf := &TUIConfig{}
	config.SetDefaults(tcm.Viper, conf, TUIDefaults)

	if err := tcm.Load(conf); err != nil {
		log.Error().Err(err).Msg("load tui config failed")
		return nil, nil, err
	}
	conf.ConfigDir = tcm.Path
	migrateLegacyWorkDirs(conf, tcm)

	logConfig("tui config", conf)

	return conf, tcm, nil
}

// LoadServiceConfig 加载服务配置
func LoadServiceConfig(configPath string, cmdConf map[string]any) (*ServerConfig, *config.Manager, error) {

	if configPath == "" {
		configPath = os.Getenv(EnvConfigDir)
	}

	scm, err := config.New(AppName, configPath, ServerConfigName, EnvPrefix, false)
	if err != nil {
		log.Error().Err(err).Msg("load server config failed")
		return nil, nil, err
	}

	conf := &ServerConfig{}
	config.SetDefaults(scm.Viper, conf, ServerDefaults)

	// Load cmd Conf
	for key, value := range cmdConf {
		scm.SetConfig(key, value)
	}

	if err := scm.Load(conf); err != nil {
		log.Error().Err(err).Msg("load server config failed")
		return nil, nil, err
	}
	// Load Data Dir config
	if len(conf.DataDir) != 0 && len(conf.DataKey) == 0 {
		dataConfigPath := filepath.Join(conf.DataDir, "chatlog.json")
		if b, err := os.ReadFile(dataConfigPath); err == nil {
			_ = os.Chmod(dataConfigPath, 0600)
			var pconf map[string]any
			if err := json.Unmarshal(b, &pconf); err == nil {
				for key, value := range pconf {
					if !DataDirConfigs[key] {
						continue
					}
					scm.SetConfig(key, value)
				}
			}
		}
		if err := scm.Load(conf); err != nil {
			log.Error().Err(err).Msg("reload server config failed")
			return nil, nil, err
		}
	}
	conf.ConfigDir = scm.Path
	if strings.TrimSpace(conf.WorkDir) == "" {
		account := "server"
		dataDir := filepath.Clean(strings.TrimSpace(conf.DataDir))
		if dataDir != "." && dataDir != "" {
			if strings.EqualFold(filepath.Base(dataDir), "db_storage") {
				dataDir = filepath.Dir(dataDir)
			}
			if base := strings.TrimSpace(filepath.Base(dataDir)); base != "" && base != "." {
				account = base
			}
		}
		conf.WorkDir = util.WorkDirAt(conf.ConfigDir, account)
	}

	logConfig("server config", conf)

	return conf, scm, nil
}

func migrateLegacyWorkDirs(conf *TUIConfig, manager *config.Manager) {
	if conf == nil || manager == nil {
		return
	}
	changed := false
	for i := range conf.History {
		item := &conf.History[i]
		next, migrated, err := util.MigrateLegacyDefaultWorkDir(conf.ConfigDir, item.Account, item.WorkDir)
		if err != nil {
			log.Warn().
				Err(err).
				Str("account", item.Account).
				Str("work_dir", item.WorkDir).
				Msg("legacy work directory migration skipped")
			continue
		}
		if migrated {
			item.WorkDir = next
			changed = true
			log.Info().
				Str("account", item.Account).
				Str("work_dir", next).
				Msg("legacy work directory migrated into unified chatlog root")
		}
	}
	if changed {
		if err := manager.SetConfig("history", conf.History); err != nil {
			log.Warn().Err(err).Msg("persist migrated work directories failed")
		}
	}
}

func logConfig(msg string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Info().Msg(msg)
		return
	}
	var payload any
	if err := json.Unmarshal(b, &payload); err != nil {
		log.Info().Msgf("%s: %s", msg, string(b))
		return
	}
	scrubConfigSecrets(payload)
	if out, err := json.Marshal(payload); err == nil {
		log.Info().Msgf("%s: %s", msg, string(out))
		return
	}
	log.Info().Msgf("%s: %s", msg, string(b))
}

func scrubConfigSecrets(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if isSensitiveConfigKey(k) {
				if s, ok := child.(string); ok && strings.TrimSpace(s) != "" {
					x[k] = "******"
				}
				continue
			}
			scrubConfigSecrets(child)
		}
	case []any:
		for _, child := range x {
			scrubConfigSecrets(child)
		}
	}
}

func isSensitiveConfigKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	normalized := strings.NewReplacer("_", "", "-", "", " ", "").Replace(key)
	switch normalized {
	case "datakey", "imgkey", "apikey", "clientsecret", "accesstoken", "refreshtoken", "password", "posturl", "hookposturl":
		return true
	}
	return strings.Contains(normalized, "token") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "apikey")
}

var DataDirConfigs = map[string]bool{
	"type":         true,
	"platform":     true,
	"version":      true,
	"full_version": true,
	"data_key":     true,
	"img_key":      true,
}
