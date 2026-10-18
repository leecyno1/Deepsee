package chatlog

import (
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/http"
	"github.com/sjzar/chatlog/pkg/util"
)

func (m *Manager) StartService() error {
	if err := m.startHTTPService(); err != nil {
		return err
	}
	m.ctx.SetHTTPEnabled(true)
	return nil
}

func (m *Manager) restoreHTTPService() error {
	if m == nil || m.ctx == nil || !m.ctx.HTTPEnabled {
		return nil
	}
	return m.startHTTPService()
}

func (m *Manager) startHTTPService() error {
	if m == nil || m.services == nil {
		return fmt.Errorf("service graph is not initialized")
	}
	if err := m.services.StartHTTP(); err != nil {
		return err
	}

	// 如果是 4.0 版本，更新下 xorkey
	m.refreshMediaKeys(m.ctx.Version, m.ctx.DataDir, m.ctx.ImgKey)

	return nil
}

func (m *Manager) refreshMediaKeys(version int, dataDir, imageKey string) {
	if version != 4 || m == nil || m.services == nil || m.services.MediaKeys == nil {
		return
	}
	m.services.MediaKeys.SetImageKey(imageKey)
	if dataDir == "" {
		return
	}
	go func() {
		if _, err := m.services.MediaKeys.RefreshXOR(dataDir); err != nil {
			log.Debug().Err(err).Msg("media XOR refresh did not complete")
		}
	}()
}

func (m *Manager) StopService() error {
	err := m.stopService()
	if m != nil && m.ctx != nil {
		// This method represents an explicit user action. Process shutdown and
		// account switching call stopService directly so the remembered choice
		// remains unchanged.
		m.ctx.SetHTTPEnabled(false)
	}
	return err
}

// IsHTTPRunning reports whether the HTTP listener is actually serving.
func (m *Manager) IsHTTPRunning() bool {
	return m != nil && m.http != nil && m.http.IsRunning()
}

func (m *Manager) stopService() error {
	if m == nil || m.services == nil {
		return nil
	}
	return m.services.Stop()
}

func (m *Manager) SetHTTPAddr(text string) error {
	var addr string
	if util.IsNumeric(text) {
		addr = fmt.Sprintf("127.0.0.1:%s", text)
	} else if strings.HasPrefix(text, "http://") {
		addr = strings.TrimPrefix(text, "http://")
	} else if strings.HasPrefix(text, "https://") {
		addr = strings.TrimPrefix(text, "https://")
	} else {
		addr = text
	}
	if err := http.ValidateListenAddress(addr); err != nil {
		return err
	}
	m.ctx.SetHTTPAddr(addr)
	return nil
}
