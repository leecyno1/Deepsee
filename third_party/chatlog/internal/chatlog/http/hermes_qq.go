package http

import (
	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/hermespush"
)

type hermesQQStatus struct {
	Installed       bool   `json:"installed"`
	HermesBin       string `json:"hermes_bin,omitempty"`
	Enabled         bool   `json:"enabled"`
	Available       bool   `json:"available"`
	Editable        bool   `json:"editable"`
	HermesHome      string `json:"hermes_home,omitempty"`
	EnvFile         string `json:"env_file,omitempty"`
	ConfigFile      string `json:"config_file,omitempty"`
	AppID           string `json:"app_id,omitempty"`
	HasClientSecret bool   `json:"has_client_secret"`
	HomeChannel     string `json:"home_channel,omitempty"`
	HomeChannelName string `json:"home_channel_name,omitempty"`
	Error           string `json:"error,omitempty"`
}

func (s *Service) getHermesQQStatus(mode string) hermesQQStatus {
	targets, ok := conf.ParseHookNotifyTargets(mode)
	if !ok {
		targets = conf.HookNotifyTargets{Post: true}
	}
	install := hermespush.DetectInstallation()
	status := hermesQQStatus{
		Installed:  install.Installed,
		HermesBin:  install.HermesBin,
		Enabled:    targets.QQ,
		HermesHome: install.HermesHome,
	}
	cfg, err := hermespush.DiscoverQQConfig()
	if err != nil {
		if status.Enabled {
			status.Error = err.Error()
		}
		return status
	}
	status.Available = true
	status.Editable = cfg.EnvFile != "" || cfg.ConfigFile != ""
	status.HermesHome = cfg.HermesHome
	status.EnvFile = cfg.EnvFile
	status.ConfigFile = cfg.ConfigFile
	status.AppID = cfg.AppID
	status.HasClientSecret = cfg.ClientSecret != ""
	status.HomeChannel = cfg.HomeChannel
	status.HomeChannelName = cfg.HomeChannelName
	return status
}
