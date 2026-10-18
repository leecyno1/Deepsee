package chatlog

import (
	"fmt"

	"github.com/fsnotify/fsnotify"

	"github.com/sjzar/chatlog/internal/model"
)

func (m *Manager) GetDataKey() error {
	return m.keys.GetDataKey()
}

func (m *Manager) GetImageKey(onStatus func(string)) error {
	return m.keys.GetImageKey(onStatus)
}

func (m *Manager) RestartAndGetDataKey(onStatus func(string)) error {
	return m.keys.CaptureDataKey(onStatus)
}

func (m *Manager) RefreshSession() error {
	return m.sessions.Refresh()
}

func (m *Manager) RefreshLatestSession() (*model.Session, error) {
	return m.sessions.RefreshLatest()
}

func (m *Manager) GetLatestSession() (*model.Session, error) {
	return m.sessions.Latest()
}

func (m *Manager) SubscribeDatabaseChanges(group string, callback func(fsnotify.Event) error) (func(), error) {
	if m == nil || m.db == nil {
		return nil, fmt.Errorf("database service is not initialized")
	}
	return m.db.SubscribeCallback(group, callback)
}
