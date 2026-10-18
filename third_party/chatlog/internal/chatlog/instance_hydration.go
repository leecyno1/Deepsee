package chatlog

import (
	"strings"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	iwechat "github.com/sjzar/chatlog/internal/wechat"
)

func (m *Manager) loadWeChatInstances() []*iwechat.Account {
	instances := m.wechat.GetWeChatInstances()
	return hydrateAccountsFromHistory(instances, m.ctx.History, m.ctx.Account)
}

func hydrateAccountsFromHistory(instances []*iwechat.Account, history map[string]conf.ProcessConfig, preferredAccount string) []*iwechat.Account {
	if len(instances) == 0 {
		return instances
	}
	hydrated := make([]*iwechat.Account, 0, len(instances))
	for _, account := range instances {
		if account == nil {
			continue
		}
		copyAccount := *account
		if len(history) != 0 && strings.HasPrefix(copyAccount.Name, "未登录微信_") {
			if candidate, ok := selectHistoryCandidate(history, preferredAccount, len(instances)); ok {
				copyAccount.Name = candidate.Account
				copyAccount.DataDir = firstNonEmpty(copyAccount.DataDir, candidate.DataDir)
				copyAccount.Key = firstNonEmpty(copyAccount.Key, candidate.DataKey)
				copyAccount.ImgKey = firstNonEmpty(copyAccount.ImgKey, candidate.ImgKey)
				copyAccount.FullVersion = firstNonEmpty(copyAccount.FullVersion, candidate.FullVersion)
				if copyAccount.Version == 0 {
					copyAccount.Version = candidate.Version
				}
				copyAccount.Platform = firstNonEmpty(copyAccount.Platform, candidate.Platform)
			}
		}
		hydrated = append(hydrated, &copyAccount)
	}
	return hydrated
}

func selectHistoryCandidate(history map[string]conf.ProcessConfig, preferredAccount string, processCount int) (conf.ProcessConfig, bool) {
	if preferredAccount != "" {
		if candidate, ok := history[preferredAccount]; ok {
			return candidate, true
		}
	}
	if processCount == 1 && len(history) == 1 {
		for _, candidate := range history {
			return candidate, true
		}
	}
	return conf.ProcessConfig{}, false
}
