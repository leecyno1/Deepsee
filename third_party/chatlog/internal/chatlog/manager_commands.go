package chatlog

import (
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/chatlog/conf"
	"github.com/sjzar/chatlog/internal/chatlog/ctx"
	"github.com/sjzar/chatlog/internal/chatlog/usecase"
)

func (m *Manager) CommandKey(configPath string, pid int, force bool, showXorKey bool) (string, error) {

	var err error
	m.ctx, err = ctx.New(configPath)
	if err != nil {
		return "", err
	}

	if err := m.compose(m.ctx); err != nil {
		return "", err
	}

	return m.keys.Inspect(pid, force, showXorKey)
}

func (m *Manager) CommandHTTPServer(configPath string, cmdConf map[string]any) error {

	var err error
	m.sc, m.scm, err = conf.LoadServiceConfig(configPath, cmdConf)
	if err != nil {
		return err
	}

	dataDir := m.sc.GetDataDir()
	if len(dataDir) == 0 {
		return fmt.Errorf("dataDir is required")
	}

	dataKey := m.sc.GetDataKey()
	if len(dataKey) == 0 {
		return fmt.Errorf("dataKey is required")
	}

	ocrConfig := m.sc.GetOCRConfig()
	log.Info().
		Str("data_dir", m.sc.GetDataDir()).
		Str("work_dir", m.sc.GetWorkDir()).
		Str("http_addr", m.sc.GetHTTPAddr()).
		Bool("ocr_enabled", ocrConfig.Enabled).
		Str("ocr_provider", ocrConfig.Provider).
		Msg("server config loaded")

	if err := m.compose(m.sc); err != nil {
		return err
	}
	m.refreshMediaKeys(m.sc.GetVersion(), dataDir, m.sc.GetImgKey())

	server := usecase.NewServer(m.db, m.http, m.services)
	return server.Run()
}
